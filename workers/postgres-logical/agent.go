package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Agent states reported by GET /status.
const (
	StateIdle       = "idle"
	StateRunning    = "running"
	StatePaused     = "paused"
	StateFinalizing = "finalizing"
	StateCompleted  = "completed"
	StateFailed     = "failed"
	StateStopped    = "stopped"
)

const (
	sqlTimeout = 30 * time.Second
	// caughtUpLag marks the initial sync complete: the target applies changes
	// close enough to real time for a cutover to be quick.
	caughtUpLag         = 15 * time.Second
	cutoverPollInterval = time.Second
	minSourceVersion    = 130000 // publish_via_partition_root, pg_partition_tree
	minTargetVersion    = 150000 // pg_stat_subscription_stats
	heartbeatTable      = migrationSchema + ".heartbeat"
	maxListedTables     = 10
	// recentErrorWindow is how long a new replication error is reported while
	// PostgreSQL keeps retrying.
	recentErrorWindow = 2 * time.Minute
)

var (
	errConflict    = errors.New("conflict")
	errInvalidBody = errors.New("invalid request")
)

// Config comes from the worker's environment; URIs carry credentials.
type Config struct {
	SourceURI string
	// SubscriptionConninfo is what the target server uses to reach the source.
	// It can differ from SourceURI, e.g. without a CA file path that exists
	// only in the worker.
	SubscriptionConninfo string
	TargetURI            string
	// TargetAdminURI connects to a maintenance database on the target server.
	TargetAdminURI    string
	TargetDatabase    string
	Name              string
	WorkDir           string
	HeartbeatInterval time.Duration
}

// Status is the GET /status response body.
type Status struct {
	State                string `json:"state"`
	Message              string `json:"message,omitempty"`
	Error                string `json:"error,omitempty"`
	CopyDone             bool   `json:"copyDone"`
	InitialSyncCompleted bool   `json:"initialSyncCompleted"`
	BytesTotal           uint64 `json:"bytesTotal"`
	BytesCopied          uint64 `json:"bytesCopied"`
	LagSeconds           int64  `json:"lagSeconds"`
}

type stage int

const (
	stageIdle stage = iota
	stagePreparing
	stageReplicating
	stageCutover
	stageCompleted
	stageStopped
)

type Agent struct {
	config Config

	mu          sync.Mutex
	stage       stage
	step        string
	failure     string
	cancel      context.CancelFunc
	copyDone    bool
	initialSync bool
	// sizes are source table sizes read once, so the total stays stable.
	sizes map[string]uint64
	// errorCount and errorsChangedAt detect new replication errors.
	errorCount      int64
	errorsChangedAt time.Time
}

func NewAgent(config Config) *Agent { return &Agent{config: config} }

func (a *Agent) vars(extra map[string]string) map[string]string {
	vars := map[string]string{"name": a.config.Name, "migration_schema": migrationSchema}
	for key, value := range extra {
		vars[key] = value
	}
	return vars
}

