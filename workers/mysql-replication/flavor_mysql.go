package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// mysqlFlavor migrates MySQL (also Percona Server, PXC) into PXC: MySQL
// Shell copies the data, GTID auto-positioning replicates.
type mysqlFlavor struct{}

const (
	// Target 8.0.23 has CHANGE REPLICATION SOURCE and DISABLE ON REPLICA.
	minMySQLSourceVersion  = 80000
	minMySQLTargetVersion  = 80023
	errMySQLChannelMissing = 3074
)

// changeSourceSQL configures the channel. REQUIRE_ROW_FORMAT rejects
// statement-based changes, which the filters could not attribute reliably.
// The source is never added to the asynchronous connection failover list:
// the PXC operator removes channels it finds there that it did not create.
const changeSourceSQL = "CHANGE REPLICATION SOURCE TO SOURCE_HOST = ?, SOURCE_PORT = ?, SOURCE_USER = ?, SOURCE_PASSWORD = ?, " +
	"SOURCE_AUTO_POSITION = 1, SOURCE_SSL = ?, GET_SOURCE_PUBLIC_KEY = 1, SOURCE_CONNECT_RETRY = 10, " +
	"SOURCE_RETRY_COUNT = 86400, REQUIRE_ROW_FORMAT = 1 FOR CHANNEL ?"

// missingGTIDsSQL and appendGTIDPurgedSQL mark the copied transactions as
// applied. Only GTIDs the target does not have yet are appended, so it is
// safe to repeat.
const (
	missingGTIDsSQL     = "SELECT GTID_SUBTRACT(?, @@GLOBAL.gtid_executed)"
	appendGTIDPurgedSQL = "SET GLOBAL gtid_purged = CONCAT('+', ?)"
)

const mysqlWorkerErrorsSQL = "SELECT LAST_ERROR_NUMBER, LAST_ERROR_MESSAGE, APPLYING_TRANSACTION " +
	"FROM performance_schema.replication_applier_status_by_worker WHERE CHANNEL_NAME = ? AND LAST_ERROR_NUMBER <> 0 " +
	"ORDER BY LAST_ERROR_TIMESTAMP DESC LIMIT 1"

func (mysqlFlavor) checkSource(source sourceFacts) []string {
	var problems []string
	switch {
	case isMariaDB(source.Info):
		problems = append(problems, "the source is MariaDB; use the MariaDB engine")
	case versionNumber(source.Info.Version) < minMySQLSourceVersion:
		problems = append(problems, fmt.Sprintf("the source runs MySQL %s; version 8.0 or later is required", source.Info.Version))
	}
	if source.Info.GTIDMode != "ON" {
		problems = append(problems, fmt.Sprintf("the source has gtid_mode=%s; set gtid_mode=ON and enforce_gtid_consistency=ON", orUnknown(source.Info.GTIDMode)))
	}
	return problems
}

func (mysqlFlavor) checkTarget(source sourceFacts, target targetFacts) []string {
	var problems []string
	sourceVersion, targetVersion := versionNumber(source.Info.Version), versionNumber(target.Info.Version)
	if targetVersion < minMySQLTargetVersion {
		problems = append(problems, fmt.Sprintf("the target runs MySQL %s; version 8.0.23 or later is required", target.Info.Version))
	} else if targetVersion/100 < sourceVersion/100 {
		problems = append(problems, fmt.Sprintf("the target runs MySQL %s, older than the source (%s); migrate to the same or a newer version",
			target.Info.Version, source.Info.Version))
	}
	if target.Info.GTIDMode != "ON" {
		problems = append(problems, fmt.Sprintf("the target has gtid_mode=%s; GTID mode must be ON", orUnknown(target.Info.GTIDMode)))
	}
	if !target.Info.SkipReplicaStart {
		problems = append(problems, "the target must run with skip_replica_start=ON, so that a restarted server does not replicate before the migration worker restores its filters")
	}
	return problems
}

// canReplicate probes the REPLICATION SLAVE privilege (also when granted
// through a role): SHOW REPLICAS requires it.
func (mysqlFlavor) canReplicate(ctx context.Context, db *sql.DB) bool {
	for _, statement := range []string{"SHOW REPLICAS", "SHOW SLAVE HOSTS"} {
		rows, err := db.QueryContext(ctx, statement)
		if err == nil {
			rows.Close()
			return true
		}
		if !isServerError(err, errSyntax) {
			return false
		}
	}
	return false
}

func (mysqlFlavor) usesLocalInfile() bool { return true }

func (mysqlFlavor) copy(ctx context.Context, workDir string, source, target Endpoint, options Options,
	exclusions map[string][]string, progress func(uint64)) (string, error) {
	return runCopy(ctx, workDir, source, target, options, exclusions, progress)
}

