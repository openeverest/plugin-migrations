// Package postgreslogical implements PostgreSQL → PostgreSQL migrations with
// PostgreSQL's native logical replication: the schema is copied with
// pg_dump, a publication on the source and a subscription on the target copy
// the data and stream changes. One small worker per migration drives it
// (workers/postgres-logical); the data flows directly between the servers.
package postgreslogical

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/agentclient"
)

const (
	ID = "postgres-logical"

	databaseTypePostgreSQL = "postgresql"
	workerPort             = 2242
	healthPath             = "/healthz"
	namePrefix             = "mig_"
	// adminDatabase exists on every PostgreSQL server; the worker connects to
	// it to create the target database.
	adminDatabase = "postgres"

	secretKeySourceURI      = "source-uri"
	secretKeySubscription   = "subscription-conninfo"
	secretKeyTargetURI      = "target-uri"
	secretKeyTargetAdminURI = "target-admin-uri"
	secretKeySourceCA       = "source-ca.crt"
	secretKeyTargetCA       = "target-ca.crt"

	controlTimeout  = 15 * time.Second
	teardownTimeout = 3 * time.Minute
)

// The target connection must be a superuser to create the subscription.
// provider-percona-postgresql publishes the postgres superuser; providers
// whose connection details are not superusers are not supported yet.
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
		DisplayName:         "PostgreSQL logical replication",
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
		return engine.Prepared{}, fmt.Errorf("encode options snapshot: %w", err)
	}

	worker := engine.WorkerSpec{
		Image:           input.Runner.Image,
		ImagePullPolicy: input.Runner.ImagePullPolicy,
		Port:            workerPort,
		HealthPath:      healthPath,
		Env: map[string]string{
			"PORT":             fmt.Sprint(workerPort),
			"REPLICATION_NAME": replicationName(input.MigrationID),
			"TARGET_DATABASE":  options.Database,
		},
		SecretData: map[string][]byte{},
		SecretEnv: map[string]string{
			"SOURCE_PGURI":          secretKeySourceURI,
			"SUBSCRIPTION_CONNINFO": secretKeySubscription,
			"TARGET_PGURI":          secretKeyTargetURI,
			"TARGET_ADMIN_PGURI":    secretKeyTargetAdminURI,
		},
		SecretFiles: map[string]string{},
	}
	source = source.withDatabase(options.Database)
	subscription := source.subscriptionConninfo(input.SourceConn.CACert != "")
	source = withCA(source, input.SourceConn.CACert, secretKeySourceCA, &worker)
	targetAdmin := withCA(target.withDatabase(adminDatabase), input.TargetConn.CACert, secretKeyTargetCA, &worker)
	target = withCA(target.withDatabase(options.Database), input.TargetConn.CACert, secretKeyTargetCA, &worker)
	worker.SecretData[secretKeySourceURI] = []byte(source.String())
	worker.SecretData[secretKeySubscription] = []byte(subscription.String())
	worker.SecretData[secretKeyTargetURI] = []byte(target.String())
	worker.SecretData[secretKeyTargetAdminURI] = []byte(targetAdmin.String())

	if err := worker.Validate(); err != nil {
		return engine.Prepared{}, fmt.Errorf("invalid migration worker: %w", err)
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

// replicationName names the publication, the subscription and the source
// replication slot, unique per migration so several can share a source.
func replicationName(migrationID string) string {
	return namePrefix + strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(migrationID, "mig-")), "-", "_")
}

func invalidRequest(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func (e *Engine) Controller(baseURL string) engine.Controller {
	return agentclient.New(baseURL, e.httpClient, e.teardownClient, startBody)
}

// startBody sends the worker its options; the database is already part of
// the connection strings it receives.
func startBody(raw json.RawMessage) (any, error) {
	options, err := decodeOptions(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"options": options.worker()}, nil
}
