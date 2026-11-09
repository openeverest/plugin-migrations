package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// Phases stored in the target's state table.
const (
	phasePreflight   = "preflight"
	phaseCopying     = "copying"
	phaseReplicating = "replicating"
	phaseCompleted   = "completed"
)

const (
	sqlTimeout = 30 * time.Second
	// caughtUpLag marks the initial sync complete: the target applies changes
	// close enough to real time for a cutover to be quick.
	caughtUpLag         = 15 * time.Second
	cutoverPollInterval = time.Second
	dmReadyTimeout      = 2 * time.Minute
	// dmRetryGrace is how long a DM error shows as a running migration: DM
	// resumes tasks that stopped on errors it can retry (a lost connection)
	// by itself.
	dmRetryGrace = 2 * time.Minute
)

var (
	errConflict    = errors.New("conflict")
	errInvalidBody = errors.New("invalid request")
	errUnavailable = errors.New("unavailable")
)

// Config comes from the worker's environment; endpoints carry credentials.
type Config struct {
	Source            Endpoint
	Target            Endpoint
	Name              string
	DataDir           string
	BinDir            string
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
	// stageRecovering lasts until the target's state table and DM were read.
	stageRecovering stage = iota
	stageIdle
	// stagePreparing runs the preflight; failure holds why it stopped.
	stagePreparing
	// stageRunning: DM copies or replicates.
	stageRunning
	stageCutover
	stageCompleted
	stageStopped
)

// controller is the part of DM the agent drives; tests replace it.
type controller interface {
	Ready(ctx context.Context) error
	SourceExists(ctx context.Context, name string) (bool, error)
	CreateSource(ctx context.Context, config []byte) error
	StopSource(ctx context.Context, name string) error
	CheckTask(ctx context.Context, config []byte) (checkResult, error)
	StartTask(ctx context.Context, config []byte) error
	OperateTask(ctx context.Context, name, op string) error
	SkipEvent(ctx context.Context, name, position string) error
	Status(ctx context.Context, name string) (TaskStatus, error)
	DumpDir(name string) string
}

type Agent struct {
	config Config
	dm     controller

	// controlMu serializes everything that changes the DM task (supervision,
	// pause, resume, cutover, teardown). Lock it before mu.
	controlMu sync.Mutex
	mu        sync.Mutex
	stage     stage
	step      string
	// failure is the agent's own error (preflight, a lost task, cutover);
	// DM's errors come from its status.
	failure     string
	cancel      context.CancelFunc
	options     Options
	phase       string
	useGTID     bool
	paused      bool
	initialSync bool
	// finishing is set once the cutover reached the source position: the
	// heartbeat stops and the task is removed.
	finishing  bool
	viewsReady bool
	bytesTotal uint64
	// loadedBytes is what DM's load imported, shown once it is done.
	loadedBytes uint64
	notes       string
	// dmErrorSince is when DM's task was first seen stopped on an error.
	dmErrorSince time.Time
	// cutoverAt is the source position the cutover waits for; taskRemoved
	// is set once the cutover reached it and removed DM's task.
	cutoverAt   sourcePosition
	taskRemoved bool

	dbMu sync.Mutex
	dbs  map[string]*sql.DB
}

func NewAgent(config Config, dm controller) *Agent {
	return &Agent{config: config, dm: dm, dbs: map[string]*sql.DB{}}
}

func (a *Agent) db(endpoint Endpoint) (*sql.DB, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	key := endpoint.User + "@" + endpoint.Address()
	if db, ok := a.dbs[key]; ok {
		return db, nil
	}
	db, err := openDB(endpoint)
	if err != nil {
		return nil, err
	}
	a.dbs[key] = db
	return db, nil
}

func (a *Agent) source() (*sql.DB, error) { return a.db(a.config.Source) }
func (a *Agent) target() (*sql.DB, error) { return a.db(a.config.Target) }

func (a *Agent) setStep(step string) {
	a.mu.Lock()
	a.step = step
	a.mu.Unlock()
	log.Print(step)
}

// RecoverLoop restores the stage after a worker restart, retrying until the
// target and DM answer: until then the agent cannot tell whether a migration
// runs, so it neither supervises nor accepts a start. DM itself resumes its
// task from the volume meanwhile.
func (a *Agent) RecoverLoop(ctx context.Context) {
	delay := time.Second
	for !a.Recover(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, 30*time.Second)
	}
}

// storedState is the target's row for this migration.
type storedState struct {
	options                string
	phase, failure, notes  string
	paused, useGTID, found bool
}

