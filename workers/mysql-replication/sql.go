package main

// SQL run by the agent. Values are passed as placeholders, which the driver
// interpolates (also into statements MySQL cannot prepare). Identifiers are
// validated names, quoted with quoteIdentifier.

// heartbeatTable is the migration's own heartbeat table, on both sides. Each
// migration has its own table and its channel replicates only that one, so
// migrations sharing a source never receive each other's heartbeats.
func heartbeatTable(channel string) string { return "heartbeat_" + channel }

func heartbeatIdentifier(channel string) string {
	return "`openeverest_migration`." + quoteIdentifier(heartbeatTable(channel))
}

const createMigrationDatabaseSQL = "CREATE DATABASE IF NOT EXISTS `openeverest_migration`"

// createHeartbeatTableSQL is identical on both sides. The source row is
// replicated into the target table; the agent seeds the target row itself
// because the source row predates the copy's snapshot.
func createHeartbeatTableSQL(channel string) string {
	return "CREATE TABLE IF NOT EXISTS " + heartbeatIdentifier(channel) +
		" (name VARCHAR(64) NOT NULL PRIMARY KEY, beat_at DATETIME(6) NOT NULL) ENGINE=InnoDB"
}

func beatSQL(channel string) string {
	return "INSERT INTO " + heartbeatIdentifier(channel) + " (name, beat_at) VALUES (?, UTC_TIMESTAMP(6)) " +
		"ON DUPLICATE KEY UPDATE beat_at = UTC_TIMESTAMP(6)"
}

// seedHeartbeatSQL creates the target row the replicated source row updates.
// Its timestamp predates any real heartbeat, so the lag stays unknown until
// the first one is replicated.
func seedHeartbeatSQL(channel string) string {
	return "INSERT INTO " + heartbeatIdentifier(channel) + " (name, beat_at) VALUES (?, '1970-01-01 00:00:01') " +
		"ON DUPLICATE KEY UPDATE beat_at = '1970-01-01 00:00:01'"
}

func readHeartbeatSQL(channel string) string {
	return "SELECT beat_at FROM " + heartbeatIdentifier(channel) + " WHERE name = ?"
}

func dropHeartbeatSQL(channel string) string {
	return "DROP TABLE IF EXISTS " + heartbeatIdentifier(channel)
}

const countMigrationTablesSQL = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'openeverest_migration'"

const dropMigrationDatabaseSQL = "DROP DATABASE IF EXISTS `openeverest_migration`"

const migrationTableExistsSQL = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'openeverest_migration' AND table_name = ?"

// The state table exists only on the target; the replication filters never
// match it. On PXC, Galera copies it to every node, so a restarted worker
// finds it through the proxy.
const createStateTableSQL = "CREATE TABLE IF NOT EXISTS `openeverest_migration`.`state` (" +
	"name VARCHAR(64) NOT NULL PRIMARY KEY, replica_address VARCHAR(255) NOT NULL, options JSON NOT NULL, " +
	"phase VARCHAR(16) NOT NULL, paused BOOLEAN NOT NULL DEFAULT FALSE, local_infile VARCHAR(8) NULL, " +
	"copied_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0, notes TEXT NULL, updated_at DATETIME(6) NOT NULL) ENGINE=InnoDB"

const insertStateSQL = "INSERT INTO `openeverest_migration`.`state` " +
	"(name, replica_address, options, phase, paused, local_infile, copied_bytes, updated_at) " +
	"VALUES (?, ?, ?, 'copying', FALSE, ?, 0, UTC_TIMESTAMP(6)) AS new " +
	"ON DUPLICATE KEY UPDATE replica_address = new.replica_address, options = new.options, phase = 'copying', " +
	"paused = FALSE, local_infile = new.local_infile, copied_bytes = 0, updated_at = new.updated_at"

const readStateSQL = "SELECT replica_address, options, phase, paused, COALESCE(local_infile, ''), copied_bytes, COALESCE(notes, '') " +
	"FROM `openeverest_migration`.`state` WHERE name = ?"

