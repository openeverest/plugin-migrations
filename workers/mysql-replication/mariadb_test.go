package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func frame(name, content string) string {
	return "\n-- " + name + " " + strconv.Itoa(len(content)) + "\n" + content
}

func TestForwardStreamReadsPositionAndProgress(t *testing.T) {
	metadata := "[source]\nFile = mysql-bin.000003\nPosition = 1234\n# executed_gtid_set = \"0-1-42,1-2-7\"\n"
	stream := frame("shop-schema-create.sql", "CREATE DATABASE `shop`;\n") +
		frame("shop.orders-schema.sql", "CREATE TABLE `orders` (id int);\n") +
		// Data may contain anything, including text that looks like a header.
		frame("shop.orders.00000.sql", "INSERT INTO `orders` VALUES (1);\n-- fake 99\n") +
		frame("shop.orders.00001.sql", "INSERT INTO `orders` VALUES (2);\n") +
		frame("metadata", metadata)
	var forwarded bytes.Buffer
	var progress []uint64
	position, err := forwardStream(strings.NewReader(stream), &forwarded, func(loaded uint64) { progress = append(progress, loaded) })
	if err != nil {
		t.Fatal(err)
	}
	if position != "0-1-42,1-2-7" {
		t.Errorf("position = %q", position)
	}
	if forwarded.String() != stream {
		t.Error("the stream must reach myloader unchanged")
	}
	first := uint64(len("INSERT INTO `orders` VALUES (1);\n-- fake 99\n"))
	if len(progress) != 2 || progress[0] != first || progress[1] != first+uint64(len("INSERT INTO `orders` VALUES (2);\n")) {
		t.Errorf("progress = %v; only data files count", progress)
	}
}

