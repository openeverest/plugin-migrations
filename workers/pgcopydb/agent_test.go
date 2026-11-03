package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFiltersINIAlwaysExcludesHeartbeatSchema(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{"defaults", Options{}, "[exclude-schema]\npgcopydb\n\n"},
		{
			"exclusions",
			Options{ExcludeSchemas: []string{"scratch"}, ExcludeTables: []string{"sales.audit_log"}},
			"[exclude-schema]\nscratch\npgcopydb\n\n[exclude-table]\nsales.audit_log\n\n",
		},
		{
			// pgcopydb rejects exclude-schema next to include-only-schema; the
			// heartbeat schema is excluded by not being included.
			"inclusions",
			Options{IncludeSchemas: []string{"public", "sales"}, ExcludeTables: []string{"sales.audit_log"}},
			"[include-only-schema]\npublic\nsales\n\n[exclude-table]\nsales.audit_log\n\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.options.filtersINI(); got != tc.want {
				t.Fatalf("filters:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestDecodeOptionsRejectsUnsafeInput(t *testing.T) {
	for _, raw := range []string{
		`{"includeSchemas":["a"],"excludeSchemas":["b"]}`,
		`{"excludeSchemas":["pgcopydb"]}`,
		`{"excludeSchemas":["bad name\n[include-only-table]"]}`,
		`{"excludeTables":["no_schema"]}`,
		`{"tableJobs":0}`,
		`{"indexJobs":65}`,
		`{"unknown":true}`,
	} {
		if _, err := decodeOptions(json.RawMessage(raw)); err == nil {
			t.Errorf("options %s must be rejected", raw)
		}
	}
	options, err := decodeOptions(json.RawMessage(`{"excludeTables":["sales.audit_log"],"tableJobs":8,"dropIfExists":true}`))
	if err != nil || *options.TableJobs != 8 || !options.DropIfExists {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

func TestParseLSN(t *testing.T) {
	for lsn, want := range map[string]uint64{"0/0": 0, "0/E7C0A88": 0xE7C0A88, "1/10": 1<<32 | 0x10, " 2/FF ": 2<<32 | 0xFF} {
		if got, err := parseLSN(lsn); err != nil || got != want {
			t.Errorf("parseLSN(%q) = %d, %v; want %d", lsn, got, err, want)
		}
	}
	for _, lsn := range []string{"", "12", "x/1", "1/g"} {
		if _, err := parseLSN(lsn); err == nil {
			t.Errorf("parseLSN(%q) must fail", lsn)
		}
	}
}

func TestLagTracker(t *testing.T) {
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	var tracker LagTracker
	if _, ok := tracker.Lag(100, base); ok {
		t.Fatal("lag is unknown before the first heartbeat")
	}
	tracker.Record(100, base)
	tracker.Record(200, base.Add(5*time.Second))
	tracker.Record(300, base.Add(10*time.Second))

	if lag, _ := tracker.Lag(50, base.Add(12*time.Second)); lag != 12*time.Second {
		t.Fatalf("nothing replayed: lag = %v, want the age of the oldest beat", lag)
	}
	if lag, _ := tracker.Lag(150, base.Add(12*time.Second)); lag != 7*time.Second {
		t.Fatalf("first beat replayed: lag = %v, want the age of the second beat", lag)
	}
	if lag, ok := tracker.Lag(300, base.Add(12*time.Second)); !ok || lag != 0 {
		t.Fatalf("all replayed: lag = %v, %v", lag, ok)
	}
	if lag, ok := tracker.Lag(300, base.Add(20*time.Second)); !ok || lag != 0 {
		t.Fatalf("lag must stay known after all beats were replayed: %v, %v", lag, ok)
	}
}

func TestLineRingLastError(t *testing.T) {
	ring := newLineRing(3)
	_, _ = ring.Write([]byte("2026-11-03 19:17:00.523 16 INFO   main.c:150  Running\n"))
	_, _ = ring.Write([]byte("2026-11-03 19:17:00.523 16 ERROR  catalog.c:889             Failed to attach the source catalog\n2026-11-03 19:17:00.6"))
	_, _ = ring.Write([]byte("64 14 INFO   copydb.c:1  exiting\n"))
	if got := ring.LastError(); got != "Failed to attach the source catalog" {
		t.Fatalf("LastError() = %q", got)
	}
	for i := 0; i < 5; i++ {
		_, _ = ring.Write([]byte("INFO line\n"))
	}
	if got := ring.LastError(); !strings.Contains(got, "worker log") {
		t.Fatalf("errors that scrolled out must not be reported: %q", got)
	}
}
