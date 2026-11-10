package main

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const maxListed = 10

type sourceFacts struct {
	Variables    map[string]string
	Missing      []string
	DataBytes    uint64
	Spatial      []string
	CascadingFKs []string
	// Unsupported counts objects TiDB does not support, by kind.
	Unsupported map[string]int
}

type targetFacts struct {
	Version    string
	Collations map[string]bool
	NonEmpty   []string
}

func (s sourceFacts) usesGTID() bool { return strings.EqualFold(s.Variables["gtid_mode"], "ON") }

func on(value string) bool {
	return strings.EqualFold(value, "ON") || value == "1"
}

func readSource(ctx context.Context, db *sql.DB, options Options) (sourceFacts, error) {
	variables, err := serverVariables(ctx, db)
	if err != nil {
		return sourceFacts{}, err
	}
	facts := sourceFacts{Variables: variables, Unsupported: map[string]int{}}
	query, args := inList(existingDatabasesSQL, options.Databases)
	existing, err := queryStrings(ctx, db, query, args...)
	if err != nil {
		return sourceFacts{}, err
	}
	for _, database := range options.Databases {
		if !slices.ContainsFunc(existing, func(name string) bool { return strings.EqualFold(name, database) }) {
			facts.Missing = append(facts.Missing, database)
		}
	}
	query, args = inList(sourceTablesSQL, options.Databases)
	err = eachRow(ctx, db, query, args, func(scan func(...any) error) error {
		var database, table string
		var size uint64
		if err := scan(&database, &table, &size); err != nil {
			return err
		}
		if !options.excludes(database, table) {
			facts.DataBytes += size
		}
		return nil
	})
	if err != nil {
		return sourceFacts{}, err
	}
	for _, check := range []struct {
		query string
		into  *[]string
	}{
		{spatialColumnsSQL, &facts.Spatial},
		{cascadingForeignKeysSQL, &facts.CascadingFKs},
	} {
		query, args := inList(check.query, options.Databases)
		err := eachRow(ctx, db, query, args, func(scan func(...any) error) error {
			var database, table, name string
			if err := scan(&database, &table, &name); err != nil {
				return err
			}
			if !options.excludes(database, table) {
				*check.into = append(*check.into, database+"."+table+"."+name)
			}
			return nil
		})
		if err != nil {
			return sourceFacts{}, err
		}
	}
	for kind, query := range map[string]string{
		"stored routines": countRoutinesSQL, "triggers": countTriggersSQL, "events": countEventsSQL, "FULLTEXT indexes": countFulltextSQL,
	} {
		query, args := inList(query, options.Databases)
		var count int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return sourceFacts{}, err
		}
		if count > 0 {
			facts.Unsupported[kind] = count
		}
	}
	return facts, nil
}

// sourceCollations lists "database.table (collation)" for every collation
// the migrated tables use.
func sourceCollations(ctx context.Context, db *sql.DB, options Options) (map[string][]string, error) {
	collations := map[string][]string{}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(options.Databases)), ", ")
	query := strings.ReplaceAll(sourceCollationsSQL, "IN (?)", "IN ("+placeholders+")")
	args := make([]any, 0, 2*len(options.Databases))
	for range 2 {
		for _, database := range options.Databases {
			args = append(args, database)
		}
	}
	err := eachRow(ctx, db, query, args, func(scan func(...any) error) error {
		var database, table, collation string
		if err := scan(&database, &table, &collation); err != nil {
			return err
		}
		if !options.excludes(database, table) {
			name := database + "." + table
			if !slices.Contains(collations[collation], name) {
				collations[collation] = append(collations[collation], name)
			}
		}
		return nil
	})
	return collations, err
}

func readTarget(ctx context.Context, db *sql.DB, options Options) (targetFacts, error) {
	var facts targetFacts
	if err := db.QueryRowContext(ctx, targetVersionSQL).Scan(&facts.Version); err != nil {
		return targetFacts{}, err
	}
	collations, err := queryStrings(ctx, db, targetCollationsSQL)
	if err != nil {
		return targetFacts{}, err
	}
	facts.Collations = map[string]bool{}
	for _, collation := range collations {
		facts.Collations[strings.ToLower(collation)] = true
	}
	query, args := inList(nonEmptyDatabasesSQL, options.Databases)
	if facts.NonEmpty, err = queryStrings(ctx, db, query, args...); err != nil {
		return targetFacts{}, err
	}
	return facts, nil
}

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// versionNumber turns "8.0.46-37" into 80046; 0 if it cannot be read.
func versionNumber(version string) int {
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return 0
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	return major*10000 + minor*100 + patch
}

