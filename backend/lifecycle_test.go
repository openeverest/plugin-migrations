package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openeverest/plugin-migrations/backend/internal/api"
	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/pcsm"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
	"github.com/openeverest/plugin-migrations/backend/internal/kube"
	"github.com/openeverest/plugin-migrations/backend/internal/kube/kubetest"
	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

// fakePCSMServer models the PCSM states the lifecycle depends on.
type fakePCSMServer struct {
	mu        sync.Mutex
	state     string
	completed bool
	startBody map[string]any
}

func (f *fakePCSMServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(body string) { _, _ = io.WriteString(w, body) }
	switch r.URL.Path {
	case "/status":
		reply(`{"ok":true,"state":"` + f.state + `","lagTimeSeconds":1,"initialSync":{"cloneCompleted":` +
			boolJSON(f.completed) + `,"completed":` + boolJSON(f.completed) + `,"estimatedCloneSizeBytes":100,"clonedSizeBytes":100}}`)
	case "/start":
		_ = json.NewDecoder(r.Body).Decode(&f.startBody)
		f.state = "running"
		reply(`{"ok":true}`)
	case "/finalize":
		if !f.completed {
			reply(`{"ok":false,"error":"initial sync is not completed"}`)
			return
		}
		f.state = "finalized"
		reply(`{"ok":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// localWorkloads keeps the real Kubernetes objects but routes control calls to
// the in-process fake PCSM and treats the worker as available.
type localWorkloads struct {
	*kube.Workloads
	baseURL string
}

func (l localWorkloads) Sync(context.Context, string) (migration.WorkerState, error) {
	return migration.WorkerState{Ready: true}, nil
}
func (l localWorkloads) BaseURL(string, int32) string { return l.baseURL }

func TestEndToEndMigrationLifecycle(t *testing.T) {
	const namespace = "everest-system"
	pcsmServer := &fakePCSMServer{state: "idle"}
	pcsmHTTP := httptest.NewServer(pcsmServer)
	defer pcsmHTTP.Close()

	everestHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/clusters/main/namespaces/shop/instances/orders/connection":
			_, _ = io.WriteString(w, `{"type":"mongodb","provider":"percona-server-mongodb","username":"databaseAdmin","password":"tpw",`+
				`"uri":"mongodb://databaseAdmin:tpw@orders-rs0.shop.svc.cluster.local:27017/admin?tls=true","ca.crt":"PEM"}`)
		case "/v1/clusters/main/namespaces/shop/instances/orders":
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer everestHTTP.Close()

	registry, err := engine.NewRegistry(engine.Registration{
		Engine: pcsm.New(),
		Runner: engine.RunnerConfig{Image: "percona/percona-clustersync-mongodb:1.0.0", ImagePullPolicy: "IfNotPresent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientset := kubetest.NewVersionedClient()
	everestClient := everest.NewClient(everestHTTP.URL)
	workloads := localWorkloads{Workloads: kube.NewWorkloads(clientset, namespace, kube.WorkerConfig{}), baseURL: pcsmHTTP.URL}
	service := migration.NewService(registry, kube.NewStore(clientset, namespace), workloads, everestClient)
	mux := http.NewServeMux()
	api.New(service, everestClient).RegisterRoutes(mux)

	call := func(method, path, body string) (int, map[string]any) {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer user-jwt")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		var decoded map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
		return recorder.Code, decoded
	}

	status, created := call(http.MethodPost, "/api/migrations", `{
		"name": "orders-into-everest", "engine": "pcsm",
		"source": {"type": "external", "uri": "mongodb://app:s3cret@db1:27017,db2:27017/?replicaSet=rs0"},
		"target": {"type": "instance", "k8sCluster": "main", "namespace": "shop", "name": "orders"},
		"options": {"includeNamespaces": ["shop.*"], "replication": {"numWorkers": 4}}
	}`)
	if status != http.StatusCreated || created["phase"] != "provisioning" {
		t.Fatalf("create = %d %v", status, created)
	}
	id, _ := created["id"].(string)
	if source, _ := created["source"].(map[string]any); source["display"] != "db1:27017,db2:27017" {
		t.Fatalf("source view = %v", created["source"])
	}

	ctx := context.Background()
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secret.Data["target-uri"]), "tlsCAFile=%2Fetc%2Fmigration%2Fsecrets%2Ftarget-ca.crt") {
		t.Fatalf("target URI missing CA file: %s", secret.Data["target-uri"])
	}
	record, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, id, metav1.GetOptions{})
	if err != nil || strings.Contains(record.Data["migration.json"], "s3cret") || strings.Contains(record.Data["migration.json"], "tpw") {
		t.Fatalf("record missing or leaks credentials: %v %v", record.Data, err)
	}

	service.Reconcile(ctx)
	if pcsmServer.startBody["replNumWorkers"] != float64(4) || pcsmServer.startBody["includeNamespaces"] == nil {
		t.Fatalf("PCSM start body = %v", pcsmServer.startBody)
	}
	_, view := call(http.MethodGet, "/api/migrations/"+id, "")
	if view["phase"] != "full-load" || view["startedAt"] == nil {
		t.Fatalf("after start = %v", view)
	}

	if status, body := call(http.MethodPost, "/api/migrations/"+id+"/cutover", ""); status != http.StatusConflict {
		t.Fatalf("cutover during full load = %d %v", status, body)
	}

	pcsmServer.mu.Lock()
	pcsmServer.completed = true
	pcsmServer.mu.Unlock()
	_, view = call(http.MethodGet, "/api/migrations/"+id, "")
	actions, _ := view["allowedActions"].([]any)
	if view["phase"] != "replicating" || len(actions) != 3 {
		t.Fatalf("replicating view = %v", view)
	}

	status, view = call(http.MethodPost, "/api/migrations/"+id+"/cutover", "")
	if status != http.StatusAccepted || view["phase"] != "completed" {
		t.Fatalf("cutover = %d %v", status, view)
	}

	status, list := call(http.MethodGet, "/api/migrations", "")
	if migrations, _ := list["migrations"].([]any); status != http.StatusOK || len(migrations) != 1 {
		t.Fatalf("list = %d %v", status, list)
	}
	if status, _ := call(http.MethodDelete, "/api/migrations/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	if status, _ := call(http.MethodGet, "/api/migrations/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete = %d", status)
	}
}
