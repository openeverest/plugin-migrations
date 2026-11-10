package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPositionReached(t *testing.T) {
	p := sourcePosition{File: "binlog.000010", Pos: 500, GTID: "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-20,\n4ab:1-5:7"}
	for _, tc := range []struct {
		checkpoint, gtid string
		useGTID, want    bool
	}{
		{"(binlog.000010, 500)", "", false, true},
		{"(binlog.000010, 499)", "", false, false},
		{"(binlog.000011, 4)", "", false, true},
		{"(binlog.000009, 900)", "", false, false},
		{"(binlog|000001.000010, 600)", "", false, true},
		{"", "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-25,4ab:1-7", true, true},
		{"", "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-10:11-20,4ab:1-3:4-5:6-7", true, true},
		{"", "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-25,4ab:1-5", true, false},
		{"", "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-19,4ab:1-7", true, false},
		{"", "", true, false},
	} {
		got, err := p.reached(tc.checkpoint, tc.gtid, tc.useGTID)
		if err != nil || got != tc.want {
			t.Errorf("reached(%q, %q, %v) = %v, %v; want %v", tc.checkpoint, tc.gtid, tc.useGTID, got, err, tc.want)
		}
	}
	if _, err := p.reached("binlog.000010:5", "", false); err == nil {
		t.Error("an unreadable position must be an error")
	}
}

func TestParseTaggedGTIDSet(t *testing.T) {
	set, err := parseGTIDSet("aaa:1-3:tag1:1-2:5,bbb:7")
	if err != nil {
		t.Fatal(err)
	}
	if len(set["aaa"]) != 1 || len(set["aaa:tag1"]) != 2 || set["bbb"][0] != (interval{7, 7}) {
		t.Fatalf("set = %v", set)
	}
	want, _ := parseGTIDSet("aaa:tag1:5")
	if !set.contains(want) {
		t.Fatal("tagged transactions must be compared by tag")
	}
	if _, err := parseGTIDSet("aaa:5-1"); err == nil {
		t.Fatal("a reversed interval must be rejected")
	}
}

// dmctlFailure is dmctl check-task's output: raw newlines inside msg.
const dmctlFailure = `{
    "result": false,
    "msg": "[code=26005:class=dm-master:scope=internal:level=medium], Message: fail to check synchronization configuration with type: check was failed, please see detail
    	detail: {
		"results": [
			{"id": 8, "name": "mysql_binlog_row_image", "state": "fail",
			 "errors": [{"severity": "fail", "short_error": "binlog_row_image is MINIMAL, and should be FULL"}]},
			{"id": 3, "name": "replication privileges checker", "state": "fail",
			 "errors": [{"severity": "fail", "short_error": "lack of REPLICATION SLAVE privilege"}]},
			{"id": 12, "name": "table structure compatibility check", "state": "warn",
			 "errors": [{"severity": "warn", "short_error": "table shop.t has no primary key"}]}
		],
		"summary": {"passed": false, "total": 13, "successful": 10, "failed": 2, "warning": 1}
	}"
}`

func TestParseCheckOutput(t *testing.T) {
	result := parseCheckOutput([]byte(dmctlFailure))
	if result.Passed || len(result.Problems) != 1 || !strings.Contains(result.Problems[0], "REPLICATION SLAVE") {
		t.Fatalf("problems = %q (items the agent checks itself must be dropped)", result.Problems)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "no primary key") {
		t.Fatalf("warnings = %q", result.Warnings)
	}
	if !parseCheckOutput([]byte(`{"result": true, "msg": "check pass!!!"}`)).Passed {
		t.Fatal("a passed check must pass")
	}
	failed := parseCheckOutput([]byte(`{"result": false, "msg": "[code=20005:class=config], Message: source mig_x not found, Workaround: create it"}`))
	if failed.Passed || len(failed.Problems) != 1 || failed.Problems[0] != "source mig_x not found" {
		t.Fatalf("problems = %q", failed.Problems)
	}
}