const setPhaseSQL = "UPDATE `openeverest_migration`.`state` SET phase = ?, copied_bytes = ?, updated_at = UTC_TIMESTAMP(6) WHERE name = ?"

const setCompletedSQL = "UPDATE `openeverest_migration`.`state` SET phase = 'completed', copied_bytes = ?, notes = ?, " +
	"updated_at = UTC_TIMESTAMP(6) WHERE name = ?"

const setPausedSQL = "UPDATE `openeverest_migration`.`state` SET paused = ?, updated_at = UTC_TIMESTAMP(6) WHERE name = ?"

const serverVariablesSQL = "SHOW GLOBAL VARIABLES WHERE Variable_name IN (" +
	"'version', 'version_comment', 'server_uuid', 'log_bin', 'gtid_mode', 'binlog_format', 'skip_replica_start', " +
	"'local_infile', 'wsrep_provider', 'wsrep_node_incoming_address')"

// sourceTablesSQL lists base tables of the migrated databases, to find those
// PXC cannot replicate, and their sizes for the copy progress.
const sourceTablesSQL = "SELECT t.table_schema, t.table_name, COALESCE(t.engine, ''), COALESCE(t.data_length, 0), " +
	"EXISTS (SELECT 1 FROM information_schema.table_constraints c WHERE c.table_schema = t.table_schema " +
	"AND c.table_name = t.table_name AND c.constraint_type = 'PRIMARY KEY') " +
	"FROM information_schema.tables t WHERE t.table_type = 'BASE TABLE' AND t.table_schema IN (?)"

const existingDatabasesSQL = "SELECT schema_name FROM information_schema.schemata WHERE schema_name IN (?)"

const enabledEventsSQL = "SELECT event_schema, event_name, definer FROM information_schema.events WHERE status = 'ENABLED' AND event_schema IN (?)"

const targetEventsSQL = "SELECT event_schema, event_name, definer, status FROM information_schema.events WHERE event_schema IN (?)"

const gtidExecutedSQL = "SELECT @@GLOBAL.gtid_executed"

const missingGTIDsSQL = "SELECT GTID_SUBTRACT(?, @@GLOBAL.gtid_executed)"

// appendGTIDPurgedSQL marks the copied transactions as applied. Only GTIDs
// the target does not have yet are appended, so it is safe to repeat.
const appendGTIDPurgedSQL = "SET GLOBAL gtid_purged = CONCAT('+', ?)"

const waitForGTIDsSQL = "SELECT WAIT_FOR_EXECUTED_GTID_SET(?, ?)"

const setLocalInfileSQL = "SET GLOBAL local_infile = ?"

// changeSourceSQL configures the channel. REQUIRE_ROW_FORMAT rejects
// statement-based changes, which the filters could not attribute reliably.
// The source is never added to the asynchronous connection failover list:
// the PXC operator removes channels it finds there that it did not create.
const changeSourceSQL = "CHANGE REPLICATION SOURCE TO SOURCE_HOST = ?, SOURCE_PORT = ?, SOURCE_USER = ?, SOURCE_PASSWORD = ?, " +
	"SOURCE_AUTO_POSITION = 1, SOURCE_SSL = ?, GET_SOURCE_PUBLIC_KEY = 1, SOURCE_CONNECT_RETRY = 10, " +
	"SOURCE_RETRY_COUNT = 86400, REQUIRE_ROW_FORMAT = 1 FOR CHANNEL ?"

const workerErrorsSQL = "SELECT LAST_ERROR_NUMBER, LAST_ERROR_MESSAGE, APPLYING_TRANSACTION " +
	"FROM performance_schema.replication_applier_status_by_worker WHERE CHANNEL_NAME = ? AND LAST_ERROR_NUMBER <> 0 " +
	"ORDER BY LAST_ERROR_TIMESTAMP DESC LIMIT 1"

// Server error numbers the agent reacts to.
const (
	errAccessDenied     = 1227
	errUnknownDatabase  = 1049
	errChannelNotExists = 3074
	errSourceFatalRead  = 1236
	errSyntax           = 1064
)
