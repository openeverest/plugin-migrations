package main

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// mariadbFlavor migrates MariaDB into MariaDB: mydumper/myloader copy the
// data, a named replication connection with MASTER_USE_GTID=slave_pos
// replicates. MariaDB has no SYSTEM_USER, so definers are kept as they are.
type mariadbFlavor struct {
	// targetSkipsReplicaStart comes from the target's provider: MariaDB does
	// not expose skip-slave-start as a variable, so it cannot be checked.
	targetSkipsReplicaStart bool
}

const (
	minMariaDBSourceVersion  = 100600
	minMariaDBTargetVersion  = 101100
	errMariaDBChannelMissing = 1617
)

const mariadbChangeMasterSQL = "CHANGE MASTER ? TO MASTER_HOST = ?, MASTER_PORT = ?, MASTER_USER = ?, MASTER_PASSWORD = ?, " +
	"MASTER_USE_GTID = slave_pos, MASTER_SSL = ?, MASTER_CONNECT_RETRY = 10"

func (mariadbFlavor) checkSource(source sourceFacts) []string {
	switch {
	case !isMariaDB(source.Info):
		return []string{"the source is MySQL; use the MySQL engine"}
	case versionNumber(source.Info.Version) < minMariaDBSourceVersion:
		return []string{fmt.Sprintf("the source runs MariaDB %s; version 10.6 or later is required", source.Info.Version)}
	}
	return nil
}

func (f mariadbFlavor) checkTarget(source sourceFacts, target targetFacts) []string {
	var problems []string
	sourceVersion, targetVersion := versionNumber(source.Info.Version), versionNumber(target.Info.Version)
	switch {
	case !isMariaDB(target.Info):
		problems = append(problems, "the target is not MariaDB")
	case targetVersion < minMariaDBTargetVersion:
		problems = append(problems, fmt.Sprintf("the target runs MariaDB %s; version 10.11 or later is required", target.Info.Version))
	case targetVersion/100 < sourceVersion/100:
		problems = append(problems, fmt.Sprintf("the target runs MariaDB %s, older than the source (%s); migrate to the same or a newer version",
			target.Info.Version, source.Info.Version))
	}
	if !f.targetSkipsReplicaStart {
		problems = append(problems, "the target does not keep replication connections stopped when it starts (skip-slave-start), "+
			"so a restarted server would replicate before the migration worker restores its filters; use provider-mariadb 0.2.2 or later")
	}
	return problems
}

var replicationGrantPattern = regexp.MustCompile("(?i)^GRANT (.*) ON \\*\\.\\* TO ")

// canReplicate reads the user's global grants: MariaDB has no statement that
// requires REPLICATION SLAVE and is harmless to run. Grants through roles are
// not seen.
func (mariadbFlavor) canReplicate(ctx context.Context, db *sql.DB) bool {
	grants, err := queryStrings(ctx, db, "SHOW GRANTS")
	return err == nil && grantsReplication(grants)
}

func grantsReplication(grants []string) bool {
	for _, grant := range grants {
		match := replicationGrantPattern.FindStringSubmatch(grant)
		if match == nil {
			continue
		}
		privileges := strings.ToUpper(match[1])
		if strings.Contains(privileges, "ALL PRIVILEGES") || strings.Contains(privileges, "REPLICATION SLAVE") {
			return true
		}
	}
	return false
}

// usesLocalInfile: myloader loads with INSERT statements.
func (mariadbFlavor) usesLocalInfile() bool { return false }

func (mariadbFlavor) copy(ctx context.Context, workDir string, source, target Endpoint, options Options,
	_ map[string][]string, progress func(uint64)) (string, error) {
	return runMydumperCopy(ctx, workDir, source, target, options, progress)
}

func (mariadbFlavor) definerPolicy(context.Context, *sql.DB) (definerPolicy, error) {
	return definerPolicy{}, nil
}

