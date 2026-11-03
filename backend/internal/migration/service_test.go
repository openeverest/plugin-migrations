package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
)

// --- fakes -----------------------------------------------------------------

type memoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	version int
}

func newMemoryStore() *memoryStore { return &memoryStore{records: map[string]Record{}} }

func (m *memoryStore) Create(_ context.Context, record Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.records[record.ID]; exists {
		return Record{}, newError(KindConflict, "exists")
	}
	m.version++
	record.ResourceVersion = fmt.Sprint(m.version)
	m.records[record.ID] = record
	return record, nil
}

func (m *memoryStore) Get(_ context.Context, id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return Record{}, newError(KindNotFound, "migration not found")
	}
	return record, nil
}

func (m *memoryStore) List(context.Context) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make([]Record, 0, len(m.records))
	for _, record := range m.records {
		records = append(records, record)
	}
	return records, nil
}

func (m *memoryStore) Update(_ context.Context, record Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records[record.ID].ResourceVersion != record.ResourceVersion {
		return Record{}, newError(KindConflict, "stale")
	}
	m.version++
	record.ResourceVersion = fmt.Sprint(m.version)
	m.records[record.ID] = record
	return record, nil
}

func (m *memoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, id)
	return nil
}

type fakeWorkloads struct {
	applied    map[string]engine.WorkerSpec
	state      WorkerState
	applyErr   error
	targetBusy bool
}

func (f *fakeWorkloads) Apply(_ context.Context, record Record, worker engine.WorkerSpec) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied[record.ID] = worker
	return nil
}
func (f *fakeWorkloads) Sync(context.Context, string) (WorkerState, error) { return f.state, nil }
func (f *fakeWorkloads) BaseURL(id string, port int32) string {
	return fmt.Sprintf("http://%s:%d", id, port)
}
func (f *fakeWorkloads) TargetHasWorkers(context.Context, everest.InstanceRef) (bool, error) {
	return f.targetBusy, nil
}

type fakeEverest struct {
	connections map[string]everest.ConnectionDetails
	// denied instances fail both access checks and connection reads.
	denied map[string]error
	calls  []string
}

func refKey(ref everest.InstanceRef) string { return ref.Namespace + "/" + ref.Name }

func (f *fakeEverest) GetConnection(_ context.Context, token string, ref everest.InstanceRef) (everest.ConnectionDetails, error) {
	f.calls = append(f.calls, "connection:"+refKey(ref))
	if token == "" {
		return everest.ConnectionDetails{}, everest.ErrUnauthorized
	}
	if err := f.denied[refKey(ref)]; err != nil {
		return everest.ConnectionDetails{}, err
	}
	details, ok := f.connections[refKey(ref)]
	if !ok {
		return everest.ConnectionDetails{}, everest.ErrNotFound
	}
	return details, nil
}

func (f *fakeEverest) CheckAccess(_ context.Context, _ string, ref everest.InstanceRef) error {
	f.calls = append(f.calls, "access:"+refKey(ref))
	if err := f.denied[refKey(ref)]; err != nil {
		return err
	}
	if _, ok := f.connections[refKey(ref)]; !ok {
		return everest.ErrNotFound
	}
	return nil
}

type fakeController struct {
	mu      sync.Mutex
	status  engine.Status
	err     error
	actions []string
}

func (c *fakeController) record(action string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actions = append(c.actions, action)
	return c.err
}
func (c *fakeController) Start(_ context.Context, options json.RawMessage) error {
	return c.record("start:" + string(options))
}
func (c *fakeController) Status(context.Context) (engine.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status, nil
}
func (c *fakeController) Pause(context.Context) error   { return c.record("pause") }
func (c *fakeController) Resume(context.Context) error  { return c.record("resume") }
func (c *fakeController) Cutover(context.Context) error { return c.record("cutover") }

type fakeEngine struct {
	controller *fakeController
	prepareErr error
}

func (e *fakeEngine) ID() string { return "fake" }
func (e *fakeEngine) Describe() engine.Descriptor {
	return engine.Descriptor{ID: "fake", SourceDatabaseTypes: []string{"mongodb"}, TargetDatabaseTypes: []string{"mongodb"}, TargetProviders: []string{"psmdb"}}
}
func (e *fakeEngine) Supports(source, target engine.EndpointInfo) bool {
	return source.DatabaseType == "mongodb" && target.DatabaseType == "mongodb"
}
func (e *fakeEngine) Prepare(input engine.PrepareInput) (engine.Prepared, error) {
	if e.prepareErr != nil {
		return engine.Prepared{}, e.prepareErr
	}
	return engine.Prepared{
		Worker:        engine.WorkerSpec{Port: 2242, SecretData: map[string][]byte{"source": []byte(input.SourceConn.URI)}},
		Snapshot:      json.RawMessage(`{"effective":true}`),
		SourceDisplay: "db1:27017",
	}, nil
}
func (e *fakeEngine) Controller(string) engine.Controller { return e.controller }

