package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
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
	caughtUpLag  = 15 * time.Second
	stopGrace    = 15 * time.Second
	logTailLines = 15
)

var (
	errConflict    = errors.New("conflict")
	errInvalidBody = errors.New("invalid request")
)

// Config comes from the worker's environment; URIs carry credentials.
type Config struct {
	SourceURI string
	TargetURI string
	// TargetAdminURI connects to a maintenance database on the target server
	// (to create TargetDatabase), so the agent never rewrites URIs itself.
	TargetAdminURI    string
	TargetDatabase    string
	SlotName          string
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

type Agent struct {
	config  Config
	catalog *Catalog
	lag     LagTracker
	logTail *lineRing

	mu          sync.Mutex
	started     bool
	starting    bool
	options     Options
	process     *exec.Cmd
	running     bool
	cutover     bool
	completed   bool
	stopped     bool
	failure     string
	initialSync bool
	copyDone    bool
	// bytesTotal survives the end of pgcopydb, so a completed migration still
	// reports how much it copied.
	bytesTotal uint64
}

func NewAgent(config Config) *Agent {
	return &Agent{config: config, catalog: NewCatalog(filepath.Join(config.WorkDir, "run")), logTail: newLineRing(logTailLines)}
}

// DetectLostState fails the agent when the source already has this
// migration's slot: the worker restarted and its local pgcopydb state is gone,
// so continuing would silently skip changes.
func (a *Agent) DetectLostState(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	exists, err := psql(ctx, a.config.SourceURI, "select exists (select 1 from pg_replication_slots where slot_name = :'slot')", map[string]string{"slot": a.config.SlotName})
	if err != nil {
		log.Printf("check for an existing replication slot: %v", err)
		return
	}
	if exists == "t" {
		a.mu.Lock()
		a.started = true
		a.failure = "The migration worker restarted and lost its local progress. Delete this migration and create it again."
		a.mu.Unlock()
	}
}

func (a *Agent) Start(ctx context.Context, options Options) error {
	a.mu.Lock()
	if a.started || a.starting {
		a.mu.Unlock()
		return nil // the reconciler retries Start after a lost response
	}
	a.starting = true
	a.mu.Unlock()

	err := a.prepare(ctx, options)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starting = false
	a.started = true
	a.options = options
	if err != nil {
		a.failure = err.Error()
		return nil
	}
	args := []string{"clone", "--follow", "--no-owner", "--no-acl"}
	if options.DropIfExists {
		args = append(args, "--drop-if-exists")
	}
	if options.TableJobs != nil {
		args = append(args, "--table-jobs", strconv.Itoa(*options.TableJobs))
	}
	if options.IndexJobs != nil {
		args = append(args, "--index-jobs", strconv.Itoa(*options.IndexJobs))
	}
	if err := a.spawnLocked(args); err != nil {
		a.failure = err.Error()
	}
	return nil
}

// prepare creates the target database and the source heartbeat, and writes
// the filters file.
func (a *Agent) prepare(ctx context.Context, options Options) error {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	maintenance := a.config.TargetAdminURI
	vars := map[string]string{"db": a.config.TargetDatabase}
	exists, err := psql(ctx, maintenance, "select exists (select 1 from pg_database where datname = :'db')", vars)
	if err != nil {
		return fmt.Errorf("check the target database: %w", err)
	}
	if exists != "t" {
		if _, err := psql(ctx, maintenance, `create database :"db"`, vars); err != nil {
			return fmt.Errorf("create the target database: %w", err)
		}
	}
	heartbeatSQL := fmt.Sprintf(`create schema if not exists %[1]s;
create table if not exists %[1]s.heartbeat (slot_name text primary key, beat_at timestamptz not null);
insert into %[1]s.heartbeat values (:'slot', now()) on conflict (slot_name) do update set beat_at = excluded.beat_at;`, heartbeatSchema)
	if _, err := psql(ctx, a.config.SourceURI, heartbeatSQL, map[string]string{"slot": a.config.SlotName}); err != nil {
		return fmt.Errorf("create the heartbeat table on the source: %w", err)
	}
	if err := os.MkdirAll(a.config.WorkDir, 0o750); err != nil {
		return fmt.Errorf("create the work directory: %w", err)
	}
	return os.WriteFile(a.filtersPath(), []byte(options.filtersINI()), 0o640)
}

func (a *Agent) filtersPath() string { return filepath.Join(a.config.WorkDir, "filters.ini") }

// spawnLocked starts pgcopydb with the migration's slot, origin and filters.
func (a *Agent) spawnLocked(args []string) error {
	args = append(args,
		"--dir", filepath.Join(a.config.WorkDir, "run"),
		"--slot-name", a.config.SlotName,
		"--origin", a.config.SlotName,
		"--filters", a.filtersPath(),
	)
	command := exec.Command("pgcopydb", args...)
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := io.MultiWriter(os.Stdout, a.logTail)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return fmt.Errorf("start pgcopydb: %w", err)
	}
	log.Printf("started pgcopydb %s (pid %d)", args[0], command.Process.Pid)
	a.process = command
	a.running = true
	a.failure = ""
	go a.wait(command)
	return nil
}

