package mysqlreplication

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

// pxcTargetURI is what provider-percona-xtradb-cluster publishes.
const pxcTargetURI = "mysql://root:p%3Fss@orders-haproxy.shop:3306/?tls=false"

func prepareInput(options string) engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID: "mig-ab12cd34",
		Source:      engine.EndpointInfo{DatabaseType: "mysql"},
		Target:      engine.EndpointInfo{DatabaseType: "mysql", Provider: "percona-xtradb-cluster"},
		SourceConn:  engine.Connection{URI: "mysql://repl:s3cret@legacy-db:3306/shop?ssl-mode=REQUIRED"},
		TargetConn:  engine.Connection{URI: pxcTargetURI, Username: "root", Password: "p?ss"},
		Options:     json.RawMessage(options),
		Runner:      engine.RunnerConfig{Image: "ghcr.io/openeverest/plugin-migrations-mysql-replication:dev", ImagePullPolicy: "IfNotPresent"},
	}
}

func workerURI(t *testing.T, prepared engine.Prepared, key string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(string(prepared.Worker.SecretData[key]))
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return parsed
}

func TestPrepareBuildsWorker(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{"databases":["shop","crm"],"excludeTables":["shop.audit"]}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	if worker.Port != workerPort || worker.HealthPath != "/healthz" || worker.Env["REPLICATION_NAME"] != "mig_ab12cd34" || worker.Bootstrap != nil {
		t.Fatalf("worker = %+v", worker)
	}
	if worker.SecretEnv["SOURCE_URI"] != secretKeySourceURI || worker.SecretEnv["TARGET_URI"] != secretKeyTargetURI || len(worker.SecretFiles) != 0 {
		t.Fatalf("secret env = %v, files = %v", worker.SecretEnv, worker.SecretFiles)
	}
	source := workerURI(t, prepared, secretKeySourceURI)
	if password, _ := source.User.Password(); source.User.Username() != "repl" || password != "s3cret" || source.Host != "legacy-db:3306" ||
		source.Query().Get("ssl-mode") != "REQUIRED" || source.Path != "/" {
		t.Fatalf("source URI = %s", source.Redacted())
	}
	target := workerURI(t, prepared, secretKeyTargetURI)
	if password, _ := target.User.Password(); password != "p?ss" || target.Host != "orders-haproxy.shop:3306" || target.Query().Get("ssl-mode") != "DISABLED" {
		t.Fatalf("target URI = %s", target.Redacted())
	}
	var snapshot Options
	if err := json.Unmarshal(prepared.Snapshot, &snapshot); err != nil || strings.Join(snapshot.Databases, ",") != "shop,crm" ||
		snapshot.CopyThreads != defaultCopyThreads || strings.Contains(string(prepared.Snapshot), "s3cret") {
		t.Fatalf("snapshot = %s, %v", prepared.Snapshot, err)
	}
	if prepared.SourceDisplay != "legacy-db:3306" {
		t.Fatalf("source display = %q", prepared.SourceDisplay)
	}
}

func TestPrepareDatabaseSelection(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{}`))
	if err != nil || !strings.Contains(string(prepared.Snapshot), `"databases":["shop"]`) {
		t.Fatalf("an external source defaults to its URI database: %s, %v", prepared.Snapshot, err)
	}
	instanceSource := prepareInput(`{}`)
	instanceSource.Source.Provider = "percona-xtradb-cluster"
	if _, err := New().Prepare(instanceSource); engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), "choose the databases") {
		t.Fatalf("an instance source must name its databases: %v", err)
	}
	noDatabase := prepareInput(`{}`)
	noDatabase.SourceConn.URI = "mysql://repl:s3cret@legacy-db/"
	if _, err := New().Prepare(noDatabase); engine.KindOf(err) != engine.KindInvalidRequest {
		t.Fatalf("a migration without databases must be rejected: %v", err)
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	cases := map[string]func(*engine.PrepareInput){
		"wrong scheme":        func(in *engine.PrepareInput) { in.SourceConn.URI = "postgres://db:5432/shop" },
		"no user":             func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://legacy-db/shop" },
		"multi host":          func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://u:p@db1,db2/shop" },
		"same server":         func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://u:p@ORDERS-haproxy.shop/shop" },
		"verify without CA":   func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://u:p@db1/shop?ssl-mode=VERIFY_CA" },
		"tls=true without CA": func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://u:p@db1/shop?tls=true" },
		"unknown ssl-mode":    func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://u:p@db1/shop?ssl-mode=ALWAYS" },
		"system database":     func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{"databases":["mysql"]}`) },
		"excluded table elsewhere": func(in *engine.PrepareInput) {
			in.Options = json.RawMessage(`{"databases":["shop"],"excludeTables":["crm.audit"]}`)
		},
	}
	for name, change := range cases {
		input := prepareInput(`{"databases":["shop"]}`)
		change(&input)
		_, err := New().Prepare(input)
		if engine.KindOf(err) != engine.KindInvalidRequest {
			t.Errorf("%s: error = %v", name, err)
		} else if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "u:p@") {
			t.Errorf("%s: error leaks credentials: %v", name, err)
		}
	}
}

