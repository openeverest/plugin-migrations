package mysqlreplication

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

// mariadbInstance is what provider-mariadb 0.2.2 publishes for a Galera
// instance with TLS.
func mariadbInstance(host string) engine.Connection {
	return engine.Connection{
		URI: "mysql://everest:apppw@" + host + "-maxscale.shop:3306/everest", Username: "everest", Password: "apppw",
		Properties: map[string]string{
			propertyAdminUsername: "root", propertyAdminPassword: "r00t", propertyAdminHost: host + "-primary.shop.svc.cluster.local",
			propertyAdminPort: "3306", propertyAdminTLS: "true", propertyAdminCA: host + "-ca", propertySkipReplicaStart: "true",
		},
	}
}

func mariadbInput() engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID: "mig-ab12cd34",
		Source:      engine.EndpointInfo{DatabaseType: "mysql", Provider: "mariadb"},
		Target:      engine.EndpointInfo{DatabaseType: "mysql", Provider: "mariadb"},
		SourceConn:  mariadbInstance("legacy"),
		TargetConn:  mariadbInstance("orders"),
		Options:     json.RawMessage(`{"databases":["shop"]}`),
		Runner:      engine.RunnerConfig{Image: "ghcr.io/openeverest/plugin-migrations-mysql-replication:dev", ImagePullPolicy: "IfNotPresent"},
	}
}

func TestMariaDBPrepareUsesAdminConnections(t *testing.T) {
	prepared, err := NewMariaDB().Prepare(mariadbInput())
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	if worker.Env["FLAVOR"] != "mariadb" || worker.Env["TARGET_SKIP_REPLICA_START"] != "true" || worker.Env["REPLICATION_NAME"] != "mig_ab12cd34" {
		t.Fatalf("env = %v", worker.Env)
	}
	for key, host := range map[string]string{secretKeySourceURI: "legacy", secretKeyTargetURI: "orders"} {
		uri := workerURI(t, prepared, key)
		password, _ := uri.User.Password()
		if uri.User.Username() != "root" || password != "r00t" || uri.Host != host+"-primary.shop.svc.cluster.local:3306" ||
			uri.Query().Get("ssl-mode") != "VERIFY_IDENTITY" || uri.Query().Get("ssl-ca") == "" {
			t.Errorf("%s = %s", key, uri.Redacted())
		}
	}
	if string(worker.SecretData[secretKeySourceCA]) != "legacy-ca" || string(worker.SecretData[secretKeyTargetCA]) != "orders-ca" {
		t.Fatalf("CAs = %v", worker.SecretFiles)
	}
	if strings.Contains(string(prepared.Snapshot), "r00t") {
		t.Fatal("the snapshot must not contain credentials")
	}
}

func TestMariaDBPrepareExternalSource(t *testing.T) {
	input := mariadbInput()
	input.Source.Provider = ""
	input.SourceConn = engine.Connection{URI: "mariadb://repl:s3cret@legacy-db:3306/shop?ssl-mode=REQUIRED"}
	input.Options = json.RawMessage(`{}`)
	prepared, err := NewMariaDB().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	source := workerURI(t, prepared, secretKeySourceURI)
	if source.Scheme != "mysql" || source.User.Username() != "repl" || source.Query().Get("ssl-mode") != "REQUIRED" ||
		!strings.Contains(string(prepared.Snapshot), `"databases":["shop"]`) {
		t.Fatalf("source URI = %s, snapshot = %s", source.Redacted(), prepared.Snapshot)
	}
}

func TestMariaDBPrepareRejectsUnsupportedInstances(t *testing.T) {
	withoutProperty := func(property string) func(*engine.PrepareInput) {
		return func(in *engine.PrepareInput) {
			in.TargetConn.Properties = maps.Clone(in.TargetConn.Properties)
			delete(in.TargetConn.Properties, property)
		}
	}
	cases := map[string]struct {
		change func(*engine.PrepareInput)
		want   string
	}{
		"replication topology target": {withoutProperty(propertySkipReplicaStart), "standalone or Galera"},
		"old provider target":         {withoutProperty(propertyAdminPassword), "0.2.2 or later"},
		"old provider source": {func(in *engine.PrepareInput) {
			in.SourceConn = engine.Connection{URI: "mysql://everest:pw@legacy.shop:3306/everest"}
		}, "source instance does not publish an admin connection"},
		"postgres external source": {func(in *engine.PrepareInput) {
			in.Source.Provider, in.SourceConn = "", engine.Connection{URI: "postgres://u:p@db/shop"}
		}, "mariadb:// or mysql://"},
	}
	for name, test := range cases {
		input := mariadbInput()
		test.change(&input)
		_, err := NewMariaDB().Prepare(input)
		if engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v", name, err)
		} else if strings.Contains(err.Error(), "r00t") || strings.Contains(err.Error(), "u:p@") {
			t.Errorf("%s: error leaks credentials: %v", name, err)
		}
	}
}

func TestEnginesSeparateMySQLAndMariaDBSources(t *testing.T) {
	target := engine.EndpointInfo{DatabaseType: "mysql", Provider: "mariadb"}
	for _, test := range []struct {
		engine   engine.Engine
		provider string
		want     bool
	}{
		{NewMariaDB(), "mariadb", true},
		{NewMariaDB(), "", true},
		{NewMariaDB(), "percona-xtradb-cluster", false},
		{New(), "percona-xtradb-cluster", true},
		{New(), "", true},
		{New(), "mariadb", false},
	} {
		source := engine.EndpointInfo{DatabaseType: "mysql", Provider: test.provider}
		if got := test.engine.Supports(source, target); got != test.want {
			t.Errorf("%s with a %q source: got %v", test.engine.ID(), test.provider, got)
		}
	}
	if descriptor := NewMariaDB().Describe(); descriptor.ID != MariaDBID || strings.Join(descriptor.TargetProviders, ",") != "mariadb" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	prepared, err := New().Prepare(prepareInput(`{"databases":["shop"]}`))
	if err != nil || prepared.Worker.Env["FLAVOR"] != "mysql" || prepared.Worker.Env["TARGET_SKIP_REPLICA_START"] != "" {
		t.Fatalf("MySQL worker env = %v, %v", prepared.Worker.Env, err)
	}
}
