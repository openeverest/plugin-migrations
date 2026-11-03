package pgcopydb

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

func prepareInput(options string) engine.PrepareInput {
	return engine.PrepareInput{
		MigrationID: "mig-ab12cd34",
		Source:      engine.EndpointInfo{DatabaseType: "postgresql"},
		Target:      engine.EndpointInfo{DatabaseType: "postgresql", Provider: "provider-percona-postgresql"},
		SourceConn:  engine.Connection{URI: "postgresql://app:s3cr%40t@legacy-db:5432/shop?sslmode=require"},
		TargetConn: engine.Connection{
			URI:      "postgresql://postgres:p%3Fss@orders-pg-primary.shop.svc:5432/postgres?sslmode=require",
			Username: "postgres", Password: "p?ss",
		},
		Options: json.RawMessage(options),
		Runner:  engine.RunnerConfig{Image: "ghcr.io/openeverest/plugin-migrations-pgcopydb:dev", ImagePullPolicy: "IfNotPresent"},
	}
}

func TestPrepareBuildsWorker(t *testing.T) {
	prepared, err := New().Prepare(prepareInput(`{"excludeSchemas":["scratch"],"tableJobs":8}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := prepared.Worker
	secrets := map[string]string{}
	for key, value := range worker.SecretData {
		secrets[key] = string(value)
	}
	if secrets[secretKeySourceURI] != "postgresql://app:s3cr%40t@legacy-db:5432/shop?sslmode=require" {
		t.Fatalf("source URI = %q", secrets[secretKeySourceURI])
	}
	// The database comes from the source URI and keeps its name on the target.
	if !strings.HasPrefix(secrets[secretKeyTargetURI], "postgresql://postgres:p%3Fss@orders-pg-primary.shop.svc:5432/shop?") {
		t.Fatalf("target URI = %q", secrets[secretKeyTargetURI])
	}
	if !strings.Contains(secrets[secretKeyTargetAdminURI], ":5432/postgres?") {
		t.Fatalf("target admin URI must use the maintenance database: %q", secrets[secretKeyTargetAdminURI])
	}
	if worker.Env["SLOT_NAME"] != "pgcopydb_mig_ab12cd34" || worker.Env["TARGET_DATABASE"] != "shop" {
		t.Fatalf("env = %v", worker.Env)
	}
	if worker.SecretEnv["PGCOPYDB_SOURCE_PGURI"] != secretKeySourceURI || worker.HealthPath != "/healthz" {
		t.Fatalf("worker = %+v", worker)
	}
	if prepared.SourceDisplay != "legacy-db:5432/shop" {
		t.Fatalf("display = %q", prepared.SourceDisplay)
	}
	var snapshot Options
	if err := json.Unmarshal(prepared.Snapshot, &snapshot); err != nil || snapshot.Database != "shop" || *snapshot.TableJobs != 8 {
		t.Fatalf("snapshot = %s", prepared.Snapshot)
	}
	for _, field := range []string{string(prepared.Snapshot), prepared.SourceDisplay} {
		if strings.Contains(field, "s3cr") || strings.Contains(field, "p?ss") {
			t.Fatalf("credentials leaked: %s", field)
		}
	}
}

func TestPrepareDatabaseSelection(t *testing.T) {
	// An OpenEverest source points at its maintenance database; the option
	// selects the database to migrate.
	input := prepareInput(`{"database":"billing"}`)
	input.SourceConn = engine.Connection{URI: "postgresql://legacy-db:5432/postgres", Username: "postgres", Password: "x"}
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(prepared.Worker.SecretData[secretKeySourceURI]); got != "postgresql://postgres:x@legacy-db:5432/billing" {
		t.Fatalf("source URI = %q", got)
	}

	input = prepareInput(`{}`)
	input.Source.Provider = "provider-percona-postgresql"
	if _, err := New().Prepare(input); engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), "source instance") {
		t.Fatalf("an instance source must name its database: %v", err)
	}

	input = prepareInput(`{}`)
	input.SourceConn = engine.Connection{URI: "postgresql://legacy-db:5432"}
	if _, err := New().Prepare(input); engine.KindOf(err) != engine.KindInvalidRequest || !strings.Contains(err.Error(), "choose the database") {
		t.Fatalf("missing database: %v", err)
	}
}

func TestPrepareRejectsInvalidEndpoints(t *testing.T) {
	cases := map[string]func(*engine.PrepareInput){
		"wrong scheme": func(in *engine.PrepareInput) { in.SourceConn.URI = "mysql://db:3306/shop" },
		"multi host":   func(in *engine.PrepareInput) { in.SourceConn.URI = "postgresql://a:5432,b:5432/shop" },
		"same server": func(in *engine.PrepareInput) {
			in.SourceConn.URI = "postgresql://u:p@ORDERS-PG-PRIMARY.shop.svc/shop"
		},
		"bad option": func(in *engine.PrepareInput) { in.Options = json.RawMessage(`{"excludeSchemas":["pgcopydb"]}`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := prepareInput(`{}`)
			mutate(&input)
			_, err := New().Prepare(input)
			if engine.KindOf(err) != engine.KindInvalidRequest {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "s3cr") || strings.Contains(err.Error(), "p?ss") {
				t.Fatalf("error leaks credentials: %v", err)
			}
		})
	}
}

func TestPrepareMalformedProviderUserinfo(t *testing.T) {
	// Credentials provided separately win over a userinfo that is not
	// percent-encoded, which url.Parse would reject.
	input := prepareInput(`{}`)
	input.TargetConn = engine.Connection{URI: "postgresql://postgres:a^b{c@target:5432/postgres", Username: "postgres", Password: "a^b{c"}
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(prepared.Worker.SecretData[secretKeyTargetURI]); got != "postgresql://postgres:a%5Eb%7Bc@target:5432/shop" {
		t.Fatalf("target URI = %q", got)
	}
}

func TestPrepareMountsCAs(t *testing.T) {
	input := prepareInput(`{}`)
	input.SourceConn.URI = "postgresql://app:pw@legacy-db/shop"
	input.SourceConn.CACert = "PEM"
	prepared, err := New().Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	source := string(prepared.Worker.SecretData[secretKeySourceURI])
	if !strings.Contains(source, "sslmode=verify-ca") || !strings.Contains(source, "sslrootcert=%2Fetc%2Fmigration%2Fsecrets%2Fsource-ca.crt") {
		t.Fatalf("source URI = %q", source)
	}
	if prepared.Worker.SecretFiles[secretKeySourceCA] != secretKeySourceCA {
		t.Fatalf("CA not mounted: %v", prepared.Worker.SecretFiles)
	}
}

func TestDecodeOptions(t *testing.T) {
	options, err := decodeOptions(json.RawMessage(`{"includeSchemas":[" public ","sales","public"],"excludeTables":["sales.audit_log"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(options.IncludeSchemas, ",") != "public,sales" || len(options.ExcludeSchemas) != 0 {
		t.Fatalf("options = %+v", options)
	}
	for _, raw := range []string{
		`{"includeSchemas":["a"],"excludeSchemas":["b"]}`,
		`{"excludeTables":["audit_log"]}`,
		`{"excludeSchemas":["x\n[include-only-table]"]}`,
		`{"database":"shop;drop"}`,
		`{"tableJobs":0}`,
		`{"unknown":1}`,
		`{"tableJobs":"8"}`,
	} {
		if _, err := decodeOptions(json.RawMessage(raw)); engine.KindOf(err) != engine.KindInvalidRequest {
			t.Errorf("%s: error = %v", raw, err)
		}
	}
}

func TestMapStatus(t *testing.T) {
	tests := []struct {
		in        agentStatus
		phase     engine.Phase
		cutoverOK bool
	}{
		{agentStatus{State: "idle"}, engine.PhaseStarting, false},
		{agentStatus{State: "running"}, engine.PhaseFullLoad, false},
		{agentStatus{State: "running", CopyDone: true}, engine.PhaseCatchingUp, false},
		{agentStatus{State: "running", CopyDone: true, InitialSyncCompleted: true}, engine.PhaseReplicating, true},
		{agentStatus{State: "paused", CopyDone: true, InitialSyncCompleted: true}, engine.PhasePaused, true},
		{agentStatus{State: "finalizing", CopyDone: true, InitialSyncCompleted: true}, engine.PhaseFinalizing, false},
		{agentStatus{State: "completed"}, engine.PhaseCompleted, false},
		{agentStatus{State: "failed", Error: "boom"}, engine.PhaseFailed, false},
	}
	for _, tc := range tests {
		status, err := mapStatus(tc.in)
		if err != nil || status.Phase != tc.phase || status.Progress.ReadyForCutover != tc.cutoverOK {
			t.Errorf("%+v → %+v, %v", tc.in, status, err)
		}
	}
	if _, err := mapStatus(agentStatus{State: "exploded"}); engine.KindOf(err) != engine.KindUnavailable {
		t.Fatalf("unknown state: %v", err)
	}
}

func TestControllerTalksToAgent(t *testing.T) {
	var started map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &started)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/status":
			_, _ = w.Write([]byte(`{"state":"running","copyDone":true,"initialSyncCompleted":true,"bytesTotal":10,"bytesCopied":10,"lagSeconds":3}`))
		case "/cutover":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"cutover is possible once the target has caught up"}`))
		case "/teardown":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"drop the source replication slot: psql: connection refused"}`))
		}
	}))
	defer server.Close()
	controller := New().Controller(server.URL)
	ctx := context.Background()

	if err := controller.Start(ctx, json.RawMessage(`{"database":"shop","excludeSchemas":["scratch"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, hasDatabase := started["options"]; !hasDatabase || strings.Contains(string(started["options"]), "database") {
		t.Fatalf("the worker must receive its options without the database: %s", started["options"])
	}
	status, err := controller.Status(ctx)
	if err != nil || status.Phase != engine.PhaseReplicating || status.Progress.LagSeconds != 3 || !status.Progress.ReadyForCutover {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if err := controller.Cutover(ctx); engine.KindOf(err) != engine.KindConflict || !strings.Contains(err.Error(), "caught up") {
		t.Fatalf("cutover error = %v", err)
	}
	teardowner, ok := controller.(engine.Teardowner)
	if !ok {
		t.Fatal("the pgcopydb controller must tear down replication resources")
	}
	if err := teardowner.Teardown(ctx); engine.KindOf(err) != engine.KindUnavailable || !strings.Contains(err.Error(), "replication slot") {
		t.Fatalf("teardown error = %v", err)
	}
}
