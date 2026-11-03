// Package pgcopydb implements PostgreSQL → PostgreSQL migrations with
// pgcopydb. One worker runs per migration: pgcopydb clone --follow wrapped by a
// small agent (workers/pgcopydb) that the plugin drives over HTTP.
package pgcopydb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const (
	ID = "pgcopydb"

	databaseTypePostgreSQL = "postgresql"
	workerPort             = 2242
	healthPath             = "/healthz"
	slotPrefix             = "pgcopydb_"
	// adminDatabase exists on every PostgreSQL server; the worker connects to
	// it to create the target database.
	adminDatabase = "postgres"

	secretKeySourceURI      = "source-uri"
	secretKeyTargetURI      = "target-uri"
	secretKeyTargetAdminURI = "target-admin-uri"
	secretKeySourceCA       = "source-ca.crt"
	secretKeyTargetCA       = "target-ca.crt"

	controlTimeout  = 15 * time.Second
	teardownTimeout = 3 * time.Minute
)

// The target needs a superuser connection (replication origins, extensions,
// object ownership). provider-percona-postgresql publishes the postgres
// superuser; providers whose connection details are not superusers are not
// supported yet.
var targetProviders = []string{"provider-percona-postgresql"}

type Engine struct {
	httpClient     *http.Client
	teardownClient *http.Client
}

func New() engine.Engine {
	return &Engine{
		httpClient:     &http.Client{Timeout: controlTimeout},
		teardownClient: &http.Client{Timeout: teardownTimeout},
	}
}

func (e *Engine) ID() string { return ID }

func (e *Engine) Describe() engine.Descriptor {
	return engine.Descriptor{
		ID:                  ID,
		DisplayName:         "pgcopydb",
		SourceDatabaseTypes: []string{databaseTypePostgreSQL},
		TargetDatabaseTypes: []string{databaseTypePostgreSQL},
		TargetProviders:     slices.Clone(targetProviders),
	}
}

func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypePostgreSQL && target.DatabaseType == databaseTypePostgreSQL
}

func (e *Engine) Prepare(input engine.PrepareInput) (engine.Prepared, error) {
	if input.MigrationID == "" {
		return engine.Prepared{}, fmt.Errorf("migration ID is required")
	}
	if err := input.Runner.Validate(); err != nil {
		return engine.Prepared{}, err
	}
	options, err := decodeOptions(input.Options)
	if err != nil {
		return engine.Prepared{}, err
	}
	source, err := parsePostgresURI(input.SourceConn.URI, input.SourceConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	target, err := parsePostgresURI(input.TargetConn.URI, input.TargetConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	// An OpenEverest instance's connection details point at its maintenance
	// database; migrating that by accident would look like a success.
	if options.Database == "" && input.Source.Provider != "" {
		return engine.Prepared{}, invalidRequest("choose the database to migrate from the source instance")
	}
	if options.Database == "" {
		options.Database = source.database()
	}
	if options.Database == "" {
		return engine.Prepared{}, invalidRequest("choose the database to migrate, or name it in the source connection string")
	}
	if !databasePattern.MatchString(options.Database) {
		return engine.Prepared{}, invalidRequest("database must be 1-63 letters, digits, '_' or '-', starting with a letter or '_'")
	}
	if source.endpoint() == target.endpoint() {
		return engine.Prepared{}, invalidRequest("source and target must be different PostgreSQL servers")
	}
	snapshot, err := json.Marshal(options)
	if err != nil {
		return engine.Prepared{}, fmt.Errorf("encode pgcopydb options snapshot: %w", err)
	}

	worker := engine.WorkerSpec{
		Image:           input.Runner.Image,
		ImagePullPolicy: input.Runner.ImagePullPolicy,
		Port:            workerPort,
		HealthPath:      healthPath,
		Env: map[string]string{
			"PORT":            fmt.Sprint(workerPort),
			"SLOT_NAME":       slotName(input.MigrationID),
			"TARGET_DATABASE": options.Database,
		},
		SecretData: map[string][]byte{},
		SecretEnv: map[string]string{
			"PGCOPYDB_SOURCE_PGURI": secretKeySourceURI,
			"PGCOPYDB_TARGET_PGURI": secretKeyTargetURI,
			"TARGET_ADMIN_PGURI":    secretKeyTargetAdminURI,
		},
		SecretFiles: map[string]string{},
	}
	source = withCA(source.withDatabase(options.Database), input.SourceConn.CACert, secretKeySourceCA, &worker)
	targetAdmin := withCA(target.withDatabase(adminDatabase), input.TargetConn.CACert, secretKeyTargetCA, &worker)
	target = withCA(target.withDatabase(options.Database), input.TargetConn.CACert, secretKeyTargetCA, &worker)
	worker.SecretData[secretKeySourceURI] = []byte(source.String())
	worker.SecretData[secretKeyTargetURI] = []byte(target.String())
	worker.SecretData[secretKeyTargetAdminURI] = []byte(targetAdmin.String())

	if err := worker.Validate(); err != nil {
		return engine.Prepared{}, fmt.Errorf("invalid pgcopydb worker: %w", err)
	}
	return engine.Prepared{Worker: worker, Snapshot: snapshot, SourceDisplay: source.display()}, nil
}

func withCA(uri postgresURI, caCert, key string, worker *engine.WorkerSpec) postgresURI {
	if caCert == "" {
		return uri
	}
	worker.SecretData[key] = []byte(caCert)
	worker.SecretFiles[key] = key
	return uri.withCA(engine.SecretMountPath + "/" + key)
}

// slotName derives the source replication slot (and target replication
// origin) name, unique per migration so several can share a source.
func slotName(migrationID string) string {
	return slotPrefix + strings.ReplaceAll(strings.ToLower(migrationID), "-", "_")
}

func invalidRequest(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func (e *Engine) Controller(baseURL string) engine.Controller {
	return &controller{baseURL: baseURL, httpClient: e.httpClient, teardownClient: e.teardownClient}
}
