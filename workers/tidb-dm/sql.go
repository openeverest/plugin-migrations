package main

// SQL run by the agent. Values are passed as placeholders, which the driver
// interpolates. Identifiers are validated names, quoted with quoteIdentifier.

// heartbeatDatabase is the migration's own database on the source, written
// every heartbeat interval. DM replicates it with the migrated databases, so
// its value on the target gives the lag with the source's clock only.
func heartbeatDatabase(name string) string { return "openeverest_" + name }

func heartbeatTable(name string) string {
	return quoteIdentifier(heartbeatDatabase(name)) + ".`heartbeat`"
}

func createHeartbeatDatabaseSQL(name string) string {
	return "CREATE DATABASE IF NOT EXISTS " + quoteIdentifier(heartbeatDatabase(name))
}

func createHeartbeatTableSQL(name string) string {
	return "CREATE TABLE IF NOT EXISTS " + heartbeatTable(name) +
		" (id TINYINT NOT NULL PRIMARY KEY, beat_at DATETIME(6) NOT NULL) ENGINE=InnoDB"
}

func beatSQL(name string) string {
	return "INSERT INTO " + heartbeatTable(name) + " (id, beat_at) VALUES (1, UTC_TIMESTAMP(6)) " +
		"ON DUPLICATE KEY UPDATE beat_at = UTC_TIMESTAMP(6)"
}

func readHeartbeatSQL(name string) string {
	return "SELECT beat_at FROM " + heartbeatTable(name) + " WHERE id = 1"
}

func dropHeartbeatDatabaseSQL(name string) string {
	return "DROP DATABASE IF EXISTS " + quoteIdentifier(heartbeatDatabase(name))
}

// The state table exists only on the target, outside DM's block-allow list.
const (
	createStateDatabaseSQL = "CREATE DATABASE IF NOT EXISTS `openeverest_migration`"
	createStateTableSQL    = "CREATE TABLE IF NOT EXISTS `openeverest_migration`.`dm_state` (" +
		"name VARCHAR(64) NOT NULL PRIMARY KEY, options TEXT NOT NULL, phase VARCHAR(16) NOT NULL, " +
		"paused BOOLEAN NOT NULL DEFAULT FALSE, failure TEXT NULL, notes TEXT NULL, updated_at DATETIME(6) NOT NULL)"
	stateTableExistsSQL = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'openeverest_migration' AND table_name = 'dm_state'"
	// writeStateSQL starts (or restarts, after a failed preflight) a migration.
	writeStateSQL = "REPLACE INTO `openeverest_migration`.`dm_state` (name, options, phase, paused, failure, notes, updated_at) " +
		"VALUES (?, ?, ?, FALSE, NULL, NULL, UTC_TIMESTAMP(6))"
	readStateSQL = "SELECT options, phase, paused, COALESCE(failure, ''), COALESCE(notes, '') " +
		"FROM `openeverest_migration`.`dm_state` WHERE name = ?"
	setPhaseSQL              = "UPDATE `openeverest_migration`.`dm_state` SET phase = ?, failure = NULL, updated_at = UTC_TIMESTAMP(6) WHERE name = ?"
	setFailureSQL            = "UPDATE `openeverest_migration`.`dm_state` SET failure = ?, updated_at = UTC_TIMESTAMP(6) WHERE name = ?"
	setPausedSQL             = "UPDATE `openeverest_migration`.`dm_state` SET paused = ?, updated_at = UTC_TIMESTAMP(6) WHERE name = ?"
	setNotesSQL              = "UPDATE `openeverest_migration`.`dm_state` SET notes = ?, updated_at = UTC_TIMESTAMP(6) WHERE name = ?"
	deleteStateSQL           = "DELETE FROM `openeverest_migration`.`dm_state` WHERE name = ?"
	countStateSQL            = "SELECT COUNT(*) FROM `openeverest_migration`.`dm_state`"
	dropStateSQL             = "DROP TABLE IF EXISTS `openeverest_migration`.`dm_state`"
	countMigrationTablesSQL  = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'openeverest_migration'"
	dropMigrationDatabaseSQL = "DROP DATABASE IF EXISTS `openeverest_migration`"
)

