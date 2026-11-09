package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

func openDB(endpoint Endpoint) (*sql.DB, error) {
	dsn, err := endpoint.DSN()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

// inList expands the single "IN (?)" of query into one placeholder per value.
func inList(query string, values []string) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return strings.Replace(query, "IN (?)", "IN ("+placeholders+")", 1), args
}

func isServerError(err error, number uint16) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == number
}

// serverError returns the server's message without driver noise.
func serverError(err error) string {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Message
	}
	return err.Error()
}

func serverVariables(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, serverVariablesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	variables := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		variables[name] = value
	}
	return variables, rows.Err()
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// channelState is the subset of SHOW REPLICA STATUS the agent uses.
type channelState struct {
	Exists      bool
	IORunning   string // Yes, No or Connecting
	SQLRunning  string // Yes or No
	IOErrno     int
	IOError     string
	SQLErrno    int
	SQLError    string
	WildDo      []string
	IgnoreTable []string
}

func (c channelState) stopped() bool { return c.IORunning == "No" && c.SQLRunning == "No" }

func readChannel(ctx context.Context, db *sql.DB, name string) (channelState, error) {
	rows, err := db.QueryContext(ctx, "SHOW REPLICA STATUS FOR CHANNEL ?", name)
	if isServerError(err, errChannelNotExists) {
		return channelState{}, nil
	}
	if err != nil {
		return channelState{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return channelState{}, err
	}
	if !rows.Next() {
		return channelState{}, rows.Err()
	}
	values := make([]sql.NullString, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return channelState{}, err
	}
	field := map[string]string{}
	for i, column := range columns {
		field[column] = values[i].String
	}
	var state channelState
	state.Exists = true
	state.IORunning, state.SQLRunning = field["Replica_IO_Running"], field["Replica_SQL_Running"]
	state.IOError, state.SQLError = field["Last_IO_Error"], field["Last_SQL_Error"]
	_, _ = fmt.Sscan(field["Last_IO_Errno"], &state.IOErrno)
	_, _ = fmt.Sscan(field["Last_SQL_Errno"], &state.SQLErrno)
	state.WildDo = splitPatterns(field["Replicate_Wild_Do_Table"])
	state.IgnoreTable = splitPatterns(field["Replicate_Ignore_Table"])
	return state, rows.Err()
}

func splitPatterns(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func samePatterns(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func channelStatement(ctx context.Context, db *sql.DB, statement, name string) error {
	_, err := db.ExecContext(ctx, statement+" FOR CHANNEL ?", name)
	return err
}

// setFilters sets exactly the migration's filters; the applier must be stopped.
// MySQL does not persist channel filters across restarts, so they are set
// again before every start.
func setFilters(ctx context.Context, db *sql.DB, name string, options Options) error {
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

func placeholders(count int) string { return strings.TrimSuffix(strings.Repeat("?, ", count), ", ") }

// startChannel stops the channel, sets its filters and starts it again.
func startChannel(ctx context.Context, db *sql.DB, name string, options Options) error {
	if err := channelStatement(ctx, db, "STOP REPLICA", name); err != nil {
		return fmt.Errorf("stop replication: %s", serverError(err))
	}
	if err := setFilters(ctx, db, name, options); err != nil {
		return fmt.Errorf("set the replication filters: %s", serverError(err))
	}
	if err := channelStatement(ctx, db, "START REPLICA", name); err != nil {
		return fmt.Errorf("start replication: %s", serverError(err))
	}
	return nil
}

// removeChannel stops and deletes the channel; a missing channel is fine.
func removeChannel(ctx context.Context, db *sql.DB, name string) error {
	for _, statement := range []string{"STOP REPLICA", "RESET REPLICA ALL"} {
		if err := channelStatement(ctx, db, statement, name); err != nil && !isServerError(err, errChannelNotExists) {
			return fmt.Errorf("%s: %s", strings.ToLower(statement), serverError(err))
		}
	}
	return nil
}

// workerError is the most recent applier worker error of the channel.
type workerError struct {
	Number      int
	Message     string
	Transaction string
}

// readWorkerError needs performance_schema; without it, it reports nothing.
func readWorkerError(ctx context.Context, db *sql.DB, name string) (workerError, bool) {
	var result workerError
	err := db.QueryRowContext(ctx, workerErrorsSQL, name).Scan(&result.Number, &result.Message, &result.Transaction)
	if err != nil {
		return workerError{}, false
	}
	return result, true
}

// skipTransaction commits an empty transaction with gtid, so the applier
// treats the failed transaction as done.
func skipTransaction(ctx context.Context, db *sql.DB, gtid string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET GTID_NEXT = ?", gtid); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "SET GTID_NEXT = 'AUTOMATIC'") }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