func (a *Agent) readState(ctx context.Context) (storedState, error) {
	target, err := a.target()
	if err != nil {
		return storedState{}, err
	}
	var tables int
	if err := target.QueryRowContext(ctx, stateTableExistsSQL).Scan(&tables); err != nil || tables == 0 {
		return storedState{}, err
	}
	var state storedState
	err = target.QueryRowContext(ctx, readStateSQL, a.config.Name).Scan(&state.options, &state.phase, &state.paused, &state.failure, &state.notes)
	if errors.Is(err, sql.ErrNoRows) {
		return storedState{}, nil
	}
	if err != nil {
		return storedState{}, err
	}
	state.found = true
	var stored stateOptions
	if err := json.Unmarshal([]byte(state.options), &stored); err == nil {
		state.useGTID = stored.UseGTID
	}
	return state, nil
}

// stateOptions is the options column: the migration's options and whether
// DM follows the source by GTID.
type stateOptions struct {
	Options Options `json:"options"`
	UseGTID bool    `json:"useGTID"`
}

// Recover reads the target's state table and waits for DM once. It reports
// false when either could not be read.
func (a *Agent) Recover(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	if err := a.dm.Ready(ctx); err != nil {
		log.Printf("recover: %v", err)
		return false
	}
	state, err := a.readState(ctx)
	if err != nil {
		log.Printf("recover: read the migration state on the target: %s", serverError(err))
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageRecovering {
		return true // torn down meanwhile
	}
	if !state.found {
		a.stage = stageIdle
		return true
	}
	var stored stateOptions
	if err := json.Unmarshal([]byte(state.options), &stored); err != nil {
		a.stage, a.failure = stagePreparing, "The migration state on the target cannot be read."
		return true
	}
	a.options, a.phase, a.useGTID, a.paused, a.notes = stored.Options, state.phase, stored.UseGTID, state.paused, state.notes
	switch state.phase {
	case phaseCopying, phaseReplicating:
		a.stage, a.failure = stageRunning, state.failure
		log.Printf("recovered a running migration (%s)", state.phase)
	case phaseCompleted:
		a.stage, a.initialSync = stageCompleted, true
	default:
		a.stage, a.failure = stagePreparing, state.failure
		if a.failure == "" {
			a.failure = "The migration worker restarted during the preflight. Resume the migration to run it again."
		}
	}
	return true
}

// Start runs the preflight and starts DM in the background. Calling it
// again after the first call does nothing.
func (a *Agent) Start(options Options) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage == stageRecovering {
		return fmt.Errorf("%w: the worker is still reading the migration state", errUnavailable)
	}
	if a.stage != stageIdle {
		return nil // the reconciler retries Start after a lost response
	}
	a.options = options
	a.startPreparing()
	return nil
}

// startPreparing runs prepare in the background, cancelling an earlier run.
// Call with mu held.
func (a *Agent) startPreparing() {
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.stage, a.cancel, a.failure = stagePreparing, cancel, ""
	options := a.options
	go func() {
		err := a.prepare(ctx, options)
		a.mu.Lock()
		defer a.mu.Unlock()
		var started *startFailure
		switch {
		case ctx.Err() != nil:
		case errors.As(err, &started):
			log.Printf("start: %v", err)
			a.stage, a.step, a.failure = stageRunning, "", started.message
		case err != nil:
			log.Printf("start: %v", err)
			a.failure = err.Error()
		default:
			a.stage, a.step = stageRunning, ""
		}
	}()
}

