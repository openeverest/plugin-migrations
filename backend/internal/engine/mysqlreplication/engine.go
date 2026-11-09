// Package mysqlreplication implements MySQL → MySQL migrations with MySQL's
// native GTID replication: MySQL Shell copies the databases, then the target
// replicates from the source over its own channel until the cutover. One
// small worker per migration drives it (workers/mysql-replication); after the
// copy, the data flows directly between the servers.
package mysqlreplication

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
	ID = "mysql-replication"

	databaseTypeMySQL = "mysql"
	workerPort        = 2243
	healthPath        = "/healthz"
	channelPrefix     = "mig_"

	secretKeySourceURI = "source-uri"
	secretKeyTargetURI = "target-uri"
	secretKeySourceCA  = "source-ca.crt"
	secretKeyTargetCA  = "target-ca.crt"

	controlTimeout  = 15 * time.Second
	teardownTimeout = 3 * time.Minute
)

// The target connection must be able to configure replication, set
// gtid_purged and local_infile. provider-percona-xtradb-cluster publishes
// root; its PXC nodes run with skip_replica_start, which the worker relies on.
var targetProviders = []string{"percona-xtradb-cluster"}

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
		DisplayName:         "MySQL replication",
		SourceDatabaseTypes: []string{databaseTypeMySQL},
		TargetDatabaseTypes: []string{databaseTypeMySQL},
		TargetProviders:     slices.Clone(targetProviders),
	}
}

func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypeMySQL && target.DatabaseType == databaseTypeMySQL
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
	source, err := parseMySQLURI(input.SourceConn.URI, input.SourceConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	target, err := parseMySQLURI(input.TargetConn.URI, input.TargetConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	if len(options.Databases) == 0 && input.Source.Provider == "" && source.database != "" {
		if err := validateName("databases", source.database, false); err != nil {
			return engine.Prepared{}, err
		}
		options.Databases = []string{source.database}
	}
	if len(options.Databases) == 0 {
		return engine.Prepared{}, invalidRequest("choose the databases to migrate")
	}
	if err := options.validateTables(); err != nil {
		return engine.Prepared{}, err
	}
	if source.endpoint() == target.endpoint() {
		return engine.Prepared{}, invalidRequest("source and target must be different MySQL servers")
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
			"REPLICATION_NAME": channelName(input.MigrationID),
		},
		SecretData:  map[string][]byte{},
		SecretEnv:   map[string]string{"SOURCE_URI": secretKeySourceURI, "TARGET_URI": secretKeyTargetURI},
		SecretFiles: map[string]string{},
	}
	sourceURI, err := source.workerURI(mountCA(input.SourceConn.CACert, secretKeySourceCA, &worker))
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	targetURI, err := target.workerURI(mountCA(input.TargetConn.CACert, secretKeyTargetCA, &worker))
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	worker.SecretData[secretKeySourceURI] = []byte(sourceURI)
	worker.SecretData[secretKeyTargetURI] = []byte(targetURI)

	if err := worker.Validate(); err != nil {
		return engine.Prepared{}, fmt.Errorf("invalid migration worker: %w", err)
	}
	return engine.Prepared{Worker: worker, Snapshot: snapshot, SourceDisplay: source.endpoint()}, nil
}

// mountCA adds a CA to the worker's secret files and returns its path, or ""
// without a CA.
func mountCA(caCert, key string, worker *engine.WorkerSpec) string {
	if caCert == "" {
		return ""
	}
	worker.SecretData[key] = []byte(caCert)
	worker.SecretFiles[key] = key
	return engine.SecretMountPath + "/" + key
}

// channelName names the replication channel and the heartbeat row, unique
// per migration so several can share a source.
func channelName(migrationID string) string {
	return channelPrefix + strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(migrationID, "mig-")), "-", "_")
}

func invalidRequest(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func (e *Engine) Controller(baseURL string) engine.Controller {
	return agentclient.New(baseURL, e.httpClient, e.teardownClient, startBody)
}

func startBody(raw json.RawMessage) (any, error) {
	options, err := decodeOptions(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"options": options}, nil
}