func (a *Agent) wait(command *exec.Cmd) {
	err := command.Wait()
	a.mu.Lock()
	if a.process != command {
		a.mu.Unlock()
		return
	}
	a.running = false
	finished := err == nil && a.cutover && !a.stopped
	switch {
	case a.stopped, finished:
	case err == nil:
		a.failure = "pgcopydb stopped unexpectedly. " + a.logTail.LastError()
	default:
		a.failure = fmt.Sprintf("pgcopydb failed (%v). %s", err, a.logTail.LastError())
	}
	a.mu.Unlock()
	if finished {
		a.complete()
	}
}

// complete releases the source slot and target origin after a cutover. They
// are no longer needed and a leftover slot retains WAL on the source.
func (a *Agent) complete() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*sqlTimeout)
	defer cancel()
	err := a.cleanup(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		log.Printf("cleanup after cutover: %v", err)
		a.failure = "The data was migrated, but cleaning up failed: " + err.Error()
		return
	}
	a.completed = true
}

func (a *Agent) Pause(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || a.cutover || !a.copyDone {
		return fmt.Errorf("%w: changes can be paused only while they are replicated", errConflict)
	}
	return a.catalog.SetApply(ctx, false)
}

func (a *Agent) Resume(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return a.catalog.SetApply(ctx, true)
	}
	if a.failure == "" || a.completed || a.stopped {
		return fmt.Errorf("%w: nothing to resume", errConflict)
	}
	if !a.copyDone {
		return fmt.Errorf("%w: the initial copy did not finish; delete this migration and create it again", errConflict)
	}
	// After the copy, only change replication is left; resume it from the
	// position the target recorded in its replication origin.
	return a.spawnLocked([]string{"follow", "--resume", "--not-consistent"})
}

func (a *Agent) Cutover(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || !a.initialSync {
		return fmt.Errorf("%w: cutover is possible once the target has caught up", errConflict)
	}
	if a.cutover {
		return nil
	}
	sqlCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	lsn, err := psql(sqlCtx, a.config.SourceURI, "select pg_current_wal_flush_lsn()", nil)
	if err != nil {
		return fmt.Errorf("read the source position: %w", err)
	}
	if err := a.catalog.SetEndPos(ctx, lsn); err != nil {
		return err
	}
	if err := a.catalog.SetApply(ctx, true); err != nil {
		return err
	}
	a.cutover = true
	go a.beat(context.Background())
	return nil
}

// Teardown stops pgcopydb and removes everything the migration created on
// the source and target except the migrated data. It is idempotent.
func (a *Agent) Teardown(ctx context.Context) error {
	a.mu.Lock()
	a.stopped = true
	process := a.process
	running := a.running
	a.mu.Unlock()
	if running && process != nil {
		stopProcessGroup(process)
	}
	return a.cleanup(ctx)
}

func stopProcessGroup(command *exec.Cmd) {
	pgid := -command.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if syscall.Kill(pgid, 0) != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = syscall.Kill(pgid, syscall.SIGKILL)
}