func (a *Agent) prepare(ctx context.Context, options Options) error {
	a.setStep("Checking the source and target")
	target, err := a.target()
	if err != nil {
		return err
	}
	source, err := a.source()
	if err != nil {
		return err
	}
	if err := waitReady(ctx, a.dm); err != nil {
		return err
	}
	sqlCtx, cancel := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancel()
	encoded, err := json.Marshal(stateOptions{Options: options})
	if err != nil {
		return err
	}
	for _, statement := range []string{createStateDatabaseSQL, createStateTableSQL} {
		if _, err := target.ExecContext(sqlCtx, statement); err != nil {
			return fmt.Errorf("connect to the target: %s", serverError(err))
		}
	}
	if _, err := target.ExecContext(sqlCtx, writeStateSQL, a.config.Name, string(encoded), phasePreflight); err != nil {
		return fmt.Errorf("record the migration on the target: %s", serverError(err))
	}
	fail := func(err error) error {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sqlTimeout)
		defer cancel()
		if _, recordErr := target.ExecContext(recordCtx, setFailureSQL, err.Error(), a.config.Name); recordErr != nil {
			log.Printf("record the failure on the target: %s", serverError(recordErr))
		}
		return err
	}

	sourceFacts, err := readSource(sqlCtx, source, options)
	if err != nil {
		return fail(fmt.Errorf("read the source: %s", serverError(err)))
	}
	collations, err := sourceCollations(sqlCtx, source, options)
	if err != nil {
		return fail(fmt.Errorf("read the source: %s", serverError(err)))
	}
	targetFacts, err := readTarget(sqlCtx, target, options)
	if err != nil {
		return fail(fmt.Errorf("read the target: %s", serverError(err)))
	}
	problems := checkPreflight(sourceFacts, collations, targetFacts, options, freeSpace(a.config.DataDir))
	useGTID := sourceFacts.usesGTID()

	// The heartbeat database must exist before DM's snapshot, which copies it.
	for _, statement := range []string{createHeartbeatDatabaseSQL(a.config.Name), createHeartbeatTableSQL(a.config.Name), beatSQL(a.config.Name)} {
		if _, err := source.ExecContext(sqlCtx, statement); err != nil {
			problems = append(problems, "the source user cannot create the migration's heartbeat database "+
				heartbeatDatabase(a.config.Name)+": "+serverError(err))
			break
		}
	}
	checkConfig, err := renderTaskConfig(a.config.Name, a.config.Target, options, a.dm.DumpDir(a.config.Name), true)
	if err != nil {
		return fail(err)
	}
	taskConfig, err := renderTaskConfig(a.config.Name, a.config.Target, options, a.dm.DumpDir(a.config.Name), false)
	if err != nil {
		return fail(err)
	}
	a.setStep("Running DM's precheck")
	if err := a.ensureSource(ctx, useGTID); err != nil {
		problems = append(problems, "DM cannot use the source: "+err.Error())
	} else if check, err := a.dm.CheckTask(ctx, checkConfig); err != nil {
		problems = append(problems, err.Error())
	} else {
		problems = append(problems, check.Problems...)
		for _, warning := range check.Warnings {
			log.Printf("DM precheck warning: %s", warning)
		}
	}
	if len(problems) > 0 {
		return fail(errors.New("Cannot start the migration: " + strings.Join(problems, "; ") + "."))
	}
	// The precheck may have taken a while: a fresh deadline for what follows.
	startCtx, cancelStart := context.WithTimeout(ctx, 2*sqlTimeout)
	defer cancelStart()

	if options.RecreateDatabases {
		a.setStep("Dropping the target databases")
		for _, database := range options.Databases {
			if _, err := target.ExecContext(startCtx, "DROP DATABASE IF EXISTS "+quoteIdentifier(database)); err != nil {
				return fail(fmt.Errorf("drop database %s on the target: %s", database, serverError(err)))
			}
		}
	}
	notes := unsupportedNote(sourceFacts.Unsupported)
	encoded, err = json.Marshal(stateOptions{Options: options, UseGTID: useGTID})
	if err != nil {
		return fail(err)
	}
	// Recorded before the task starts: a restarted worker must never run the
	// preflight (or drop the databases) again once DM may have written to them.
	if _, err := target.ExecContext(startCtx, writeStateSQL, a.config.Name, string(encoded), phaseCopying); err != nil {
		return fail(fmt.Errorf("record the migration on the target: %s", serverError(err)))
	}
	if _, err := target.ExecContext(startCtx, setNotesSQL, notes, a.config.Name); err != nil {
		log.Printf("record the notes on the target: %s", serverError(err))
	}
	a.mu.Lock()
	a.phase, a.useGTID, a.notes, a.bytesTotal = phaseCopying, useGTID, notes, sourceFacts.DataBytes
	a.mu.Unlock()

	a.setStep("Starting DM")
	if err := a.dm.StartTask(ctx, taskConfig); err != nil {
		// Past the preflight: Resume starts the task again (ensureTask), never
		// the preflight, so the failure goes with the running stage.
		failure := "DM could not start the task: " + err.Error()
		if _, recordErr := target.ExecContext(context.WithoutCancel(startCtx), setFailureSQL, failure, a.config.Name); recordErr != nil {
			log.Printf("record the failure on the target: %s", serverError(recordErr))
		}
		return &startFailure{failure}
	}
	return nil
}

// startFailure is a failure to start DM's task after a passed preflight.
type startFailure struct{ message string }

func (f *startFailure) Error() string { return f.message }

func waitReady(ctx context.Context, dm controller) error {
	ctx, cancel := context.WithTimeout(ctx, dmReadyTimeout)
	defer cancel()
	for {
		err := dm.Ready(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("DM did not start: %v", err)
		case <-time.After(time.Second):
		}
	}
}

// ensureSource registers the source with DM unless it already is.
func (a *Agent) ensureSource(ctx context.Context, useGTID bool) error {
	exists, err := a.dm.SourceExists(ctx, a.config.Name)
	if err != nil || exists {
		return err
	}
	config, err := renderSourceConfig(a.config.Name, a.config.Source, useGTID)
	if err != nil {
		return err
	}
	return a.dm.CreateSource(ctx, config)
}

