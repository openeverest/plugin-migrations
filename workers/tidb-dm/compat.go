package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// DM neither dumps nor replicates views (it skips CREATE VIEW), so the agent
// creates them from the source's definitions: once replication starts, so
// the target can be checked before the cutover, and again at the cutover.
// They are defined by the target user: accounts are not migrated.

var viewDefinerPattern = regexp.MustCompile("(?i)\\bDEFINER\\s*=\\s*(`(?:[^`]|``)*`|'(?:[^']|'')*'|[^\\s@]+)@(`(?:[^`]|``)*`|'(?:[^']|'')*'|\\S+)\\s+")

// createViews creates or replaces every view of the migrated databases on
// the target and returns those that failed ("db.view: error"). Views that
// use other views are retried until no more succeed.
func createViews(ctx context.Context, source, target *sql.DB, options Options) ([]string, error) {
	type view struct{ database, name, definition string }
	var pending []view
	query, args := inList(viewsSQL, options.Databases)
	err := eachRow(ctx, source, query, args, func(scan func(...any) error) error {
		var v view
		if err := scan(&v.database, &v.name); err != nil {
			return err
		}
		pending = append(pending, v)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list views on the source: %s", serverError(err))
	}
	for i := range pending {
		row, found, err := readRow(ctx, source, "SHOW CREATE VIEW "+quoteIdentifier(pending[i].database)+"."+quoteIdentifier(pending[i].name))
		if err != nil || !found {
			return nil, fmt.Errorf("read view %s.%s on the source: %s", pending[i].database, pending[i].name, errorText(err))
		}
		pending[i].definition = viewStatement(row["Create View"])
	}
	failures := map[string]string{}
	for progress := true; progress && len(pending) > 0; {
		progress = false
		var retry []view
		for _, v := range pending {
			if err := execIn(ctx, target, v.database, v.definition); err != nil {
				failures[v.database+"."+v.name] = serverError(err)
				retry = append(retry, v)
				continue
			}
			delete(failures, v.database+"."+v.name)
			progress = true
		}
		pending = retry
	}
	var failed []string
	for _, name := range sortedKeys(failures) {
		failed = append(failed, name+": "+truncate(failures[name], 120))
	}
	return failed, nil
}

// viewStatement turns SHOW CREATE VIEW into CREATE OR REPLACE with the
// target user as definer.
func viewStatement(definition string) string {
	definition = viewDefinerPattern.ReplaceAllString(definition, "DEFINER=CURRENT_USER ")
	if rest, found := cutPrefixFold(strings.TrimSpace(definition), "CREATE "); found {
		return "CREATE OR REPLACE " + rest
	}
	return definition
}

// execIn runs a statement with database as the default database; SHOW
// CREATE VIEW does not qualify the view's own name.
func execIn(ctx context.Context, db *sql.DB, database, statement string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "USE "+quoteIdentifier(database)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, statement)
	return err
}

func cutPrefixFold(value, prefix string) (string, bool) {
	if len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
		return value[len(prefix):], true
	}
	return value, false
}

func errorText(err error) string {
	if err == nil {
		return "not found"
	}
	return serverError(err)
}

// DM parses every DDL statement before it filters by database, so a
// statement TiDB cannot parse stops the migration even when it changes a
// database that is not migrated. The agent skips such a statement when it
// creates or alters a table or database that is provably outside the
// migration; everything else stays an error for the user.

// errCodeParseDDL is DM's "parse DDL" error.
const errCodeParseDDL = 36067

var (
	failedLocationPattern = regexp.MustCompile(`startLocation: \[position: \(([^,\s]+), (\d+)\)`)
	useDatabasePattern    = regexp.MustCompile("(?is)^use\\s+(`(?:[^`]|``)+`|[A-Za-z0-9_$]+)\\s*;\\s*(.*)$")
	identifier            = "(`(?:[^`]|``)+`|[A-Za-z0-9_$]+)"
	ddlTargetPattern      = regexp.MustCompile(`(?is)^\s*(?:CREATE|ALTER)\s+(?:(TABLE)|DATABASE|SCHEMA)\s+(?:IF\s+NOT\s+EXISTS\s+)?` +
		identifier + `(?:\s*\.\s*` + identifier + `)?`)
	xidCommentPattern = regexp.MustCompile(`\s*/\*\s*xid=\d+\s*\*/\s*$`)
)

// failedEventPosition reads "file:pos" of the event a parse error is about.
func failedEventPosition(failure DMError) (string, string, bool) {
	match := failedLocationPattern.FindStringSubmatch(failure.Message)
	if failure.ErrCode != errCodeParseDDL || match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

// skippableDDL returns the database a statement (as SHOW BINLOG EVENTS
// shows it: "use `db`; CREATE TABLE …") changes, if it is a CREATE or ALTER
// of a table or database outside the migration and names no migrated
// database anywhere.
func skippableDDL(info string, options Options, heartbeat string) (string, bool) {
	statement := strings.TrimSpace(xidCommentPattern.ReplaceAllString(info, ""))
	defaultDatabase := ""
	if match := useDatabasePattern.FindStringSubmatch(statement); match != nil {
		defaultDatabase, statement = unquoteIdentifier(match[1]), match[2]
	}
	match := ddlTargetPattern.FindStringSubmatch(statement)
	if match == nil {
		return "", false
	}
	database := unquoteIdentifier(match[2])
	if match[1] != "" && match[3] == "" {
		// An unqualified table is in the default database.
		database = defaultDatabase
	}
	if database == "" || options.migrates(database) || strings.EqualFold(database, heartbeat) {
		return "", false
	}
	lower := strings.ToLower(statement)
	for _, migrated := range append(append([]string{}, options.Databases...), heartbeat) {
		if regexp.MustCompile(`(^|[^a-z0-9_$])` + regexp.QuoteMeta(strings.ToLower(migrated)) + `($|[^a-z0-9_$])`).MatchString(lower) {
			return "", false
		}
	}
	return database, true
}

func unquoteIdentifier(name string) string {
	if strings.HasPrefix(name, "`") && strings.HasSuffix(name, "`") && len(name) >= 2 {
		return strings.ReplaceAll(name[1:len(name)-1], "``", "`")
	}
	return name
}

// skipUnparseableDDL skips the failed event when skippableDDL allows it.
func (a *Agent) skipUnparseableDDL(ctx context.Context, failure DMError, options Options) bool {
	file, pos, ok := failedEventPosition(failure)
	if !ok {
		return false
	}
	source, err := a.source()
	if err != nil {
		return false
	}
	row, found, err := readRow(ctx, source, "SHOW BINLOG EVENTS IN ? FROM "+pos+" LIMIT 1", file)
	if err != nil || !found || !strings.EqualFold(row["Event_type"], "Query") {
		return false
	}
	database, skippable := skippableDDL(row["Info"], options, heartbeatDatabase(a.config.Name))
	if !skippable {
		return false
	}
	if err := a.dm.SkipEvent(ctx, a.config.Name, file+":"+pos); err != nil {
		log.Printf("supervise: skip the statement at %s:%s: %v", file, pos, err)
		return false
	}
	log.Printf("supervise: skipped a statement TiDB cannot parse at %s:%s; it changes database %q, which is not migrated", file, pos, database)
	return true
}
