package mysqlreplication

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
	maxFilterEntries   = 200
	defaultCopyThreads = 4
	maxCopyThreads     = 32
	// migrationDatabase holds the worker's heartbeat and state; never migrated.
	migrationDatabase = "openeverest_migration"
)

// namePattern is stricter than MySQL allows: names travel through SQL,
// replication filter patterns and MySQL Shell options.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

var systemDatabases = []string{"mysql", "sys", "information_schema", "performance_schema", migrationDatabase}

// Options is the public, credential-free shape stored in the migration
// record. The worker accepts the same fields.
type Options struct {
	// Databases to migrate; they keep their names on the target. Empty means
	// the database in the source connection string.
	Databases         []string `json:"databases"`
	ExcludeTables     []string `json:"excludeTables"`
	RecreateDatabases bool     `json:"recreateDatabases"`
	CopyThreads       int      `json:"copyThreads"`
}

func invalidOptions(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options := Options{Databases: []string{}, ExcludeTables: []string{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return Options{}, invalidOptions("invalid options: %s", safeDecodeError(err))
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return Options{}, invalidOptions("invalid options: unexpected data after JSON object")
		}
	}
	var err error
	if options.Databases, err = normalizeNames("databases", options.Databases, false); err != nil {
		return Options{}, err
	}
	if options.ExcludeTables, err = normalizeNames("excludeTables", options.ExcludeTables, true); err != nil {
		return Options{}, err
	}
	if options.CopyThreads == 0 {
		options.CopyThreads = defaultCopyThreads
	}
	if options.CopyThreads < 1 || options.CopyThreads > maxCopyThreads {
		return Options{}, invalidOptions("copyThreads must be between 1 and %d", maxCopyThreads)
	}
	return options, nil
}

// validateTables checks excluded tables once the databases are known.
func (o Options) validateTables() error {
	for _, table := range o.ExcludeTables {
		database, _, _ := strings.Cut(table, ".")
		if !slices.Contains(o.Databases, database) {
			return invalidOptions("excludeTables: %q is not in a migrated database", table)
		}
	}
	return nil
}

// normalizeNames trims, deduplicates and validates database or database.table names.
func normalizeNames(field string, names []string, qualified bool) ([]string, error) {
	if len(names) > maxFilterEntries {
		return nil, invalidOptions("%s accepts at most %d entries", field, maxFilterEntries)
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if err := validateName(field, name, qualified); err != nil {
			return nil, err
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result, nil
}

func validateName(field, name string, qualified bool) error {
	parts := []string{name}
	if qualified {
		database, table, ok := strings.Cut(name, ".")
		if !ok {
			return invalidOptions("%s: %q must be a database-qualified table name (database.table)", field, name)
		}
		parts = []string{database, table}
	}
	for _, part := range parts {
		if !namePattern.MatchString(part) {
			return invalidOptions("%s: %q must use 1-64 letters, digits, '_', '$' or '-'", field, name)
		}
	}
	if slices.ContainsFunc(systemDatabases, func(system string) bool { return strings.EqualFold(system, parts[0]) }) {
		return invalidOptions("%s: database %q cannot be migrated", field, parts[0])
	}
	return nil
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
