package main

import (
	"context"
	"database/sql"
	"errors"
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
func inList(query string, values []string, extra ...any) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")
	args := make([]any, 0, len(values)+len(extra))
	for _, value := range values {
		args = append(args, value)
	}
	args = append(args, extra...)
	return strings.Replace(query, "IN (?)", "IN ("+placeholders+")", 1), args
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
		variables[strings.ToLower(name)] = value
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

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