// Recover restores the stage from the source and target after a worker
// restart: the subscription and heartbeat table hold all durable state.
func (a *Agent) Recover(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	exists, err := psql(ctx, a.config.TargetAdminURI, databaseExistsSQL, map[string]string{"db": a.config.TargetDatabase})
	if err != nil {
		log.Printf("recover: check the target database: %v", err)
		return
	}
	if exists != "t" {
		return
	}
	var target struct {
		SubExists bool `json:"subExists"`
		Heartbeat bool `json:"heartbeat"`
	}
	if err := psqlJSON(ctx, a.config.TargetURI, recoverTargetSQL, a.vars(nil), &target); err != nil {
		log.Printf("recover: read the target: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case target.SubExists:
		a.stage = stageReplicating
		log.Printf("recovered a running migration")
	case target.Heartbeat:
		var row struct {
			CompletedAt *time.Time `json:"completedAt"`
		}
		if err := psqlJSON(ctx, a.config.TargetURI, completedAtSQL, a.vars(nil), &row); err != nil {
			log.Printf("recover: read the completion marker: %v", err)
			return
		}
		if row.CompletedAt != nil {
			a.stage, a.copyDone, a.initialSync = stageCompleted, true, true
			return
		}
		a.stage = stagePreparing
		a.failure = "The migration worker restarted before the migration was set up or finished. Delete this migration and create it again."
	}
}

// Start prepares the source and target in the background and creates the
// subscription. Calling it again after the first call does nothing.
func (a *Agent) Start(options Options) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageIdle {
		return nil // the reconciler retries Start after a lost response
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.stage, a.cancel = stagePreparing, cancel
	go func() {
		err := a.prepare(ctx, options)
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Printf("start: %v", err)
			a.failure = err.Error()
		default:
			a.stage, a.step = stageReplicating, ""
		}
	}()
	return nil
}

func (a *Agent) setStep(step string) {
	a.mu.Lock()
	a.step = step
	a.mu.Unlock()
	log.Print(step)
}

func (a *Agent) prepare(ctx context.Context, options Options) error {
	a.setStep("Checking the source and target")
	if err := a.preflight(ctx, options); err != nil {
		return err
	}

	a.setStep("Creating the target database")
	sqlCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	dbVars := map[string]string{"db": a.config.TargetDatabase}
	exists, err := psql(sqlCtx, a.config.TargetAdminURI, databaseExistsSQL, dbVars)
	if err != nil {
		return fmt.Errorf("check the target database: %w", err)
	}
	if exists == "t" && options.RecreateDatabase {
		if _, err := psql(sqlCtx, a.config.TargetAdminURI, dropDatabaseSQL, dbVars); err != nil {
			return fmt.Errorf("drop the target database: %w", err)
		}
		exists = "f"
	}
	if exists != "t" {
		if _, err := psql(sqlCtx, a.config.TargetAdminURI, createDatabaseSQL, dbVars); err != nil {
			return fmt.Errorf("create the target database: %w", err)
		}
	}
	for _, uri := range []string{a.config.TargetURI, a.config.SourceURI} {
		if _, err := psql(sqlCtx, uri, heartbeatTableSQL, a.vars(nil)); err != nil {
			return fmt.Errorf("create the heartbeat table: %w", err)
		}
	}
	if _, err := psql(sqlCtx, a.config.SourceURI, sourceHeartbeatRowSQL, a.vars(nil)); err != nil {
		return fmt.Errorf("write the source heartbeat: %w", err)
	}

	a.setStep("Copying the schema")
	if err := os.MkdirAll(a.config.WorkDir, 0o750); err != nil {
		return fmt.Errorf("create the work directory: %w", err)
	}
	dump := filepath.Join(a.config.WorkDir, "schema.dump")
	dumpArgs := append([]string{"--schema-only", "--format=custom", "--no-publications", "--no-subscriptions", "--file=" + dump},
		options.dumpFilterArgs()...)
	if err := runTool(ctx, "pg_dump", append(dumpArgs, "--dbname="+a.config.SourceURI)...); err != nil {
		return err
	}
	restoreArgs := []string{"--no-owner", "--no-acl", "--no-tablespaces", "--exit-on-error", "--dbname=" + a.config.TargetURI, dump}
	if err := runTool(ctx, "pg_restore", restoreArgs...); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("%w. The target database already has objects with these names; recreate the target database or clean it", err)
		}
		return err
	}

	a.setStep("Creating the publication and subscription")
	sqlCtx, cancel = context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()
	if _, err := psql(sqlCtx, a.config.SourceURI, createPublicationSQL, a.vars(options.filterVars())); err != nil {
		return fmt.Errorf("create the publication on the source: %w", err)
	}
	if _, err := psql(sqlCtx, a.config.TargetURI, createSubscriptionSQL, a.vars(map[string]string{"conninfo": a.config.SubscriptionConninfo})); err != nil {
		return fmt.Errorf("create the subscription on the target (the target server must reach the source): %w", err)
	}
	return nil
}

