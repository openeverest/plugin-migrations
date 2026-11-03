package pcsm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

var testRunner = engine.RunnerConfig{Image: "percona/percona-clustersync-mongodb:1.0.0", ImagePullPolicy: "IfNotPresent"}

func prepareInput(options string) engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID: "mig-abc",
		Source:      engine.EndpointInfo{DatabaseType: "mongodb"},
		Target:      engine.EndpointInfo{DatabaseType: "mongodb", Provider: "percona-server-mongodb"},
		SourceConn:  engine.Connection{URI: "mongodb://app:s3cret@db1:27017,db2:27017/?replicaSet=rs0"},
		TargetConn: engine.Connection{
			URI:    "mongodb://databaseAdmin:t%40rget@orders-rs0.shop.svc.cluster.local:27017:27017/admin?tls=true",
			CACert: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		},
		Options: json.RawMessage(options),
		Runner:  testRunner,
	}
}

func TestDecodeOptions(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
		check   func(t *testing.T, options Options)
	}{
		{name: "null yields defaults", raw: "null", check: func(t *testing.T, o Options) {
			if o.IncludeNamespaces == nil || o.ExcludeNamespaces == nil || o.PauseOnInitialSync {
				t.Fatalf("unexpected defaults: %+v", o)
			}
		}},
		{name: "normalizes and deduplicates namespaces", raw: `{"includeNamespaces":[" shop.* ","shop","crm.users"]}`, check: func(t *testing.T, o Options) {
			if strings.Join(o.IncludeNamespaces, ",") != "shop.*,crm.users" {
				t.Fatalf("got %v", o.IncludeNamespaces)
			}
		}},
		// PCSM reads a bare "scratch" as collection "" and excludes nothing.
		{name: "bare database becomes a whole-database filter", raw: `{"excludeNamespaces":["scratch"]}`, check: func(t *testing.T, o Options) {
			if strings.Join(o.ExcludeNamespaces, ",") != "scratch.*" {
				t.Fatalf("got %v", o.ExcludeNamespaces)
			}
		}},
		{name: "rejects unknown fields", raw: `{"includeNamespace":["a"]}`, wantErr: `unknown field "includeNamespace"`},
		{name: "rejects wrong type without echoing value", raw: `{"pauseOnInitialSync":"s3cret"}`, wantErr: `field "pauseOnInitialSync" must be bool`},
		{name: "rejects invalid database name", raw: `{"excludeNamespaces":["bad db"]}`, wantErr: "not a valid database name"},
		{name: "rejects empty collection", raw: `{"includeNamespaces":["shop."]}`, wantErr: "not a valid namespace"},
		{name: "rejects include and exclude overlap", raw: `{"includeNamespaces":["shop"],"excludeNamespaces":["shop"]}`, wantErr: "both included and excluded"},
		{name: "rejects out-of-range tuning", raw: `{"clone":{"numParallelCollections":0}}`, wantErr: "clone.numParallelCollections must be between 1 and 64"},
		{name: "rejects trailing data", raw: `{} {}`, wantErr: "unexpected data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := decodeOptions(json.RawMessage(test.raw))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				if engine.KindOf(err) != engine.KindInvalidRequest {
					t.Fatalf("kind = %q", engine.KindOf(err))
				}
				if strings.Contains(err.Error(), "s3cret") {
					t.Fatal("error echoes input")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, options)
		})
	}
}