type harness struct {
	service    *Service
	store      *memoryStore
	workloads  *fakeWorkloads
	everest    *fakeEverest
	controller *fakeController
	engine     *fakeEngine
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	controller := &fakeController{status: engine.Status{Phase: engine.PhaseStarting, Idle: true}}
	fake := &fakeEngine{controller: controller}
	registry, err := engine.NewRegistry(engine.Registration{Engine: fake, Runner: engine.RunnerConfig{Image: "img:1", ImagePullPolicy: "IfNotPresent"}})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		store:      newMemoryStore(),
		workloads:  &fakeWorkloads{applied: map[string]engine.WorkerSpec{}, state: WorkerState{Ready: true}},
		everest:    &fakeEverest{connections: map[string]everest.ConnectionDetails{}, denied: map[string]error{}},
		controller: controller,
		engine:     fake,
	}
	h.everest.connections["shop/orders"] = everest.ConnectionDetails{Type: "mongodb", Provider: "psmdb", URI: "mongodb://admin:pw@orders/admin"}
	h.everest.connections["shop/legacy"] = everest.ConnectionDetails{Type: "mongodb", Provider: "psmdb", URI: "mongodb://admin:pw@legacy/admin"}
	h.service = NewService(registry, h.store, h.workloads, h.everest)
	ids := 0
	h.service.newID = func() string { ids++; return fmt.Sprintf("mig-%d", ids) }
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	h.service.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	return h
}

func externalRequest() CreateRequest {
	return CreateRequest{
		Name: "orders-into-everest", Engine: "fake",
		Source:  EndpointRequest{Type: EndpointExternal, URI: "mongodb://app:s3cret@db1:27017"},
		Target:  EndpointRequest{Type: EndpointInstance, K8sCluster: "main", Namespace: "shop", Name: "orders"},
		Options: json.RawMessage(`{}`),
	}
}

// --- tests -----------------------------------------------------------------

func TestCreateExternalSource(t *testing.T) {
	h := newHarness(t)
	view, err := h.service.Create(context.Background(), "jwt", externalRequest())
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != "mig-1" || view.Phase != engine.PhaseProvisioning || view.Source.Display != "db1:27017" || view.Target.Provider != "psmdb" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if string(view.Options) != `{"effective":true}` {
		t.Fatalf("view must expose the engine snapshot, got %s", view.Options)
	}
	record, _ := h.store.Get(context.Background(), "mig-1")
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), "s3cret") || strings.Contains(string(encoded), "pw@") {
		t.Fatalf("record persisted credentials: %s", encoded)
	}
	if string(h.workloads.applied["mig-1"].SecretData["source"]) != "mongodb://app:s3cret@db1:27017" {
		t.Fatal("worker did not receive the source connection")
	}
}

func TestCreateValidation(t *testing.T) {
	tests := map[string]func(*CreateRequest){
		"bad name":            func(r *CreateRequest) { r.Name = "Orders!" },
		"long name":           func(r *CreateRequest) { r.Name = strings.Repeat("a", 41) },
		"external target":     func(r *CreateRequest) { r.Target = EndpointRequest{Type: EndpointExternal, URI: "mongodb://x"} },
		"missing source uri":  func(r *CreateRequest) { r.Source.URI = " " },
		"unknown source type": func(r *CreateRequest) { r.Source.Type = "s3" },
		"same instance": func(r *CreateRequest) {
			r.Source = EndpointRequest{Type: EndpointInstance, K8sCluster: "main", Namespace: "shop", Name: "orders"}
		},
		"instance with uri": func(r *CreateRequest) { r.Target.URI = "mongodb://x" },
		"unknown engine":    func(r *CreateRequest) { r.Engine = "dms" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			request := externalRequest()
			mutate(&request)
			_, err := h.service.Create(context.Background(), "jwt", request)
			if KindOf(err) != KindInvalid {
				t.Fatalf("error = %v (kind %q)", err, KindOf(err))
			}
		})
	}
}

