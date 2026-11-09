package mssqllogshipping

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
	maxDatabases             = 50
	defaultLogBackupInterval = 10
	minLogBackupInterval     = 5
	maxLogBackupInterval     = 300
)

// databaseNamePattern is stricter than SQL Server allows: names travel
// through T-SQL identifiers, S3 object keys and file names.
var databaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_$-]{0,99}$`)

var systemDatabases = []string{"master", "model", "msdb", "tempdb"}

// Options is the public, credential-free shape stored in the migration
// record. The worker accepts the same fields.
type Options struct {
	// Databases to migrate; they keep their names on the target. Empty means
	// the database in the source connection string.
	Databases         []string `json:"databases"`
	RecreateDatabases bool     `json:"recreateDatabases"`
	// LogBackupIntervalSeconds is how often a log backup is taken and restored.
	LogBackupIntervalSeconds int `json:"logBackupIntervalSeconds"`
	// CopyLogins creates the SQL logins of the databases' users on the target
	// at the cutover. Defaults to true.
	CopyLogins bool `json:"copyLogins"`
	// LeaveSourceRestoring takes the cutover's tail-log backup WITH
	// NORECOVERY, so the source databases take no more writes.
	LeaveSourceRestoring bool `json:"leaveSourceRestoring"`
	// IgnoreRecentLogBackups skips the preflight check for log backups taken
	// outside the migration in the last 24 hours.
	IgnoreRecentLogBackups bool `json:"ignoreRecentLogBackups"`
}

// input distinguishes an omitted copyLogins from false.
type input struct {
	Databases                []string `json:"databases"`
	RecreateDatabases        bool     `json:"recreateDatabases"`
	LogBackupIntervalSeconds int      `json:"logBackupIntervalSeconds"`
	CopyLogins               *bool    `json:"copyLogins"`
	LeaveSourceRestoring     bool     `json:"leaveSourceRestoring"`
	IgnoreRecentLogBackups   bool     `json:"ignoreRecentLogBackups"`
}

func invalidOptions(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	parsed := input{Databases: []string{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&parsed); err != nil {
			return Options{}, invalidOptions("invalid options: %s", safeDecodeError(err))
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return Options{}, invalidOptions("invalid options: unexpected data after JSON object")
		}
	}
	options := Options{
		RecreateDatabases: parsed.RecreateDatabases, LogBackupIntervalSeconds: parsed.LogBackupIntervalSeconds,
		CopyLogins: parsed.CopyLogins == nil || *parsed.CopyLogins, LeaveSourceRestoring: parsed.LeaveSourceRestoring,
		IgnoreRecentLogBackups: parsed.IgnoreRecentLogBackups,
	}
	if len(parsed.Databases) > maxDatabases {
		return Options{}, invalidOptions("databases accepts at most %d entries", maxDatabases)
	}
	options.Databases = make([]string, 0, len(parsed.Databases))
	for _, name := range parsed.Databases {
		name = strings.TrimSpace(name)
		if err := validateDatabaseName(name); err != nil {
			return Options{}, err
		}
		if !slices.ContainsFunc(options.Databases, func(existing string) bool { return strings.EqualFold(existing, name) }) {
			options.Databases = append(options.Databases, name)
		}
	}
	if options.LogBackupIntervalSeconds == 0 {
		options.LogBackupIntervalSeconds = defaultLogBackupInterval
	}
	if options.LogBackupIntervalSeconds < minLogBackupInterval || options.LogBackupIntervalSeconds > maxLogBackupInterval {
		return Options{}, invalidOptions("logBackupIntervalSeconds must be between %d and %d", minLogBackupInterval, maxLogBackupInterval)
	}
	return options, nil
}

func validateDatabaseName(name string) error {
	if !databaseNamePattern.MatchString(name) {
		return invalidOptions("databases: %q must use 1-100 letters, digits, '_', '$' or '-', not starting with '$' or '-'", name)
	}
	if slices.ContainsFunc(systemDatabases, func(system string) bool { return strings.EqualFold(system, name) }) {
		return invalidOptions("databases: system database %q cannot be migrated", name)
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