// recordFailure sets and persists the agent's failure.
func (a *Agent) recordFailure(failure string) {
	a.mu.Lock()
	a.failure = failure
	a.mu.Unlock()
	if failure != "" {
		log.Print(failure)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sqlTimeout)
	defer cancel()
	if target, err := a.target(); err == nil {
		if _, err := target.ExecContext(ctx, setFailureSQL, nullable(failure), a.config.Name); err != nil {
			log.Printf("record the failure on the target: %s", serverError(err))
		}
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ensureTask starts the task again when DM lost it: after a restart that
// lost the volume, or when starting it failed. DM continues from its
// checkpoints on the target once its load finished; before the load started
// it copies from the beginning. A load that was interrupted cannot continue
// without the dump, which was on the lost volume.
func (a *Agent) ensureTask(ctx context.Context) {
	a.mu.Lock()
	phase, options, useGTID, paused := a.phase, a.options, a.useGTID, a.paused
	a.mu.Unlock()
	if phase == phaseCopying {
		load, err := a.loadStatus(ctx)
		if err != nil {
			log.Printf("supervise: read DM's checkpoints: %s", serverError(err))
			return
		}
		if load != "" && load != loadInit && load != loadFinished {
			a.recordFailure("DM lost its state during the copy, so it cannot finish it. Delete this migration and create it again " +
				"with \"Recreate the target databases\" enabled.")
			return
		}
	}
	log.Print("supervise: DM has no task for the migration; starting it")
	if err := a.ensureSource(ctx, useGTID); err != nil {
		a.recordFailure("DM cannot use the source: " + err.Error())
		return
	}
	config, err := renderTaskConfig(a.config.Name, a.config.Target, options, a.dm.DumpDir(a.config.Name), false)
	if err == nil {
		err = a.dm.StartTask(ctx, config)
	}
	if err != nil {
		a.recordFailure("DM could not start the task: " + err.Error())
		return
	}
	a.recordFailure("")
	if paused {
		if err := a.dm.OperateTask(ctx, a.config.Name, taskPause); err != nil {
			log.Printf("supervise: pause the task again: %v", err)
		}
	}
}

// Statuses of DM's load checkpoint: init until the load begins.
const (
	loadInit     = "init"
	loadFinished = "finished"
)

// loadStatus reads DM's load checkpoint on the target: "" before the load
// started, otherwise init, running or finished.
func (a *Agent) loadStatus(ctx context.Context) (string, error) {
	tables, err := a.taskMetaTables(ctx)
	if err != nil {
		return "", err
	}
	table := a.config.Name + "_lightning_checkpoint_list"
	if !contains(tables, table) {
		if len(tables) > 0 {
			return "unknown", nil // DM wrote checkpoints without a load checkpoint
		}
		return "", nil
	}
	target, err := a.target()
	if err != nil {
		return "", err
	}
	var status string
	err = target.QueryRowContext(ctx, "SELECT status FROM "+quoteIdentifier(metaSchema)+"."+quoteIdentifier(table)+" WHERE task_name = ?",
		a.config.Name).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return loadInit, nil
	}
	return status, err
}

// taskMetaTables lists DM's tables of this task in the meta schema.
func (a *Agent) taskMetaTables(ctx context.Context) ([]string, error) {
	target, err := a.target()
	if err != nil {
		return nil, err
	}
	tables, err := queryStrings(ctx, target, schemaTablesSQL, metaSchema)
	if err != nil {
		return nil, err
	}
	var own []string
	for _, table := range tables {
		if strings.HasPrefix(table, a.config.Name+"_") {
			own = append(own, table)
		}
	}
	return own, nil
}

func (a *Agent) Pause(ctx context.Context) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	ok := a.stage == stageRunning && a.failure == "" && !a.paused
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: only a running migration can be paused", errConflict)
	}
	if err := a.dm.OperateTask(ctx, a.config.Name, taskPause); err != nil {
		return fmt.Errorf("pause DM's task: %w", err)
	}
	return a.setPaused(ctx, true)
}

func (a *Agent) setPaused(ctx context.Context, paused bool) error {
	target, err := a.target()
	if err != nil {
		return err
	}
	if _, err := target.ExecContext(ctx, setPausedSQL, paused, a.config.Name); err != nil {
		return fmt.Errorf("record the pause on the target: %s", serverError(err))
	}
	a.mu.Lock()
	a.paused = paused
	a.mu.Unlock()
	return nil
}

