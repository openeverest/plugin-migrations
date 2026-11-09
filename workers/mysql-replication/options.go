package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	// migrationDatabase holds the heartbeat table on the source and the
	// migration state on the target. It is never copied.
	migrationDatabase  = "openeverest_migration"
	defaultCopyThreads = 4
	maxCopyThreads     = 32
)

// systemDatabases exist on every server and are never migrated.
var systemDatabases = []string{"mysql", "sys", "information_schema", "performance_schema", migrationDatabase}

// namePattern is stricter than MySQL: names travel through SQL, replication
// filter patterns and MySQL Shell options, so keep them unambiguous.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

// Options are the user-facing settings. The plugin backend validates them
// first; the agent re-validates because they reach SQL and MySQL Shell.
type Options struct {
	// Databases are copied and replicated; they keep their names.
	Databases []string `json:"databases"`
	// ExcludeTables (database.table) are neither copied nor replicated.
	ExcludeTables []string `json:"excludeTables"`
	// RecreateDatabases drops the databases on the target first.
	RecreateDatabases bool `json:"recreateDatabases"`
	// CopyThreads is how many threads read and how many write the copy.
	CopyThreads int `json:"copyThreads"`
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	var options Options
	if len(raw) != 0 && string(raw) != "null" {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return Options{}, fmt.Errorf("invalid options: %w", err)
		}
	}
	if options.CopyThreads == 0 {
		options.CopyThreads = defaultCopyThreads
	}
	return options, options.validate()
}

func (o Options) validate() error {
	if len(o.Databases) == 0 {
		return fmt.Errorf("choose at least one database to migrate")
	}
	for _, database := range o.Databases {
		if !namePattern.MatchString(database) {
			return fmt.Errorf("%q is not a valid database name", database)
		}
		if isSystemDatabase(database) {
			return fmt.Errorf("database %q cannot be migrated", database)
		}
	}
	for _, table := range o.ExcludeTables {
		database, name, ok := strings.Cut(table, ".")
		if !ok || !namePattern.MatchString(database) || !namePattern.MatchString(name) {
			return fmt.Errorf("%q is not a valid database.table name", table)
		}
		if !slices.Contains(o.Databases, database) {
			return fmt.Errorf("excluded table %q is not in a migrated database", table)
		}
	}
	if o.CopyThreads < 1 || o.CopyThreads > maxCopyThreads {
		return fmt.Errorf("copyThreads must be between 1 and %d", maxCopyThreads)
	}
	return nil
}

func isSystemDatabase(name string) bool {
	return slices.ContainsFunc(systemDatabases, func(system string) bool { return strings.EqualFold(system, name) })
}

// migrates tells whether changes in database are replicated.
func (o Options) migrates(database string) bool { return slices.Contains(o.Databases, database) }

// doPatterns are REPLICATE_WILD_DO_TABLE patterns. Table patterns (not
// REPLICATE_DO_DB) are used on purpose: database filters match DDL by the
// session's default database and would silently skip qualified statements
// such as ALTER TABLE shop.orders issued from another database.
func (o Options) doPatterns(channel string) []string {
	patterns := make([]string, 0, len(o.Databases)+1)
	for _, database := range o.Databases {
		patterns = append(patterns, likeEscape(database)+".%")
	}
	return append(patterns, likeEscape(migrationDatabase)+"."+likeEscape(heartbeatTable(channel)))
}

// ignoreTables are REPLICATE_IGNORE_TABLE names. MySQL checks exact table
// rules before the wild patterns, so they win over "database.%"; a
// REPLICATE_WILD_IGNORE_TABLE rule would never be consulted.
func (o Options) ignoreTables() []string { return slices.Clone(o.ExcludeTables) }

// likeEscape escapes the wildcards of replication filter patterns.
func likeEscape(name string) string {
	return strings.NewReplacer(`\`, `\\`, "_", `\_`, "%", `\%`).Replace(name)
}

// quoteIdentifier quotes a validated name for SQL.
func quoteIdentifier(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