// configure sets gtid_slave_pos, which MASTER_USE_GTID=slave_pos resumes
// from. Setting it requires every replication connection to be stopped; the
// migration's does not exist yet.
func (mariadbFlavor) configure(ctx context.Context, db *sql.DB, name string, source Endpoint, position string) error {
	if _, err := db.ExecContext(ctx, "SET GLOBAL gtid_slave_pos = ?", position); err != nil {
		return fmt.Errorf("record the copied transactions on the target (gtid_slave_pos): %s", serverError(err))
	}
	if _, err := db.ExecContext(ctx, mariadbChangeMasterSQL, name, source.Host, source.Port, source.User, source.Password, tlsFlag(source)); err != nil {
		return fmt.Errorf("configure replication on the target: %s", serverError(err))
	}
	return nil
}

func (mariadbFlavor) readChannel(ctx context.Context, db *sql.DB, name string) (channelState, error) {
	field, found, err := readRow(ctx, db, "SHOW SLAVE ? STATUS", name)
	if isServerError(err, errMariaDBChannelMissing) || (err == nil && !found) {
		return channelState{}, nil
	}
	if err != nil {
		return channelState{}, err
	}
	return newChannelState(field, "Slave"), nil
}

// readWorkerError: MariaDB reports the applier's error in the connection's
// status. The failed transaction is skipped by count, not by GTID.
func (mariadbFlavor) readWorkerError(_ context.Context, _ *sql.DB, _ string, channel channelState) (workerError, bool) {
	if channel.SQLErrno == 0 {
		return workerError{}, false
	}
	return workerError{Number: channel.SQLErrno, Message: channel.SQLError}, true
}

// setFilters sets exactly the migration's filters through the connection's
// replicate_* variables; the connection must be stopped. Names are validated,
// so the comma-separated lists are unambiguous.
func (mariadbFlavor) setFilters(ctx context.Context, db *sql.DB, name string, options Options) error {
	values := []struct{ variable, value string }{
		{"replicate_wild_do_table", strings.Join(options.doPatterns(name), ",")},
		{"replicate_ignore_table", strings.Join(options.ignoreTables(), ",")},
		{"replicate_wild_ignore_table", ""},
		{"replicate_do_table", ""},
		{"replicate_do_db", ""},
		{"replicate_ignore_db", ""},
	}
	for _, setting := range values {
		if _, err := db.ExecContext(ctx, "SET GLOBAL "+name+"."+setting.variable+" = ?", setting.value); err != nil {
			return err
		}
	}
	return nil
}

func (mariadbFlavor) channel(ctx context.Context, db *sql.DB, name string, action channelAction) error {
	statement := map[channelAction]string{channelStart: "START SLAVE ?", channelStop: "STOP SLAVE ?", channelReset: "RESET SLAVE ? ALL"}[action]
	_, err := db.ExecContext(ctx, statement, name)
	if isServerError(err, errMariaDBChannelMissing) {
		return errChannelMissing
	}
	return err
}

// skip skips the connection's next event group, the one that failed.
func (mariadbFlavor) skip(ctx context.Context, db *sql.DB, name string, _ workerError) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET @@default_master_connection = ?", name); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "SET GLOBAL sql_slave_skip_counter = 1")
	return err
}

func (mariadbFlavor) sourcePosition(ctx context.Context, db *sql.DB) (string, error) {
	var position string
	err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.gtid_binlog_pos").Scan(&position)
	return position, err
}

// waitForPosition: MASTER_GTID_WAIT returns 0 once applied, -1 on timeout.
func (mariadbFlavor) waitForPosition(ctx context.Context, db *sql.DB, position string, seconds int) (bool, error) {
	var result sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MASTER_GTID_WAIT(?, ?)", position, seconds).Scan(&result); err != nil {
		return false, err
	}
	return result.Valid && result.Int64 == 0, nil
}

func (mariadbFlavor) disableOnReplica() string { return "DISABLE ON SLAVE" }