// Resume continues a paused task or one DM stopped on an error, runs a
// failed preflight again, or retries what the agent itself failed to do.
func (a *Agent) Resume(ctx context.Context) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	current, failure := a.stage, a.failure
	switch {
	case current == stagePreparing && failure != "":
		a.startPreparing()
		a.mu.Unlock()
		return nil
	case current == stageCutover && failure != "":
		a.failure = ""
		a.startCutover(a.cutoverAt)
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	if current == stageCutover {
		// DM stopped on an error while the cutover waited for it.
		return a.dm.OperateTask(ctx, a.config.Name, taskResume)
	}
	if current != stageRunning {
		return fmt.Errorf("%w: only a paused or failed migration can be resumed", errConflict)
	}
	if failure != "" {
		// The supervisor starts the task again if DM has none.
		a.recordFailure("")
	}
	if _, err := a.dm.Status(ctx, a.config.Name); errors.Is(err, errTaskNotFound) {
		return nil
	}
	if err := a.setPaused(ctx, false); err != nil {
		return err
	}
	if err := a.dm.OperateTask(ctx, a.config.Name, taskResume); err != nil {
		return fmt.Errorf("resume DM's task: %w", err)
	}
	return nil
}

// Cutover waits, in the background, until DM saved a position that includes
// every transaction committed on the source before the request, then removes
// the task and the migration's helpers.
func (a *Agent) Cutover(ctx context.Context) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	current, initialSync, failure, paused := a.stage, a.initialSync, a.failure, a.paused
	a.mu.Unlock()
	if current == stageCutover {
		return nil
	}
	if current != stageRunning || !initialSync || failure != "" {
		return fmt.Errorf("%w: cutover is possible once the target has caught up", errConflict)
	}
	task, err := a.dm.Status(ctx, a.config.Name)
	if err != nil {
		return fmt.Errorf("read DM's status: %w", err)
	}
	if len(task.Errors()) > 0 || !strings.EqualFold(task.Unit, "Sync") {
		return fmt.Errorf("%w: cutover is possible once DM replicates without errors", errConflict)
	}
	source, err := a.source()
	if err != nil {
		return err
	}
	position, err := readSourcePosition(ctx, source)
	if err != nil {
		return fmt.Errorf("read the source position: %s", serverError(err))
	}
	if paused {
		if err := a.setPaused(ctx, false); err != nil {
			return err
		}
		if err := a.dm.OperateTask(ctx, a.config.Name, taskResume); err != nil {
			return fmt.Errorf("resume DM's task: %w", err)
		}
	}
	a.mu.Lock()
	a.startCutover(position)
	a.mu.Unlock()
	return nil
}

