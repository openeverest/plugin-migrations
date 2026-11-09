package mssqllogshipping

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

// mssqlInstance is what provider-mssql publishes: sa and a URI without TLS
// settings or a CA.
func mssqlInstance(host string) engine.Connection {
	return engine.Connection{URI: "sqlserver://sa:p%23ss@" + host + ":1433", Username: "sa", Password: "p#ss"}
}

func storage() *engine.BackupStorage {
	return &engine.BackupStorage{
		Namespace: "shop", Name: "minio", EndpointURL: "https://minio.minio.svc.cluster.local", Bucket: "migrations",
		Region: "us-east-1", ForcePathStyle: true, VerifyTLS: true, AccessKeyID: "key", SecretAccessKey: "s3cret", CACert: "S3-CA",
	}
}

func prepareInput(options string) engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID:   "mig-ab12cd34",
		Source:        engine.EndpointInfo{DatabaseType: "mssql", Provider: "mssql"},
		Target:        engine.EndpointInfo{DatabaseType: "mssql", Provider: "mssql"},
		SourceConn:    mssqlInstance("legacy.legacy.svc.cluster.local"),
		TargetConn:    mssqlInstance("orders-primary.shop.svc.cluster.local"),
		Options:       json.RawMessage(options),
		Runner:        engine.RunnerConfig{Image: "ghcr.io/openeverest/plugin-migrations-mssql-log-shipping:dev", ImagePullPolicy: "IfNotPresent"},
		BackupStorage: storage(),
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

func TestPrepareInstances(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{"databases":["shop","Shop","orders"]}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	if worker.Port != workerPort || worker.HealthPath != healthPath || worker.Env["MIGRATION_NAME"] != "mig-ab12cd34" || worker.Bootstrap != nil {
		t.Fatalf("worker = %+v", worker)
	}
	if worker.Env["S3_ENDPOINT"] != "https://minio.minio.svc.cluster.local" || worker.Env["S3_BUCKET"] != "migrations" ||
		worker.Env["S3_REGION"] != "us-east-1" || worker.Env["S3_FORCE_PATH_STYLE"] != "true" ||
		worker.Env["S3_CA_FILE"] != engine.SecretMountPath+"/s3-ca.crt" {
		t.Fatalf("env = %v", worker.Env)
	}
	if string(worker.SecretData[secretKeyS3Key]) != "key" || string(worker.SecretData[secretKeyS3Secret]) != "s3cret" ||
		worker.SecretEnv["S3_SECRET_ACCESS_KEY"] != secretKeyS3Secret || worker.SecretFiles[secretKeyS3CA] != "s3-ca.crt" {
		t.Fatalf("secrets = %v %v", worker.SecretEnv, worker.SecretFiles)
	}
	for key, host := range map[string]string{
		secretKeySourceURI: "legacy.legacy.svc.cluster.local", secretKeyTargetURI: "orders-primary.shop.svc.cluster.local",
	} {
		uri := workerURI(t, prepared, key)
		password, _ := uri.User.Password()
		query := uri.Query()
		if uri.Scheme != "sqlserver" || uri.Hostname() != host || uri.Port() != "1433" || uri.User.Username() != "sa" || password != "p#ss" ||
			query.Get("encrypt") != "true" || query.Get("TrustServerCertificate") != "true" || query.Has("certificate") {
			t.Errorf("%s = %s", key, uri.Redacted())
		}
	}
	if string(prepared.Snapshot) != `{"databases":["shop","orders"],"recreateDatabases":false,"logBackupIntervalSeconds":10,`+
		`"copyLogins":true,"leaveSourceRestoring":false,"ignoreRecentLogBackups":false}` {
		t.Fatalf("snapshot = %s", prepared.Snapshot)
	}
	if strings.Contains(string(prepared.Snapshot), "s3cret") || prepared.SourceDisplay != "legacy.legacy.svc.cluster.local:1433" {
		t.Fatalf("snapshot = %s, display = %s", prepared.Snapshot, prepared.SourceDisplay)
	}
}

