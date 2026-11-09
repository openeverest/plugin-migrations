package tidbdm

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
	defaultSyncThreads = 16
	maxSyncThreads     = 64
	// defaultStorageGiB holds the dump of most small databases; many clouds
	// round smaller volumes up to 10 GiB anyway.
	defaultStorageGiB = 10
	maxStorageGiB     = 16384
	// heartbeatDatabasePrefix names each migration's heartbeat database on the
	// source ("openeverest_mig_…"), which DM replicates with the data.
	heartbeatDatabasePrefix = "openeverest_mig_"
)

// namePattern is stricter than MySQL allows: names travel through SQL and
// DM's block-allow list.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

// reservedDatabases are system databases of MySQL and TiDB, and those of the
// migration machinery (DM's checkpoints, Lightning's task info).
var reservedDatabases = []string{
	"mysql", "sys", "information_schema", "performance_schema", "metrics_schema",
	"openeverest_migration", "dm_meta", "lightning_task_info",
}

// Options is the public, credential-free shape stored in the migration
// record. The worker accepts the same fields.
type Options struct {
	// Databases to migrate; they keep their names on the target. Empty means
	// the database in the source connection string.
	Databases         []string `json:"databases"`
	ExcludeTables     []string `json:"excludeTables"`
	RecreateDatabases bool     `json:"recreateDatabases"`
	// StorageGiB sizes the worker's volume, which holds DM's dump of the
	// databases until it is loaded.
	StorageGiB int `json:"storageGiB"`
	// CopyThreads are dumpling's threads on the source and the load's on the target.
	CopyThreads int `json:"copyThreads"`
	// SyncThreads apply replicated changes on the target concurrently.
	SyncThreads int `json:"syncThreads"`
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
	for _, setting := range []struct {
		field        string
		value        *int
		defaultValue int
		max          int
	}{
		{"storageGiB", &options.StorageGiB, defaultStorageGiB, maxStorageGiB},
		{"copyThreads", &options.CopyThreads, defaultCopyThreads, maxCopyThreads},
		{"syncThreads", &options.SyncThreads, defaultSyncThreads, maxSyncThreads},
	} {
		if *setting.value == 0 {
			*setting.value = setting.defaultValue
		}
		if *setting.value < 1 || *setting.value > setting.max {
			return Options{}, invalidOptions("%s must be between 1 and %d", setting.field, setting.max)
		}
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
	if slices.ContainsFunc(reservedDatabases, func(reserved string) bool { return strings.EqualFold(reserved, parts[0]) }) ||
		strings.HasPrefix(strings.ToLower(parts[0]), heartbeatDatabasePrefix) {
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