func TestParseMongoURI(t *testing.T) {
	tests := []struct {
		raw, want, display string
		wantErr            bool
	}{
		{raw: "mongodb://u:p@h1:1,h2:2/db?replicaSet=rs0", want: "mongodb://u:p@h1:1,h2:2/db?replicaSet=rs0", display: "h1:1,h2:2"},
		{raw: "mongodb://h1?tls=true", want: "mongodb://h1/?tls=true", display: "h1"},
		{raw: "mongodb://u:p@h:27017:27017/admin", want: "mongodb://u:p@h:27017/admin", display: "h:27017"},
		{raw: "mongodb+srv://u:p@cluster.example.com/", want: "mongodb+srv://u:p@cluster.example.com/", display: "mongodb+srv://cluster.example.com"},
		{raw: "postgres://h", wantErr: true},
		{raw: "mongodb://u:p@/db", wantErr: true},
		{raw: "mongodb+srv://a,b", wantErr: true},
	}
	for _, test := range tests {
		uri, err := parseMongoURI(test.raw)
		if test.wantErr {
			if err == nil {
				t.Errorf("parse(%q) succeeded", test.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parse(%q): %v", test.raw, err)
		}
		if uri.String() != test.want || uri.display() != test.display {
			t.Errorf("parse(%q) = %q (display %q), want %q (display %q)", test.raw, uri.String(), uri.display(), test.want, test.display)
		}
	}
}

func TestMongoURIOptionsAndCredentials(t *testing.T) {
	uri, _ := parseMongoURI("mongodb://h1")
	uri = uri.withCredentials("user", "p@ss/word").withOption("tls", "true").withOption("tlsCAFile", "/x/ca.crt")
	if got, want := uri.String(), "mongodb://user:p%40ss%2Fword@h1/?tls=true&tlsCAFile=%2Fx%2Fca.crt"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !uri.hasOption("TLS") {
		t.Fatal("option lookup must be case-insensitive")
	}
	explicit, _ := parseMongoURI("mongodb://keep:me@h1")
	if got := explicit.withCredentials("other", "x").String(); got != "mongodb://keep:me@h1" {
		t.Fatalf("explicit credentials were replaced: %q", got)
	}
}

func TestPrepare(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{"includeNamespaces":["shop.*"]}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	if worker.Image != testRunner.Image || worker.Port != workerPort || worker.HealthPath != "/metrics" {
		t.Fatalf("unexpected worker basics: %+v", worker)
	}
	if worker.Bootstrap == nil || worker.Bootstrap.Command[0] != "/usr/bin/pcsm" || worker.Bootstrap.Args[0] != "reset" {
		t.Fatalf("PCSM workers must reset leftover target state once: %+v", worker.Bootstrap)
	}
	if worker.Env["PCSM_LISTEN_HOST"] != "0.0.0.0" || worker.Env["PCSM_GROUP_NAME"] != "mig-abc" {
		t.Fatalf("unexpected env: %v", worker.Env)
	}
	if got := string(worker.SecretData[secretKeySourceURI]); got != "mongodb://app:s3cret@db1:27017,db2:27017/?replicaSet=rs0" {
		t.Fatalf("source URI = %q", got)
	}
	wantTarget := "mongodb://databaseAdmin:t%40rget@orders-rs0.shop.svc.cluster.local:27017/admin?tls=true&tlsCAFile=%2Fetc%2Fmigration%2Fsecrets%2Ftarget-ca.crt"
	if got := string(worker.SecretData[secretKeyTargetURI]); got != wantTarget {
		t.Fatalf("target URI = %q, want %q", got, wantTarget)
	}
	if worker.SecretFiles[secretKeyTargetCA] != secretKeyTargetCA {
		t.Fatalf("target CA not mounted: %v", worker.SecretFiles)
	}
	if _, mounted := worker.SecretFiles[secretKeySourceCA]; mounted {
		t.Fatal("source CA mounted without a certificate")
	}
	for _, value := range worker.Env {
		if strings.Contains(value, "s3cret") {
			t.Fatal("credentials leaked into plain env")
		}
	}
	if strings.Contains(string(prepared.Snapshot), "s3cret") || !strings.Contains(string(prepared.Snapshot), `"shop.*"`) {
		t.Fatalf("unexpected snapshot: %s", prepared.Snapshot)
	}
	if prepared.SourceDisplay != "db1:27017,db2:27017" {
		t.Fatalf("source display = %q", prepared.SourceDisplay)
	}
}

func TestPrepareRejectsInvalidEndpoints(t *testing.T) {
	sameTarget := prepareInput("")
	sameTarget.TargetConn = engine.Connection{URI: "mongodb://other:creds@db1:27017,db2:27017/admin"}
	badSource := prepareInput("")
	badSource.SourceConn.URI = "mysql://app:s3cret@db1"

	for name, input := range map[string]engine.PrepareInput{"same deployment": sameTarget, "bad scheme": badSource} {
		_, err := New().Prepare(input)
		if engine.KindOf(err) != engine.KindInvalidRequest {
			t.Fatalf("%s: error = %v", name, err)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("%s: error leaks credentials", name)
		}
	}
}

func TestMapStatus(t *testing.T) {
	tests := []struct {
		body  string
		phase engine.Phase
		ready bool
	}{
		{`{"ok":true,"state":"idle"}`, engine.PhaseStarting, false},
		{`{"ok":true,"state":"running","initialSync":{"cloneCompleted":false,"estimatedCloneSizeBytes":100,"clonedSizeBytes":40}}`, engine.PhaseFullLoad, false},
		{`{"ok":true,"state":"running","initialSync":{"cloneCompleted":true}}`, engine.PhaseCatchingUp, false},
		{`{"ok":true,"state":"running","lagTimeSeconds":2,"initialSync":{"cloneCompleted":true,"completed":true}}`, engine.PhaseReplicating, true},
		{`{"ok":true,"state":"paused","initialSync":{"cloneCompleted":true,"completed":true}}`, engine.PhasePaused, true},
		{`{"ok":true,"state":"paused","initialSync":{"cloneCompleted":true}}`, engine.PhasePaused, false},
		{`{"ok":true,"state":"finalizing"}`, engine.PhaseFinalizing, false},
		{`{"ok":true,"state":"finalized"}`, engine.PhaseCompleted, false},
		{`{"ok":false,"state":"failed","error":"boom"}`, engine.PhaseFailed, false},
	}
	for _, test := range tests {
		var response statusResponse
		if err := json.Unmarshal([]byte(test.body), &response); err != nil {
			t.Fatal(err)
		}
		status, err := mapStatus(response)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase != test.phase || status.Progress.ReadyForCutover != test.ready {
			t.Errorf("%s → phase %q ready %v, want %q ready %v", test.body, status.Phase, status.Progress.ReadyForCutover, test.phase, test.ready)
		}
	}
	if _, err := mapStatus(statusResponse{State: "exploded"}); engine.KindOf(err) != engine.KindUnavailable {
		t.Fatalf("unknown state error = %v", err)
	}
}

type recordedRequest struct {
	method, path, body string
}

func fakePCSM(t *testing.T, handler func(path string) (int, string)) (engine.Controller, *[]recordedRequest) {
	t.Helper()
	var requests []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, recordedRequest{r.Method, r.URL.Path, string(body)})
		status, payload := handler(r.URL.Path)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)
	return New().Controller(server.URL), &requests
}

