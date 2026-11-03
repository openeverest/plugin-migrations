package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// heartbeatSchema is filtered out by pgcopydb's change decoding, so heartbeat
// updates move the source WAL forward without ever reaching the target.
const heartbeatSchema = "pgcopydb"

const (
	minJobs = 1
	maxJobs = 64
)

// Options are the user-facing pgcopydb settings. The plugin backend validates
// them first; the agent re-validates because it renders them into a file.
type Options struct {
	IncludeSchemas []string `json:"includeSchemas"`
	ExcludeSchemas []string `json:"excludeSchemas"`
	ExcludeTables  []string `json:"excludeTables"`
	DropIfExists   bool     `json:"dropIfExists"`
	TableJobs      *int     `json:"tableJobs,omitempty"`
	IndexJobs      *int     `json:"indexJobs,omitempty"`
}

// identifierPattern accepts plain PostgreSQL identifiers; quoted identifiers
// with special characters are not supported in filters yet.
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
		if schema == heartbeatSchema {
			return fmt.Errorf("schema %q is reserved for the migration worker", heartbeatSchema)
		}
	}
	for _, table := range o.ExcludeTables {
		schema, name, ok := strings.Cut(table, ".")
		if !ok || !identifierPattern.MatchString(schema) || !identifierPattern.MatchString(name) {
			return fmt.Errorf("%q is not a valid schema-qualified table name", table)
		}
	}
	for name, jobs := range map[string]*int{"tableJobs": o.TableJobs, "indexJobs": o.IndexJobs} {
		if jobs != nil && (*jobs < minJobs || *jobs > maxJobs) {
			return fmt.Errorf("%s must be between %d and %d", name, minJobs, maxJobs)
		}
	}
	return nil
}

// filtersINI renders the pgcopydb filters file. The worker's own heartbeat
// schema is always excluded: pgcopydb rejects exclude-schema together with
// include-only-schema, where the heartbeat schema is excluded implicitly.
func (o Options) filtersINI() string {
	var builder strings.Builder
	section := func(name string, entries []string) {
		if len(entries) == 0 {
			return
		}
		fmt.Fprintf(&builder, "[%s]\n", name)
		for _, entry := range entries {
			builder.WriteString(entry)
			builder.WriteByte('\n')
		}
		builder.WriteByte('\n')
	}
	if len(o.IncludeSchemas) > 0 {
		section("include-only-schema", o.IncludeSchemas)
	} else {
		section("exclude-schema", append(append([]string{}, o.ExcludeSchemas...), heartbeatSchema))
	}
	section("exclude-table", o.ExcludeTables)
	return builder.String()
}