func TestCreateRequiresReadConnectionOnInstances(t *testing.T) {
	h := newHarness(t)
	h.everest.denied["shop/legacy"] = everest.ErrForbidden
	request := externalRequest()
	request.Source = EndpointRequest{Type: EndpointInstance, K8sCluster: "main", Namespace: "shop", Name: "legacy"}
	if _, err := h.service.Create(context.Background(), "jwt", request); !errors.Is(err, everest.ErrForbidden) {
		t.Fatalf("error = %v", err)
	}
	if records, _ := h.store.List(context.Background()); len(records) != 0 {
		t.Fatal("record created despite denied source")
	}
}

func TestCreateRejectsUnsupportedTargetProvider(t *testing.T) {
	h := newHarness(t)
	h.everest.connections["shop/orders"] = everest.ConnectionDetails{Type: "mongodb", Provider: "other-mongo", URI: "mongodb://o"}
	if _, err := h.service.Create(context.Background(), "jwt", externalRequest()); KindOf(err) != KindInvalid {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateRejectsTargetUntilPreviousMigrationIsGone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.service.Create(ctx, "jwt", externalRequest())
	if err != nil {
		t.Fatal(err)
	}
	second := externalRequest()
	second.Name = "second"
	if _, err := h.service.Create(ctx, "jwt", second); KindOf(err) != KindConflict {
		t.Fatalf("running migration: error = %v", err)
	}
	// A completed migration's worker keeps running until the record is deleted.
	h.controller.status = engine.Status{Phase: engine.PhaseCompleted}
	if _, err := h.service.Create(ctx, "jwt", second); KindOf(err) != KindConflict {
		t.Fatalf("completed migration: error = %v", err)
	}
	if err := h.service.Delete(ctx, "jwt", first.ID); err != nil {
		t.Fatal(err)
	}
	h.workloads.targetBusy = true
	if _, err := h.service.Create(ctx, "jwt", second); KindOf(err) != KindConflict {
		t.Fatalf("worker still shutting down: error = %v", err)
	}
	h.workloads.targetBusy = false
	if _, err := h.service.Create(ctx, "jwt", second); err != nil {
		t.Fatalf("deleted migration must free the target: %v", err)
	}
}

func TestCreateRollsBackRecordWhenProvisioningFails(t *testing.T) {
	h := newHarness(t)
	h.workloads.applyErr = errors.New("quota exceeded")
	_, err := h.service.Create(context.Background(), "jwt", externalRequest())
	if KindOf(err) != KindUnavailable || strings.Contains(err.Error(), "quota") {
		t.Fatalf("error = %v", err)
	}
	if records, _ := h.store.List(context.Background()); len(records) != 0 {
		t.Fatal("record not rolled back")
	}
}

func TestReconcileStartsIdleWorkerOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Create(ctx, "jwt", externalRequest()); err != nil {
		t.Fatal(err)
	}
	h.service.Reconcile(ctx)
	record, _ := h.store.Get(ctx, "mig-1")
	if record.StartedAt == nil {
		t.Fatal("startedAt not stamped")
	}
	if len(h.controller.actions) != 1 || h.controller.actions[0] != `start:{"effective":true}` {
		t.Fatalf("actions = %v", h.controller.actions)
	}
	h.service.Reconcile(ctx)
	if len(h.controller.actions) != 1 {
		t.Fatalf("started twice: %v", h.controller.actions)
	}
}

func TestReconcileWaitsForWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Create(ctx, "jwt", externalRequest()); err != nil {
		t.Fatal(err)
	}
	h.workloads.state = WorkerState{Message: "Preparing the target"}
	h.service.Reconcile(ctx)
	if len(h.controller.actions) != 0 {
		t.Fatal("started before the worker was available")
	}
	views, _ := h.service.List(ctx, "jwt")
	if views[0].Phase != engine.PhaseProvisioning || views[0].Message != "Preparing the target" {
		t.Fatalf("view = %+v", views[0])
	}
}

func TestFailedBootstrapOnlyAllowsDelete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Create(ctx, "jwt", externalRequest()); err != nil {
		t.Fatal(err)
	}
	h.workloads.state = WorkerState{Failed: true, Message: "Preparing the target failed"}
	view, err := h.service.Get(ctx, "jwt", "mig-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Phase != engine.PhaseFailed || view.Error != "Preparing the target failed" || len(view.AllowedActions) != 1 || view.AllowedActions[0] != ActionDelete {
		t.Fatalf("view = %+v", view)
	}
	if _, err := h.service.Act(ctx, "jwt", "mig-1", ActionResume); KindOf(err) != KindConflict {
		t.Fatalf("resume without a worker must conflict, got %v", err)
	}
}