type sourcePreflight struct {
	Version      int      `json:"version"`
	WalLevel     string   `json:"walLevel"`
	CanReplicate bool     `json:"canReplicate"`
	Tables       int      `json:"tables"`
	Unidentified []string `json:"unidentified"`
}

type targetPreflight struct {
	Version   int  `json:"version"`
	Superuser bool `json:"superuser"`
}

func (a *Agent) preflight(ctx context.Context, options Options) error {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	var source sourcePreflight
	if err := psqlJSON(ctx, a.config.SourceURI, sourcePreflightSQL, a.vars(options.filterVars()), &source); err != nil {
		return fmt.Errorf("connect to the source: %w", err)
	}
	var target targetPreflight
	if err := psqlJSON(ctx, a.config.TargetAdminURI, targetPreflightSQL, nil, &target); err != nil {
		return fmt.Errorf("connect to the target: %w", err)
	}
	return checkPreflight(source, target)
}

// checkPreflight lists every problem at once so they can be fixed together.
func checkPreflight(source sourcePreflight, target targetPreflight) error {
	var problems []string
	if source.Version < minSourceVersion {
		problems = append(problems, fmt.Sprintf("the source runs PostgreSQL %d; version 13 or later is required", source.Version/10000))
	}
	if source.WalLevel != "logical" {
		problems = append(problems, fmt.Sprintf("the source has wal_level=%s; set wal_level=logical and restart it", source.WalLevel))
	}
	if !source.CanReplicate {
		problems = append(problems, "the source user needs the REPLICATION attribute (or superuser)")
	}
	if source.Tables == 0 {
		problems = append(problems, "no tables to migrate on the source with the selected filters")
	}
	if len(source.Unidentified) > 0 {
		names := source.Unidentified
		more := ""
		if len(names) > maxListedTables {
			more = fmt.Sprintf(" and %d more", len(names)-maxListedTables)
			names = names[:maxListedTables]
		}
		problems = append(problems, fmt.Sprintf(
			"tables without a primary key or replica identity cannot replicate updates and deletes (once published, those would fail on the source): %s%s. "+
				"Add a primary key, run ALTER TABLE ... REPLICA IDENTITY FULL, or exclude them", strings.Join(names, ", "), more))
	}
	if target.Version < minTargetVersion {
		problems = append(problems, fmt.Sprintf("the target runs PostgreSQL %d; version 15 or later is required", target.Version/10000))
	} else if target.Version/10000 < source.Version/10000 {
		problems = append(problems, fmt.Sprintf("the target runs PostgreSQL %d, older than the source (%d); migrate to the same or a newer major version",
			target.Version/10000, source.Version/10000))
	}
	if !target.Superuser {
		problems = append(problems, "the target connection must be a superuser to create the subscription")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("Cannot start the migration: " + strings.Join(problems, "; ") + ".")
}

func (a *Agent) Pause(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageReplicating || !a.copyDone || a.failure != "" {
		return fmt.Errorf("%w: changes can be paused only while they are replicated", errConflict)
	}
	_, err := psql(ctx, a.config.TargetURI, pauseSQL, a.vars(nil))
	return err
}

func (a *Agent) Resume(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageReplicating || a.failure != "" {
		return fmt.Errorf("%w: only a paused or failed subscription can be resumed; delete this migration and create it again", errConflict)
	}
	_, err := psql(ctx, a.config.TargetURI, resumeSQL, a.vars(nil))
	return err
}

// Cutover waits, in the background, until the target applied every change
// committed on the source before the request, then finalizes the target.
func (a *Agent) Cutover(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage == stageCutover {
		return nil
	}
	if a.stage != stageReplicating || !a.initialSync || a.failure != "" {
		return fmt.Errorf("%w: cutover is possible once the target has caught up", errConflict)
	}
	if _, err := psql(ctx, a.config.TargetURI, enableSQL, a.vars(nil)); err != nil {
		return err
	}
	cutoverCtx, cancel := context.WithCancel(context.Background())
	a.stage, a.cancel = stageCutover, cancel
	go func() {
		err := a.cutover(cutoverCtx)
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case cutoverCtx.Err() != nil:
		case err != nil:
			log.Printf("cutover: %v", err)
			a.failure = "Cutover failed: " + err.Error()
		default:
			a.stage = stageCompleted
			log.Print("migration completed")
		}
	}()
	return nil
}