func TestSkippableDDL(t *testing.T) {
	options := Options{Databases: []string{"shop", "crm"}}
	const heartbeat = "openeverest_mig_x"
	for _, tc := range []struct {
		info     string
		database string
		skip     bool
	}{
		{"CREATE TABLE other.t (g GEOMETRY) /* xid=44 */", "other", true},
		{"use `other`; CREATE TABLE t (b TEXT CHARACTER SET latin2)", "other", true},
		{"use `other`; ALTER TABLE `other`.`t` ADD COLUMN g POINT", "other", true},
		{"CREATE DATABASE legacy CHARACTER SET latin2", "legacy", true},
		{"CREATE TABLE IF NOT EXISTS `my``db`.t (g GEOMETRY)", "my`db", true},
		{"use `shop`; CREATE TABLE t (g GEOMETRY)", "", false},
		{"CREATE TABLE SHOP.t (g GEOMETRY)", "", false},
		{"CREATE TABLE t (g GEOMETRY)", "", false},
		{"use `other`; CREATE TABLE t LIKE shop.orders", "", false},
		{"ALTER TABLE other.t RENAME TO crm.t", "", false},
		{"CREATE TABLE openeverest_mig_x.t (g GEOMETRY)", "", false},
		{"DROP TABLE other.t", "", false},
		{"CREATE PROCEDURE other.p() SELECT 1", "", false},
	} {
		database, skip := skippableDDL(tc.info, options, heartbeat)
		if skip != tc.skip || database != tc.database {
			t.Errorf("%q: got %q, %v; want %q, %v", tc.info, database, skip, tc.database, tc.skip)
		}
	}
	location := DMError{ErrCode: errCodeParseDDL, Message: "startLocation: [position: (binlog.000003, 112132), gtid-set: x:1-10], endLocation: …"}
	if file, pos, ok := failedEventPosition(location); !ok || file != "binlog.000003" || pos != "112132" {
		t.Fatalf("position = %s:%s %v", file, pos, ok)
	}
	location.ErrCode = 1
	if _, _, ok := failedEventPosition(location); ok {
		t.Fatal("only parse errors are skipped")
	}
}

func TestViewStatement(t *testing.T) {
	got := viewStatement("CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS select `shop`.`a`.`id` AS `id` from `shop`.`a`")
	want := "CREATE OR REPLACE ALGORITHM=UNDEFINED DEFINER=CURRENT_USER SQL SECURITY DEFINER VIEW `v` AS select `shop`.`a`.`id` AS `id` from `shop`.`a`"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestRenderConfigs(t *testing.T) {
	source := Endpoint{Host: "db", Port: 3306, User: "dm", Password: "p: \"#x", SSLMode: sslVerifyCA, SSLCA: "/etc/migration/secrets/source-ca.crt"}
	rendered, err := renderSourceConfig("mig_x", source, true)
	if err != nil {
		t.Fatal(err)
	}
	var parsed sourceConfig
	if err := yaml.Unmarshal(rendered, &parsed); err != nil || parsed.From.Password != source.Password || !parsed.EnableGTID ||
		parsed.From.Security == nil || parsed.From.Security.SSLCA != source.SSLCA {
		t.Fatalf("source config = %s, %v", rendered, err)
	}
	target := Endpoint{Host: "tidb", Port: 4000, User: "root", Password: "pw", SSLMode: sslDisabled}
	options := Options{Databases: []string{"shop"}, ExcludeTables: []string{"shop.audit"}, CopyThreads: 4, SyncThreads: 16, StorageGiB: 10}
	rendered, err = renderTaskConfig("mig_x", target, options, "/data/dump/mig_x", false)
	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	if err := yaml.Unmarshal(rendered, &task); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(task)
	for _, want := range []string{
		`"task-mode":"all"`, `"collation_compatible":"strict"`, `"do-dbs":["shop","openeverest_mig_x"]`,
		`"ignore-tables":[{"db-name":"shop","tbl-name":"audit"}]`, `"checkpoint-flush-interval":5`, `"dir":"/data/dump/mig_x"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("task config lacks %s: %s", want, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"ignore-checking-items":["all"]`) {
		t.Errorf("a task started after the check must skip DM's precheck: %s", encoded)
	}
	checked, _ := renderTaskConfig("mig_x", target, options, "/data/dump/mig_x", true)
	if strings.Contains(string(checked), "ignore-checking-items") {
		t.Errorf("the checked config must run every precheck: %s", checked)
	}
	if strings.Contains(string(encoded), "do-tables") || strings.Contains(string(encoded), "security") {
		t.Errorf("unexpected keys: %s", encoded)
	}
}

func TestCheckPreflight(t *testing.T) {
	good := sourceFacts{Variables: map[string]string{
		"version": "8.0.46-37", "log_bin": "ON", "binlog_format": "ROW", "binlog_row_image": "FULL", "server_id": "1", "gtid_mode": "ON",
	}}
	target := targetFacts{Version: "8.0.11-TiDB-v8.5.2", Collations: map[string]bool{"utf8mb4_0900_ai_ci": true, "utf8mb4_bin": true}}
	options := Options{Databases: []string{"shop"}}
	collations := map[string][]string{"utf8mb4_0900_ai_ci": {"shop.orders"}}
	if problems := checkPreflight(good, collations, target, options, 1<<30); len(problems) != 0 {
		t.Fatalf("problems = %q", problems)
	}
	bad := good
	bad.Variables = map[string]string{"version": "10.11.6-MariaDB", "log_bin": "OFF", "binlog_format": "MIXED", "binlog_row_image": "MINIMAL",
		"binlog_transaction_compression": "ON", "server_id": "0"}
	bad.Missing, bad.Spatial, bad.CascadingFKs, bad.DataBytes = []string{"crm"}, []string{"shop.places.g"}, []string{"shop.items.fk"}, 2<<30
	target.Version, target.NonEmpty = "8.0.36", []string{"shop"}
	collations["latin2_general_ci"] = []string{"shop.legacy"}
	problems := strings.Join(checkPreflight(bad, collations, target, options, 1<<30), "\n")
	for _, want := range []string{"MariaDB", "log_bin", "binlog_format=MIXED", "binlog_row_image=MINIMAL", "binlog_transaction_compression",
		"server_id", "crm", "not TiDB", "shop.legacy (latin2_general_ci)", "spatial", "shop.items.fk", "already have tables", "may not fit"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems lack %q:\n%s", want, problems)
		}
	}
	options.RecreateDatabases = true
	if strings.Contains(strings.Join(checkPreflight(bad, collations, target, options, 0), "\n"), "already have tables") {
		t.Error("recreateDatabases allows non-empty target databases")
	}
	if note := unsupportedNote(map[string]int{"triggers": 2, "events": 1}); note != "Not migrated because TiDB does not support them: 1 events, 2 triggers." {
		t.Errorf("note = %q", note)
	}
}