func TestListFiltersInaccessibleMigrations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Create(ctx, "jwt", externalRequest()); err != nil {
		t.Fatal(err)
	}
	h.everest.denied["shop/orders"] = everest.ErrForbidden
	views, err := h.service.List(ctx, "jwt")
	if err != nil || len(views) != 0 {
		t.Fatalf("views = %v, err = %v", views, err)
	}
	if _, err := h.service.Get(ctx, "jwt", "mig-1"); KindOf(err) != KindNotFound {
		t.Fatalf("forbidden migration must look missing, got %v", err)
	}
}

func TestListUsesCacheAndSortsNewestFirst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first := externalRequest()
	if _, err := h.service.Create(ctx, "jwt", first); err != nil {
		t.Fatal(err)
	}
	h.controller.status = engine.Status{Phase: engine.PhaseCompleted}
	second := externalRequest()
	second.Name = "second"
	second.Target.Name = "legacy"
	if _, err := h.service.Create(ctx, "jwt", second); err != nil {
		t.Fatal(err)
	}
	views, err := h.service.List(ctx, "jwt")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Name != "second" {
		t.Fatalf("unexpected order: %+v", views)
	}
	accessChecks := 0
	for _, call := range h.everest.calls {
		if strings.HasPrefix(call, "access:") {
			accessChecks++
		}
	}
	if accessChecks != 2 {
		t.Fatalf("access checks = %d, want one per distinct instance", accessChecks)
	}
}

func TestActEnforcesAllowedActions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Create(ctx, "jwt", externalRequest()); err != nil {
		t.Fatal(err)
	}
	h.controller.status = engine.Status{Phase: engine.PhaseFullLoad}
	if _, err := h.service.Act(ctx, "jwt", "mig-1", ActionCutover); KindOf(err) != KindConflict {
		t.Fatalf("cutover during full load: %v", err)
	}
	if _, err := h.service.Act(ctx, "jwt", "mig-1", ActionPause); KindOf(err) != KindConflict {
		t.Fatalf("pause during full load: %v", err)
	}

	h.controller.status = engine.Status{Phase: engine.PhaseReplicating, Progress: engine.Progress{ReadyForCutover: true}}
	view, err := h.service.Act(ctx, "jwt", "mig-1", ActionCutover)
	if err != nil {
		t.Fatal(err)
	}
	if h.controller.actions[len(h.controller.actions)-1] != "cutover" {
		t.Fatalf("actions = %v", h.controller.actions)
	}
	record, _ := h.store.Get(ctx, "mig-1")
	if record.CutoverRequestedAt == nil || view.ID != "mig-1" {
		t.Fatal("cutover request not recorded")
	}
}

func TestActAndDeleteRequireReadConnection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Create(ctx, "jwt", externalRequest()); err != nil {
		t.Fatal(err)
	}
	h.everest.denied["shop/orders"] = everest.ErrForbidden
	if _, err := h.service.Act(ctx, "jwt", "mig-1", ActionPause); KindOf(err) != KindNotFound {
		t.Fatalf("act error = %v", err)
	}
	if err := h.service.Delete(ctx, "jwt", "mig-1"); KindOf(err) != KindNotFound {
		t.Fatalf("delete error = %v", err)
	}
	delete(h.everest.denied, "shop/orders")
	if err := h.service.Delete(ctx, "jwt", "mig-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Get(ctx, "mig-1"); KindOf(err) != KindNotFound {
		t.Fatal("record not deleted")
	}
}

func TestAllowedActions(t *testing.T) {
	tests := []struct {
		status engine.Status
		want   string
	}{
		{engine.Status{Phase: engine.PhaseProvisioning}, "delete"},
		{engine.Status{Phase: engine.PhaseFullLoad}, "delete"},
		{engine.Status{Phase: engine.PhaseCatchingUp}, "pause,delete"},
		{engine.Status{Phase: engine.PhaseReplicating, Progress: engine.Progress{ReadyForCutover: true}}, "pause,cutover,delete"},
		{engine.Status{Phase: engine.PhasePaused, Progress: engine.Progress{ReadyForCutover: true}}, "resume,cutover,delete"},
		{engine.Status{Phase: engine.PhaseFailed}, "resume,delete"},
		{engine.Status{Phase: engine.PhaseCompleted}, "delete"},
	}
	for _, test := range tests {
		got := make([]string, 0)
		for _, action := range allowedActions(observation{status: test.status, workerReady: true}) {
			got = append(got, string(action))
		}
		if strings.Join(got, ",") != test.want {
			t.Errorf("%s: got %v, want %s", test.status.Phase, got, test.want)
		}
	}
}
