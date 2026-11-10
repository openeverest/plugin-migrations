package tidbdm

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

// tidbTargetURI is what provider-tidb publishes without client TLS.
const tidbTargetURI = "mysql://root:p%3Fss@orders-tidb.shop:4000/?tls=false"

func prepareInput(options string) engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID: "mig-ab12cd34",
		Source:      engine.EndpointInfo{DatabaseType: "mysql"},
		Target:      engine.EndpointInfo{DatabaseType: "mysql", Provider: "tidb"},
		SourceConn:  engine.Connection{URI: "mysql://dm:s3cret@legacy-db:3306/shop"},
		TargetConn:  engine.Connection{URI: tidbTargetURI, Username: "root", Password: "p?ss"},
		Options:     json.RawMessage(options),
		Runner:      engine.RunnerConfig{Image: "ghcr.io/openeverest/plugin-migrations-tidb-dm:dev", ImagePullPolicy: "IfNotPresent"},
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

func TestDescribeAndSupports(t *testing.T) {
	descriptor := New().Describe()
	if descriptor.ID != ID || !descriptor.CanPause || len(descriptor.TargetProviders) != 1 || descriptor.TargetProviders[0] != "tidb" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	tidb := engine.EndpointInfo{DatabaseType: "mysql", Provider: "tidb"}
	for _, tc := range []struct {
		source engine.EndpointInfo
		want   bool
	}{
		{engine.EndpointInfo{DatabaseType: "mysql"}, true},
		{engine.EndpointInfo{DatabaseType: "mysql", Provider: "percona-xtradb-cluster"}, true},
		{engine.EndpointInfo{DatabaseType: "mysql", Provider: "mariadb"}, false},
		{engine.EndpointInfo{DatabaseType: "mysql", Provider: "tidb"}, false},
		{engine.EndpointInfo{DatabaseType: "postgresql"}, false},
	} {
		if got := New().Supports(tc.source, tidb); got != tc.want {
			t.Errorf("Supports(%+v) = %v", tc.source, got)
		}
	}
}

func TestPrepareBuildsWorker(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{"databases":["shop","crm"],"excludeTables":["shop.audit"],"storageGiB":25}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	if worker.Port != workerPort || worker.HealthPath != "/healthz" || worker.Env["MIGRATION_NAME"] != "mig_ab12cd34" ||
		worker.Env["DATA_DIR"] != "/data" || worker.Bootstrap != nil {
		t.Fatalf("worker = %+v", worker)
	}
	if v := worker.Volume; v == nil || v.SizeGiB != 25 || v.MountPath != "/data" || v.FSGroup != workerGroup {
		t.Fatalf("volume = %+v", worker.Volume)
	}
	if worker.SecretEnv["SOURCE_URI"] != secretKeySourceURI || worker.SecretEnv["TARGET_URI"] != secretKeyTargetURI || len(worker.SecretFiles) != 0 {
		t.Fatalf("secret env = %v, files = %v", worker.SecretEnv, worker.SecretFiles)
	}
	source := workerURI(t, prepared, secretKeySourceURI)
	if password, _ := source.User.Password(); source.User.Username() != "dm" || password != "s3cret" || source.Host != "legacy-db:3306" ||
		source.Query().Get("ssl-mode") != "PREFERRED" {
		t.Fatalf("source URI = %s", source.Redacted())
	}
	target := workerURI(t, prepared, secretKeyTargetURI)
	if password, _ := target.User.Password(); password != "p?ss" || target.Host != "orders-tidb.shop:4000" || target.Query().Get("ssl-mode") != "DISABLED" {
		t.Fatalf("target URI = %s", target.Redacted())
	}
	var snapshot Options
	if err := json.Unmarshal(prepared.Snapshot, &snapshot); err != nil || strings.Join(snapshot.Databases, ",") != "shop,crm" ||
		snapshot.CopyThreads != defaultCopyThreads || snapshot.SyncThreads != defaultSyncThreads || snapshot.StorageGiB != 25 ||
		strings.Contains(string(prepared.Snapshot), "s3cret") {
		t.Fatalf("snapshot = %s, %v", prepared.Snapshot, err)
	}
	if prepared.SourceDisplay != "legacy-db:3306" {
		t.Fatalf("source display = %q", prepared.SourceDisplay)
	}
}

