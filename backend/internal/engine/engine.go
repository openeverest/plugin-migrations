// Package engine defines the technology-agnostic contracts every migration
// engine implements. It has no Kubernetes, HTTP-server or OpenEverest
// dependencies so engines stay small and independently testable.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
)

// Engine prepares workers for one source/target technology pair. Implementations
// must be immutable and safe for concurrent use; Prepare performs no I/O.
type Engine interface {
	ID() string
	Describe() Descriptor
	Supports(source, target EndpointInfo) bool
	Prepare(input PrepareInput) (Prepared, error)
	// Controller returns a client for a running worker reachable at baseURL.
	Controller(baseURL string) Controller
}

// Controller drives a running worker. Methods map onto the shared lifecycle;
// engine-specific rules (e.g. how to resume a failed run) stay inside the engine.
type Controller interface {
	Start(ctx context.Context, options json.RawMessage) error
	Status(ctx context.Context) (Status, error)
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	Cutover(ctx context.Context) error
}

// Teardowner is implemented by controllers whose workers leave state on the
// source or target (e.g. a PostgreSQL replication slot that retains WAL). The
// service calls Teardown before deleting a migration; data on the target stays.
type Teardowner interface {
	Teardown(ctx context.Context) error
}

// Descriptor is the public, credential-free description served by /api/status.
type Descriptor struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"displayName"`
	SourceDatabaseTypes []string `json:"sourceDatabaseTypes"`
	TargetDatabaseTypes []string `json:"targetDatabaseTypes"`
	TargetProviders     []string `json:"targetProviders"`
	// CanPause tells whether a running migration can be paused and resumed
	// where it stopped.
	CanPause bool `json:"canPause"`
}

// EndpointInfo is what an engine needs to decide compatibility.
type EndpointInfo struct {
	DatabaseType string
	Provider     string // empty for external endpoints
}

// Connection holds endpoint credentials. Never log or persist it outside the
// migration's Secret.
type Connection struct {
	URI      string
	Username string
	Password string
	// CACert is a PEM bundle used to verify the endpoint's TLS certificate.
	CACert string
	// Properties are the other keys an instance's provider publishes, such
	// as an administrative connection. They may hold credentials.
	Properties map[string]string
}

// PrepareInput carries resolved endpoints and the request's engine options in
// their public JSON form; the engine strictly decodes them.
type PrepareInput struct {
	MigrationID string
	Source      EndpointInfo
	Target      EndpointInfo
	SourceConn  Connection
	TargetConn  Connection
	Options     json.RawMessage
	Runner      RunnerConfig
}

// RunnerConfig comes from trusted deployment configuration, never a request.
type RunnerConfig struct {
	Image           string
	ImagePullPolicy string
}

func (c RunnerConfig) Validate() error {
	if strings.TrimSpace(c.Image) == "" {
		return errors.New("runner image is required")
	}
	switch c.ImagePullPolicy {
	case "Always", "IfNotPresent", "Never":
		return nil
	default:
		return errors.New("image pull policy must be Always, IfNotPresent, or Never")
	}
}

// Prepared is the result of a successful Prepare.
type Prepared struct {
	Worker WorkerSpec
	// Snapshot holds the effective options after defaults. It is persisted and
	// returned by the API, so it must never contain credentials.
	Snapshot json.RawMessage
	// SourceDisplay is a credential-free summary of the source (e.g. hosts).
	SourceDisplay string
}

// WorkerSpec describes the long-running worker container. SecretData values
// are credentials: never log a WorkerSpec.
type WorkerSpec struct {
	Image           string
	ImagePullPolicy string
	Port            int32
	// HealthPath answers 200 on every worker role; used for probes.
	HealthPath string
	Env        map[string]string
	SecretData map[string][]byte
	// SecretEnv maps environment variable names to keys in SecretData.
	SecretEnv map[string]string
	// SecretFiles maps SecretData keys to file names under SecretMountPath.
	SecretFiles map[string]string
	// Bootstrap, when set, runs to completion once per migration before the
	// worker first starts, with the worker's image, env and secrets. It never
	// runs again on worker restarts, so it may discard state a previous
	// migration left on the target.
	Bootstrap *Command
}

// Command overrides a container's entrypoint (Command) and arguments.
type Command struct {
	Command []string
	Args    []string
}

// SecretMountPath is where SecretFiles are mounted inside the worker.
const SecretMountPath = "/etc/migration/secrets"

func (s WorkerSpec) Validate() error {
	if strings.TrimSpace(s.Image) == "" {
		return errors.New("worker image is required")
	}
	if s.Port <= 0 || s.Port > 65535 {
		return errors.New("worker port must be between 1 and 65535")
	}
	if !strings.HasPrefix(s.HealthPath, "/") {
		return errors.New("worker health path must be absolute")
	}
	for name := range s.Env {
		if strings.TrimSpace(name) == "" {
			return errors.New("worker environment names must not be blank")
		}
	}
	for name, key := range s.SecretEnv {
		if strings.TrimSpace(name) == "" {
			return errors.New("worker secret environment names must not be blank")
		}
		if _, exists := s.Env[name]; exists {
			return errors.New("worker environment name cannot appear in both Env and SecretEnv")
		}
		if _, exists := s.SecretData[key]; !exists {
			return errors.New("worker secret environment reference must identify an existing Secret key")
		}
	}
	for key, file := range s.SecretFiles {
		if _, exists := s.SecretData[key]; !exists {
			return errors.New("worker secret file must identify an existing Secret key")
		}
		if file == "" || strings.ContainsAny(file, "/\\") {
			return errors.New("worker secret file names must be plain file names")
		}
	}
	if s.Bootstrap != nil && len(s.Bootstrap.Command) == 0 && len(s.Bootstrap.Args) == 0 {
		return errors.New("worker bootstrap must set a command or arguments")
	}
	return nil
}

// Clone deep-copies every mutable collection.
func (s WorkerSpec) Clone() WorkerSpec {
	clone := s
	clone.Env = maps.Clone(s.Env)
	clone.SecretEnv = maps.Clone(s.SecretEnv)
	clone.SecretFiles = maps.Clone(s.SecretFiles)
	clone.SecretData = maps.Clone(s.SecretData)
	for key, value := range clone.SecretData {
		clone.SecretData[key] = slices.Clone(value)
	}
	if s.Bootstrap != nil {
		clone.Bootstrap = &Command{Command: slices.Clone(s.Bootstrap.Command), Args: slices.Clone(s.Bootstrap.Args)}
	}
	return clone
}

// Phase is the engine-agnostic migration phase shown to users.
type Phase string

const (
	PhaseProvisioning Phase = "provisioning"
	PhaseStarting     Phase = "starting"
	PhaseFullLoad     Phase = "full-load"
	PhaseCatchingUp   Phase = "catching-up"
	PhaseReplicating  Phase = "replicating"
	PhasePaused       Phase = "paused"
	PhaseFinalizing   Phase = "finalizing"
	PhaseCompleted    Phase = "completed"
	PhaseFailed       Phase = "failed"
	PhaseUnknown      Phase = "unknown"
)

// Status is a normalized snapshot of a worker's progress.
type Status struct {
	Phase    Phase
	Message  string
	Error    string
	Progress Progress
	// Idle is true when the worker is up but no migration was started yet.
	Idle bool
	// InitialSyncCompleted lets the lifecycle allow cutover from a paused state.
	InitialSyncCompleted bool
}

// Progress uses zero values for metrics the engine cannot report.
type Progress struct {
	BytesTotal       uint64     `json:"bytesTotal"`
	BytesCopied      uint64     `json:"bytesCopied"`
	LagSeconds       int64      `json:"lagSeconds"`
	EventsApplied    int64      `json:"eventsApplied"`
	LastReplicatedAt *time.Time `json:"lastReplicatedAt,omitempty"`
	ReadyForCutover  bool       `json:"readyForCutover"`
}