func TestPrepareExternalSource(t *testing.T) {
	input := prepareInput(`{"copyLogins":false,"logBackupIntervalSeconds":30}`)
	input.Source = engine.EndpointInfo{DatabaseType: "mssql"}
	input.SourceConn = engine.Connection{URI: "sqlserver://migrator:s3cret@legacy-sql:1500?database=shop&encrypt=strict", CACert: "SRC-CA"}
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	uri := workerURI(t, prepared, secretKeySourceURI)
	query := uri.Query()
	if uri.Host != "legacy-sql:1500" || uri.User.Username() != "migrator" || query.Get("encrypt") != "strict" ||
		query.Get("certificate") != engine.SecretMountPath+"/source-ca.crt" || query.Has("TrustServerCertificate") {
		t.Fatalf("source = %s", uri.Redacted())
	}
	if string(prepared.Worker.SecretData[secretKeySourceCA]) != "SRC-CA" {
		t.Fatal("source CA not mounted")
	}
	var options Options
	_ = json.Unmarshal(prepared.Snapshot, &options)
	if len(options.Databases) != 1 || options.Databases[0] != "shop" || options.CopyLogins || options.LogBackupIntervalSeconds != 30 {
		t.Fatalf("options = %+v", options)
	}
}

func TestPrepareRejects(t *testing.T) {
	tests := map[string]func(*engine.PrepareInput){
		"no databases":    func(i *engine.PrepareInput) { i.Options = json.RawMessage(`{}`) },
		"system database": func(i *engine.PrepareInput) { i.Options = json.RawMessage(`{"databases":["MSDB"]}`) },
		"bad name":        func(i *engine.PrepareInput) { i.Options = json.RawMessage(`{"databases":["a;b"]}`) },
		"unknown option":  func(i *engine.PrepareInput) { i.Options = json.RawMessage(`{"databases":["a"],"stripes":3}`) },
		"interval too short": func(i *engine.PrepareInput) {
			i.Options = json.RawMessage(`{"databases":["a"],"logBackupIntervalSeconds":1}`)
		},
		"same server": func(i *engine.PrepareInput) {
			i.SourceConn = mssqlInstance("ORDERS-primary.shop.svc.cluster.local")
		},
		"not sqlserver":   func(i *engine.PrepareInput) { i.SourceConn = engine.Connection{URI: "mysql://a:b@h:3306"} },
		"named instance":  func(i *engine.PrepareInput) { i.SourceConn = engine.Connection{URI: "sqlserver://a:b@h/SQLEXPRESS"} },
		"bad encrypt":     func(i *engine.PrepareInput) { i.SourceConn = engine.Connection{URI: "sqlserver://a:b@h?encrypt=maybe"} },
		"no storage":      func(i *engine.PrepareInput) { i.BackupStorage = nil },
		"http storage":    func(i *engine.PrepareInput) { i.BackupStorage.EndpointURL = "http://minio:9000" },
		"storage path":    func(i *engine.PrepareInput) { i.BackupStorage.EndpointURL = "https://minio/bucket" },
		"colon in key ID": func(i *engine.PrepareInput) { i.BackupStorage.AccessKeyID = "a:b" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := prepareInput(`{"databases":["shop"]}`)
			mutate(&input)
			_, err := New().Prepare(input)
			if engine.KindOf(err) != engine.KindInvalidRequest {
				t.Fatalf("error = %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "p#ss")) {
				t.Fatalf("error leaks a credential: %v", err)
			}
		})
	}
}

func TestSupports(t *testing.T) {
	e := New()
	mssql := engine.EndpointInfo{DatabaseType: "mssql", Provider: "mssql"}
	if !e.Supports(engine.EndpointInfo{DatabaseType: "mssql"}, mssql) || !e.Supports(mssql, mssql) {
		t.Fatal("must support external and instance sources")
	}
	if e.Supports(engine.EndpointInfo{DatabaseType: "mssql", Provider: "other"}, mssql) || e.Supports(mssql, engine.EndpointInfo{DatabaseType: "mysql"}) {
		t.Fatal("must reject other providers and types")
	}
	descriptor := e.Describe()
	if !descriptor.RequiresBackupStorage || !descriptor.CanPause || descriptor.TargetProviders[0] != "mssql" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}

func TestControllerStartsWithNormalizedOptions(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	if err := New().Controller(server.URL).Start(context.Background(), json.RawMessage(`{"databases":["shop"]}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"databases":["shop"]`) || !strings.Contains(body, `"copyLogins":true`) || !strings.Contains(body, `"logBackupIntervalSeconds":10`) {
		t.Fatalf("body = %s", body)
	}
}