func TestPrepareDefaultsAndDatabaseSelection(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{}`))
	if err != nil || !strings.Contains(string(prepared.Snapshot), `"databases":["shop"]`) || prepared.Worker.Volume.SizeGiB != defaultStorageGiB {
		t.Fatalf("an external source defaults to its URI database and %d GiB: %s, %v", defaultStorageGiB, prepared.Snapshot, err)
	}
	instanceSource := prepareInput(`{}`)
	instanceSource.Source.Provider = "percona-xtradb-cluster"
	if _, err := New().Prepare(instanceSource); engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), "choose the databases") {
		t.Fatalf("an instance source must name its databases: %v", err)
	}
}

func TestPrepareRejectsInvalidOptions(t *testing.T) {
	for _, options := range []string{
		`{"databases":["mysql"]}`,
		`{"databases":["dm_meta"]}`,
		`{"databases":["openeverest_mig_x"]}`,
		`{"databases":["shop"],"excludeTables":["crm.t"]}`,
		`{"databases":["shop"],"excludeTables":["t"]}`,
		`{"databases":["sh op"]}`,
		`{"databases":["shop"],"storageGiB":-1}`,
		`{"databases":["shop"],"storageGiB":20000}`,
		`{"databases":["shop"],"copyThreads":33}`,
		`{"databases":["shop"],"syncThreads":65}`,
		`{"databases":["shop"],"unknown":true}`,
		`{"databases":["shop"]} {}`,
	} {
		if _, err := New().Prepare(prepareInput(options)); engine.KindOf(err) != engine.KindInvalidRequest {
			t.Errorf("%s: error = %v", options, err)
		}
	}
}

func TestPrepareTLS(t *testing.T) {
	// DM cannot require TLS without verifying the server.
	input := prepareInput(`{}`)
	input.SourceConn.URI = "mysql://dm:s3cret@legacy-db:3306/shop?ssl-mode=REQUIRED"
	if _, err := New().Prepare(input); engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), "DM does not support") {
		t.Fatalf("REQUIRED without a CA: %v", err)
	}
	// A CA turns it into verification.
	input.SourceConn.CACert = "PEM"
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	source := workerURI(t, prepared, secretKeySourceURI)
	if source.Query().Get("ssl-mode") != "VERIFY_CA" || source.Query().Get("ssl-ca") != engine.SecretMountPath+"/"+secretKeySourceCA {
		t.Fatalf("source URI = %s", source.Redacted())
	}
	// provider-tidb with client TLS publishes tls=true and ca.crt.
	input = prepareInput(`{}`)
	input.TargetConn.URI = "mysql://orders-tidb.shop:4000/?tls=true"
	if _, err := New().Prepare(input); engine.KindOf(err) != engine.KindInvalidRequest {
		t.Fatalf("tls=true without a CA: %v", err)
	}
	input.TargetConn.CACert = "PEM"
	prepared, err = New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if target := workerURI(t, prepared, secretKeyTargetURI); target.Query().Get("ssl-mode") != "VERIFY_IDENTITY" {
		t.Fatalf("target URI = %s", target.Redacted())
	}
}

func TestPrepareRejectsSameServer(t *testing.T) {
	input := prepareInput(`{}`)
	input.SourceConn.URI = "mysql://root:x@ORDERS-TIDB.shop:4000/shop"
	if _, err := New().Prepare(input); engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), "different servers") {
		t.Fatalf("same server: %v", err)
	}
}

func TestStartBody(t *testing.T) {
	body, err := startBody(json.RawMessage(`{"databases":["shop"]}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	if !strings.Contains(string(encoded), `"options":{"databases":["shop"],"excludeTables":[],"recreateDatabases":false,"storageGiB":10,"copyThreads":4,"syncThreads":16}`) {
		t.Fatalf("start body = %s", encoded)
	}
}
