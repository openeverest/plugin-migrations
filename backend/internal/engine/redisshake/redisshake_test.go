package redisshake

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

// valkeyInstance is what provider-valkey publishes for a TLS instance with
// client certificates.
func valkeyInstance(host string) engine.Connection {
	return engine.Connection{
		URI: "rediss://default:p%40ss@" + host + ":6379", Username: "default", Password: "p@ss", CACert: host + "-ca",
		Properties: map[string]string{propertySSL: "true", propertyClientCert: host + "-cert", propertyClientKey: host + "-key"},
	}
}

func prepareInput(options string) engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID: "mig-ab12cd34",
		Source:      engine.EndpointInfo{DatabaseType: "valkey", Provider: "valkey"},
		Target:      engine.EndpointInfo{DatabaseType: "valkey", Provider: "valkey"},
		SourceConn:  valkeyInstance("valkey-legacy.legacy.svc.cluster.local"),
		TargetConn:  valkeyInstance("valkey-orders-primary.shop.svc.cluster.local"),
		Options:     json.RawMessage(options),
		Runner:      engine.RunnerConfig{Image: "ghcr.io/openeverest/plugin-migrations-redis-shake:dev", ImagePullPolicy: "IfNotPresent"},
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
	prepared, err := New().Prepare(prepareInput(`{"excludeKeyPrefixes":["cache:","cache:"]}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	if worker.Port != workerPort || worker.HealthPath != healthPath || worker.Env["MIGRATION_NAME"] != "mig_ab12cd34" || worker.Bootstrap != nil {
		t.Fatalf("worker = %+v", worker)
	}
	for key, host := range map[string]string{
		secretKeySourceURI: "valkey-legacy.legacy.svc.cluster.local", secretKeyTargetURI: "valkey-orders-primary.shop.svc.cluster.local",
	} {
		uri := workerURI(t, prepared, key)
		password, _ := uri.User.Password()
		side := strings.TrimSuffix(key, "-uri")
		query := uri.Query()
		if uri.Scheme != "rediss" || uri.Hostname() != host || uri.Port() != "6379" || uri.User.Username() != "default" || password != "p@ss" ||
			query.Get("ca") != engine.SecretMountPath+"/"+side+"-ca.crt" || query.Get("cert") != engine.SecretMountPath+"/"+side+"-tls.crt" ||
			query.Get("key") != engine.SecretMountPath+"/"+side+"-tls.key" {
			t.Errorf("%s = %s", key, uri.Redacted())
		}
	}
	if string(worker.SecretData[secretKeyTargetKey]) != "valkey-orders-primary.shop.svc.cluster.local-key" || len(worker.SecretFiles) != 6 {
		t.Fatalf("secret files = %v", worker.SecretFiles)
	}
	if string(prepared.Snapshot) != `{"flushTarget":false,"excludeKeyPrefixes":["cache:"]}` {
		t.Fatalf("snapshot = %s", prepared.Snapshot)
	}
}

func TestPrepareExternalSource(t *testing.T) {
	input := prepareInput(`{"flushTarget":true}`)
	input.Source = engine.EndpointInfo{DatabaseType: "valkey"}
	input.SourceConn = engine.Connection{URI: "redis://:s3cret@legacy-redis:6380/0"}
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	source := workerURI(t, prepared, secretKeySourceURI)
	if password, _ := source.User.Password(); source.Scheme != "redis" || source.Host != "legacy-redis:6380" || password != "s3cret" || source.RawQuery != "" {
		t.Fatalf("source = %s", source.Redacted())
	}
	if prepared.SourceDisplay != "legacy-redis:6380" || strings.Contains(string(prepared.Snapshot), "s3cret") {
		t.Fatalf("display = %q, snapshot = %s", prepared.SourceDisplay, prepared.Snapshot)
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	cases := map[string]func(*engine.PrepareInput){
		"wrong scheme":   func(in *engine.PrepareInput) { in.SourceConn = engine.Connection{URI: "mysql://u:s3cret@db/shop"} },
		"no host":        func(in *engine.PrepareInput) { in.SourceConn = engine.Connection{URI: "redis://:s3cret@/"} },
		"multi host":     func(in *engine.PrepareInput) { in.SourceConn = engine.Connection{URI: "redis://:s3cret@a:1,b:2"} },
		"other database": func(in *engine.PrepareInput) { in.SourceConn = engine.Connection{URI: "redis://:s3cret@db/3"} },
		"CA without TLS": func(in *engine.PrepareInput) {
			in.SourceConn = engine.Connection{URI: "redis://:s3cret@db", CACert: "pem"}
		},
		"same server":      func(in *engine.PrepareInput) { in.SourceConn = in.TargetConn },
		"unknown option":   func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{"databases":["0"]}`) },
		"empty prefix":     func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{"excludeKeyPrefixes":[""]}`) },
		"own keys":         func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{"excludeKeyPrefixes":["__"]}`) },
		"unprintable":      func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{"excludeKeyPrefixes":["a\u0000"]}`) },
		"trailing content": func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{} {}`) },
	}
	for name, change := range cases {
		input := prepareInput(`{}`)
		change(&input)
		_, err := New().Prepare(input)
		if engine.KindOf(err) != engine.KindInvalidRequest {
			t.Errorf("%s: error = %v", name, err)
		} else if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "p@ss") {
			t.Errorf("%s: error leaks credentials: %v", name, err)
		}
	}
}

func TestDescribeAndSupports(t *testing.T) {
	descriptor := New().Describe()
	if descriptor.ID != ID || descriptor.CanPause || strings.Join(descriptor.TargetProviders, ",") != "valkey" ||
		strings.Join(descriptor.SourceDatabaseTypes, ",") != "valkey" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	target := engine.EndpointInfo{DatabaseType: "valkey", Provider: "valkey"}
	for provider, want := range map[string]bool{"": true, "valkey": true, "percona-server-mongodb": false} {
		if got := New().Supports(engine.EndpointInfo{DatabaseType: "valkey", Provider: provider}, target); got != want {
			t.Errorf("source provider %q: got %v", provider, got)
		}
	}
	if New().Supports(engine.EndpointInfo{DatabaseType: "mysql"}, target) {
		t.Error("MySQL sources are not supported")
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
	if err := controller.Start(context.Background(), json.RawMessage(`{"flushTarget":true,"excludeKeyPrefixes":["tmp:"]}`)); err != nil {
		t.Fatal(err)
	}
	if !started.Options.FlushTarget || strings.Join(started.Options.ExcludeKeyPrefixes, ",") != "tmp:" {
		t.Fatalf("worker options = %+v", started.Options)
	}
	if _, ok := controller.(engine.Teardowner); !ok {
		t.Fatal("the controller must remove the worker's keys on delete")
	}
}
