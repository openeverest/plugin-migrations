package pcsm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const (
	maxNamespaceFilters = 200
	maxDatabaseNameLen  = 64
)

// Options is the public engine option schema (see docs/architecture.md §8).
// Pointer fields distinguish "use the PCSM default" from an explicit value.
type Options struct {
	IncludeNamespaces  []string           `json:"includeNamespaces"`
	ExcludeNamespaces  []string           `json:"excludeNamespaces"`
	PauseOnInitialSync bool               `json:"pauseOnInitialSync"`
	Clone              CloneOptions       `json:"clone"`
	Replication        ReplicationOptions `json:"replication"`
}

type CloneOptions struct {
	NumParallelCollections *int `json:"numParallelCollections,omitempty"`
	NumReadWorkers         *int `json:"numReadWorkers,omitempty"`
	NumInsertWorkers       *int `json:"numInsertWorkers,omitempty"`
}

type ReplicationOptions struct {
	NumWorkers *int `json:"numWorkers,omitempty"`
}

// startRequest mirrors the subset of PCSM's /start body this engine exposes.
type startRequest struct {
	PauseOnInitialSync          bool     `json:"pauseOnInitialSync,omitempty"`
	IncludeNamespaces           []string `json:"includeNamespaces,omitempty"`
	ExcludeNamespaces           []string `json:"excludeNamespaces,omitempty"`
	CloneNumParallelCollections *int     `json:"cloneNumParallelCollections,omitempty"`
	CloneNumReadWorkers         *int     `json:"cloneNumReadWorkers,omitempty"`
	CloneNumInsertWorkers       *int     `json:"cloneNumInsertWorkers,omitempty"`
	ReplNumWorkers              *int     `json:"replNumWorkers,omitempty"`
}

func invalidOptions(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

// decodeOptions strictly decodes, defaults and validates options. A missing or
// null value yields the defaults.
func decodeOptions(raw json.RawMessage) (Options, error) {
	options := Options{IncludeNamespaces: []string{}, ExcludeNamespaces: []string{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return Options{}, invalidOptions("invalid PCSM options: %s", safeDecodeError(err))
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return Options{}, invalidOptions("invalid PCSM options: unexpected data after JSON object")
		}
	}

	var err error
	if options.IncludeNamespaces, err = normalizeNamespaces("includeNamespaces", options.IncludeNamespaces); err != nil {
		return Options{}, err
	}
	if options.ExcludeNamespaces, err = normalizeNamespaces("excludeNamespaces", options.ExcludeNamespaces); err != nil {
		return Options{}, err
	}
	for _, namespace := range options.ExcludeNamespaces {
		if slices.Contains(options.IncludeNamespaces, namespace) {
			return Options{}, invalidOptions("namespace %q cannot be both included and excluded", namespace)
		}
	}

	limits := []struct {
		name     string
		value    *int
		min, max int
	}{
		{"clone.numParallelCollections", options.Clone.NumParallelCollections, 1, 64},
		{"clone.numReadWorkers", options.Clone.NumReadWorkers, 1, 256},
		{"clone.numInsertWorkers", options.Clone.NumInsertWorkers, 1, 256},
		{"replication.numWorkers", options.Replication.NumWorkers, 1, 256},
	}
	for _, limit := range limits {
		if limit.value != nil && (*limit.value < limit.min || *limit.value > limit.max) {
			return Options{}, invalidOptions("%s must be between %d and %d", limit.name, limit.min, limit.max)
		}
	}
	return options, nil
}

// safeDecodeError keeps type/field information but never echoes input values.
func safeDecodeError(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Sprintf("field %q must be %s", typeErr.Field, typeErr.Type.String())
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return strings.TrimPrefix(err.Error(), "json: ")
	}
	return "malformed JSON"
}

// normalizeNamespaces accepts "db", "db.*" and "db.collection" filters and
// removes duplicates while keeping order. A bare "db" becomes "db.*": PCSM's
// selector reads "db" as a collection named "" and would silently match nothing.
func normalizeNamespaces(field string, namespaces []string) ([]string, error) {
	if len(namespaces) > maxNamespaceFilters {
		return nil, invalidOptions("%s accepts at most %d entries", field, maxNamespaceFilters)
	}
	normalized := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespace = strings.TrimSpace(namespace)
		if err := validateNamespace(namespace); err != nil {
			return nil, invalidOptions("%s: %v", field, err)
		}
		if !strings.Contains(namespace, ".") {
			namespace += ".*"
		}
		if !slices.Contains(normalized, namespace) {
			normalized = append(normalized, namespace)
		}
	}
	return normalized, nil
}

func validateNamespace(namespace string) error {
	database, collection, hasCollection := strings.Cut(namespace, ".")
	if database == "" || len(database) > maxDatabaseNameLen || strings.ContainsAny(database, "/\\ \"$*<>:|?\x00") {
		return fmt.Errorf("%q is not a valid database name", database)
	}
	if hasCollection && (collection == "" || (collection != "*" && strings.ContainsAny(collection, "$\x00"))) {
		return fmt.Errorf("%q is not a valid namespace", namespace)
	}
	return nil
}

func (o Options) startRequest() startRequest {
	return startRequest{
		PauseOnInitialSync:          o.PauseOnInitialSync,
		IncludeNamespaces:           o.IncludeNamespaces,
		ExcludeNamespaces:           o.ExcludeNamespaces,
		CloneNumParallelCollections: o.Clone.NumParallelCollections,
		CloneNumReadWorkers:         o.Clone.NumReadWorkers,
		CloneNumInsertWorkers:       o.Clone.NumInsertWorkers,
		ReplNumWorkers:              o.Replication.NumWorkers,
	}
}