func TestPrepareMountsCAs(t *testing.T) {
	input := prepareInput(`{"databases":["shop"]}`)
	input.SourceConn.CACert = "-----BEGIN CERTIFICATE-----\nsource\n-----END CERTIFICATE-----\n"
	input.TargetConn = engine.Connection{URI: "mysql://orders-mysql.shop:3306/", Username: "root", Password: "pw", CACert: "target-ca"}
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Worker.SecretFiles[secretKeySourceCA] != secretKeySourceCA || string(prepared.Worker.SecretData[secretKeyTargetCA]) != "target-ca" {
		t.Fatalf("secret files = %v", prepared.Worker.SecretFiles)
	}
	source := workerURI(t, prepared, secretKeySourceURI)
	if source.Query().Get("ssl-ca") != engine.SecretMountPath+"/"+secretKeySourceCA || source.Query().Get("ssl-mode") != "VERIFY_CA" {
		t.Fatalf("source URI = %s", source.Redacted())
	}
	identity := prepareInput(`{"databases":["shop"]}`)
	identity.SourceConn = engine.Connection{URI: "mysql://u:p@db1/shop?ssl-mode=VERIFY_IDENTITY", CACert: "ca"}
	prepared, err = New().Prepare(identity)
	if err != nil || workerURI(t, prepared, secretKeySourceURI).Query().Get("ssl-mode") != "VERIFY_IDENTITY" {
		t.Fatalf("an explicit VERIFY_IDENTITY must be kept: %v", err)
	}
	disabled := prepareInput(`{"databases":["shop"]}`)
	disabled.SourceConn = engine.Connection{URI: "mysql://u:p@db1/shop?tls=false", CACert: "ca"}
	prepared, err = New().Prepare(disabled)
	if source := workerURI(t, prepared, secretKeySourceURI); err != nil || source.Query().Get("ssl-mode") != "DISABLED" || source.Query().Has("ssl-ca") {
		t.Fatalf("disabled TLS must stay disabled: %v", err)
	}
}

func TestDecodeOptions(t *testing.T) {
	options, err := decodeOptions(json.RawMessage(` {"databases":[" shop ","shop","crm"],"excludeTables":["shop.audit"],"recreateDatabases":true,"copyThreads":8} `))
	if err != nil || strings.Join(options.Databases, ",") != "shop,crm" || !options.RecreateDatabases || options.CopyThreads != 8 {
		t.Fatalf("options = %+v, %v", options, err)
	}
	for _, raw := range []string{
		`{"databases":["shop"],"unknown":1}`,
		`{"databases":"shop"}`,
		`{"databases":["shop"]} {}`,
		`{"databases":["shop.orders"]}`,
		"{\"databases\":[\"shop`\"]}",
		`{"databases":["Performance_Schema"]}`,
		`{"databases":["openeverest_migration"]}`,
		`{"excludeTables":["audit"]}`,
		`{"copyThreads":64}`,
	} {
		if _, err := decodeOptions(json.RawMessage(raw)); engine.KindOf(err) != engine.KindInvalidRequest {
			t.Errorf("%s: error = %v", raw, err)
		}
	}
}

func TestControllerSendsOptions(t *testing.T) {
	var started struct {
		Options Options `json:"options"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &started)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	controller := New().Controller(server.URL)
	if err := controller.Start(context.Background(), json.RawMessage(`{"databases":["shop"],"excludeTables":["shop.audit"],"copyThreads":2}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(started.Options.Databases, ",") != "shop" || started.Options.CopyThreads != 2 || started.Options.ExcludeTables[0] != "shop.audit" {
		t.Fatalf("worker options = %+v", started.Options)
	}
	if _, ok := controller.(engine.Teardowner); !ok {
		t.Fatal("the controller must tear down the replication channel")
	}
}