// checkPreflight lists every problem it finds, so they can be fixed together.
func checkPreflight(source sourceFacts, collations map[string][]string, target targetFacts, options Options, freeBytes uint64) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	variables := source.Variables
	version := variables["version"]
	switch number := versionNumber(version); {
	case strings.Contains(strings.ToLower(version+" "+variables["version_comment"]), "mariadb"):
		add("the source is MariaDB, which this engine does not support")
	case strings.Contains(strings.ToLower(version), "tidb"):
		add("the source is TiDB; use TiCDC or BR to move data between TiDB clusters")
	case number < 50700:
		add("the source runs MySQL %s; DM needs MySQL 5.7 or later", orUnknown(version))
	case number >= 90000:
		add("the source runs MySQL %s, which DM does not support (5.7 to 8.4)", version)
	}
	if !on(variables["log_bin"]) {
		add("binary logging is disabled on the source; enable log_bin")
	}
	if format := strings.ToUpper(variables["binlog_format"]); format != "ROW" {
		add("the source has binlog_format=%s; set binlog_format=ROW", orUnknown(format))
	}
	if image := strings.ToUpper(variables["binlog_row_image"]); image != "FULL" {
		add("the source has binlog_row_image=%s; set binlog_row_image=FULL", orUnknown(image))
	}
	if on(variables["binlog_transaction_compression"]) {
		add("the source compresses binary log transactions, which DM cannot read; set binlog_transaction_compression=OFF")
	}
	if variables["server_id"] == "" || variables["server_id"] == "0" {
		add("the source has no server_id; set a non-zero server_id")
	}
	if len(source.Missing) > 0 {
		add("databases not found on the source: %s", strings.Join(source.Missing, ", "))
	}
	if !strings.Contains(strings.ToLower(target.Version), "tidb") {
		add("the target is not TiDB (version %s)", orUnknown(target.Version))
	}
	var unsupported []string
	for _, collation := range sortedKeys(collations) {
		if !target.Collations[strings.ToLower(collation)] {
			unsupported = append(unsupported, fmt.Sprintf("%s (%s)", listNames(collations[collation]), collation))
		}
	}
	if len(unsupported) > 0 {
		add("TiDB does not support the collations of these tables; convert or exclude them: %s", listNames(unsupported))
	}
	if len(source.Spatial) > 0 {
		add("TiDB does not support spatial columns; drop the columns or exclude the tables: %s", listNames(source.Spatial))
	}
	if len(source.CascadingFKs) > 0 {
		add("foreign keys with CASCADE or SET NULL/DEFAULT actions cannot be migrated: MySQL does not log the changes they make "+
			"and DM applies changes without foreign key checks; change them to RESTRICT or exclude the tables: %s", listNames(source.CascadingFKs))
	}
	if len(target.NonEmpty) > 0 && !options.RecreateDatabases {
		add("these databases already have tables on the target: %s; drop them or enable \"Recreate the target databases\"",
			strings.Join(target.NonEmpty, ", "))
	}
	if freeBytes > 0 && source.DataBytes > freeBytes {
		add("the source data (about %s) may not fit the worker's dump volume (%s free); create the migration with more storage",
			formatBytes(source.DataBytes), formatBytes(freeBytes))
	}
	return problems
}

// unsupportedNote tells which source objects TiDB cannot have.
func unsupportedNote(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	var parts []string
	for _, kind := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[kind], kind))
	}
	return "Not migrated because TiDB does not support them: " + strings.Join(parts, ", ") + "."
}

// freeSpace is the space left on the volume holding dir; 0 if unknown.
func freeSpace(dir string) uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0
	}
	return stat.Bavail * uint64(stat.Bsize)
}

func formatBytes(size uint64) string {
	const unit = 1 << 30
	if size >= unit {
		return fmt.Sprintf("%.1f GiB", float64(size)/unit)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func listNames(names []string) string {
	if len(names) <= maxListed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxListed], ", "), len(names)-maxListed)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func eachRow(ctx context.Context, db *sql.DB, query string, args []any, handle func(scan func(...any) error) error) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := handle(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}
