package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Options are the backend's tidb-dm options (see backend/internal/engine/tidbdm).
type Options struct {
	Databases         []string `json:"databases"`
	ExcludeTables     []string `json:"excludeTables"`
	RecreateDatabases bool     `json:"recreateDatabases"`
	StorageGiB        int      `json:"storageGiB"`
	CopyThreads       int      `json:"copyThreads"`
	SyncThreads       int      `json:"syncThreads"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

func decodeOptions(raw json.RawMessage) (Options, error) {
	var options Options
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return Options{}, errors.New("options must be a JSON object with known fields")
	}
	if len(options.Databases) == 0 {
		return Options{}, errors.New("databases must not be empty")
	}
	for _, database := range options.Databases {
		lower := strings.ToLower(database)
		if !namePattern.MatchString(database) || lower == "openeverest_migration" || strings.HasPrefix(lower, "openeverest_mig_") {
			return Options{}, fmt.Errorf("invalid database %q", database)
		}
	}
	for _, table := range options.ExcludeTables {
		database, name, ok := strings.Cut(table, ".")
		if !ok || !namePattern.MatchString(database) || !namePattern.MatchString(name) || !contains(options.Databases, database) {
			return Options{}, fmt.Errorf("invalid excluded table %q", table)
		}
	}
	if options.CopyThreads < 1 || options.CopyThreads > 32 || options.SyncThreads < 1 || options.SyncThreads > 64 || options.StorageGiB < 1 {
		return Options{}, errors.New("copyThreads, syncThreads and storageGiB are out of range")
	}
	return options, nil
}

func (o Options) excludes(database, table string) bool {
	for _, excluded := range o.ExcludeTables {
		if strings.EqualFold(excluded, database+"."+table) {
			return true
		}
	}
	return false
}

func (o Options) migrates(database string) bool {
	for _, migrated := range o.Databases {
		if strings.EqualFold(migrated, database) {
			return true
		}
	}
	return false
}
