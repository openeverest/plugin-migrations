// Package pcsm implements MongoDB → MongoDB migrations with Percona ClusterSync
// for MongoDB. One PCSM server runs per migration as a long-lived worker; the
// plugin drives it through PCSM's HTTP API.
package pcsm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const (
	ID = "pcsm"

	databaseTypeMongoDB = "mongodb"
	workerPort          = 2242
	// PCSM serves /metrics on every HA role; /status answers 409 on standbys.
	healthPath = "/metrics"

	secretKeySourceURI = "source-uri"
	secretKeyTargetURI = "target-uri"
	secretKeySourceCA  = "source-ca.crt"
	secretKeyTargetCA  = "target-ca.crt"

	controlTimeout = 15 * time.Second
)

var targetProviders = []string{"percona-server-mongodb"}

type Engine struct {
	httpClient *http.Client
}

func New() engine.Engine {
	return &Engine{httpClient: &http.Client{Timeout: controlTimeout}}
}

func (e *Engine) ID() string { return ID }

func (e *Engine) Describe() engine.Descriptor {
	return engine.Descriptor{
		ID:                  ID,
		DisplayName:         "Percona ClusterSync for MongoDB",
		SourceDatabaseTypes: []string{databaseTypeMongoDB},
		TargetDatabaseTypes: []string{databaseTypeMongoDB},
		TargetProviders:     slices.Clone(targetProviders),
		CanPause:            true,
	}
}

func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypeMongoDB && target.DatabaseType == databaseTypeMongoDB
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
	snapshot, err := json.Marshal(options)
	if err != nil {
		return engine.Prepared{}, fmt.Errorf("encode PCSM options snapshot: %w", err)
	}

	worker := engine.WorkerSpec{
		Image:           input.Runner.Image,
		ImagePullPolicy: input.Runner.ImagePullPolicy,
		Port:            workerPort,
		HealthPath:      healthPath,
		Env: map[string]string{
			"PCSM_LISTEN_HOST":  "0.0.0.0",
			"PCSM_PORT":         fmt.Sprint(workerPort),
			"PCSM_LOG_JSON":     "true",
			"PCSM_LOG_NO_COLOR": "true",
			"PCSM_GROUP_NAME":   input.MigrationID,
		},
		SecretData:  map[string][]byte{},
		SecretEnv:   map[string]string{"PCSM_SOURCE_URI": secretKeySourceURI, "PCSM_TARGET_URI": secretKeyTargetURI},
		SecretFiles: map[string]string{},
		// PCSM keeps recovery checkpoints on the target keyed independently of
		// the group name. Without a reset, a new worker "recovers" the previous
		// migration (possibly already finalized) instead of starting fresh.
		// The binary is called directly: the image entrypoint appends
		// --source/--target, which "reset" rejects.
		Bootstrap: &engine.Command{Command: []string{"/usr/bin/pcsm"}, Args: []string{"reset"}},
	}

	source, err := connectionURI("source", input.SourceConn, secretKeySourceCA, &worker)
	if err != nil {
		return engine.Prepared{}, err
	}
	target, err := connectionURI("target", input.TargetConn, secretKeyTargetCA, &worker)
	if err != nil {
		return engine.Prepared{}, err
	}
	if source.hosts == target.hosts && source.scheme == target.scheme {
		return engine.Prepared{}, &engine.Error{Kind: engine.KindInvalidRequest, Message: "source and target must be different MongoDB deployments"}
	}
	worker.SecretData[secretKeySourceURI] = []byte(source.String())
	worker.SecretData[secretKeyTargetURI] = []byte(target.String())

	if err := worker.Validate(); err != nil {
		return engine.Prepared{}, fmt.Errorf("invalid PCSM worker: %w", err)
	}
	return engine.Prepared{Worker: worker, Snapshot: snapshot, SourceDisplay: source.display()}, nil
}

// connectionURI builds the URI PCSM will use. A CA is mounted as a file and
// referenced with tlsCAFile, enabling TLS unless the URI decides explicitly.
func connectionURI(role string, connection engine.Connection, caKey string, worker *engine.WorkerSpec) (mongoURI, error) {
	uri, err := parseMongoURI(connection.URI)
	if err != nil {
		return mongoURI{}, &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf("%s connection string %v", role, err)}
	}
	uri = uri.withCredentials(connection.Username, connection.Password)
	if connection.CACert != "" {
		worker.SecretData[caKey] = []byte(connection.CACert)
		worker.SecretFiles[caKey] = caKey
		uri = uri.withOption("tlsCAFile", engine.SecretMountPath+"/"+caKey)
		if !uri.hasOption("tls") && !uri.hasOption("ssl") {
			uri = uri.withOption("tls", "true")
		}
	}
	return uri, nil
}

func (e *Engine) Controller(baseURL string) engine.Controller {
	return &controller{baseURL: baseURL, httpClient: e.httpClient}
}