func (a *Agent) cleanup(ctx context.Context) error {
	vars := map[string]string{"slot": a.config.SlotName}
	// The slot's walsender can stay active for a moment after pgcopydb stops.
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		_, err = psql(ctx, a.config.SourceURI, `select pg_terminate_backend(active_pid) from pg_replication_slots where slot_name = :'slot' and active_pid is not null;
select pg_drop_replication_slot(slot_name) from pg_replication_slots where slot_name = :'slot';`, vars)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return fmt.Errorf("drop the source replication slot: %w", err)
	}
	present, err := psql(ctx, a.config.SourceURI, fmt.Sprintf("select to_regclass('%s.heartbeat') is not null", heartbeatSchema), nil)
	if err != nil {
		return fmt.Errorf("check the source heartbeat table: %w", err)
	}
	if present == "t" {
		if _, err := psql(ctx, a.config.SourceURI, fmt.Sprintf("delete from %s.heartbeat where slot_name = :'slot'", heartbeatSchema), vars); err != nil {
			return fmt.Errorf("remove the source heartbeat: %w", err)
		}
	}
	if _, err := psql(ctx, a.config.TargetURI, "select pg_replication_origin_drop(roname) from pg_replication_origin where roname = :'slot'", vars); err != nil {
		return fmt.Errorf("drop the target replication origin: %w", err)
	}
	return nil
}

// RunHeartbeats writes a heartbeat on the source at a fixed interval while
// pgcopydb runs. pgcopydb ignores these changes, but they let the agent
// measure lag, and after a cutover they move the source past the end position
// so pgcopydb finishes even when the source is otherwise idle.
func (a *Agent) RunHeartbeats(ctx context.Context) {
	ticker := time.NewTicker(a.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			running := a.running
			a.mu.Unlock()
			if running {
				a.beat(ctx)
			}
		}
	}
}

func (a *Agent) beat(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	at := time.Now()
	output, err := psql(ctx, a.config.SourceURI, fmt.Sprintf(
		"update %s.heartbeat set beat_at = now() where slot_name = :'slot' returning pg_current_wal_insert_lsn()", heartbeatSchema),
		map[string]string{"slot": a.config.SlotName})
	if err != nil {
		log.Printf("heartbeat: %v", err)
		return
	}
	lsn, err := parseLSN(firstLine(output, ""))
	if err != nil {
		log.Printf("heartbeat: %v", err)
		return
	}
	a.lag.Record(lsn, at)
}

func (a *Agent) Status(ctx context.Context) Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.completed:
		return Status{State: StateCompleted, Message: "Migration completed", CopyDone: true, InitialSyncCompleted: true,
			BytesTotal: a.bytesTotal, BytesCopied: a.bytesTotal}
	case a.stopped:
		return Status{State: StateStopped, Message: "Stopped"}
	case !a.started:
		if a.starting {
			return Status{State: StateRunning, Message: "Preparing the source and target"}
		}
		return Status{State: StateIdle}
	}

	status := Status{State: StateRunning}
	progress, err := a.catalog.Read(ctx)
	if err != nil && !errors.Is(err, errCatalogMissing) {
		log.Printf("read pgcopydb catalog: %v", err)
	}
	if progress.CopyDone {
		a.copyDone = true
		a.bytesTotal = progress.BytesTotal
	}
	status.CopyDone = a.copyDone
	status.BytesTotal = progress.BytesTotal
	status.BytesCopied = progress.BytesCopied
	if sentinel := progress.Sentinel; sentinel != nil && a.copyDone {
		if replay, err := parseLSN(sentinel.ReplayLSN); err == nil {
			if lag, ok := a.lag.Lag(replay, time.Now()); ok {
				status.LagSeconds = int64(lag.Seconds())
				if lag <= caughtUpLag {
					a.initialSync = true
				}
			}
		}
	}
	status.InitialSyncCompleted = a.initialSync

	switch {
	case a.failure != "":
		status.State, status.Error = StateFailed, a.failure
	case a.cutover:
		status.State, status.Message = StateFinalizing, "Applying the last changes before the cutover"
	case !a.copyDone:
		status.Message = "Copying data"
	case progress.Sentinel != nil && !progress.Sentinel.Apply:
		status.State, status.Message = StatePaused, "Applying changes is paused"
	case !a.initialSync:
		status.Message = "Catching up with changes made during the copy"
	default:
		status.Message = "Replicating changes"
	}
	return status
}
