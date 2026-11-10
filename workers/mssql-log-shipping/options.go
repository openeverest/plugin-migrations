package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
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

// Options are the migration options, as the backend stores and sends them.
type Options struct {
	Databases         []string `json:"databases"`
	RecreateDatabases bool     `json:"recreateDatabases"`
	// LogBackupIntervalSeconds is how often a log backup is taken and restored.
	LogBackupIntervalSeconds int  `json:"logBackupIntervalSeconds"`
	CopyLogins               bool `json:"copyLogins"`
	// LeaveSourceRestoring takes the cutover's tail-log backup WITH
	// NORECOVERY: the source databases stay RESTORING and take no writes.
	LeaveSourceRestoring bool `json:"leaveSourceRestoring"`
	// IgnoreRecentLogBackups skips the preflight check for log backups taken
	// outside the migration in the last 24 hours.
	IgnoreRecentLogBackups bool `json:"ignoreRecentLogBackups"`
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	var options Options
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Options{}, errors.New("options are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return Options{}, errors.New("options must be a JSON object with known fields")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Options{}, errors.New("unexpected data after the options")
	}
	return options.normalize()
}

func (o Options) normalize() (Options, error) {
	if len(o.Databases) == 0 {
		return Options{}, errors.New("choose at least one database")
	}
	if len(o.Databases) > maxDatabases {
		return Options{}, fmt.Errorf("at most %d databases can be migrated at once", maxDatabases)
	}
	databases := make([]string, 0, len(o.Databases))
	for _, name := range o.Databases {
		name = strings.TrimSpace(name)
		if err := validateDatabaseName(name); err != nil {
			return Options{}, err
		}
		if !slices.ContainsFunc(databases, func(existing string) bool { return strings.EqualFold(existing, name) }) {
			databases = append(databases, name)
		}
	}
	o.Databases = databases
	if o.LogBackupIntervalSeconds == 0 {
		o.LogBackupIntervalSeconds = defaultLogBackupInterval
	}
	if o.LogBackupIntervalSeconds < minLogBackupInterval || o.LogBackupIntervalSeconds > maxLogBackupInterval {
		return Options{}, fmt.Errorf("logBackupIntervalSeconds must be between %d and %d", minLogBackupInterval, maxLogBackupInterval)
	}
	return o, nil
}

func validateDatabaseName(name string) error {
	if !databaseNamePattern.MatchString(name) {
		return fmt.Errorf("database %q must be 1-100 letters, digits, '_', '$' or '-', not starting with '$' or '-'", name)
	}
	if slices.ContainsFunc(systemDatabases, func(system string) bool { return strings.EqualFold(system, name) }) {
		return fmt.Errorf("system database %q cannot be migrated", name)
	}
	return nil
}