type cutoverSource struct {
	Sequences []map[string]any `json:"sequences"`
	Schemas   []string         `json:"schemas"`
}

func (a *Agent) cutover(ctx context.Context) error {
	token := strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := psqlCtx(ctx, a.config.SourceURI, cutoverTokenSQL, a.vars(map[string]string{"token": token})); err != nil {
		return fmt.Errorf("write the cutover marker on the source: %w", err)
	}
	want, _ := strconv.ParseInt(token, 10, 64)
	for {
		var progress struct {
			Token int64 `json:"token"`
		}
		if err := psqlJSONCtx(ctx, a.config.TargetURI, targetTokenSQL, a.vars(nil), &progress); err != nil {
			log.Printf("cutover: read the target: %v", err)
		} else if progress.Token >= want {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cutoverPollInterval):
		}
	}

	var source cutoverSource
	if err := psqlJSONCtx(ctx, a.config.SourceURI, cutoverSourceSQL, a.vars(nil), &source); err != nil {
		return fmt.Errorf("read sequences on the source: %w", err)
	}
	if err := a.dropSubscription(ctx); err != nil {
		return err
	}
	sequences, schemas := encodeJSON(source.Sequences), encodeJSON(source.Schemas)
	if _, err := psqlCtx(ctx, a.config.TargetURI, syncSequencesSQL, a.vars(map[string]string{"sequences": sequences})); err != nil {
		return fmt.Errorf("set sequences on the target: %w", err)
	}
	if _, err := psqlCtx(ctx, a.config.TargetURI, refreshMatviewsSQL, a.vars(map[string]string{"schemas": schemas})); err != nil {
		return fmt.Errorf("refresh materialized views on the target: %w", err)
	}
	if err := a.cleanupSource(ctx); err != nil {
		return err
	}
	if _, err := psqlCtx(ctx, a.config.TargetURI, markCompletedSQL, a.vars(nil)); err != nil {
		return fmt.Errorf("record the completion on the target: %w", err)
	}
	return nil
}

// dropSubscription removes the subscription and its slot on the source. If
// the target cannot reach the source, it detaches the subscription and the
// worker drops the slot itself.
func (a *Agent) dropSubscription(ctx context.Context) error {
	if _, err := psqlCtx(ctx, a.config.TargetURI, dropSubscriptionSQL, a.vars(nil)); err == nil {
		return nil
	} else {
		log.Printf("drop the subscription: %v; detaching it instead", err)
	}
	if _, err := psqlCtx(ctx, a.config.TargetURI, detachSubscriptionSQL, a.vars(nil)); err != nil {
		return fmt.Errorf("remove the subscription on the target: %w", err)
	}
	return nil
}