// startCutover finishes the migration in the background once DM reached
// position. Call with mu held.
func (a *Agent) startCutover(position sourcePosition) {
	cutoverCtx, cancel := context.WithCancel(context.Background())
	a.stage, a.cancel, a.cutoverAt = stageCutover, cancel, position
	log.Printf("cutover: waiting for DM to apply the source up to %s:%d", position.File, position.Pos)
	go func() {
		err := a.cutover(cutoverCtx, position)
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
}

// readSourcePosition reads the source's binary log position and GTIDs.
// MySQL 8.4 renamed SHOW MASTER STATUS.
func readSourcePosition(ctx context.Context, db *sql.DB) (sourcePosition, error) {
	row, found, err := readRow(ctx, db, "SHOW BINARY LOG STATUS")
	if err != nil {
		row, found, err = readRow(ctx, db, "SHOW MASTER STATUS")
	}
	if err != nil {
		return sourcePosition{}, err
	}
	if !found {
		return sourcePosition{}, errors.New("the source has no binary log")
	}
	var pos uint64
	if _, err := fmt.Sscan(row["Position"], &pos); err != nil {
		return sourcePosition{}, errors.New("the source reported an unreadable position")
	}
	return sourcePosition{File: row["File"], Pos: pos, GTID: row["Executed_Gtid_Set"]}, nil
}

func (a *Agent) cutover(ctx context.Context, position sourcePosition) error {
	a.mu.Lock()
	removed := a.taskRemoved
	a.mu.Unlock()
	for !removed {
		reached, err := a.positionReached(ctx, position)
		if reached {
			break
		}
		if err != nil {
			log.Printf("cutover: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cutoverPollInterval):
		}
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	a.finishing = true
	options, notes := a.options, a.notes
	a.mu.Unlock()

	sqlCtx, cancel := context.WithTimeout(ctx, 4*sqlTimeout)
	defer cancel()
	source, err := a.source()
	if err != nil {
		return err
	}
	target, err := a.target()
	if err != nil {
		return err
	}
	// The views as they are on the source now: DM does not replicate them.
	failed, err := createViews(sqlCtx, source, target, options)
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		notes = strings.TrimSpace(notes + " Views that could not be created on TiDB: " + listNames(failed) + ".")
	}
	if !removed {
		if err := a.dm.OperateTask(sqlCtx, a.config.Name, taskStop); err != nil {
			return fmt.Errorf("stop DM's task: %w", err)
		}
		a.mu.Lock()
		a.taskRemoved = true
		a.mu.Unlock()
	}
	if err := a.dm.StopSource(sqlCtx, a.config.Name); err != nil {
		log.Printf("cutover: remove DM's source: %v", err)
	}
	if _, err := target.ExecContext(sqlCtx, setNotesSQL, nullable(notes), a.config.Name); err != nil {
		return fmt.Errorf("record the completion on the target: %s", serverError(err))
	}
	if _, err := target.ExecContext(sqlCtx, setPhaseSQL, phaseCompleted, a.config.Name); err != nil {
		return fmt.Errorf("record the completion on the target: %s", serverError(err))
	}
	a.mu.Lock()
	a.notes, a.phase = notes, phaseCompleted
	a.mu.Unlock()
	if err := a.cleanup(sqlCtx, source, target); err != nil {
		log.Printf("cutover: %v", err)
	}
	return nil
}

// positionReached tells whether DM saved a checkpoint at or past position.
func (a *Agent) positionReached(ctx context.Context, position sourcePosition) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, err := a.dm.Status(ctx, a.config.Name)
	if err != nil {
		return false, err
	}
	if status.Sync == nil || !strings.EqualFold(status.Unit, "Sync") {
		return false, errors.New("DM is not replicating")
	}
	a.mu.Lock()
	useGTID := a.useGTID
	a.mu.Unlock()
	return position.reached(status.Sync.SyncerBinlog, status.Sync.SyncerBinlogGTID, useGTID)
}

// cleanup removes the migration's helpers on both sides, except the state
// row: the heartbeat databases and DM's checkpoints. Idempotent.
func (a *Agent) cleanup(ctx context.Context, source, target *sql.DB) error {
	var failures []string
	if _, err := target.ExecContext(ctx, dropHeartbeatDatabaseSQL(a.config.Name)); err != nil {
		failures = append(failures, "drop the heartbeat database on the target: "+serverError(err))
	}
	tables, err := a.taskMetaTables(ctx)
	if err != nil {
		failures = append(failures, "list DM's checkpoints: "+serverError(err))
	}
	for _, table := range tables {
		if _, err := target.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteIdentifier(metaSchema)+"."+quoteIdentifier(table)); err != nil {
			failures = append(failures, "drop DM's checkpoints: "+serverError(err))
		}
	}
	if err := dropSharedSchemas(ctx, target); err != nil {
		failures = append(failures, "drop DM's databases: "+serverError(err))
	}
	if _, err := source.ExecContext(ctx, dropHeartbeatDatabaseSQL(a.config.Name)); err != nil {
		failures = append(failures, "drop the heartbeat database on the source: "+serverError(err))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// dropSharedSchemas drops DM's meta schema once no task has tables in it,
// then Lightning's task info if nothing is recorded there: both are shared by
// every DM task on the target.
func dropSharedSchemas(ctx context.Context, db *sql.DB) error {
	tables, err := queryStrings(ctx, db, schemaTablesSQL, metaSchema)
	if err != nil || len(tables) > 0 {
		return err
	}
	if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(metaSchema)); err != nil {
		return err
	}
	if tables, err = queryStrings(ctx, db, schemaTablesSQL, lightningSchema); err != nil {
		return err
	}
	for _, table := range tables {
		var rows int
		err := db.QueryRowContext(ctx, countRowsSQL+quoteIdentifier(lightningSchema)+"."+quoteIdentifier(table)).Scan(&rows)
		if err != nil || rows > 0 {
			return nil // a view, or conflicts someone may want to see
		}
	}
	_, err = db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(lightningSchema))
	return err
}

