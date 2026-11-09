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

// readRow returns the first row of a query as column → value; found is false
// when there is no row.
func readRow(ctx context.Context, db *sql.DB, query string, args ...any) (map[string]string, bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	values := make([]sql.NullString, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return nil, false, err
	}
	field := make(map[string]string, len(columns))
	for i, column := range columns {
		field[column] = values[i].String
	}
	return field, true, rows.Err()
}

// newChannelState reads SHOW REPLICA/SLAVE STATUS columns; prefix is "Replica"
// for MySQL and "Slave" for MariaDB.
func newChannelState(field map[string]string, prefix string) channelState {
	state := channelState{Exists: true}
	state.IORunning, state.SQLRunning = field[prefix+"_IO_Running"], field[prefix+"_SQL_Running"]
	state.IOError, state.SQLError = field["Last_IO_Error"], field["Last_SQL_Error"]
	_, _ = fmt.Sscan(field["Last_IO_Errno"], &state.IOErrno)
	_, _ = fmt.Sscan(field["Last_SQL_Errno"], &state.SQLErrno)
	state.WildDo = splitPatterns(field["Replicate_Wild_Do_Table"])
	state.IgnoreTable = splitPatterns(field["Replicate_Ignore_Table"])
	return state
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

func placeholders(count int) string { return strings.TrimSuffix(strings.Repeat("?, ", count), ", ") }

// workerError is the most recent applier worker error of the channel.
type workerError struct {
	Number      int
	Message     string
	Transaction string
}
