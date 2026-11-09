// Package migration owns the engine-agnostic migration lifecycle: request
// validation, authorization, endpoint resolution, persistence through Store,
// worker provisioning through Workloads, and the background reconciler.
package migration

import (
	"context"
	"encoding/json"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
)

type EndpointType string

const (
	EndpointInstance EndpointType = "instance"
	EndpointExternal EndpointType = "external"
)

type Action string

const (
	ActionPause   Action = "pause"
	ActionResume  Action = "resume"
	ActionCutover Action = "cutover"
	ActionDelete  Action = "delete"
)

// Endpoint is persisted in the record. It never contains credentials.
type Endpoint struct {
	Type         EndpointType `json:"type"`
	K8sCluster   string       `json:"k8sCluster,omitempty"`
	Namespace    string       `json:"namespace,omitempty"`
	Name         string       `json:"name,omitempty"`
	Provider     string       `json:"provider,omitempty"`
	DatabaseType string       `json:"databaseType,omitempty"`
	// Display is a credential-free summary of an external endpoint.
	Display string `json:"display,omitempty"`
}

func (e Endpoint) instanceRef() everest.InstanceRef {
	return everest.InstanceRef{K8sCluster: e.K8sCluster, Namespace: e.Namespace, Name: e.Name}
}

// Record is the durable state of one migration (see docs/architecture.md §5).
type Record struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Engine             string          `json:"engine"`
	Source             Endpoint        `json:"source"`
	Target             Endpoint        `json:"target"`
	Options            json.RawMessage `json:"options"`
	WorkerPort         int32           `json:"workerPort"`
	CreatedAt          time.Time       `json:"createdAt"`
	StartedAt          *time.Time      `json:"startedAt,omitempty"`
	CutoverRequestedAt *time.Time      `json:"cutoverRequestedAt,omitempty"`
	// BackupStorage is the staging storage of engines that require one.
	BackupStorage *BackupStorageRef `json:"backupStorage,omitempty"`
	// ResourceVersion enables optimistic concurrency; it is not serialized.
	ResourceVersion string `json:"-"`
}

// BackupStorageRef names an OpenEverest BackupStorage. It lives in the
// target's Kubernetes cluster and namespace.
type BackupStorageRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Store persists records. Update must fail with KindConflict when the record's
// ResourceVersion is stale. Delete must remove everything the record owns.
type Store interface {
	Create(ctx context.Context, record Record) (Record, error)
	Get(ctx context.Context, id string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, record Record) (Record, error)
	Delete(ctx context.Context, id string) error
}

// Workloads runs engine workers owned by their record.
type Workloads interface {
	Apply(ctx context.Context, record Record, worker engine.WorkerSpec) error
	// Sync advances the worker rollout (bootstrap, then worker) and reports it.
	Sync(ctx context.Context, id string) (WorkerState, error)
	BaseURL(id string, port int32) string
	// TargetHasWorkers reports pods still running against the target, including
	// those of deleted migrations that are shutting down.
	TargetHasWorkers(ctx context.Context, target everest.InstanceRef) (bool, error)
}

// WorkerState describes the worker's rollout, independent of migration progress.
type WorkerState struct {
	Ready bool
	// Failed means the worker can never become ready (e.g. bootstrap failed).
	Failed  bool
	Message string
}

// Everest is the subset of the OpenEverest API the service uses.
type Everest interface {
	GetConnection(ctx context.Context, token string, ref everest.InstanceRef) (everest.ConnectionDetails, error)
	CheckAccess(ctx context.Context, token string, ref everest.InstanceRef) error
	GetBackupStorage(ctx context.Context, token string, ref everest.BackupStorageRef) (everest.BackupStorageSpec, error)
}

// Secrets reads Secrets with the plugin's own ServiceAccount. The service
// reads only Secrets that a resource the caller may read names.
type Secrets interface {
	ReadSecret(ctx context.Context, namespace, name string) (map[string][]byte, error)
}

