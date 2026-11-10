// Package tidbdm implements MySQL → TiDB migrations with TiDB Data Migration
// (DM). One worker per migration runs a dm-master and a dm-worker next to a
// small agent (workers/tidb-dm): DM dumps the databases to the worker's
// volume, loads them into TiDB and then replicates the source's binary log,
// keeping its checkpoints on the target.
package tidbdm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/agentclient"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/mysqluri"
)

const (
	ID = "tidb-dm"

	databaseTypeMySQL = "mysql"
	providerTiDB      = "tidb"
	providerPXC       = "percona-xtradb-cluster"
	workerPort        = 2243
	healthPath        = "/healthz"
	namePrefix        = "mig_"
	// dataPath holds DM's state and the dump; workerGroup is the image's
	// non-root group, which must be able to write it.
	dataPath    = "/data"
	workerGroup = 65532

	secretKeySourceURI = "source-uri"
	secretKeyTargetURI = "target-uri"
	secretKeySourceCA  = "source-ca.crt"
	secretKeyTargetCA  = "target-ca.crt"

	controlTimeout  = 15 * time.Second
	teardownTimeout = 3 * time.Minute
)

var schemes = []string{"mysql"}

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

// Describe offers provider-tidb targets: they publish root, which DM needs
// to create the databases and its checkpoint tables.
func (e *Engine) Describe() engine.Descriptor {
	return engine.Descriptor{
		ID:                  ID,
		DisplayName:         "TiDB Data Migration (DM)",
		SourceDatabaseTypes: []string{databaseTypeMySQL},
		TargetDatabaseTypes: []string{databaseTypeMySQL},
		TargetProviders:     []string{providerTiDB},
		CanPause:            true,
	}
}

// Supports accepts MySQL sources: external servers and PXC instances.
func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypeMySQL && target.DatabaseType == databaseTypeMySQL &&
		(source.Provider == "" || source.Provider == providerPXC)
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
	source, err := mysqluri.Parse(input.SourceConn.URI, input.SourceConn, schemes)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	target, err := mysqluri.Parse(input.TargetConn.URI, input.TargetConn, schemes)
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	if len(options.Databases) == 0 && input.Source.Provider == "" && source.Database != "" {
		if err := validateName("databases", source.Database, false); err != nil {
			return engine.Prepared{}, err
		}
		options.Databases = []string{source.Database}
	}
	if len(options.Databases) == 0 {
		return engine.Prepared{}, invalidRequest("choose the databases to migrate")
	}
	if err := options.validateTables(); err != nil {
		return engine.Prepared{}, err
	}
	if source.Endpoint() == target.Endpoint() {
		return engine.Prepared{}, invalidRequest("source and target must be different servers")
	}
	if err := checkDMTLS("source", source, input.SourceConn.CACert != ""); err != nil {
		return engine.Prepared{}, err
	}
	if err := checkDMTLS("target", target, input.TargetConn.CACert != ""); err != nil {
		return engine.Prepared{}, err
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
			"PORT":           fmt.Sprint(workerPort),
			"MIGRATION_NAME": migrationName(input.MigrationID),
			"DATA_DIR":       dataPath,
		},
		SecretData:  map[string][]byte{},
		SecretEnv:   map[string]string{"SOURCE_URI": secretKeySourceURI, "TARGET_URI": secretKeyTargetURI},
		SecretFiles: map[string]string{},
		Volume:      &engine.Volume{SizeGiB: options.StorageGiB, MountPath: dataPath, FSGroup: workerGroup},
	}
	sourceURI, err := source.WorkerURI(mysqluri.MountCA(input.SourceConn.CACert, secretKeySourceCA, &worker))
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	targetURI, err := target.WorkerURI(mysqluri.MountCA(input.TargetConn.CACert, secretKeyTargetCA, &worker))
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	worker.SecretData[secretKeySourceURI] = []byte(sourceURI)
	worker.SecretData[secretKeyTargetURI] = []byte(targetURI)

	if err := worker.Validate(); err != nil {
		return engine.Prepared{}, fmt.Errorf("invalid migration worker: %w", err)
	}
	return engine.Prepared{Worker: worker, Snapshot: snapshot, SourceDisplay: source.Endpoint()}, nil
}

// checkDMTLS rejects TLS settings DM cannot honor: DM either connects
// without TLS or verifies the server against a CA, so a connection that
// requires TLS without verifying it is impossible.
func checkDMTLS(role string, uri mysqluri.URI, hasCA bool) error {
	if uri.EffectiveSSLMode(hasCA) == mysqluri.SSLRequired {
		return invalidRequest("%s connection string requires TLS without verifying the server (ssl-mode=REQUIRED or tls=skip-verify), "+
			"which DM does not support; provide the server's CA certificate, or use ssl-mode=PREFERRED or DISABLED", role)
	}
	return nil
}

// migrationName names the DM source and task, the heartbeat database and
// DM's checkpoint tables, unique per migration so several can share a source.
func migrationName(migrationID string) string {
	return namePrefix + strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(migrationID, "mig-")), "-", "_")
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
