// Package mysqlreplication implements MySQL → MySQL and MariaDB → MariaDB
// migrations with the servers' native GTID replication: MySQL Shell (MySQL)
// or mydumper/myloader (MariaDB) copies the databases, then the target
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
	"github.com/openeverest/plugin-migrations/backend/internal/engine/mysqluri"
)

const (
	ID        = "mysql-replication"
	MariaDBID = "mariadb-replication"

	databaseTypeMySQL = "mysql"
	providerMariaDB   = "mariadb"
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

// variant is what differs between the MySQL and MariaDB engines; both run
// the same worker image with a different FLAVOR.
type variant struct {
	id, displayName, flavor, product string
	schemes                          []string
	// targetProviders must publish a connection that can configure
	// replication on servers that keep it stopped when they start.
	targetProviders []string
	// sourceAllowed tells whether an instance of the provider can be the
	// source; external sources have no provider.
	sourceAllowed func(provider string) bool
	// adminConnection makes instance endpoints use the provider's admin
	// connection instead of the application user's.
	adminConnection bool
}

// provider-percona-xtradb-cluster publishes root, and its PXC nodes run with
// skip_replica_start, which the worker checks.
var mysqlVariant = variant{
	id: ID, displayName: "MySQL replication", flavor: "mysql", product: "MySQL", schemes: []string{"mysql"},
	targetProviders: []string{"percona-xtradb-cluster"},
	sourceAllowed:   func(provider string) bool { return provider != providerMariaDB },
}

// provider-mariadb publishes its admin (root) connection as admin-* keys,
// and admin-skip-replica-start for the topologies it starts with
// skip-slave-start (standalone and Galera).
var mariadbVariant = variant{
	id: MariaDBID, displayName: "MariaDB replication", flavor: "mariadb", product: "MariaDB", schemes: []string{"mariadb", "mysql"},
	targetProviders: []string{providerMariaDB},
	sourceAllowed:   func(provider string) bool { return provider == "" || provider == providerMariaDB },
	adminConnection: true,
}

type Engine struct {
	variant        variant
	httpClient     *http.Client
	teardownClient *http.Client
}

// New returns the MySQL engine.
func New() engine.Engine { return newEngine(mysqlVariant) }

// NewMariaDB returns the MariaDB engine.
func NewMariaDB() engine.Engine { return newEngine(mariadbVariant) }

func newEngine(v variant) *Engine {
	return &Engine{
		variant:        v,
		httpClient:     &http.Client{Timeout: controlTimeout},
		teardownClient: &http.Client{Timeout: teardownTimeout},
	}
}

func (e *Engine) ID() string { return e.variant.id }

func (e *Engine) Describe() engine.Descriptor {
	return engine.Descriptor{
		ID:                  e.variant.id,
		DisplayName:         e.variant.displayName,
		SourceDatabaseTypes: []string{databaseTypeMySQL},
		TargetDatabaseTypes: []string{databaseTypeMySQL},
		TargetProviders:     slices.Clone(e.variant.targetProviders),
		CanPause:            true,
	}
}

func (e *Engine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == databaseTypeMySQL && target.DatabaseType == databaseTypeMySQL && e.variant.sourceAllowed(source.Provider)
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
	sourceConn, targetConn := input.SourceConn, input.TargetConn
	env := map[string]string{
		"PORT":             fmt.Sprint(workerPort),
		"REPLICATION_NAME": channelName(input.MigrationID),
		"FLAVOR":           e.variant.flavor,
	}
	if e.variant.adminConnection {
		if input.Source.Provider != "" {
			if sourceConn, err = adminConnection(sourceConn); err != nil {
				return engine.Prepared{}, invalidRequest("source instance %v", err)
			}
		}
		if targetConn, err = adminConnection(targetConn); err != nil {
			return engine.Prepared{}, invalidRequest("target instance %v", err)
		}
		if targetConn.Properties[propertySkipReplicaStart] != "true" {
			return engine.Prepared{}, invalidRequest("the target instance must be a standalone or Galera MariaDB instance managed by provider-mariadb 0.2.2 or later")
		}
		env["TARGET_SKIP_REPLICA_START"] = "true"
	}
	source, err := mysqluri.Parse(sourceConn.URI, sourceConn, e.variant.schemes)
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	target, err := mysqluri.Parse(targetConn.URI, targetConn, e.variant.schemes)
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
		return engine.Prepared{}, invalidRequest("source and target must be different %s servers", e.variant.product)
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
		Env:             env,
		SecretData:      map[string][]byte{},
		SecretEnv:       map[string]string{"SOURCE_URI": secretKeySourceURI, "TARGET_URI": secretKeyTargetURI},
		SecretFiles:     map[string]string{},
	}
	sourceURI, err := source.WorkerURI(mysqluri.MountCA(sourceConn.CACert, secretKeySourceCA, &worker))
	if err != nil {
		return engine.Prepared{}, invalidRequest("source connection string %v", err)
	}
	targetURI, err := target.WorkerURI(mysqluri.MountCA(targetConn.CACert, secretKeyTargetCA, &worker))
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
