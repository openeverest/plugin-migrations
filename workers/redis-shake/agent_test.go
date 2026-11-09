package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func TestKeySlotAndTags(t *testing.T) {
	if crc16("123456789") != 0x31C3 {
		t.Fatal("crc16 must be CRC16-XMODEM")
	}
	if keySlot("foo") != 12182 || keySlot("{user1000}.following") != keySlot("user1000") || keySlot("{}x") != keySlot("{}x") {
		t.Fatal("keySlot does not follow the cluster spec")
	}
	if keySlot("a{}b{c}") != int(crc16("a{}b{c}"))%clusterSlots {
		t.Fatal("an empty hash tag hashes the whole key")
	}
	for slot := range clusterSlots {
		if got := keySlot("{" + slotTag(slot) + "}:x"); got != slot {
			t.Fatalf("tag for slot %d hashes to %d", slot, got)
		}
	}
	if keySlot(heartbeatKey("mig_ab", 5461)) != 5461 {
		t.Fatal("the heartbeat key must land on the requested slot")
	}
}

func TestParseEndpoint(t *testing.T) {
	endpoint, err := parseEndpoint("rediss://default:p%40ss@valkey-orders.shop.svc:6380/?ca=/etc/ca.crt&cert=/etc/tls.crt&key=/etc/tls.key")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "valkey-orders.shop.svc" || endpoint.Port != 6380 || endpoint.User != "default" || endpoint.Password != "p@ss" ||
		!endpoint.TLS || endpoint.CA != "/etc/ca.crt" || endpoint.Cert != "/etc/tls.crt" || endpoint.Key != "/etc/tls.key" {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	if endpoint, err := parseEndpoint("valkey://:pw@db"); err != nil || endpoint.Port != 6379 || endpoint.TLS || endpoint.Password != "pw" {
		t.Fatalf("endpoint = %+v, %v", endpoint, err)
	}
	for _, raw := range []string{
		"mysql://u:secret@db", "redis://", "redis://u:secret@db:0", "rediss://u:secret@db/?cert=/c",
		"redis://u:secret@db/?ca=/ca.crt", "::",
	} {
		_, err := parseEndpoint(raw)
		if err == nil {
			t.Errorf("%s accepted", raw)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error leaks the URI: %v", raw, err)
		}
	}
}

func TestDecodeOptions(t *testing.T) {
	options, err := decodeOptions(json.RawMessage(`{"flushTarget":true,"excludeKeyPrefixes":["cache:","tmp:"]}`))
	if err != nil || !options.FlushTarget || !slices.Equal(options.ExcludeKeyPrefixes, []string{"cache:", "tmp:"}) {
		t.Fatalf("options = %+v, %v", options, err)
	}
	if options, err := decodeOptions(nil); err != nil || options.FlushTarget {
		t.Fatalf("empty options = %+v, %v", options, err)
	}
	for _, raw := range []string{
		`{"unknown":1}`, `{"excludeKeyPrefixes":[""]}`, `{"excludeKeyPrefixes":["a\nb"]}`,
		`{"excludeKeyPrefixes":["__open"]}`, `{} {}`, `[]`,
	} {
		if _, err := decodeOptions(json.RawMessage(raw)); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
}

func TestShakeConfig(t *testing.T) {
	source := Endpoint{Host: "src", Port: 6379, User: "default", Password: `p"w\x`, TLS: true, CA: "/s/ca.crt", Cert: "/s/tls.crt", Key: "/s/tls.key"}
	target := Endpoint{Host: "valkey-orders.shop.svc", Port: 6379, User: "default", Password: "t", TLS: true, CA: "/t/ca.crt"}
	config := shakeConfig(source, target, false, true, Options{ExcludeKeyPrefixes: []string{`it's'''"`}}, "/work/shake")
	for _, fragment := range []string{
		"[sync_reader]\ncluster = false\naddress = \"src:6379\"\nusername = \"default\"\npassword = \"p\\\"w\\\\x\"\ntls = true\n" +
			"tls_config = { ca_cert = \"/s/ca.crt\", cert = \"/s/tls.crt\", key = \"/s/tls.key\" }\n",
		"[redis_writer]\ncluster = true\naddress = \"valkey-orders.shop.svc:6379\"",
		`local EXCLUDE = {"it\039s\039\039\039\034"}`,
		"rdb_restore_command_behavior = \"rewrite\"", "empty_db_before_sync = false", "status_port = 2244", `dir = "/work/shake"`,
	} {
		if !strings.Contains(config, fragment) {
			t.Errorf("config lacks %q:\n%s", fragment, config)
		}
	}
	writer := config[strings.Index(config, "[redis_writer]"):strings.Index(config, "[filter]")]
	if strings.Contains(writer, "tls_config") {
		t.Error("RedisShake 4.7 ignores a CA without a client certificate; tls_config must be left out")
	}
	function := config[strings.Index(config, "'''")+3 : strings.LastIndex(config, "'''")]
	if strings.Contains(function, "'''") {
		t.Error("the Lua function must not end the TOML literal string")
	}
}

type luaResult struct {
	calls [][]string
	err   error
}

// runShakeLua runs the generated function the way RedisShake 4.7 does.
func runShakeLua(t *testing.T, exclude []string, db int, cmd string, argv []string, keyIndexes []int) luaResult {
	t.Helper()
	config := shakeConfig(Endpoint{}, Endpoint{}, false, true, Options{ExcludeKeyPrefixes: exclude}, "/")
	code := config[strings.Index(config, "'''")+3 : strings.LastIndex(config, "'''")]
	state := lua.NewState()
	defer state.Close()
	state.SetGlobal("DB", lua.LNumber(db))
	state.SetGlobal("CMD", lua.LString(cmd))
	keys, slots, args := state.NewTable(), state.NewTable(), state.NewTable()
	for _, index := range keyIndexes {
		keys.Append(lua.LString(argv[index]))
		slots.Append(lua.LNumber(keySlot(argv[index])))
	}
	for _, arg := range argv {
		args.Append(lua.LString(arg))
	}
	state.SetGlobal("KEYS", keys)
	state.SetGlobal("SLOTS", slots)
	state.SetGlobal("ARGV", args)
	var result luaResult
	shake := state.NewTable()
	state.SetField(shake, "call", state.NewFunction(func(ls *lua.LState) int {
		table := ls.ToTable(2)
		var call []string
		for i := 1; i <= table.Len(); i++ {
			call = append(call, table.RawGetInt(i).String())
		}
		result.calls = append(result.calls, call)
		return 0
	}))
	state.SetGlobal("shake", shake)
	result.err = state.DoString(code)
	return result
}

func TestShakeLua(t *testing.T) {
	join := func(calls [][]string) string {
		var parts []string
		for _, call := range calls {
			parts = append(parts, strings.Join(call, " "))
		}
		return strings.Join(parts, "; ")
	}
	cases := []struct {
		name       string
		exclude    []string
		db         int
		argv       []string
		keyIndexes []int
		want       string
		wantErr    string
	}{
		{"single key", nil, 0, []string{"SET", "user:1", "a"}, []int{1}, "SET user:1 a", ""},
		{"keyless", nil, 0, []string{"FLUSHALL"}, nil, "FLUSHALL", ""},
		{"same slot", nil, 0, []string{"MSET", "{u}a", "1", "{u}b", "2"}, []int{1, 3}, "MSET {u}a 1 {u}b 2", ""},
		{"cross-slot MSET", nil, 0, []string{"MSET", "a", "1", "b", "2"}, []int{1, 3}, "SET a 1; SET b 2", ""},
		{"cross-slot DEL", nil, 0, []string{"DEL", "a", "b"}, []int{1, 2}, "DEL a; DEL b", ""},
		{"excluded key", []string{"cache:"}, 0, []string{"SET", "cache:1", "a"}, []int{1}, "", ""},
		{"partly excluded DEL", []string{"cache:"}, 0, []string{"UNLINK", "cache:1", "user:2"}, []int{1, 2}, "UNLINK user:2", ""},
		{"partly excluded MSETNX", []string{"cache:"}, 0, []string{"MSETNX", "cache:{x}1", "1", "{x}user", "2"}, []int{1, 3}, "SET {x}user 2", ""},
		{"publish", nil, 0, []string{"PUBLISH", "ch", "m"}, nil, "", ""},
		{"other database", nil, 2, []string{"SET", "a", "1"}, []int{1}, "", "only database 0"},
		{"cross-slot RENAME", nil, 0, []string{"RENAME", "a", "b"}, []int{1, 2}, "", "cannot replicate RENAME: its keys hash to different cluster slots"},
		{"mixed RENAME", []string{"cache:"}, 0, []string{"RENAME", "cache:{x}a", "{x}b"}, []int{1, 2}, "", "changes both excluded and migrated keys"},
	}
	for _, test := range cases {
		result := runShakeLua(t, test.exclude, test.db, test.argv[0], test.argv, test.keyIndexes)
		switch {
		case test.wantErr != "" && (result.err == nil || !strings.Contains(result.err.Error(), test.wantErr)):
			t.Errorf("%s: error = %v, want %q", test.name, result.err, test.wantErr)
		case test.wantErr == "" && result.err != nil:
			t.Errorf("%s: %v", test.name, result.err)
		case join(result.calls) != test.want:
			t.Errorf("%s: calls = %q, want %q", test.name, join(result.calls), test.want)
		}
	}
}

func TestFollowCapturesColoredErrors(t *testing.T) {
	process := &shakeProcess{}
	process.follow(strings.NewReader("\x1b[90m2026-11-09 14:02:05\x1b[0m \x1b[1m\x1b[31mERR\x1b[0m\x1b[0m [writer_10.0.0.4_6379] receive reply failed. " +
		"cmd=[SET user:1 secret], error=[read tcp 1->2: read: connection reset by peer]\n\t\t\tRedisShake/internal/writer/x.go:1 -> f()\n"))
	if process.lastError != "writing SET to the target failed: read tcp 1->2: read: connection reset by peer" {
		t.Fatalf("lastError = %q", process.lastError)
	}
}

func TestShakeErrorMessages(t *testing.T) {
	cases := map[string]string{
		"2026-11-09 12:54:23 ERR [writer_172.21.0.4_6379] receive reply failed. cmd=[RESTORE hfe 0 \x00secret\x01 replace], error=[ERR Bad data format]":         "writing RESTORE to the target failed: ERR Bad data format",
		"2026-11-09 12:52:55 ERR CROSSSLOT Keys in request don't hash to the same slot. argv=[MSET ms:a:0 1 ms:b:0 2]":                                           "CROSSSLOT Keys in request don't hash to the same slot. argv=[MSET …]",
		"2026-11-09 12:57:55 ERR [writer_172.21.0.3_6379] receive reply failed. cmd=[SET str:479 w1005], error=[read tcp 1->2: read: connection reset by peer]":  "writing SET to the target failed: read tcp 1->2: read: connection reset by peer",
		"2026-11-09 13:00:00 ERR load function script failed: <string>:12: the source changed database 3: only database 0 can be migrated into a Valkey cluster": "the source changed database 3: only database 0 can be migrated into a Valkey cluster",
		"2026-11-09 13:00:00 ERR dial failed. address=[db:6379], tls=[true], err=[dial tcp: lookup db: no such host]":                                            "dial failed. address=[db:6379], tls=[true], err=[dial tcp: lookup db: no such host]",
	}
	for line, want := range cases {
		if got := shakeErrorMessage(line); got != want {
			t.Errorf("%q\n got %q\nwant %q", line, got, want)
		}
	}
	if strings.Contains(redactArguments("cmd=[SET user:1 secret-value], error=[x]"), "secret-value") {
		t.Error("command arguments must be redacted")
	}
	if !transientError("writing SET to the target failed: read: connection reset by peer") || !transientError("dial failed. err=[dial tcp: i/o timeout]") ||
		!transientError("empty the target: dial tcp: lookup valkey-0.valkey.shop.svc.cluster.local on 10.43.0.10:53: no such host") ||
		transientError("writing RESTORE to the target failed: ERR Bad data format") || transientError("auth failed with reply: WRONGPASS") {
		t.Error("transient classification")
	}
}

func TestParseShakeStatus(t *testing.T) {
	standalone := `{"consistent":false,"reader":{"name":"r","status":"syncing rdb","rdb_file_size_bytes":1000,"rdb_received_bytes":1000,"rdb_sent_bytes":400},
		"writer":{"name":"w","unanswered_bytes":10,"unanswered_entries":3}}`
	status, err := parseShakeStatus([]byte(standalone))
	if err != nil {
		t.Fatal(err)
	}
	if total, copied := status.copyBytes(); status.copyDone() || total != 1000 || copied != 400 {
		t.Fatalf("standalone = %+v", status)
	}
	cluster := `{"consistent":true,"reader":[{"status":"syncing aof","rdb_file_size_bytes":10,"rdb_sent_bytes":10},{"status":"syncing aof","rdb_file_size_bytes":20,"rdb_sent_bytes":20}],
		"writer":[{"unanswered_entries":0},{"unanswered_entries":0},{"unanswered_entries":0}]}`
	status, err = parseShakeStatus([]byte(cluster))
	if err != nil {
		t.Fatal(err)
	}
	if total, copied := status.copyBytes(); !status.copyDone() || total != 30 || copied != 30 || len(status.Readers) != 2 {
		t.Fatalf("cluster = %+v", status)
	}
	if (shakeStatus{}).copyDone() {
		t.Fatal("no readers yet means the copy has not started")
	}
}

func TestCheckPreflight(t *testing.T) {
	source := serverFacts{Version: "7.2.4", Role: "master", ClusterOK: true, RunIDs: []string{"a"}, Keys: map[int]int64{0: 10}}
	target := serverFacts{Valkey: true, Version: "9.0.6", Role: "master", ClusterOK: true, RunIDs: []string{"b"}, Keys: map[int]int64{}}
	if err := checkPreflight(Endpoint{}, source, target, true, Options{}); err != nil {
		t.Fatalf("valid setup rejected: %v", err)
	}
	badSource := serverFacts{Version: "8.2.1", Role: "slave", ClusterOK: false, RunIDs: []string{"b"}, Keys: map[int]int64{0: 1, 3: 2}, VolatileItems: 4}
	badTarget := target
	badTarget.Keys = map[int]int64{0: 7}
	err := checkPreflight(Endpoint{Cert: "/c"}, badSource, badTarget, true, Options{})
	if err == nil {
		t.Fatal("invalid setup accepted")
	}
	for _, fragment := range []string{
		"the source is a slave", "source cluster is not healthy", "needs its CA", "database 3", "4 hash(es) with field expiration",
		"from Redis 8.2.1 to Valkey 9.0.6", "already has 7 key(s)", "same server",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error lacks %q: %v", fragment, err)
		}
	}
	valkeySource := serverFacts{Valkey: true, Version: "9.0.6", Role: "master", ClusterOK: true, Keys: map[int]int64{0: 1}, VolatileItems: 2}
	if err := checkPreflight(Endpoint{}, valkeySource, target, true, Options{}); err != nil {
		t.Fatalf("field expiration between Valkey 9 servers must be allowed: %v", err)
	}
	oldTarget := target
	oldTarget.Version = "8.1.1"
	if err := checkPreflight(Endpoint{}, valkeySource, oldTarget, true, Options{}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("Valkey 8 has no field expiration: %v", err)
	}
	if err := checkPreflight(Endpoint{}, source, badTarget, true, Options{FlushTarget: true}); err != nil {
		t.Fatalf("flushTarget allows a non-empty target: %v", err)
	}
}

func TestAgentRefusesPauseAndEarlyActions(t *testing.T) {
	agent := NewAgent(Config{Name: "mig_x"})
	if err := agent.Start(Options{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("start while recovering: %v", err)
	}
	if err := agent.Pause(); !errors.Is(err, errConflict) {
		t.Fatalf("pause: %v", err)
	}
	if err := agent.Cutover(); !errors.Is(err, errConflict) {
		t.Fatalf("cutover before catching up: %v", err)
	}
	if err := agent.Resume(); !errors.Is(err, errConflict) {
		t.Fatalf("resume of a migration that did not fail: %v", err)
	}
	if status := agent.Status(); status.State != StateStopped {
		t.Fatalf("status while recovering = %+v", status)
	}
}

func TestResumeWaitsForThePreviousRun(t *testing.T) {
	agent := NewAgent(Config{Name: "mig_x"})
	agent.stage, agent.runDone = stageFailed, make(chan struct{})
	if err := agent.Resume(); !errors.Is(err, errConflict) {
		t.Fatalf("resume while the previous run is still stopping: %v", err)
	}
	if agent.stage != stageFailed {
		t.Fatalf("stage = %v", agent.stage)
	}
}

func TestHeartbeatSlotsCoverEveryShardPair(t *testing.T) {
	all := [][]slotRange{{{0, clusterSlots - 1}}}
	threeShards := [][]slotRange{{{0, 5460}}, {{5461, 10922}}, {{10923, 16383}}}
	if got := heartbeatSlots(all, all); !slices.Equal(got, []int{0}) {
		t.Fatalf("standalone to one shard: %v", got)
	}
	if got := heartbeatSlots(all, threeShards); !slices.Equal(got, []int{0, 5461, 10923}) {
		t.Fatalf("standalone to three shards: %v", got)
	}
	// Two source shards, the second owning two ranges; three target shards.
	source := [][]slotRange{{{0, 8000}}, {{8001, 9000}, {12000, 16383}}}
	if got := heartbeatSlots(source, threeShards); !slices.Equal(got, []int{0, 5461, 8001, 12000}) {
		t.Fatalf("pairs = %v", got)
	}
	if got := heartbeatSlots(threeShards, all); !slices.Equal(got, []int{0, 5461, 10923}) {
		t.Fatalf("three shards to one: %v", got)
	}
}
