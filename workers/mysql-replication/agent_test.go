package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDecodeOptionsValidates(t *testing.T) {
	options, err := decodeOptions(json.RawMessage(`{"databases":["shop"],"excludeTables":["shop.audit"],"recreateDatabases":true}`))
	if err != nil || options.CopyThreads != defaultCopyThreads || !options.RecreateDatabases {
		t.Fatalf("options = %+v, %v", options, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"databases":["mysql"]}`,
		`{"databases":["OpenEverest_Migration"]}`,
		"{\"databases\":[\"shop`; drop\"]}",
		`{"databases":["shop"],"excludeTables":["audit"]}`,
		`{"databases":["shop"],"excludeTables":["other.audit"]}`,
		`{"databases":["shop"],"copyThreads":33}`,
		`{"databases":["shop"],"unknown":true}`,
	} {
		if _, err := decodeOptions(json.RawMessage(raw)); err == nil {
			t.Errorf("options %s must be rejected", raw)
		}
	}
}

func TestFilterPatternsEscapeWildcards(t *testing.T) {
	options := Options{Databases: []string{"shop_1", "a%b"}, ExcludeTables: []string{"shop_1.audit_log"}}
	want := []string{`shop\_1.%`, `a\%b.%`, `openeverest\_migration.heartbeat\_mig\_ab12`}
	if got := options.doPatterns("mig_ab12"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doPatterns = %v, want %v", got, want)
	}
	if got := options.ignoreTables(); !reflect.DeepEqual(got, []string{"shop_1.audit_log"}) {
		t.Fatalf("ignoreTables = %v", got)
	}
	if !options.migrates("shop_1") || options.migrates("shopX1") {
		t.Fatal("migrates must compare names exactly")
	}
}