// cleanupSource drops the publication, the replication slot and the
// heartbeat row. A slot left behind retains WAL on the source indefinitely.
func (a *Agent) cleanupSource(ctx context.Context) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if _, err = psqlCtx(ctx, a.config.SourceURI, terminateSlotSQL, a.vars(nil)); err == nil {
			if _, err = psqlCtx(ctx, a.config.SourceURI, dropSlotSQL, a.vars(nil)); err == nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		return fmt.Errorf("drop the replication slot and publication on the source: %w", err)
	}
	present, err := psqlCtx(ctx, a.config.SourceURI, heartbeatExistsSQL, a.vars(nil))
	if err != nil {
		return fmt.Errorf("check the source heartbeat table: %w", err)
	}
	if present == "t" {
		if _, err := psqlCtx(ctx, a.config.SourceURI, dropSourceHeartbeatSQL, a.vars(nil)); err != nil {
			return fmt.Errorf("remove the source heartbeat: %w", err)
		}
	}
	return nil
}

// Teardown stops background work and removes everything the migration
// created on the source and target except the migrated data. Idempotent.
func (a *Agent) Teardown(ctx context.Context) error {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.stage = stageStopped
	a.mu.Unlock()

	exists, err := psqlCtx(ctx, a.config.TargetAdminURI, databaseExistsSQL, map[string]string{"db": a.config.TargetDatabase})
	if err != nil {
		return fmt.Errorf("check the target database: %w", err)
	}
	if exists == "t" {
		if _, err := psqlCtx(ctx, a.config.TargetURI, detachSubscriptionSQL, a.vars(nil)); err != nil {
			return fmt.Errorf("remove the subscription on the target: %w", err)
		}
		if _, err := psqlCtx(ctx, a.config.TargetURI, dropTargetMigrationSchemaSQL, a.vars(nil)); err != nil {
			return fmt.Errorf("remove the heartbeat table on the target: %w", err)
		}
	}
	return a.cleanupSource(ctx)
}

// RunHeartbeats updates the source heartbeat while changes replicate. Its
// replicated copy on the target tells how far behind the target is.
func (a *Agent) RunHeartbeats(ctx context.Context) {
	ticker := time.NewTicker(a.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			active := a.stage == stageReplicating || a.stage == stageCutover
			a.mu.Unlock()
			if !active {
				continue
			}
			beatCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
			if _, err := psql(beatCtx, a.config.SourceURI, beatSQL, a.vars(nil)); err != nil {
				log.Printf("heartbeat: %v", err)
			}
			cancel()
		}
	}
}

// snapshot is the replication state read from the target and the source.
type snapshot struct {
	SubExists   bool         `json:"subExists"`
	SubEnabled  bool         `json:"subEnabled"`
	Errors      int64        `json:"errors"`
	Tables      []tableState `json:"tables"`
	BeatAt      *time.Time   `json:"beatAt"`
	CompletedAt *time.Time   `json:"completedAt"`
	SourceNow   time.Time    `json:"-"`
	Sizes       map[string]uint64
}

type tableState struct {
	Table string `json:"table"`
	State string `json:"state"`
}