// EndpointRequest is the API input for a source or target.
type EndpointRequest struct {
	Type       EndpointType `json:"type"`
	K8sCluster string       `json:"k8sCluster,omitempty"`
	Namespace  string       `json:"namespace,omitempty"`
	Name       string       `json:"name,omitempty"`
	URI        string       `json:"uri,omitempty"`
	CACert     string       `json:"caCert,omitempty"`
}

type CreateRequest struct {
	Name    string          `json:"name"`
	Engine  string          `json:"engine"`
	Source  EndpointRequest `json:"source"`
	Target  EndpointRequest `json:"target"`
	Options json.RawMessage `json:"options"`
	// BackupStorage names a BackupStorage in the target's namespace, for
	// engines that stage data in object storage.
	BackupStorage *BackupStorageRequest `json:"backupStorage,omitempty"`
}

type BackupStorageRequest struct {
	Name string `json:"name"`
}

// EndpointView is the API representation of an endpoint.
type EndpointView struct {
	Type       EndpointType `json:"type"`
	K8sCluster string       `json:"k8sCluster,omitempty"`
	Namespace  string       `json:"namespace,omitempty"`
	Name       string       `json:"name,omitempty"`
	Provider   string       `json:"provider,omitempty"`
	Display    string       `json:"display,omitempty"`
}

// View is the API representation of a migration (docs/architecture.md §8).
type View struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Engine         string            `json:"engine"`
	Source         EndpointView      `json:"source"`
	Target         EndpointView      `json:"target"`
	Options        json.RawMessage   `json:"options"`
	BackupStorage  *BackupStorageRef `json:"backupStorage,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	StartedAt      *time.Time        `json:"startedAt,omitempty"`
	Phase          engine.Phase      `json:"phase"`
	Message        string            `json:"message"`
	Error          string            `json:"error"`
	Progress       engine.Progress   `json:"progress"`
	ObservedAt     *time.Time        `json:"observedAt,omitempty"`
	AllowedActions []Action          `json:"allowedActions"`
}

func endpointView(endpoint Endpoint) EndpointView {
	return EndpointView{
		Type: endpoint.Type, K8sCluster: endpoint.K8sCluster, Namespace: endpoint.Namespace,
		Name: endpoint.Name, Provider: endpoint.Provider, Display: endpoint.Display,
	}
}

// observation is a worker status captured at a point in time.
type observation struct {
	status engine.Status
	at     time.Time
	// workerReady is false when no worker can act, so only delete is allowed.
	workerReady bool
	// canPause comes from the engine: some cannot continue a stopped run.
	canPause bool
}

func newView(record Record, observed observation) View {
	view := View{
		ID: record.ID, Name: record.Name, Engine: record.Engine,
		Source: endpointView(record.Source), Target: endpointView(record.Target),
		Options: record.Options, BackupStorage: record.BackupStorage, CreatedAt: record.CreatedAt, StartedAt: record.StartedAt,
		Phase: observed.status.Phase, Message: observed.status.Message, Error: observed.status.Error,
		Progress: observed.status.Progress, AllowedActions: allowedActions(observed),
	}
	if !observed.at.IsZero() {
		at := observed.at
		view.ObservedAt = &at
	}
	return view
}

// allowedActions encodes the lifecycle rules from docs/architecture.md §4.
func allowedActions(observed observation) []Action {
	if !observed.workerReady {
		return []Action{ActionDelete}
	}
	status := observed.status
	actions := make([]Action, 0, 3)
	switch status.Phase {
	case engine.PhaseCatchingUp, engine.PhaseReplicating:
		if observed.canPause {
			actions = append(actions, ActionPause)
		}
	case engine.PhasePaused, engine.PhaseFailed:
		actions = append(actions, ActionResume)
	}
	if status.Progress.ReadyForCutover {
		actions = append(actions, ActionCutover)
	}
	return append(actions, ActionDelete)
}