func TestControllerStartSendsPCSMFieldNames(t *testing.T) {
	controller, requests := fakePCSM(t, func(string) (int, string) { return http.StatusOK, `{"ok":true}` })
	options := json.RawMessage(`{"includeNamespaces":["shop.*"],"pauseOnInitialSync":true,"clone":{"numReadWorkers":4},"replication":{"numWorkers":8}}`)
	if err := controller.Start(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte((*requests)[0].body), &sent); err != nil {
		t.Fatal(err)
	}
	if (*requests)[0].path != "/start" || sent["cloneNumReadWorkers"] != float64(4) || sent["replNumWorkers"] != float64(8) || sent["pauseOnInitialSync"] != true {
		t.Fatalf("unexpected start request %s %v", (*requests)[0].path, sent)
	}
	if _, present := sent["cloneNumInsertWorkers"]; present {
		t.Fatal("unset tuning must be omitted so PCSM applies its defaults")
	}
}

func TestControllerErrorKinds(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		call   func(engine.Controller) error
		want   engine.Kind
	}{
		{"rejected action is a conflict", 200, `{"ok":false,"error":"cannot pause: not running"}`, func(c engine.Controller) error { return c.Pause(context.Background()) }, engine.KindConflict},
		{"standby is unavailable", 409, `{"ok":false,"error":"not_active"}`, func(c engine.Controller) error { return c.Cutover(context.Background()) }, engine.KindUnavailable},
		{"server error is unavailable", 500, `oops`, func(c engine.Controller) error { _, err := c.Status(context.Background()); return err }, engine.KindUnavailable},
		{"already running start is success", 200, `{"ok":false,"error":"already running"}`, func(c engine.Controller) error { return c.Start(context.Background(), nil) }, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, _ := fakePCSM(t, func(string) (int, string) { return test.status, test.body })
			err := test.call(controller)
			if test.want == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if engine.KindOf(err) != test.want {
				t.Fatalf("error = %v, kind %q, want %q", err, engine.KindOf(err), test.want)
			}
		})
	}
}

func TestControllerResumeFromFailure(t *testing.T) {
	for state, fromFailure := range map[string]bool{"failed": true, "paused": false} {
		controller, requests := fakePCSM(t, func(path string) (int, string) {
			if path == "/status" {
				return http.StatusOK, `{"ok":true,"state":"` + state + `"}`
			}
			return http.StatusOK, `{"ok":true}`
		})
		if err := controller.Resume(context.Background()); err != nil {
			t.Fatal(err)
		}
		last := (*requests)[len(*requests)-1]
		wantBody := `{"fromFailure":false}`
		if fromFailure {
			wantBody = `{"fromFailure":true}`
		}
		if last.path != "/resume" || last.body != wantBody {
			t.Fatalf("state %s: got %s %s, want body %s", state, last.path, last.body, wantBody)
		}
	}
}