func TestFormatDMErrors(t *testing.T) {
	got := formatDMErrors([]DMError{{
		ErrCode:  errCodeParseDDL,
		Message:  "startLocation: [position: (binlog.000003, 1)], endLocation: [position: (binlog.000003, 2)], origin SQL: [CREATE TABLE shop.geo (g GEOMETRY)]: parse DDL: CREATE TABLE shop.geo (g GEOMETRY)",
		RawCause: `line 1 column 53 near "GEOMETRY)"`,
	}})
	if !strings.HasPrefix(got, "DM stopped: parse DDL: CREATE TABLE shop.geo") || !strings.Contains(got, "cause: line 1") || !strings.Contains(got, "TiDB cannot run this statement") {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeOptions(t *testing.T) {
	valid := `{"databases":["shop"],"excludeTables":["shop.a"],"recreateDatabases":false,"storageGiB":10,"copyThreads":4,"syncThreads":16}`
	if _, err := decodeOptions(json.RawMessage(valid)); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeOptions(json.RawMessage(strings.Replace(valid, `"shop"]`, `"shop","openeverest_shop"]`, 1))); err != nil {
		t.Fatalf("the backend accepts openeverest_shop, so must the worker: %v", err)
	}
	for _, invalid := range []string{
		`{"databases":[]}`,
		`{"databases":["openeverest_mig_x"],"storageGiB":10,"copyThreads":4,"syncThreads":16}`,
		`{"databases":["shop"],"excludeTables":["crm.a"],"storageGiB":10,"copyThreads":4,"syncThreads":16}`,
		`{"databases":["shop"],"storageGiB":10,"copyThreads":0,"syncThreads":16}`,
		`{"databases":["shop"],"other":1}`,
	} {
		if _, err := decodeOptions(json.RawMessage(invalid)); err == nil {
			t.Errorf("%s: expected an error", invalid)
		}
	}
}

func TestFlexNumber(t *testing.T) {
	var status TaskStatus
	if err := json.Unmarshal([]byte(`{"stage":"Running","unit":"Load","load":{"finishedBytes":"1024","totalBytes":"2048"}}`), &status); err != nil {
		t.Fatal(err)
	}
	if status.Load.FinishedBytes != 1024 || status.Load.TotalBytes != 2048 {
		t.Fatalf("load = %+v", status.Load)
	}
}

func TestDMErrorGrace(t *testing.T) {
	a := NewAgent(Config{}, nil)
	if a.dmErrorFailed(DMError{ErrCode: 10006}) {
		t.Fatal("a new DM error must leave DM time to retry")
	}
	a.dmErrorSince = a.dmErrorSince.Add(-dmRetryGrace)
	if !a.dmErrorFailed(DMError{ErrCode: 10006}) {
		t.Fatal("an error DM did not get past must fail the migration")
	}
	b := NewAgent(Config{}, nil)
	if !b.dmErrorFailed(DMError{ErrCode: errCodeParseDDL}) {
		t.Fatal("a statement TiDB cannot parse fails at once")
	}
}