func TestForwardStreamRejectsGarbage(t *testing.T) {
	for name, stream := range map[string]string{
		"no header": "hello\n",
		"bad size":  "\n-- file.sql x\n",
		"truncated": "\n-- file.sql 10\nabc",
	} {
		if _, err := forwardStream(strings.NewReader(stream), &bytes.Buffer{}, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	position, err := forwardStream(strings.NewReader(frame("metadata", "[source]\n")), &bytes.Buffer{}, nil)
	if err != nil || position != "" {
		t.Errorf("metadata without a position: %q, %v", position, err)
	}
}

func TestParseFrameHeaderKeepsSpacesInNames(t *testing.T) {
	name, size, err := parseFrameHeader("-- my db.my table.00000.sql 17\n")
	if err != nil || name != "my db.my table.00000.sql" || size != 17 {
		t.Fatalf("got %q %d %v", name, size, err)
	}
}

func TestWriteOptionFileEscapesValues(t *testing.T) {
	dir := t.TempDir()
	path, err := writeOptionFile(dir, "dump.cnf", "mydumper", Endpoint{
		Host: "db", Port: 3306, User: "root", Password: ` p\a#s"s` + "\n", SSLMode: sslRequired, SSLCA: "/tls/ca.crt",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || filepath.Dir(path) != dir {
		t.Fatalf("option file %s: %v %v", path, info, err)
	}
	content, _ := os.ReadFile(path)
	// The client library reads host, user and password: an unquoted "#" would
	// start a comment there.
	for _, line := range []string{"[mydumper]", `host="db"`, "port=3306", `user="root"`, `password=" p\\a#s\"s\n"`, "ssl-mode=REQUIRED", "ca=/tls/ca.crt"} {
		if !strings.Contains(string(content), line+"\n") {
			t.Errorf("missing %q in:\n%s", line, content)
		}
	}
}

func mariadbFacts() (sourceFacts, targetFacts) {
	source := sourceFacts{
		Info:         serverInfo{Version: "10.11.9-MariaDB-log", Comment: "MariaDB Server", ServerID: "10", LogBin: true, BinlogFormat: "ROW"},
		CanReplicate: true,
	}
	target := targetFacts{Info: serverInfo{Version: "11.4.5-MariaDB", Comment: "MariaDB Server", ServerID: "1", Galera: true}}
	return source, target
}

func TestCheckPreflightMariaDB(t *testing.T) {
	options := Options{Databases: []string{"shop"}, CopyThreads: 4}
	ready := mariadbFlavor{targetSkipsReplicaStart: true}
	source, target := mariadbFacts()
	if err := checkPreflight(ready, source, target, options); err != nil {
		t.Fatalf("valid setup rejected: %v", err)
	}
	cases := map[string]struct {
		change func(*sourceFacts, *targetFacts)
		flavor mariadbFlavor
		want   string
	}{
		"old source":         {func(s *sourceFacts, _ *targetFacts) { s.Info.Version = "10.5.27-MariaDB" }, ready, "10.6 or later"},
		"old target":         {func(_ *sourceFacts, t *targetFacts) { t.Info.Version = "10.6.20-MariaDB" }, ready, "10.11 or later"},
		"downgrade":          {func(s *sourceFacts, _ *targetFacts) { s.Info.Version = "11.8.2-MariaDB" }, ready, "older than the source"},
		"MySQL source":       {func(s *sourceFacts, _ *targetFacts) { s.Info.Version, s.Info.Comment = "8.0.36", "MySQL" }, ready, "use the MySQL engine"},
		"MySQL target":       {func(_ *sourceFacts, t *targetFacts) { t.Info.Version, t.Info.Comment = "8.4.3", "MySQL" }, ready, "not MariaDB"},
		"mixed binlog":       {func(s *sourceFacts, _ *targetFacts) { s.Info.BinlogFormat = "MIXED" }, ready, "binlog_format=MIXED"},
		"same server_id":     {func(s *sourceFacts, _ *targetFacts) { s.Info.ServerID = "1" }, ready, "same server_id (1)"},
		"replica auto-start": {func(*sourceFacts, *targetFacts) {}, mariadbFlavor{}, "skip-slave-start"},
	}
	for name, test := range cases {
		source, target := mariadbFacts()
		test.change(&source, &target)
		if err := checkPreflight(test.flavor, source, target, options); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: want %q, got %v", name, test.want, err)
		}
	}
}

func TestMySQLRejectsMariaDBTarget(t *testing.T) {
	source, target := goodFacts()
	source.Info.ServerID, target.Info.ServerID = "1", "2"
	if err := checkPreflight(mysqlFlavor{}, source, target, Options{Databases: []string{"shop"}}); err != nil {
		t.Fatalf("valid setup rejected: %v", err)
	}
	_, mariadbTarget := mariadbFacts()
	if err := checkPreflight(mysqlFlavor{}, source, mariadbTarget, Options{Databases: []string{"shop"}}); err == nil {
		t.Fatal("a MariaDB target must be rejected")
	}
}

func TestNewServerInfoDerivesMariaDBGaleraNode(t *testing.T) {
	info := newServerInfo(map[string]string{
		"version": "11.4.5-MariaDB", "port": "3306", "server_id": "1",
		"wsrep_provider": "/usr/lib/galera/libgalera_smm.so", "wsrep_node_incoming_address": "AUTO",
		"wsrep_cluster_address": "gcomm://db-0.db-internal.shop.svc.cluster.local,db-1.db-internal.shop.svc.cluster.local:4567,db-2.db-internal.shop.svc.cluster.local",
		"wsrep_node_name":       "db-1",
	})
	if !info.Galera || info.NodeAddress != "db-1.db-internal.shop.svc.cluster.local:3306" || info.ServerID != "1" {
		t.Fatalf("info = %+v", info)
	}
	if host := clusterMemberHost("gcomm://db-0.x,db-1.x", "db-2"); host != "" {
		t.Errorf("unknown node matched %q", host)
	}
	if host := clusterMemberHost("gcomm://", "db-0"); host != "" {
		t.Errorf("empty address matched %q", host)
	}
}

func TestSkippableDatabaseMariaDBMessage(t *testing.T) {
	failure := workerError{Number: errUnknownDatabase, Message: "Error 'Unknown database 'other'' on query. Default database: 'shop'. Query: 'CREATE PROCEDURE other.p() SELECT 1'"}
	if database, ok := skippableDatabase(failure, Options{Databases: []string{"shop"}}); !ok || database != "other" {
		t.Fatalf("got %q, %v", database, ok)
	}
}

func TestGrantsReplication(t *testing.T) {
	for grant, want := range map[string]bool{
		"GRANT ALL PRIVILEGES ON *.* TO `root`@`%` IDENTIFIED BY PASSWORD '*X' WITH GRANT OPTION": true,
		"GRANT SELECT, RELOAD, REPLICATION SLAVE, BINLOG MONITOR ON *.* TO `repl`@`%`":            true,
		"GRANT ALL PRIVILEGES ON `everest`.* TO `everest`@`%`":                                    false,
		"GRANT SELECT ON *.* TO `reader`@`%`":                                                     false,
	} {
		if got := grantsReplication([]string{grant}); got != want {
			t.Errorf("%s: got %v", grant, got)
		}
	}
}

func TestExitedWithErrorIgnoresToolsTheAgentKilled(t *testing.T) {
	failed := exec.Command("sh", "-c", "exit 3")
	if err := failed.Run(); !exitedWithError(failed, err) {
		t.Fatalf("a tool exiting with an error must count: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	killed := exec.CommandContext(ctx, "sleep", "5")
	if err := killed.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := killed.Wait(); exitedWithError(killed, err) {
		t.Fatalf("a tool the agent killed must not count: %v", err)
	}
}
