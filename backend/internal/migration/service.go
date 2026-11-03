package migration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
)

const (
	maxNameLength   = 40
	maxURILength    = 4096
	maxCACertLength = 64 << 10
	idAlphabet      = "abcdefghijklmnopqrstuvwxyz0123456789"
	idRandomLength  = 8
	// Bounds a single worker call so one stuck worker cannot stall a request.
	workerCallTimeout = 5 * time.Second
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type Service struct {
	registry  *engine.Registry
	store     Store
	workloads Workloads
	everest   Everest
	now       func() time.Time
	newID     func() string

	// createMu serializes creation so the one-migration-per-target check holds.
	createMu sync.Mutex
	cacheMu  sync.RWMutex
	cache    map[string]observation
}

func NewService(registry *engine.Registry, store Store, workloads Workloads, everestAPI Everest) *Service {
	return &Service{
		registry: registry, store: store, workloads: workloads, everest: everestAPI,
		now: time.Now, newID: randomID, cache: map[string]observation{},
	}
}

func randomID() string {
	random := make([]byte, idRandomLength)
	_, _ = rand.Read(random)
	id := make([]byte, idRandomLength)
	for i, b := range random {
		id[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return "mig-" + string(id)
}

func (s *Service) Engines() []engine.Descriptor {
	return s.registry.Descriptors()
}

// Create validates the request, resolves both endpoints with the caller's
// permissions, persists the record and provisions its worker.
func (s *Service) Create(ctx context.Context, token string, request CreateRequest) (View, error) {
	if err := validateCreateRequest(request); err != nil {
		return View{}, err
	}
	registration, ok := s.registry.Lookup(request.Engine)
	if !ok {
		return View{}, newError(KindInvalid, "engine is not available")
	}

	target, targetConn, err := s.resolveInstance(ctx, token, request.Target)
	if err != nil {
		return View{}, err
	}
	var (
		source     Endpoint
		sourceConn engine.Connection
	)
	if request.Source.Type == EndpointInstance {
		source, sourceConn, err = s.resolveInstance(ctx, token, request.Source)
		if err != nil {
			return View{}, err
		}
	} else {
		sourceTypes := registration.Engine.Describe().SourceDatabaseTypes
		if len(sourceTypes) != 1 {
			return View{}, newError(KindInvalid, "engine cannot infer the external source database type")
		}
		source = Endpoint{Type: EndpointExternal, DatabaseType: sourceTypes[0]}
		sourceConn = engine.Connection{URI: strings.TrimSpace(request.Source.URI), CACert: request.Source.CACert}
	}

	sourceInfo := engine.EndpointInfo{DatabaseType: source.DatabaseType, Provider: source.Provider}
	targetInfo := engine.EndpointInfo{DatabaseType: target.DatabaseType, Provider: target.Provider}
	if _, err := s.registry.Resolve(request.Engine, sourceInfo, targetInfo); err != nil {
		return View{}, err
	}
	if !slices.Contains(registration.Engine.Describe().TargetProviders, target.Provider) {
		return View{}, newError(KindInvalid, "engine does not support the target instance's provider")
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()
	if err := s.ensureTargetIsFree(ctx, target); err != nil {
		return View{}, err
	}

	id := s.newID()
	prepared, err := registration.Engine.Prepare(engine.PrepareInput{
		MigrationID: id, Source: sourceInfo, Target: targetInfo,
		SourceConn: sourceConn, TargetConn: targetConn, Options: request.Options, Runner: registration.Runner,
	})
	if err != nil {
		return View{}, err
	}
	if source.Type == EndpointExternal {
		source.Display = prepared.SourceDisplay
	}

	record, err := s.store.Create(ctx, Record{
		ID: id, Name: request.Name, Engine: request.Engine, Source: source, Target: target,
		Options: prepared.Snapshot, WorkerPort: prepared.Worker.Port, CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return View{}, err
	}
	if err := s.workloads.Apply(ctx, record, prepared.Worker); err != nil {
		log.Printf("migration %s: provision worker: %v", id, err)
		if deleteErr := s.store.Delete(context.WithoutCancel(ctx), id); deleteErr != nil {
			log.Printf("migration %s: roll back record: %v", id, deleteErr)
		}
		return View{}, newError(KindUnavailable, "failed to provision the migration worker")
	}
	initial := observation{status: engine.Status{Phase: engine.PhaseProvisioning}, at: s.now().UTC()}
	s.storeObservation(id, initial)
	return newView(record, initial), nil
}

func validateCreateRequest(request CreateRequest) error {
	if !namePattern.MatchString(request.Name) || len(request.Name) > maxNameLength {
		return newError(KindInvalid, fmt.Sprintf("name must be 1-%d lowercase letters, digits or '-', starting and ending with a letter or digit", maxNameLength))
	}
	if strings.TrimSpace(request.Engine) == "" {
		return newError(KindInvalid, "engine is required")
	}
	if request.Target.Type != EndpointInstance {
		return newError(KindInvalid, "target must be an OpenEverest instance")
	}
	if err := validateInstanceRequest("target", request.Target); err != nil {
		return err
	}
	switch request.Source.Type {
	case EndpointInstance:
		if err := validateInstanceRequest("source", request.Source); err != nil {
			return err
		}
		if request.Source.K8sCluster == request.Target.K8sCluster && request.Source.Namespace == request.Target.Namespace &&
			request.Source.Name == request.Target.Name {
			return newError(KindInvalid, "source and target must be different instances")
		}
	case EndpointExternal:
		if strings.TrimSpace(request.Source.URI) == "" || len(request.Source.URI) > maxURILength {
			return newError(KindInvalid, "source uri is required and must be at most 4096 characters")
		}
		if len(request.Source.CACert) > maxCACertLength {
			return newError(KindInvalid, "source caCert must be at most 64 KiB")
		}
	default:
		return newError(KindInvalid, "source type must be instance or external")
	}
	return nil
}

func validateInstanceRequest(role string, request EndpointRequest) error {
	if strings.TrimSpace(request.K8sCluster) == "" || strings.TrimSpace(request.Namespace) == "" || strings.TrimSpace(request.Name) == "" {
		return newError(KindInvalid, role+" instance requires k8sCluster, namespace and name")
	}
	if request.URI != "" || request.CACert != "" {
		return newError(KindInvalid, role+" instance must not include uri or caCert")
	}
	return nil
}

// resolveInstance reads connection details with the caller's JWT, which also
// proves the caller holds read-connection on the instance.
func (s *Service) resolveInstance(ctx context.Context, token string, request EndpointRequest) (Endpoint, engine.Connection, error) {
	ref := everest.InstanceRef{K8sCluster: request.K8sCluster, Namespace: request.Namespace, Name: request.Name}
	details, err := s.everest.GetConnection(ctx, token, ref)
	if err != nil {
		return Endpoint{}, engine.Connection{}, err
	}
	if details.Type == "" || details.URI == "" {
		return Endpoint{}, engine.Connection{}, newError(KindInvalid, fmt.Sprintf("instance %s/%s has no usable connection details yet", ref.Namespace, ref.Name))
	}
	endpoint := Endpoint{
		Type: EndpointInstance, K8sCluster: ref.K8sCluster, Namespace: ref.Namespace, Name: ref.Name,
		Provider: details.Provider, DatabaseType: details.Type,
	}
	connection := engine.Connection{URI: details.URI, Username: details.Username, Password: details.Password, CACert: details.CACert}
	return endpoint, connection, nil
}

// ensureTargetIsFree allows one migration per target, completed or not. PCSM
// keeps a single lease and checkpoint per target, and a still-running worker
// of an earlier migration would undo the new migration's state reset.
func (s *Service) ensureTargetIsFree(ctx context.Context, target Endpoint) error {
	records, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Target.instanceRef() == target.instanceRef() {
			return newError(KindConflict, fmt.Sprintf("target instance already has a migration (%s); delete it before starting a new one", record.Name))
		}
	}
	busy, err := s.workloads.TargetHasWorkers(ctx, target.instanceRef())
	if err != nil {
		return err
	}
	if busy {
		return newError(KindConflict, "a previous migration to this target is still shutting down; try again in a few seconds")
	}
	return nil
}

// List returns the migrations the caller can see, newest first, using cached
// observations so listing never fans out to every worker.
func (s *Service) List(ctx context.Context, token string) ([]View, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	access := map[everest.InstanceRef]error{}
	canRead := func(ref everest.InstanceRef) error {
		if err, checked := access[ref]; checked {
			return err
		}
		err := s.everest.CheckAccess(ctx, token, ref)
		access[ref] = err
		return err
	}

	views := make([]View, 0, len(records))
	for _, record := range records {
		if err := s.authorizeView(record, canRead); err != nil {
			if errors.Is(err, everest.ErrForbidden) || errors.Is(err, everest.ErrNotFound) {
				continue
			}
			return nil, err
		}
		views = append(views, newView(record, s.cachedObservation(record)))
	}
	slices.SortFunc(views, func(a, b View) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return views, nil
}

func (s *Service) authorizeView(record Record, canRead func(everest.InstanceRef) error) error {
	if err := canRead(record.Target.instanceRef()); err != nil {
		return err
	}
	if record.Source.Type == EndpointInstance {
		return canRead(record.Source.instanceRef())
	}
	return nil
}

// authorizeMutation requires read-connection on every managed endpoint.
func (s *Service) authorizeMutation(ctx context.Context, token string, record Record) error {
	if _, err := s.everest.GetConnection(ctx, token, record.Target.instanceRef()); err != nil {
		return err
	}
	if record.Source.Type == EndpointInstance {
		if _, err := s.everest.GetConnection(ctx, token, record.Source.instanceRef()); err != nil {
			return err
		}
	}
	return nil
}

// Get returns one migration with a live status.
func (s *Service) Get(ctx context.Context, token, id string) (View, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if err := s.authorizeView(record, func(ref everest.InstanceRef) error { return s.everest.CheckAccess(ctx, token, ref) }); err != nil {
		return View{}, hideForbidden(err)
	}
	observed := s.observe(ctx, record)
	s.storeObservation(id, observed)
	return newView(record, observed), nil
}

// hideForbidden reports inaccessible migrations as missing so IDs do not leak.
func hideForbidden(err error) error {
	if errors.Is(err, everest.ErrForbidden) || errors.Is(err, everest.ErrNotFound) {
		return newError(KindNotFound, "migration not found")
	}
	return err
}

// Act performs pause, resume or cutover after checking the live phase allows it.
func (s *Service) Act(ctx context.Context, token, id string, action Action) (View, error) {
	if action == ActionDelete || !slices.Contains([]Action{ActionPause, ActionResume, ActionCutover}, action) {
		return View{}, newError(KindInvalid, "unsupported action")
	}
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if err := s.authorizeMutation(ctx, token, record); err != nil {
		return View{}, hideForbidden(err)
	}
	registration, ok := s.registry.Lookup(record.Engine)
	if !ok {
		return View{}, newError(KindUnavailable, "the migration's engine is no longer enabled")
	}

	current := s.observe(ctx, record)
	if !slices.Contains(allowedActions(current), action) {
		return View{}, newError(KindConflict, fmt.Sprintf("cannot %s a migration in phase %s", action, current.status.Phase))
	}
	controller := registration.Engine.Controller(s.workloads.BaseURL(record.ID, record.WorkerPort))
	callCtx, cancel := context.WithTimeout(ctx, workerCallTimeout)
	defer cancel()
	switch action {
	case ActionPause:
		err = controller.Pause(callCtx)
	case ActionResume:
		err = controller.Resume(callCtx)
	case ActionCutover:
		err = controller.Cutover(callCtx)
	}
	if err != nil {
		return View{}, err
	}
	if action == ActionCutover {
		now := s.now().UTC()
		record.CutoverRequestedAt = &now
		if updated, err := s.store.Update(ctx, record); err == nil {
			record = updated
		} else {
			log.Printf("migration %s: record cutover request: %v", id, err)
		}
	}
	observed := s.observe(ctx, record)
	s.storeObservation(id, observed)
	return newView(record, observed), nil
}

// Delete removes the record and, through ownership, its worker. Target data stays.
func (s *Service) Delete(ctx context.Context, token, id string) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authorizeMutation(ctx, token, record); err != nil {
		return hideForbidden(err)
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.cacheMu.Lock()
	delete(s.cache, id)
	s.cacheMu.Unlock()
	return nil
}

// observe reads the worker's live status, mapping unavailability onto phases.
func (s *Service) observe(ctx context.Context, record Record) observation {
	now := s.now().UTC()
	registration, ok := s.registry.Lookup(record.Engine)
	if !ok {
		return observation{status: engine.Status{Phase: engine.PhaseUnknown, Error: "the migration's engine is no longer enabled"}, at: now}
	}
	callCtx, cancel := context.WithTimeout(ctx, workerCallTimeout)
	defer cancel()

	notReadyPhase := engine.PhaseUnknown
	if record.StartedAt == nil {
		notReadyPhase = engine.PhaseProvisioning
	}
	worker, err := s.workloads.Sync(callCtx, record.ID)
	switch {
	case err != nil:
		log.Printf("migration %s: sync worker: %v", record.ID, err)
		return observation{status: engine.Status{Phase: notReadyPhase, Error: "The migration worker state could not be read"}, at: now}
	case worker.Failed:
		return observation{status: engine.Status{Phase: engine.PhaseFailed, Error: worker.Message}, at: now}
	case !worker.Ready:
		return observation{status: engine.Status{Phase: notReadyPhase, Message: worker.Message}, at: now}
	}
	status, err := registration.Engine.Controller(s.workloads.BaseURL(record.ID, record.WorkerPort)).Status(callCtx)
	if err != nil {
		return observation{status: engine.Status{Phase: notReadyPhase, Error: err.Error()}, at: now}
	}
	return observation{status: status, at: now, workerReady: true}
}

func (s *Service) storeObservation(id string, observed observation) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache[id] = observed
}

func (s *Service) cachedObservation(record Record) observation {
	s.cacheMu.RLock()
	observed, ok := s.cache[record.ID]
	s.cacheMu.RUnlock()
	if ok {
		return observed
	}
	if record.StartedAt == nil {
		return observation{status: engine.Status{Phase: engine.PhaseProvisioning}}
	}
	return observation{status: engine.Status{Phase: engine.PhaseUnknown}}
}
