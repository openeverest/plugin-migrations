package pgcopydb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const (
	maxFilterEntries = 200
	minJobs          = 1
	maxJobs          = 64
	// reservedSchema holds the worker's heartbeat on the source; pgcopydb
	// never replicates changes to it.
	reservedSchema = "pgcopydb"
)

// identifierPattern accepts unquoted PostgreSQL identifiers. Names that need
// quoting are not supported in filters yet.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

// databasePattern is stricter than PostgreSQL allows, but databases are
// created and passed around by name, so keep them unambiguous.
var databasePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)

// Options is the public, credential-free shape stored in the migration record.
type Options struct {
	// Database to migrate; it keeps its name on the target. Empty means the
	// database in the source connection string.
	Database       string   `json:"database"`
	IncludeSchemas []string `json:"includeSchemas"`
	ExcludeSchemas []string `json:"excludeSchemas"`
	ExcludeTables  []string `json:"excludeTables"`
	DropIfExists   bool     `json:"dropIfExists"`
	TableJobs      *int     `json:"tableJobs,omitempty"`
	IndexJobs      *int     `json:"indexJobs,omitempty"`
}

// workerOptions is what the worker agent accepts; the database is already
// part of the connection strings it receives.
type workerOptions struct {
	IncludeSchemas []string `json:"includeSchemas"`
	ExcludeSchemas []string `json:"excludeSchemas"`
	ExcludeTables  []string `json:"excludeTables"`
	DropIfExists   bool     `json:"dropIfExists"`
	TableJobs      *int     `json:"tableJobs,omitempty"`
	IndexJobs      *int     `json:"indexJobs,omitempty"`
}

func (o Options) worker() workerOptions {
	return workerOptions{
		IncludeSchemas: o.IncludeSchemas, ExcludeSchemas: o.ExcludeSchemas, ExcludeTables: o.ExcludeTables,
		DropIfExists: o.DropIfExists, TableJobs: o.TableJobs, IndexJobs: o.IndexJobs,
	}
}

func invalidOptions(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options := Options{IncludeSchemas: []string{}, ExcludeSchemas: []string{}, ExcludeTables: []string{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return Options{}, invalidOptions("invalid pgcopydb options: %s", safeDecodeError(err))
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return Options{}, invalidOptions("invalid pgcopydb options: unexpected data after JSON object")
		}
	}
	options.Database = strings.TrimSpace(options.Database)
	if options.Database != "" && !databasePattern.MatchString(options.Database) {
		return Options{}, invalidOptions("database must be 1-63 letters, digits, '_' or '-', starting with a letter or '_'")
	}

	var err error
	if options.IncludeSchemas, err = normalizeNames("includeSchemas", options.IncludeSchemas, false); err != nil {
		return Options{}, err
	}
	if options.ExcludeSchemas, err = normalizeNames("excludeSchemas", options.ExcludeSchemas, false); err != nil {
		return Options{}, err
	}
	if options.ExcludeTables, err = normalizeNames("excludeTables", options.ExcludeTables, true); err != nil {
		return Options{}, err
	}
	if len(options.IncludeSchemas) > 0 && len(options.ExcludeSchemas) > 0 {
		return Options{}, invalidOptions("includeSchemas and excludeSchemas cannot be combined")
	}
	for name, jobs := range map[string]*int{"tableJobs": options.TableJobs, "indexJobs": options.IndexJobs} {
		if jobs != nil && (*jobs < minJobs || *jobs > maxJobs) {
			return Options{}, invalidOptions("%s must be between %d and %d", name, minJobs, maxJobs)
		}
	}
	return options, nil
}

// normalizeNames trims, deduplicates and validates schema or schema.table names.
func normalizeNames(field string, names []string, qualified bool) ([]string, error) {
	if len(names) > maxFilterEntries {
		return nil, invalidOptions("%s accepts at most %d entries", field, maxFilterEntries)
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		parts := []string{name}
		if qualified {
			schema, table, ok := strings.Cut(name, ".")
			if !ok {
				return nil, invalidOptions("%s: %q must be a schema-qualified table name (schema.table)", field, name)
			}
			parts = []string{schema, table}
		}
		for _, part := range parts {
			if !identifierPattern.MatchString(part) {
				return nil, invalidOptions("%s: %q is not a valid unquoted PostgreSQL name", field, name)
			}
		}
		if parts[0] == reservedSchema {
			return nil, invalidOptions("%s: schema %q is reserved for the migration worker", field, reservedSchema)
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result, nil
}

// safeDecodeError keeps type and field information but never echoes values.
func safeDecodeError(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Sprintf("field %q must be %s", typeErr.Field, typeErr.Type)
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return strings.TrimPrefix(err.Error(), "json: ")
	}
	return "malformed JSON"
}
