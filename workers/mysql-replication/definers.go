package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
)

// Objects with a DEFINER can be created only by a user holding SYSTEM_USER
// when the definer holds it. OpenEverest's PXC root@% does not, while
// root@localhost (a common definer on self-managed servers) does. Such
// objects are left out of the MySQL Shell copy and recreated by the agent
// with the agent's user as definer; SQL SECURITY and everything else stay.
// Replicated DDL keeps its definer: the applier is not privilege-checked.

const systemUserGrantsSQL = "SELECT USER, HOST FROM mysql.global_grants WHERE PRIV = 'SYSTEM_USER'"

// privilegedDefinersSQL lists the objects of the migrated databases with
// their definers, which are compared with the target's SYSTEM_USER holders.
const privilegedDefinersSQL = "" +
	"SELECT 'VIEW', TABLE_SCHEMA, TABLE_NAME, '', DEFINER FROM information_schema.VIEWS WHERE TABLE_SCHEMA IN (?) " +
	"UNION ALL SELECT ROUTINE_TYPE, ROUTINE_SCHEMA, ROUTINE_NAME, '', DEFINER FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA IN (?) " +
	"UNION ALL SELECT 'TRIGGER', TRIGGER_SCHEMA, TRIGGER_NAME, EVENT_OBJECT_TABLE, DEFINER FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA IN (?) " +
	"UNION ALL SELECT 'EVENT', EVENT_SCHEMA, EVENT_NAME, '', DEFINER FROM information_schema.EVENTS WHERE EVENT_SCHEMA IN (?)"

// definedObject is a view, routine, trigger or event.
type definedObject struct {
	Kind     string // VIEW, PROCEDURE, FUNCTION, TRIGGER or EVENT
	Database string
	Name     string
	Table    string // triggers only
	Definer  string
}

// definerPolicy tells which definers the agent's user cannot assign.
type definerPolicy struct {
	privileged map[string]bool
}

// readDefinerPolicy returns an empty policy when the agent's user holds
// SYSTEM_USER itself.
func readDefinerPolicy(ctx context.Context, db *sql.DB) (definerPolicy, error) {
	var current string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&current); err != nil {
		return definerPolicy{}, err
	}
	rows, err := db.QueryContext(ctx, systemUserGrantsSQL)
	if err != nil {
		return definerPolicy{}, err
	}
	defer rows.Close()
	privileged := map[string]bool{}
	for rows.Next() {
		var user, host string
		if err := rows.Scan(&user, &host); err != nil {
			return definerPolicy{}, err
		}
		privileged[user+"@"+host] = true
	}
	if err := rows.Err(); err != nil {
		return definerPolicy{}, err
	}
	if privileged[current] {
		return definerPolicy{}, nil
	}
	return definerPolicy{privileged: privileged}, nil
}

// remaps tells whether the agent must define the object as itself.
func (p definerPolicy) remaps(definer string) bool { return p.privileged[definer] }

func (p definerPolicy) objects(ctx context.Context, source *sql.DB, databases []string) ([]definedObject, error) {
	if len(p.privileged) == 0 {
		return nil, nil
	}
	query := privilegedDefinersSQL
	args := make([]any, 0, 4*len(databases))
	for range 4 {
		var listArgs []any
		query, listArgs = inList(query, databases)
		args = append(args, listArgs...)
	}
	rows, err := source.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []definedObject
	for rows.Next() {
		var object definedObject
		if err := rows.Scan(&object.Kind, &object.Database, &object.Name, &object.Table, &object.Definer); err != nil {
			return nil, err
		}
		if p.remaps(object.Definer) {
			objects = append(objects, object)
		}
	}
	return objects, rows.Err()
}

// copyExclusions are the MySQL Shell options that leave objects out.
func copyExclusions(objects []definedObject) map[string][]string {
	exclusions := map[string][]string{}
	for _, object := range objects {
		qualified := quoteIdentifier(object.Database) + "." + quoteIdentifier(object.Name)
		switch object.Kind {
		case "VIEW":
			exclusions["excludeTables"] = append(exclusions["excludeTables"], qualified)
		case "PROCEDURE", "FUNCTION":
			exclusions["excludeRoutines"] = append(exclusions["excludeRoutines"], qualified)
		case "TRIGGER":
			exclusions["excludeTriggers"] = append(exclusions["excludeTriggers"],
				quoteIdentifier(object.Database)+"."+quoteIdentifier(object.Table)+"."+quoteIdentifier(object.Name))
		case "EVENT":
			exclusions["excludeEvents"] = append(exclusions["excludeEvents"], qualified)
		}
	}
	return exclusions
}