// Teardown stops background work and removes everything the migration
// created on the source and target except the migrated data. Idempotent.
func (a *Agent) Teardown(ctx context.Context) error {
	a.mu.Lock()
	recovering := a.stage == stageRecovering
	a.mu.Unlock()
	if recovering && !a.Recover(ctx) {
		return errors.New("cannot reach the target or DM to clean up")
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.stage, a.finishing = stageStopped, true
	a.mu.Unlock()

	// Without DM the pod is going away anyway, and with it DM's connections.
	if _, err := a.dm.Status(ctx, a.config.Name); err == nil {
		if err := a.dm.OperateTask(ctx, a.config.Name, taskStop); err != nil {
			return fmt.Errorf("stop DM's task: %w", err)
		}
	} else if !errors.Is(err, errTaskNotFound) {
		log.Printf("teardown: read DM's task: %v", err)
	}
	if err := a.dm.StopSource(ctx, a.config.Name); err != nil {
		log.Printf("teardown: remove DM's source: %v", err)
	}
	source, err := a.source()
	if err != nil {
		return err
	}
	target, err := a.target()
	if err != nil {
		return err
	}
	if err := a.cleanup(ctx, source, target); err != nil {
		return err
	}
	var tables int
	if err := target.QueryRowContext(ctx, stateTableExistsSQL).Scan(&tables); err != nil {
		return fmt.Errorf("read the target: %s", serverError(err))
	}
	if tables > 0 {
		if _, err := target.ExecContext(ctx, deleteStateSQL, a.config.Name); err != nil {
			return fmt.Errorf("remove the migration state on the target: %s", serverError(err))
		}
		var remaining int
		if err := target.QueryRowContext(ctx, countStateSQL).Scan(&remaining); err == nil && remaining == 0 {
			if _, err := target.ExecContext(ctx, dropStateSQL); err != nil {
				return fmt.Errorf("remove the migration state on the target: %s", serverError(err))
			}
		}
	}
	if err := target.QueryRowContext(ctx, countMigrationTablesSQL).Scan(&tables); err == nil && tables == 0 {
		if _, err := target.ExecContext(ctx, dropMigrationDatabaseSQL); err != nil {
			log.Printf("teardown: drop the migration database on the target: %s", serverError(err))
		}
	}
	return nil
}

// RunSupervisor writes the source heartbeat and looks after DM's task.
func (a *Agent) RunSupervisor(ctx context.Context) {
	ticker := time.NewTicker(a.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		current, finishing := a.stage, a.finishing
		a.mu.Unlock()
		if (current != stageRunning && current != stageCutover) || finishing {
			continue
		}
		tickCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
		if db, err := a.source(); err == nil {
			if _, err := db.ExecContext(tickCtx, beatSQL(a.config.Name)); err != nil {
				log.Printf("heartbeat: %s", serverError(err))
			}
		}
		a.supervise(tickCtx)
		cancel()
	}
}

// supervise checks DM's task once, under controlMu: a pause, resume or
// cutover may have happened since the tick.
func (a *Agent) supervise(ctx context.Context) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	current, finishing, failure, phase, options, viewsReady := a.stage, a.finishing, a.failure, a.phase, a.options, a.viewsReady
	a.mu.Unlock()
	if (current != stageRunning && current != stageCutover) || finishing || failure != "" {
		return
	}
	status, err := a.dm.Status(ctx, a.config.Name)
	switch {
	case errors.Is(err, errTaskNotFound):
		a.ensureTask(ctx)
		return
	case err != nil:
		log.Printf("supervise: %v", err)
		return
	}
	if errs := status.Errors(); len(errs) > 0 && strings.EqualFold(status.Stage, "Paused") {
		a.skipUnparseableDDL(ctx, errs[0], options)
		return
	}
	if !strings.EqualFold(status.Unit, "Sync") {
		return
	}
	if phase != phaseReplicating && phase != phaseCompleted {
		target, err := a.target()
		if err != nil {
			return
		}
		if _, err := target.ExecContext(ctx, setPhaseSQL, phaseReplicating, a.config.Name); err != nil {
			log.Printf("supervise: record the phase on the target: %s", serverError(err))
			return
		}
		a.mu.Lock()
		a.phase = phaseReplicating
		a.mu.Unlock()
		log.Print("the copy is loaded; DM replicates changes")
	}
	if !viewsReady {
		a.refreshViews(ctx, options)
	}
}

// refreshViews creates the views once replication runs and records those
// TiDB rejects in the notes.
func (a *Agent) refreshViews(ctx context.Context, options Options) {
	source, err := a.source()
	if err != nil {
		return
	}
	target, err := a.target()
	if err != nil {
		return
	}
	failed, err := createViews(ctx, source, target, options)
	if err != nil {
		log.Printf("supervise: create views: %v", err)
		return
	}
	a.mu.Lock()
	a.viewsReady = true
	a.mu.Unlock()
	if len(failed) > 0 {
		log.Printf("views TiDB rejected: %s", strings.Join(failed, "; "))
	}
}