func TestParseEndpoint(t *testing.T) {
	endpoint, err := parseEndpoint("mysql://app:p%40ss@db1.example:3307/shop?ssl-mode=verify_identity&ssl-ca=/etc/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	want := Endpoint{Host: "db1.example", Port: 3307, User: "app", Password: "p@ss", SSLMode: sslVerifyIdentity, SSLCA: "/etc/ca.pem"}
	if endpoint != want {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	node, err := endpoint.WithAddress("db-pxc-0.db-pxc.shop.svc.cluster.local:3306")
	if err != nil || node.Host != "db-pxc-0.db-pxc.shop.svc.cluster.local" || node.Port != 3306 || node.SSLMode != sslVerifyCA || node.Password != "p@ss" {
		t.Fatalf("node = %+v, %v", node, err)
	}
	if defaults, err := parseEndpoint("mysql://root@db1"); err != nil || defaults.Port != 3306 || defaults.SSLMode != sslPreferred {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	for _, raw := range []string{"postgres://u@db1", "mysql://db1", "mysql://u@db1?ssl-mode=bogus", "mysql://u@db1?ssl-mode=VERIFY_CA", "mysql://u@db1:0"} {
		if _, err := parseEndpoint(raw); err == nil {
			t.Errorf("%s must be rejected", raw)
		} else if strings.Contains(err.Error(), "u@") {
			t.Errorf("error leaks the URI: %v", err)
		}
	}
}

func TestCopyOutputProgressAndGTIDs(t *testing.T) {
	var loaded []uint64
	output := newCopyOutput(func(bytes uint64) { loaded = append(loaded, bytes) })
	stream := "1 thds dumping - 1% (6.00K rows / ~394.09K rows), 0.00 rows/s\r" +
		"Dump still in progress - 1 thds loading - ?% (12.50 MB / ?), 5.26 MB/s (23.56K rows/s), 0 / 1 tables done\r" +
		"100% (1.02 GB / 1.02 GB), 4.96 MB/s (25.14K rows/s), 1 / 1 tables done\n" +
		"---\nDump_metadata:\n  Binlog_file: binlog.000003\n  Executed_GTID_set: |-\n" +
		"    11111111-2222-3333-4444-555555555555:1,\n    a97e8886-db8b-11f1-bdf6-828235aafccc:1-24\n\n"
	for _, chunk := range []string{stream[:40], stream[40:200], stream[200:]} {
		_, _ = output.Write([]byte(chunk))
	}
	output.flush()
	if !reflect.DeepEqual(loaded, []uint64{12_500_000, 1_020_000_000}) {
		t.Fatalf("loaded = %v", loaded)
	}
	set, err := parseExecutedGTIDSet(output.text())
	if err != nil || set != "11111111-2222-3333-4444-555555555555:1,a97e8886-db8b-11f1-bdf6-828235aafccc:1-24" {
		t.Fatalf("set = %q, %v", set, err)
	}
	inline, err := parseExecutedGTIDSet("Dump_metadata:\n  Executed_GTID_set: a97e8886-db8b-11f1-bdf6-828235aafccc:1-12\n")
	if err != nil || inline != "a97e8886-db8b-11f1-bdf6-828235aafccc:1-12" {
		t.Fatalf("inline = %q, %v", inline, err)
	}
	for _, bad := range []string{"no metadata", "Executed_GTID_set: ''", "Executed_GTID_set: x'; DROP"} {
		if _, err := parseExecutedGTIDSet(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestCopyOutputFailureKeepsErrorLines(t *testing.T) {
	output := newCopyOutput(nil)
	_, _ = output.Write([]byte("Initializing\nERROR: The 'local_infile' global system variable must be set to ON\nUtil.copySchemas: local_infile disabled in server (MYSQLSH 53025)\n"))
	got := output.failure(nil)
	if !strings.Contains(got, "local_infile' global") || !strings.Contains(got, "MYSQLSH 53025") || strings.Contains(got, "Initializing") {
		t.Fatalf("failure = %q", got)
	}
}

func goodFacts() (sourceFacts, targetFacts) {
	source := sourceFacts{
		Info:         serverInfo{Version: "8.0.46-37", UUID: "a", LogBin: true, GTIDMode: "ON", BinlogFormat: "ROW"},
		CanReplicate: true,
	}
	target := targetFacts{Info: serverInfo{Version: "8.4.8-8.1", UUID: "b", GTIDMode: "ON", SkipReplicaStart: true, Galera: true}}
	return source, target
}

func TestCheckPreflight(t *testing.T) {
	options := Options{Databases: []string{"shop"}, CopyThreads: 4}
	source, target := goodFacts()
	if err := checkPreflight(mysqlFlavor{}, source, target, options); err != nil {
		t.Fatalf("valid setup rejected: %v", err)
	}

	source.Info = serverInfo{Version: "5.7.44", UUID: "a", GTIDMode: "OFF_PERMISSIVE", BinlogFormat: "MIXED"}
	source.CanReplicate = false
	source.Missing = []string{"crm"}
	source.Unsupported = make([]string, 12)
	for i := range source.Unsupported {
		source.Unsupported[i] = "shop.t" + string(rune('a'+i))
	}
	target.Info = serverInfo{Version: "8.0.22", UUID: "b", GTIDMode: "OFF", Galera: true}
	target.Existing = []string{"shop"}
	target.ChannelExists = true
	err := checkPreflight(mysqlFlavor{}, source, target, options)
	if err == nil {
		t.Fatal("invalid setup accepted")
	}
	for _, fragment := range []string{
		"8.0 or later", "log_bin", "gtid_mode=OFF_PERMISSIVE", "binlog_format=MIXED", "REPLICATION SLAVE", "not found on the source: crm",
		"shop.ta", "and 2 more", "8.0.23 or later", "target has gtid_mode=OFF", "skip_replica_start", "already exist on the target: shop",
		"replication channel",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error does not mention %q: %v", fragment, err)
		}
	}

	source, target = goodFacts()
	source.Info.Version, target.Info.Version = "8.4.3", "8.0.45-36.1"
	if err := checkPreflight(mysqlFlavor{}, source, target, options); err == nil || !strings.Contains(err.Error(), "older than the source") {
		t.Fatalf("a downgrade must be rejected: %v", err)
	}
	source, target = goodFacts()
	source.Info.Version, source.Info.Comment = "10.11.6-MariaDB", "MariaDB Server"
	if err := checkPreflight(mysqlFlavor{}, source, target, options); err == nil || !strings.Contains(err.Error(), "MariaDB") {
		t.Fatalf("MariaDB must be rejected: %v", err)
	}
	source, target = goodFacts()
	source.Unsupported, target.Info.Galera = []string{"shop.nopk"}, false
	target.Existing = []string{"shop"}
	if err := checkPreflight(mysqlFlavor{}, source, target, Options{Databases: []string{"shop"}, RecreateDatabases: true}); err != nil {
		t.Fatalf("tables without a primary key are fine outside Galera, and recreate allows existing databases: %v", err)
	}
}

func TestNewServerInfoDetectsPXCNode(t *testing.T) {
	info := newServerInfo(map[string]string{
		"version": "8.4.8-8.1", "gtid_mode": "ON", "skip_replica_start": "ON", "local_infile": "OFF",
		"wsrep_provider": "/usr/lib64/galera4/libgalera_smm.so", "wsrep_node_incoming_address": "db-pxc-0.db-pxc.shop.svc.cluster.local:3306",
	})
	if !info.Galera || info.NodeAddress != "db-pxc-0.db-pxc.shop.svc.cluster.local:3306" || !info.SkipReplicaStart || info.LocalInfile {
		t.Fatalf("info = %+v", info)
	}
	plain := newServerInfo(map[string]string{"wsrep_provider": "none", "wsrep_node_incoming_address": "AUTO"})
	if plain.Galera || plain.NodeAddress != "" {
		t.Fatalf("plain = %+v", plain)
	}
	if versionNumber("8.0.46-37") != 80046 || versionNumber("garbage") != 0 {
		t.Fatal("versionNumber")
	}
}

func TestPlanSupervision(t *testing.T) {
	options := Options{Databases: []string{"shop"}, ExcludeTables: []string{"shop.scratch"}}
	healthy := channelState{Exists: true, IORunning: "Yes", SQLRunning: "Yes", WildDo: options.doPatterns("mig_x"), IgnoreTable: options.ignoreTables()}
	long := time.Hour
	cases := map[string]struct {
		channel channelState
		since   time.Duration
		want    supervision
	}{
		"healthy":                    {healthy, long, superviseNothing},
		"missing":                    {channelState{}, long, superviseNothing},
		"restarted server":           {channelState{Exists: true, IORunning: "No", SQLRunning: "No"}, long, superviseStart},
		"just started":               {channelState{Exists: true, IORunning: "No", SQLRunning: "No"}, time.Second, superviseNothing},
		"connecting":                 {with(healthy, func(c *channelState) { c.IORunning, c.IOErrno = "Connecting", 2003 }), long, superviseNothing},
		"unknown database":           {with(healthy, func(c *channelState) { c.SQLRunning, c.SQLErrno = "No", errUnknownDatabase }), long, superviseSkip},
		"applier error, recent":      {with(healthy, func(c *channelState) { c.SQLRunning, c.SQLErrno = "No", 1062 }), 30 * time.Second, superviseNothing},
		"applier error, retry":       {with(healthy, func(c *channelState) { c.SQLRunning, c.SQLErrno = "No", 1062 }), 2 * time.Minute, superviseStart},
		"filters lost while running": {with(healthy, func(c *channelState) { c.WildDo = nil }), long, superviseStart},
	}
	for name, test := range cases {
		if got := planSupervision(test.channel, options, "mig_x", test.since); got != test.want {
			t.Errorf("%s: got %v, want %v", name, got, test.want)
		}
	}
}

func with(channel channelState, change func(*channelState)) channelState {
	change(&channel)
	return channel
}

func TestSkippableDatabase(t *testing.T) {
	options := Options{Databases: []string{"shop"}}
	failure := workerError{
		Number:      errUnknownDatabase,
		Message:     "Worker 1 failed executing transaction 'u:13'; Error 'Unknown database 'other'' on query. Query: 'CREATE PROCEDURE `other`.`p`() SELECT 1'",
		Transaction: "a01013a7-db8c-11f1-9606-da9bd8d88a82:13",
	}
	if database, ok := skippableDatabase(failure, options); !ok || database != "other" {
		t.Fatalf("got %q, %v", database, ok)
	}
	for name, change := range map[string]func(*workerError){
		"migrated database": func(f *workerError) { f.Message = strings.ReplaceAll(f.Message, "'other'", "'shop'") },
		"other error":       func(f *workerError) { f.Number = 1146 },
		"migration db":      func(f *workerError) { f.Message = "Unknown database 'openeverest_migration'" },
	} {
		copy := failure
		change(&copy)
		if _, ok := skippableDatabase(copy, options); ok {
			t.Errorf("%s must not be skipped", name)
		}
	}
}

func TestDeriveStatus(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *time.Time { value := now.Add(-ago); return &value }
	running := channelState{Exists: true, IORunning: "Yes", SQLRunning: "Yes"}
	base := snapshot{Channel: running, SourceNow: now}

	unknown := deriveStatus(false, "", base, false, 400)
	if unknown.InitialSyncCompleted || unknown.LagSeconds != 0 || !unknown.CopyDone || unknown.BytesCopied != 400 {
		t.Fatalf("no heartbeat yet: %+v", unknown)
	}
	catchingUp := base
	catchingUp.BeatAt = at(40 * time.Second)
	if status := deriveStatus(false, "", catchingUp, false, 400); status.InitialSyncCompleted || status.LagSeconds != 40 ||
		!strings.Contains(status.Message, "Catching up") {
		t.Fatalf("catching up: %+v", status)
	}
	caughtUp := base
	caughtUp.BeatAt = at(2 * time.Second)
	if status := deriveStatus(false, "", caughtUp, false, 400); !status.InitialSyncCompleted || status.Message != "Replicating changes" {
		t.Fatalf("caught up: %+v", status)
	}
	if status := deriveStatus(false, "", catchingUp, true, 400); !status.InitialSyncCompleted {
		t.Fatalf("initial sync must latch: %+v", status)
	}
	paused := caughtUp
	paused.Paused, paused.Channel = true, channelState{Exists: true, IORunning: "No", SQLRunning: "No"}
	if status := deriveStatus(false, "", paused, true, 400); status.State != StatePaused || status.Error != "" {
		t.Fatalf("paused: %+v", status)
	}
	broken := caughtUp
	broken.Channel = channelState{Exists: true, IORunning: "Yes", SQLRunning: "No", SQLErrno: 1062, SQLError: "Coordinator stopped"}
	broken.Worker = workerError{Message: "Duplicate entry '7' for key 'PRIMARY'"}
	if status := deriveStatus(false, "", broken, true, 400); status.State != StateRunning || !strings.Contains(status.Error, "Duplicate entry") {
		t.Fatalf("applier error: %+v", status)
	}
	purged := caughtUp
	purged.Channel = channelState{Exists: true, IORunning: "No", SQLRunning: "Yes", IOErrno: errSourceFatalRead, IOError: "purged binlogs"}
	if status := deriveStatus(false, "", purged, true, 400); !strings.Contains(status.Error, "binary logs") {
		t.Fatalf("purged binlogs: %+v", status)
	}
	gone := caughtUp
	gone.Channel = channelState{}
	if status := deriveStatus(false, "", gone, true, 400); status.State != StateFailed {
		t.Fatalf("missing channel: %+v", status)
	}
	if status := deriveStatus(true, "", caughtUp, true, 400); status.State != StateFinalizing {
		t.Fatalf("cutover: %+v", status)
	}
	if status := deriveStatus(true, "Cutover failed: boom", caughtUp, true, 400); status.State != StateFailed || status.Error != "Cutover failed: boom" {
		t.Fatalf("cutover failure: %+v", status)
	}
}

func TestEventStatusChange(t *testing.T) {
	source := []scheduledEvent{{Database: "shop", Name: "nightly", Status: "ENABLED"}, {Database: "shop", Name: "off", Status: "DISABLED"}}
	cases := map[scheduledEvent]string{
		{Database: "shop", Name: "nightly", Status: "REPLICA_SIDE_DISABLED"}: "ENABLE",
		{Database: "shop", Name: "nightly", Status: "SLAVESIDE_DISABLED"}:    "ENABLE",
		{Database: "shop", Name: "nightly", Status: "ENABLED"}:               "",
		{Database: "shop", Name: "off", Status: "REPLICA_SIDE_DISABLED"}:     "DISABLE",
		{Database: "shop", Name: "off", Status: "DISABLED"}:                  "",
		{Database: "shop", Name: "dropped", Status: "ENABLED"}:               "DISABLE",
	}
	for event, want := range cases {
		if got := eventStatusChange(event, source); got != want {
			t.Errorf("%s (%s): got %q, want %q", event.Name, event.Status, got, want)
		}
	}
}

func TestInList(t *testing.T) {
	query, args := inList("SELECT 1 FROM t WHERE a IN (?) AND b = ?", []string{"x", "y"})
	if query != "SELECT 1 FROM t WHERE a IN (?, ?) AND b = ?" || len(args) != 2 {
		t.Fatalf("query = %q, args = %v", query, args)
	}
}

func TestWithCurrentUserDefiner(t *testing.T) {
	got, err := withCurrentUserDefiner("CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS select 'DEFINER=`x`@`y`'")
	if err != nil || got != "CREATE ALGORITHM=UNDEFINED DEFINER=CURRENT_USER SQL SECURITY DEFINER VIEW `v` AS select 'DEFINER=`x`@`y`'" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, _ := withCurrentUserDefiner("CREATE DEFINER=`we``ird`@`%` PROCEDURE `p`() SELECT 1"); got != "CREATE DEFINER=CURRENT_USER PROCEDURE `p`() SELECT 1" {
		t.Fatalf("quoted backticks: %q", got)
	}
	if _, err := withCurrentUserDefiner("CREATE VIEW v AS SELECT 1"); err == nil {
		t.Fatal("a statement without a definer must be rejected")
	}
}

func TestCopyOptionsExcludeRemappedObjects(t *testing.T) {
	objects := []definedObject{
		{Kind: "VIEW", Database: "shop", Name: "totals"},
		{Kind: "PROCEDURE", Database: "shop", Name: "add_order"},
		{Kind: "TRIGGER", Database: "shop", Name: "orders_audit", Table: "orders"},
		{Kind: "EVENT", Database: "shop", Name: "cleanup"},
	}
	options := copyOptions(Options{Databases: []string{"shop"}, ExcludeTables: []string{"shop.scratch"}, CopyThreads: 2}, copyExclusions(objects))
	want := map[string]any{
		"threads": 2, "consistent": true, "showProgress": true, "updateGtidSet": "off", "skipBinlog": false,
		"excludeTables":   []string{"shop.scratch", "`shop`.`totals`"},
		"excludeRoutines": []string{"`shop`.`add_order`"},
		"excludeTriggers": []string{"`shop`.`orders`.`orders_audit`"},
		"excludeEvents":   []string{"`shop`.`cleanup`"},
	}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("options = %v", options)
	}
	if plain := copyOptions(Options{Databases: []string{"shop"}, CopyThreads: 4}, nil); plain["excludeTables"] != nil {
		t.Fatalf("no exclusions expected: %v", plain)
	}
	policy := definerPolicy{privileged: map[string]bool{"root@localhost": true}}
	if !policy.remaps("root@localhost") || policy.remaps("app@%") || (definerPolicy{}).remaps("root@localhost") {
		t.Fatal("remaps")
	}
}

func TestAgentWaitsForRecoveryBeforeStarting(t *testing.T) {
	agent := NewAgent(Config{Name: "mig_x"})
	if status := agent.Status(context.Background()); status.State != StateStopped || status.Message == "" {
		t.Fatalf("a recovering agent must not report idle (the backend would start it): %+v", status)
	}
	if err := agent.Start(Options{Databases: []string{"shop"}}); !errors.Is(err, errUnavailable) {
		t.Fatalf("start during recovery = %v", err)
	}
}

func TestHeartbeatTableIsPerMigration(t *testing.T) {
	if got := beatSQL("mig_ab12"); !strings.Contains(got, "`openeverest_migration`.`heartbeat_mig_ab12`") {
		t.Fatalf("beatSQL = %s", got)
	}
	other := Options{Databases: []string{"shop"}}.doPatterns("mig_cd34")
	if slices.Contains(other, `openeverest\_migration.heartbeat\_mig\_ab12`) {
		t.Fatal("a channel must not replicate another migration's heartbeat")
	}
}