var definerClause = regexp.MustCompile("DEFINER=`(?:[^`]|``)*`@`(?:[^`]|``)*`")

// withCurrentUserDefiner replaces the definer of a SHOW CREATE statement.
func withCurrentUserDefiner(statement string) (string, error) {
	location := definerClause.FindStringIndex(statement)
	if location == nil {
		return "", fmt.Errorf("no DEFINER clause found")
	}
	return statement[:location[0]] + "DEFINER=CURRENT_USER" + statement[location[1]:], nil
}

// showCreate returns an object's definition and the session settings it was
// created with.
type showCreate struct {
	statement, sqlMode, charset, collation, timeZone string
	hasSQLMode                                       bool
}

func readShowCreate(ctx context.Context, source *sql.DB, object definedObject) (showCreate, error) {
	kind := object.Kind
	statementColumn := map[string]string{
		"VIEW": "Create View", "PROCEDURE": "Create Procedure", "FUNCTION": "Create Function",
		"TRIGGER": "SQL Original Statement", "EVENT": "Create Event",
	}[kind]
	if statementColumn == "" {
		return showCreate{}, fmt.Errorf("unsupported object type %s", kind)
	}
	rows, err := source.QueryContext(ctx, "SHOW CREATE "+kind+" "+quoteIdentifier(object.Database)+"."+quoteIdentifier(object.Name))
	if err != nil {
		return showCreate{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return showCreate{}, err
	}
	if !rows.Next() {
		return showCreate{}, fmt.Errorf("%s %s.%s no longer exists", strings.ToLower(kind), object.Database, object.Name)
	}
	values := make([]sql.NullString, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return showCreate{}, err
	}
	field := map[string]sql.NullString{}
	for i, column := range columns {
		field[column] = values[i]
	}
	result := showCreate{
		statement: field[statementColumn].String, charset: field["character_set_client"].String,
		collation: field["collation_connection"].String, timeZone: field["time_zone"].String,
	}
	result.sqlMode, result.hasSQLMode = field["sql_mode"].String, field["sql_mode"].Valid
	return result, rows.Err()
}

// recreateObjects creates the objects on the target with the agent's user as
// definer. Views may depend on each other, so failed ones are retried while
// some succeed.
func recreateObjects(ctx context.Context, source, target *sql.DB, objects []definedObject) error {
	pending := objects
	for len(pending) > 0 {
		var failed []definedObject
		var lastErr error
		for _, object := range pending {
			if err := recreateObject(ctx, source, target, object); err != nil {
				failed, lastErr = append(failed, object), err
			}
		}
		if len(failed) == len(pending) {
			return lastErr
		}
		pending = failed
	}
	return nil
}

func recreateObject(ctx context.Context, source, target *sql.DB, object definedObject) error {
	definition, err := readShowCreate(ctx, source, object)
	if err != nil {
		return fmt.Errorf("read %s %s.%s on the source: %s", strings.ToLower(object.Kind), object.Database, object.Name, serverError(err))
	}
	statement, err := withCurrentUserDefiner(definition.statement)
	if err != nil {
		return fmt.Errorf("%s %s.%s: %w", strings.ToLower(object.Kind), object.Database, object.Name, err)
	}
	conn, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	settings := []struct {
		statement string
		value     string
		set       bool
	}{
		{"USE " + quoteIdentifier(object.Database), "", true},
		{"SET SESSION sql_mode = ?", definition.sqlMode, definition.hasSQLMode},
		{"SET SESSION character_set_client = ?", definition.charset, definition.charset != ""},
		{"SET SESSION collation_connection = ?", definition.collation, definition.collation != ""},
		{"SET SESSION time_zone = ?", definition.timeZone, definition.timeZone != ""},
	}
	for _, setting := range settings {
		if !setting.set {
			continue
		}
		args := []any{}
		if strings.Contains(setting.statement, "?") {
			args = append(args, setting.value)
		}
		if _, err := conn.ExecContext(ctx, setting.statement, args...); err != nil {
			return fmt.Errorf("prepare the session for %s.%s: %s", object.Database, object.Name, serverError(err))
		}
	}
	if _, err := conn.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create %s %s.%s on the target: %s", strings.ToLower(object.Kind), object.Database, object.Name, serverError(err))
	}
	// The pooled connection must not keep the object's session settings.
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	return nil
}