func (a *Agent) Status(ctx context.Context) Status {
	a.mu.Lock()
	current, step, failure, paused := a.stage, a.step, a.failure, a.paused
	initialSync, notes, total, phase, loaded := a.initialSync, a.notes, a.bytesTotal, a.phase, a.loadedBytes
	a.mu.Unlock()

	switch current {
	case stageRecovering:
		return Status{State: StateStopped, Message: "Connecting to the target and starting DM"}
	case stageIdle:
		return Status{State: StateIdle}
	case stageStopped:
		return Status{State: StateStopped, Message: "Stopped"}
	case stageCompleted:
		return Status{State: StateCompleted, Message: strings.TrimSpace("Migration completed. " + notes), CopyDone: true, InitialSyncCompleted: true,
			BytesTotal: loaded, BytesCopied: loaded}
	case stagePreparing:
		if failure != "" {
			return Status{State: StateFailed, Error: failure}
		}
		return Status{State: StateRunning, Message: step}
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if loaded == 0 {
		loaded = total
	}
	status := Status{State: StateRunning, InitialSyncCompleted: initialSync, CopyDone: phase == phaseReplicating,
		BytesTotal: loaded, BytesCopied: loaded}
	if current == stageCutover {
		status.State, status.Message = StateFinalizing, "Waiting until the target has every change made on the source before the cutover"
	}
	if failure != "" {
		status.State, status.Error = StateFailed, failure
		return status
	}
	task, err := a.dm.Status(ctx, a.config.Name)
	if errors.Is(err, errTaskNotFound) {
		status.Message = "Starting DM's task"
		return status
	}
	if err != nil {
		status.Error = "Could not read DM's status: " + err.Error()
		return status
	}
	if errs := task.Errors(); len(errs) > 0 && strings.EqualFold(task.Stage, "Paused") {
		// No cutover while DM is stopped.
		status.Error, status.InitialSyncCompleted = formatDMErrors(errs), false
		if current != stageCutover && a.dmErrorFailed(errs[0]) {
			status.State = StateFailed
		} else {
			status.Error += " (DM retries)"
		}
		return status
	}
	a.mu.Lock()
	a.dmErrorSince = time.Time{}
	a.mu.Unlock()
	switch strings.ToLower(task.Unit) {
	case "sync":
		status.CopyDone = true
	case "load":
		status.Message = "Loading the data into TiDB"
		if task.Load != nil {
			status.BytesTotal, status.BytesCopied = uint64(task.Load.TotalBytes), uint64(task.Load.FinishedBytes)
			a.mu.Lock()
			a.loadedBytes = status.BytesTotal
			a.mu.Unlock()
		}
	default:
		status.Message = "Dumping the source"
		if task.Dump != nil {
			status.BytesTotal, status.BytesCopied = max(total, uint64(task.Dump.FinishedBytes)), uint64(task.Dump.FinishedBytes)
			if task.Dump.TotalTables > 0 {
				status.Message = fmt.Sprintf("Dumping the source (%.0f of %.0f tables)", float64(task.Dump.CompletedTables), float64(task.Dump.TotalTables))
			}
		}
	}
	if paused && current != stageCutover {
		status.State = StatePaused
	}
	if !status.CopyDone {
		return status
	}
	if lag, err := a.lag(ctx); err != nil {
		status.Error = err.Error()
	} else if lag >= 0 {
		status.LagSeconds = int64(lag / time.Second)
		if lag <= caughtUpLag && !paused {
			status.InitialSyncCompleted = true
			a.mu.Lock()
			a.initialSync = true
			a.mu.Unlock()
		}
	}
	if current != stageCutover {
		status.Message = strings.TrimSpace("Replicating changes. " + notes)
		if paused {
			status.Message = "Paused"
		}
	}
	return status
}

// dmErrorFailed tells whether DM's error should fail the migration: at once
// for a statement TiDB cannot parse, which no retry gets past, otherwise
// once DM has had dmRetryGrace to resume the task.
func (a *Agent) dmErrorFailed(failure DMError) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dmErrorSince.IsZero() {
		a.dmErrorSince = time.Now()
	}
	return failure.ErrCode == errCodeParseDDL || time.Since(a.dmErrorSince) >= dmRetryGrace
}

// lag is the source's time minus the last heartbeat on the target, both from
// the source clock; -1 before the first heartbeat arrived.
func (a *Agent) lag(ctx context.Context) (time.Duration, error) {
	target, err := a.target()
	if err != nil {
		return -1, err
	}
	var beatAt time.Time
	if err := target.QueryRowContext(ctx, readHeartbeatSQL(a.config.Name)).Scan(&beatAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return -1, nil
		}
		return -1, fmt.Errorf("Could not read the target: %s", serverError(err))
	}
	source, err := a.source()
	if err != nil {
		return -1, err
	}
	var now time.Time
	if err := source.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(6)").Scan(&now); err != nil {
		return -1, fmt.Errorf("Could not read the source: %s", serverError(err))
	}
	return max(now.Sub(beatAt), 0), nil
}

// formatDMErrors keeps DM's cause and advice; its messages may quote a
// statement, which is cut short.
func formatDMErrors(errs []DMError) string {
	first := errs[0]
	message := strings.Join(strings.Fields(first.Message), " ")
	if _, after, found := strings.Cut(message, "]: "); found && strings.HasPrefix(message, "startLocation") {
		message = after
	}
	parts := []string{"DM stopped: " + truncate(message, 300)}
	if cause := strings.TrimSpace(first.RawCause); cause != "" && !strings.Contains(message, cause) {
		parts = append(parts, "cause: "+truncate(cause, 200))
	}
	if first.ErrCode == errCodeParseDDL {
		parts = append(parts, "TiDB cannot run this statement, so the migration cannot continue past it. "+
			"Make the table compatible with TiDB on the source (or exclude it), then delete this migration and create it again")
	}
	return strings.Join(parts, "; ")
}