func (a *Agent) Status(ctx context.Context) Status {
	a.mu.Lock()
	current, step, failure := a.stage, a.step, a.failure
	sizes, copyDone, initialSync := a.sizes, a.copyDone, a.initialSync
	a.mu.Unlock()

	switch {
	case current == stageIdle:
		return Status{State: StateIdle}
	case current == stageStopped:
		return Status{State: StateStopped, Message: "Stopped"}
	case current == stageCompleted:
		total := sumSizes(sizes, nil)
		return Status{State: StateCompleted, Message: "Migration completed", CopyDone: true, InitialSyncCompleted: true, BytesTotal: total, BytesCopied: total}
	case current == stagePreparing && failure != "":
		return Status{State: StateFailed, Error: failure}
	case current == stagePreparing:
		return Status{State: StateRunning, Message: step}
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var snap snapshot
	if err := psqlJSON(ctx, a.config.TargetURI, targetStatusSQL, a.vars(nil), &snap); err != nil {
		return Status{State: StateRunning, CopyDone: copyDone, InitialSyncCompleted: initialSync, Error: "Could not read the target: " + err.Error()}
	}
	var source struct {
		Now   time.Time         `json:"now"`
		Sizes map[string]uint64 `json:"sizes"`
	}
	withSizes := strconv.FormatBool(len(sizes) == 0)
	if err := psqlJSON(ctx, a.config.SourceURI, sourceStatusSQL, a.vars(map[string]string{"with_sizes": withSizes}), &source); err != nil {
		return Status{State: StateRunning, CopyDone: copyDone, InitialSyncCompleted: initialSync, Error: "Could not read the source: " + err.Error()}
	}
	if len(sizes) == 0 {
		sizes = source.Sizes
	}
	snap.SourceNow, snap.Sizes = source.Now, sizes

	a.mu.Lock()
	if snap.Errors > a.errorCount {
		a.errorsChangedAt = time.Now()
	}
	a.errorCount = snap.Errors
	recentErrors := time.Since(a.errorsChangedAt) < recentErrorWindow
	a.mu.Unlock()

	status := deriveStatus(current == stageCutover, failure, snap, copyDone, initialSync, recentErrors)
	a.mu.Lock()
	if len(a.sizes) == 0 {
		a.sizes = sizes
	}
	a.copyDone = status.CopyDone
	a.initialSync = status.InitialSyncCompleted
	a.mu.Unlock()
	return status
}

// deriveStatus maps a snapshot onto agent states. copyDone and initialSync
// latch: once reached they stay true. recentErrors reports replication errors
// that PostgreSQL is still retrying.
func deriveStatus(cutover bool, failure string, snap snapshot, copyDone, initialSync, recentErrors bool) Status {
	copied := map[string]bool{}
	ready, userTables := 0, 0
	for _, table := range snap.Tables {
		if table.Table == heartbeatTable {
			continue
		}
		userTables++
		switch table.State {
		case "r":
			ready++
			copied[table.Table] = true
		case "s", "f":
			copied[table.Table] = true
		}
	}
	copyDone = copyDone || (userTables > 0 && ready == userTables)
	status := Status{
		State:       StateRunning,
		CopyDone:    copyDone,
		BytesTotal:  sumSizes(snap.Sizes, nil),
		BytesCopied: sumSizes(snap.Sizes, copied),
	}
	if copyDone {
		status.BytesCopied = status.BytesTotal
	}
	if snap.BeatAt != nil && !snap.SourceNow.IsZero() {
		lag := snap.SourceNow.Sub(*snap.BeatAt)
		if lag < 0 {
			lag = 0
		}
		status.LagSeconds = int64(lag.Seconds())
		initialSync = initialSync || (copyDone && lag <= caughtUpLag)
	}
	status.InitialSyncCompleted = initialSync

	switch {
	case failure != "":
		status.State, status.Error = StateFailed, failure
	case !snap.SubExists:
		status.State, status.Error = StateFailed, "The subscription no longer exists on the target. Delete this migration and create it again."
	case cutover:
		status.State, status.Message = StateFinalizing, "Applying the last changes before the cutover"
	case !snap.SubEnabled:
		status.State, status.Message = StatePaused, "Applying changes is paused"
	case !copyDone:
		status.Message = fmt.Sprintf("Copying data (%d of %d tables done)", ready, userTables)
	case !initialSync:
		status.Message = "Catching up with changes made during the copy"
	default:
		status.Message = "Replicating changes"
	}
	if recentErrors && status.State != StateFailed {
		status.Error = fmt.Sprintf("PostgreSQL could not copy or apply changes recently (%d error(s) so far) and keeps retrying. "+
			"If the lag keeps growing, check the target instance's PostgreSQL log.", snap.Errors)
	}
	return status
}

// sumSizes adds the sizes of the tables in only, or of all tables when only
// is nil. The heartbeat table is not user data.
func sumSizes(sizes map[string]uint64, only map[string]bool) uint64 {
	var total uint64
	for table, size := range sizes {
		if table == heartbeatTable || (only != nil && !only[table]) {
			continue
		}
		total += size
	}
	return total
}
