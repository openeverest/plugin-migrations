// Package redisshake implements Redis/Valkey → Valkey migrations with
// RedisShake: it copies a snapshot through the source's replication protocol
// (PSYNC), then applies the changes the source streams until the cutover.
// One worker per migration runs it (workers/redis-shake). RedisShake cannot
// resume an interrupted run, so the worker starts the copy again from an
// empty target instead, and a migration cannot be paused.
package redisshake

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
	ID = "redis-shake"

	databaseTypeValkey = "valkey"
	providerValkey     = "valkey"
	workerPort         = 2243
	healthPath         = "/healthz"
	namePrefix         = "mig_"

	secretKeySourceURI  = "source-uri"
	secretKeyTargetURI  = "target-uri"
	secretKeySourceCA   = "source-ca.crt"
	secretKeySourceCert = "source-tls.crt"
	secretKeySourceKey  = "source-tls.key"
	secretKeyTargetCA   = "target-ca.crt"
	secretKeyTargetCert = "target-tls.crt"
	secretKeyTargetKey  = "target-tls.key"

	controlTimeout  = 15 * time.Second
	teardownTimeout = 3 * time.Minute
)

// provider-valkey publishes the default user (+@all), which can serve PSYNC
// and empty the target; its instances always run in cluster mode.
var targetProviders = []string{providerValkey}

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
		DisplayName:         "Redis / Valkey (RedisShake)",
		SourceDatabaseTypes: []string{databaseTypeValkey},
		TargetDatabaseTypes: []string{databaseTypeValkey},
		TargetProviders:     slices.Clone(targetProviders),
	}
}

func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypeValkey && target.DatabaseType == databaseTypeValkey &&
		(source.Provider == "" || source.Provider == providerValkey)
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
	source, err := parseRedisURI(input.SourceConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	target, err := parseRedisURI(input.TargetConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	if source.endpoint() == target.endpoint() {
		return engine.Prepared{}, invalidRequest("source and target must be different servers")
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
		Env:             map[string]string{"PORT": fmt.Sprint(workerPort), "MIGRATION_NAME": migrationName(input.MigrationID)},
		SecretData:      map[string][]byte{},
		SecretEnv:       map[string]string{"SOURCE_URI": secretKeySourceURI, "TARGET_URI": secretKeyTargetURI},
		SecretFiles:     map[string]string{},
	}
	sourceURI, err := source.workerURI(
		mount(input.SourceConn.CACert, secretKeySourceCA, &worker),
		mount(input.SourceConn.Properties[propertyClientCert], secretKeySourceCert, &worker),
		mount(input.SourceConn.Properties[propertyClientKey], secretKeySourceKey, &worker),
	)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	targetURI, err := target.workerURI(
		mount(input.TargetConn.CACert, secretKeyTargetCA, &worker),
		mount(input.TargetConn.Properties[propertyClientCert], secretKeyTargetCert, &worker),
		mount(input.TargetConn.Properties[propertyClientKey], secretKeyTargetKey, &worker),
	)
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

// mount adds a PEM file to the worker's secret files and returns its path,
// or "" without content.
func mount(content, key string, worker *engine.WorkerSpec) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	worker.SecretData[key] = []byte(content)
	worker.SecretFiles[key] = key
	return engine.SecretMountPath + "/" + key
}

// migrationName names the worker's keys on the source and target.
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