func (mysqlFlavor) definerPolicy(ctx context.Context, db *sql.DB) (definerPolicy, error) {
	return readDefinerPolicy(ctx, db)
}

func (mysqlFlavor) configure(ctx context.Context, db *sql.DB, name string, source Endpoint, position string) error {
	var missing string
	if err := db.QueryRowContext(ctx, missingGTIDsSQL, position).Scan(&missing); err != nil {
		return fmt.Errorf("compare GTID sets on the target: %s", serverError(err))
	}
	if missing = strings.Join(strings.Fields(missing), ""); missing != "" {
		if _, err := db.ExecContext(ctx, appendGTIDPurgedSQL, missing); err != nil {
			return fmt.Errorf("record the copied transactions on the target (gtid_purged): %s", serverError(err))
		}
	}
	if _, err := db.ExecContext(ctx, changeSourceSQL, source.Host, source.Port, source.User, source.Password, tlsFlag(source), name); err != nil {
		return fmt.Errorf("configure replication on the target: %s", serverError(err))
	}
	return nil
}

func tlsFlag(source Endpoint) int {
	if source.UsesTLS() {
		return 1
	}
	return 0
}

func (mysqlFlavor) readChannel(ctx context.Context, db *sql.DB, name string) (channelState, error) {
	field, found, err := readRow(ctx, db, "SHOW REPLICA STATUS FOR CHANNEL ?", name)
	if isServerError(err, errMySQLChannelMissing) || (err == nil && !found) {
		return channelState{}, nil
	}
	if err != nil {
		return channelState{}, err
	}
	return newChannelState(field, "Replica"), nil
}

// readWorkerError needs performance_schema; without it, it reports nothing.
func (mysqlFlavor) readWorkerError(ctx context.Context, db *sql.DB, name string, _ channelState) (workerError, bool) {
	var result workerError
	if err := db.QueryRowContext(ctx, mysqlWorkerErrorsSQL, name).Scan(&result.Number, &result.Message, &result.Transaction); err != nil {
		return workerError{}, false
	}
	return result, true
}

// setFilters sets exactly the migration's filters; the applier must be stopped.
func (mysqlFlavor) setFilters(ctx context.Context, db *sql.DB, name string, options Options) error {
	do := options.doPatterns(name)
	// REPLICATE_IGNORE_TABLE takes identifiers; the names are validated.
	ignore := make([]string, 0, len(options.ignoreTables()))
	for _, table := range options.ignoreTables() {
		database, tableName, _ := strings.Cut(table, ".")
		ignore = append(ignore, quoteIdentifier(database)+"."+quoteIdentifier(tableName))
	}
	statement := "CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = (" + placeholders(len(do)) +
		"), REPLICATE_IGNORE_TABLE = (" + strings.Join(ignore, ", ") + "), REPLICATE_WILD_IGNORE_TABLE = (), " +
		"REPLICATE_DO_TABLE = (), REPLICATE_DO_DB = (), REPLICATE_IGNORE_DB = () FOR CHANNEL ?"
	args := make([]any, 0, len(do)+1)
	for _, pattern := range do {
		args = append(args, pattern)
	}
	_, err := db.ExecContext(ctx, statement, append(args, name)...)
	return err
}

func (mysqlFlavor) channel(ctx context.Context, db *sql.DB, name string, action channelAction) error {
	statement := map[channelAction]string{channelStart: "START REPLICA", channelStop: "STOP REPLICA", channelReset: "RESET REPLICA ALL"}[action]
	_, err := db.ExecContext(ctx, statement+" FOR CHANNEL ?", name)
	if isServerError(err, errMySQLChannelMissing) {
		return errChannelMissing
	}
	return err
}

// skip commits an empty transaction with the failed transaction's GTID.
func (mysqlFlavor) skip(ctx context.Context, db *sql.DB, _ string, failure workerError) error {
	if failure.Transaction == "" {
		return fmt.Errorf("the failed transaction is unknown")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET GTID_NEXT = ?", failure.Transaction); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "SET GTID_NEXT = 'AUTOMATIC'") }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (mysqlFlavor) sourcePosition(ctx context.Context, db *sql.DB) (string, error) {
	var position string
	err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.gtid_executed").Scan(&position)
	return position, err
}

// waitForPosition: WAIT_FOR_EXECUTED_GTID_SET returns 0 once applied, 1 on timeout.
func (mysqlFlavor) waitForPosition(ctx context.Context, db *sql.DB, position string, seconds int) (bool, error) {
	var result sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT WAIT_FOR_EXECUTED_GTID_SET(?, ?)", position, seconds).Scan(&result); err != nil {
		return false, err
	}
	return result.Valid && result.Int64 == 0, nil
}

func (mysqlFlavor) disableOnReplica() string { return "DISABLE ON REPLICA" }
