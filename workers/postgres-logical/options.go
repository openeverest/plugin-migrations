package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// migrationSchema holds the worker's heartbeat table on the source and the
// target. It is never part of the copied schema.
const migrationSchema = "openeverest_migration"

// operatorSchemas are created by PostgreSQL operators in every database, so
// they exist on both sides and are never copied. Percona's and Crunchy Data's
// operators keep PgBouncer's authentication function in "pgbouncer".
var operatorSchemas = []string{"pgbouncer"}

// Options are the user-facing settings. The plugin backend validates them
// first; the agent re-validates because they reach pg_dump and SQL.
type Options struct {
	IncludeSchemas []string `json:"includeSchemas"`
	ExcludeSchemas []string `json:"excludeSchemas"`
	ExcludeTables  []string `json:"excludeTables"`
	// RecreateDatabase drops the target database, if it exists, before the
	// schema is copied. pg_restore --clean cannot drop partitioned tables'
	// inherited constraints, so the database is replaced as a whole.
	RecreateDatabase bool `json:"recreateDatabase"`
}

// identifierPattern accepts unquoted PostgreSQL identifiers. Names are always
// passed quoted, so their case is kept.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

func decodeOptions(raw json.RawMessage) (Options, error) {
	var options Options
	if len(raw) == 0 || string(raw) == "null" {
		return options, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return Options{}, fmt.Errorf("invalid options: %w", err)
	}
	return options, options.validate()
}

func (o Options) validate() error {
	if len(o.IncludeSchemas) > 0 && len(o.ExcludeSchemas) > 0 {
		return fmt.Errorf("includeSchemas and excludeSchemas cannot be combined")
	}
	for _, schema := range append(append([]string{}, o.IncludeSchemas...), o.ExcludeSchemas...) {
		if !identifierPattern.MatchString(schema) {
			return fmt.Errorf("%q is not a valid schema name", schema)
		}
		if schema == migrationSchema {
			return fmt.Errorf("schema %q is reserved for the migration worker", migrationSchema)
		}
	}
	for _, table := range o.ExcludeTables {
		schema, name, ok := strings.Cut(table, ".")
		if !ok || !identifierPattern.MatchString(schema) || !identifierPattern.MatchString(name) {
			return fmt.Errorf("%q is not a valid schema-qualified table name", table)
		}
	}
	return nil
}

// dumpFilterArgs returns pg_dump filters matching the publication filters.
// pg_dump patterns fold unquoted names to lower case, so names are quoted.
func (o Options) dumpFilterArgs() []string {
	args := []string{"--exclude-schema=" + quote(migrationSchema)}
	for _, schema := range operatorSchemas {
		args = append(args, "--exclude-schema="+quote(schema))
	}
	for _, schema := range o.IncludeSchemas {
		args = append(args, "--schema="+quote(schema))
	}
	for _, schema := range o.ExcludeSchemas {
		args = append(args, "--exclude-schema="+quote(schema))
	}
	for _, table := range o.ExcludeTables {
		schema, name, _ := strings.Cut(table, ".")
		args = append(args, "--exclude-table="+quote(schema)+"."+quote(name))
	}
	return args
}

// filterVars passes the filters to SQL as JSON arrays (psql variables).
func (o Options) filterVars() map[string]string {
	encode := func(values []string) string {
		if values == nil {
			values = []string{}
		}
		encoded, _ := json.Marshal(values)
		return string(encoded)
	}
	return map[string]string{
		"include_schemas":  encode(o.IncludeSchemas),
		"exclude_schemas":  encode(append(append([]string{}, o.ExcludeSchemas...), operatorSchemas...)),
		"exclude_tables":   encode(o.ExcludeTables),
		"migration_schema": migrationSchema,
	}
}

func quote(identifier string) string { return `"` + identifier + `"` }
