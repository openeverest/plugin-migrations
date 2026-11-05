package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDumpFilterArgsQuoteNamesAndSkipMigrationSchema(t *testing.T) {
	options := Options{IncludeSchemas: []string{"Sales"}, ExcludeTables: []string{"Sales.Audit"}}
	got := strings.Join(options.dumpFilterArgs(), " ")
	want := `--exclude-schema="openeverest_migration" --exclude-schema="pgbouncer" --schema="Sales" --exclude-table="Sales"."Audit"`
	if got != want {
		t.Fatalf("args = %s\nwant   %s", got, want)
	}
	vars := Options{}.filterVars()
	if vars["include_schemas"] != "[]" || vars["exclude_schemas"] != `["pgbouncer"]` || vars["exclude_tables"] != "[]" || vars["migration_schema"] != migrationSchema {
		t.Fatalf("empty filters must be empty JSON arrays: %v", vars)
	}
}

func TestDecodeOptionsRejectsUnsafeInput(t *testing.T) {
	for _, raw := range []string{
		`{"includeSchemas":["a"],"excludeSchemas":["b"]}`,
		`{"excludeSchemas":["openeverest_migration"]}`,
		`{"excludeSchemas":["x\"; drop table y"]}`,
		`{"excludeTables":["no_schema"]}`,
		`{"unknown":true}`,
	} {
		if _, err := decodeOptions(json.RawMessage(raw)); err == nil {
			t.Errorf("options %s must be rejected", raw)
		}
	}
	options, err := decodeOptions(json.RawMessage(`{"excludeTables":["sales.audit_log"],"recreateDatabase":true}`))
	if err != nil || !options.RecreateDatabase {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

func TestCheckPreflight(t *testing.T) {
	good := sourcePreflight{Version: 170002, WalLevel: "logical", CanReplicate: true, Tables: 3}
	target := targetPreflight{Version: 180004, Superuser: true}
	if err := checkPreflight(good, target); err != nil {
		t.Fatalf("valid setup rejected: %v", err)
	}

	bad := sourcePreflight{Version: 120000, WalLevel: "replica", Unidentified: make([]string, 12)}
	for i := range bad.Unidentified {
		bad.Unidentified[i] = "public.t" + string(rune('a'+i))
	}
	err := checkPreflight(bad, targetPreflight{Version: 140000})
	if err == nil {
		t.Fatal("invalid setup accepted")
	}
	for _, fragment := range []string{
		"version 13 or later", "wal_level=replica", "REPLICATION attribute", "no tables to migrate",
		"public.ta", "and 2 more", "REPLICA IDENTITY FULL", "version 15 or later", "superuser",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error does not mention %q: %v", fragment, err)
		}
	}

	if err := checkPreflight(sourcePreflight{Version: 180000, WalLevel: "logical", CanReplicate: true, Tables: 1}, targetPreflight{Version: 170005, Superuser: true}); err == nil ||
		!strings.Contains(err.Error(), "older than the source") {
		t.Fatalf("a downgrade must be rejected: %v", err)
	}
}

func TestDeriveStatus(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *time.Time { value := now.Add(-ago); return &value }
	sizes := map[string]uint64{"sales.orders": 300, "sales.customers": 100, heartbeatTable: 8192}
	tables := func(orders, customers string) []tableState {
		return []tableState{{"sales.orders", orders}, {"sales.customers", customers}, {heartbeatTable, "r"}}
	}
	base := snapshot{SubExists: true, SubEnabled: true, SourceNow: now, Sizes: sizes}

	copying := base
	copying.Tables, copying.BeatAt = tables("d", "r"), at(2*time.Second)
	status := deriveStatus(false, "", copying, false, false, false)
	if status.State != StateRunning || status.CopyDone || status.BytesTotal != 400 || status.BytesCopied != 100 || status.InitialSyncCompleted {
		t.Fatalf("copying: %+v", status)
	}
	if !strings.Contains(status.Message, "1 of 2 tables") {
		t.Fatalf("copying message = %q", status.Message)
	}

	catchingUp := base
	catchingUp.Tables, catchingUp.BeatAt = tables("r", "r"), at(40*time.Second)
	status = deriveStatus(false, "", catchingUp, false, false, false)
	if !status.CopyDone || status.BytesCopied != 400 || status.InitialSyncCompleted || status.LagSeconds != 40 {
		t.Fatalf("catching up: %+v", status)
	}

	caughtUp := catchingUp
	caughtUp.BeatAt = at(3 * time.Second)
	status = deriveStatus(false, "", caughtUp, true, false, false)
	if !status.InitialSyncCompleted || status.Message != "Replicating changes" {
		t.Fatalf("replicating: %+v", status)
	}

	// Latched: a later lag spike does not undo the initial sync.
	status = deriveStatus(false, "", catchingUp, true, true, false)
	if !status.InitialSyncCompleted || status.LagSeconds != 40 {
		t.Fatalf("latched: %+v", status)
	}

	paused := caughtUp
	paused.SubEnabled = false
	if status = deriveStatus(false, "", paused, true, true, false); status.State != StatePaused {
		t.Fatalf("paused: %+v", status)
	}

	// Errors do not fail the migration: PostgreSQL retries them. New ones are
	// reported while they are recent; old ones are not.
	retrying := caughtUp
	retrying.Errors = 2
	if status = deriveStatus(false, "", retrying, true, true, true); status.State != StateRunning || !strings.Contains(status.Error, "2 error(s)") {
		t.Fatalf("recent errors: %+v", status)
	}
	if status = deriveStatus(false, "", retrying, true, true, false); status.Error != "" {
		t.Fatalf("old errors must not be reported: %+v", status)
	}

	gone := caughtUp
	gone.SubExists = false
	if status = deriveStatus(false, "", gone, true, true, false); status.State != StateFailed {
		t.Fatalf("dropped subscription: %+v", status)
	}

	if status = deriveStatus(true, "", caughtUp, true, true, false); status.State != StateFinalizing {
		t.Fatalf("cutover: %+v", status)
	}
	if status = deriveStatus(true, "Cutover failed: boom", caughtUp, true, true, true); status.State != StateFailed || status.Error != "Cutover failed: boom" {
		t.Fatalf("cutover failure: %+v", status)
	}
}
