// Package mssqllogshipping implements SQL Server → SQL Server migrations
// with log shipping through S3: the worker restores a full backup of every
// database WITH NORECOVERY, then restores log backups until the cutover,
// which takes a final log backup and recovers the databases. Backups are
// staged in an OpenEverest BackupStorage. One worker per migration drives it
// (workers/mssql-log-shipping); the data flows between the SQL Servers and
// the bucket.
package mssqllogshipping

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/agentclient"
)

const (
	ID = "mssql-log-shipping"

	databaseTypeMSSQL = "mssql"
	providerMSSQL     = "mssql"
	workerPort        = 2243
	healthPath        = "/healthz"

	secretKeySourceURI = "source-uri"
	secretKeyTargetURI = "target-uri"
	secretKeySourceCA  = "source-ca.crt"
	secretKeyTargetCA  = "target-ca.crt"
	secretKeyS3Key     = "s3-access-key-id"
	secretKeyS3Secret  = "s3-secret-access-key"
	secretKeyS3CA      = "s3-ca.crt"

	controlTimeout  = 15 * time.Second
	teardownTimeout = 3 * time.Minute
)

// provider-mssql publishes the sa login, which can back up, restore, create
// credentials and logins.
var targetProviders = []string{providerMSSQL}

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
		ID:                    ID,
		DisplayName:           "SQL Server log shipping",
		SourceDatabaseTypes:   []string{databaseTypeMSSQL},
		TargetDatabaseTypes:   []string{databaseTypeMSSQL},
		TargetProviders:       append([]string(nil), targetProviders...),
		CanPause:              true,
		RequiresBackupStorage: true,
	}
}

func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypeMSSQL && target.DatabaseType == databaseTypeMSSQL &&
		(source.Provider == "" || source.Provider == providerMSSQL)
}

func (e *Engine) Prepare(input engine.PrepareInput) (engine.Prepared, error) {
	if input.MigrationID == "" {
		return engine.Prepared{}, fmt.Errorf("migration ID is required")
	}
	if err := input.Runner.Validate(); err != nil {
		return engine.Prepared{}, err
	}
	if input.BackupStorage == nil {
		return engine.Prepared{}, invalidRequest("choose a backup storage")
	}
	options, err := decodeOptions(input.Options)
	if err != nil {
		return engine.Prepared{}, err
	}
	source, err := parseURI(input.SourceConn.URI, input.SourceConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	target, err := parseURI(input.TargetConn.URI, input.TargetConn)
	if err != nil {
		return engine.Prepared{}, invalidRequest("target connection string %v", err)
	}
	if len(options.Databases) == 0 && input.Source.Provider == "" && source.database != "" {
		if err := validateDatabaseName(source.database); err != nil {
			return engine.Prepared{}, err
		}
		options.Databases = []string{source.database}
	}
	if len(options.Databases) == 0 {
		return engine.Prepared{}, invalidRequest("choose the databases to migrate")
	}
	if source.endpoint() == target.endpoint() {
		return engine.Prepared{}, invalidRequest("source and target must be different SQL Servers")
	}
	storage := input.BackupStorage
	endpoint, err := storageEndpoint(storage.EndpointURL)
	if err != nil {
		return engine.Prepared{}, invalidRequest("backup storage %s %v", storage.Name, err)
	}
	if strings.Contains(storage.AccessKeyID, ":") {
		return engine.Prepared{}, invalidRequest("backup storage %s: SQL Server cannot use an access key ID that contains ':'", storage.Name)
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
			"PORT":                fmt.Sprint(workerPort),
			"MIGRATION_NAME":      strings.ToLower(input.MigrationID),
			"S3_ENDPOINT":         endpoint,
			"S3_BUCKET":           storage.Bucket,
			"S3_REGION":           storage.Region,
			"S3_FORCE_PATH_STYLE": fmt.Sprint(storage.ForcePathStyle),
		},
		SecretData: map[string][]byte{
			secretKeyS3Key:    []byte(storage.AccessKeyID),
			secretKeyS3Secret: []byte(storage.SecretAccessKey),
		},
		SecretEnv: map[string]string{
			"SOURCE_URI": secretKeySourceURI, "TARGET_URI": secretKeyTargetURI,
			"S3_ACCESS_KEY_ID": secretKeyS3Key, "S3_SECRET_ACCESS_KEY": secretKeyS3Secret,
		},
		SecretFiles: map[string]string{},
	}
	if path := mount(storage.CACert, secretKeyS3CA, &worker); path != "" {
		worker.Env["S3_CA_FILE"] = path
	}
	worker.SecretData[secretKeySourceURI] = []byte(source.workerURI(mount(input.SourceConn.CACert, secretKeySourceCA, &worker)))
	worker.SecretData[secretKeyTargetURI] = []byte(target.workerURI(mount(input.TargetConn.CACert, secretKeyTargetCA, &worker)))
	if err := worker.Validate(); err != nil {
		return engine.Prepared{}, fmt.Errorf("invalid migration worker: %w", err)
	}
	return engine.Prepared{Worker: worker, Snapshot: snapshot, SourceDisplay: source.endpoint()}, nil
}

// storageEndpoint checks the BackupStorage endpoint: SQL Server reaches S3
// over HTTPS only.
func storageEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", errors.New("has no valid endpoint URL")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("must use an https:// endpoint: SQL Server backs up to S3 over HTTPS only")
	}
	if strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" {
		return "", errors.New("must have an endpoint URL without a path")
	}
	return "https://" + parsed.Host, nil
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