// DM keeps its checkpoints in metaSchema on the target, in tables named
// after the task; Lightning (DM's load) records conflicts in lightningSchema.
const (
	metaSchema      = "dm_meta"
	lightningSchema = "lightning_task_info"
	schemaTablesSQL = "SELECT table_name FROM information_schema.tables WHERE table_schema = ?"
	countRowsSQL    = "SELECT COUNT(*) FROM "
)

const serverVariablesSQL = "SHOW GLOBAL VARIABLES WHERE Variable_name IN (" +
	"'version', 'version_comment', 'server_uuid', 'server_id', 'log_bin', 'gtid_mode', 'binlog_format', " +
	"'binlog_row_image', 'binlog_transaction_compression')"

// sourceTablesSQL lists the base tables of the migrated databases with their
// size, which estimates the dump.
const sourceTablesSQL = "SELECT table_schema, table_name, COALESCE(data_length, 0) FROM information_schema.tables " +
	"WHERE table_type = 'BASE TABLE' AND table_schema IN (?)"

const existingDatabasesSQL = "SELECT schema_name FROM information_schema.schemata WHERE schema_name IN (?)"

// sourceCollationsSQL lists the collations of the migrated tables and
// columns; DM creates them on TiDB as they are (collation_compatible: strict).
const sourceCollationsSQL = "SELECT table_schema, table_name, table_collation FROM information_schema.tables " +
	"WHERE table_type = 'BASE TABLE' AND table_collation IS NOT NULL AND table_schema IN (?) " +
	"UNION SELECT table_schema, table_name, collation_name FROM information_schema.columns " +
	"WHERE collation_name IS NOT NULL AND table_schema IN (?)"

const spatialColumnsSQL = "SELECT table_schema, table_name, column_name FROM information_schema.columns WHERE table_schema IN (?) " +
	"AND data_type IN ('geometry', 'point', 'linestring', 'polygon', 'multipoint', 'multilinestring', 'multipolygon', " +
	"'geometrycollection', 'geomcollection')"

// cascadingForeignKeysSQL finds foreign keys whose actions change other
// rows. MySQL does not write those changes to the binary log, and DM applies
// changes with foreign_key_checks off, so TiDB would never make them.
const cascadingForeignKeysSQL = "SELECT constraint_schema, table_name, constraint_name FROM information_schema.referential_constraints " +
	"WHERE constraint_schema IN (?) AND (update_rule NOT IN ('RESTRICT', 'NO ACTION') OR delete_rule NOT IN ('RESTRICT', 'NO ACTION'))"

// Objects TiDB does not support; they are listed in the migration's notes.
const (
	countRoutinesSQL = "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema IN (?)"
	countTriggersSQL = "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema IN (?)"
	countEventsSQL   = "SELECT COUNT(*) FROM information_schema.events WHERE event_schema IN (?)"
	countFulltextSQL = "SELECT COUNT(DISTINCT table_schema, table_name, index_name) FROM information_schema.statistics " +
		"WHERE index_type = 'FULLTEXT' AND table_schema IN (?)"
)

const viewsSQL = "SELECT table_schema, table_name FROM information_schema.views WHERE table_schema IN (?) ORDER BY table_schema, table_name"

const (
	targetVersionSQL     = "SELECT VERSION()"
	targetCollationsSQL  = "SELECT collation_name FROM information_schema.collations"
	nonEmptyDatabasesSQL = "SELECT DISTINCT table_schema FROM information_schema.tables WHERE table_schema IN (?)"
)

// Server error numbers the agent reacts to.
const (
	errNoSuchTable    = 1146
	errUnknownCommand = 1064
)
