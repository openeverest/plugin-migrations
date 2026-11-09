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

	_ "github.com/microsoft/go-mssqldb"
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
	phasePreparing = "preparing"
	phaseCopying   = "copying"
	phaseShipping  = "shipping"
	phaseCutover   = "cutover"
	phaseCompleted = "completed"
)

// Database stages stored in the target's state table.
const (
	dbCopying   = "copying"
	dbShipping  = "shipping"
	dbRecovered = "recovered"
)

const (
	sqlTimeout = 30 * time.Second
	// caughtUpLag marks the initial sync complete: the target restores the
	// source's log close enough to real time for a quick cutover.
	caughtUpLag = 15 * time.Second
	// maxRetryDelay bounds the wait between attempts after a transient error.
	maxRetryDelay = time.Minute
)

var (
	errConflict    = errors.New("conflict")
	errInvalidBody = errors.New("invalid request")
	errUnavailable = errors.New("unavailable")
)

// Config comes from the worker's environment; it holds credentials.
type Config struct {
	Name    string
	Source  Endpoint
	Target  Endpoint
	Storage *Storage
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

// databaseState is one database's progress, persisted on the target. Seq
// numbers the backups: 0 is the full backup, then the log backups. The
// worker restores a backup before it takes the next one.
type databaseState struct {
	Name      string
	Stage     string
	SizeBytes int64
	Seq       int
	Stripes   int
	FirstLSN  string
	LastLSN   string
	// TakenAt is the source's clock when the last backup started: every
	// transaction committed before it is in that backup.
	TakenAt    *time.Time
	Final      bool
	Applied    bool
	AppliedLSN string
	AppliedAt  *time.Time
}

// fatalError stops the migration and is persisted on the target. Resumable
// failures can be retried after the cause is fixed.
type fatalError struct {
	message   string
	resumable bool
}

func (e *fatalError) Error() string { return e.message }

func fatal(resumable bool, format string, args ...any) error {
	return &fatalError{message: fmt.Sprintf(format, args...), resumable: resumable}
}

type stage int

const (
	// stageRecovering lasts until the target's state table has been read.
	stageRecovering stage = iota
	stageIdle
	stageRunning
	stageCompleted
	stageStopped
)

// copyProgress tracks the full backup and restore of one database.
type copyProgress struct {
	database       string
	size           int64
	backupSession  int64
	restoreSession int64
	backupDone     bool
}

type Agent struct {
	config Config

	mu        sync.Mutex
	stage     stage
	phase     string
	options   Options
	replica   Endpoint
	databases []databaseState
	paused    bool
	failure   string
	resumable bool
	// transient is the last error of a step that is being retried.
	transient   string
	step        string
	notes       string
	initialSync bool
	// sourceClock is the source's clock read at sourceClockAt (local,
	// monotonic): lag uses the source clock only.
	sourceClock   time.Time
	sourceClockAt time.Time
	copying       *copyProgress
	running       bool
	cancel        context.CancelFunc
	done          chan struct{}
	wake          chan struct{}
	// server facts read by the preflight, used to build statements.
	sourceServer serverFacts
	targetServer serverFacts

	dbMu sync.Mutex
	dbs  map[string]*sql.DB
}

func NewAgent(config Config) *Agent {
	return &Agent{config: config, replica: config.Target, dbs: map[string]*sql.DB{}, wake: make(chan struct{}, 1)}
}

// db returns a connection pool for endpoint, shared by address and user.
func (a *Agent) db(endpoint Endpoint) (*sql.DB, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	key := endpoint.User + "@" + endpoint.Address()
	if db, ok := a.dbs[key]; ok {
		return db, nil
	}
	db, err := sql.Open("sqlserver", endpoint.dsn())
	if err != nil {
		return nil, errors.New("invalid SQL Server connection settings")
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	a.dbs[key] = db
	return db, nil
}

func (a *Agent) source() (*sql.DB, error) { return a.db(a.config.Source) }

func (a *Agent) replicaDB() (*sql.DB, error) {
	a.mu.Lock()
	replica := a.replica
	a.mu.Unlock()
	return a.db(replica)
}

func (a *Agent) setStep(step string) {
	a.mu.Lock()
	a.step = step
	a.mu.Unlock()
	log.Print(step)
}

func (a *Agent) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// RecoverLoop restores the state after a worker restart, retrying until the
// target answers: until then the agent cannot tell whether a migration runs.
func (a *Agent) RecoverLoop(ctx context.Context) {
	delay := time.Second
	for !a.Recover(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, time.Minute)
	}
}

type persistedState struct {
	replica, options, phase, failure, notes string
	paused, resumable                       bool
	updatedAt                               time.Time
	databases                               []databaseState
}

// readState reads the migration's state from one server; found is false
// when it has none.
func (a *Agent) readState(ctx context.Context, db *sql.DB) (persistedState, bool, error) {
	var state persistedState
	var exists int
	if err := db.QueryRowContext(ctx, stateTableExistsSQL).Scan(&exists); err != nil || exists == 0 {
		return state, false, err
	}
	err := db.QueryRowContext(ctx, readStateSQL, a.config.Name).Scan(&state.replica, &state.options, &state.phase, &state.paused,
		&state.failure, &state.resumable, &state.notes, &state.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	state.databases, err = readDatabases(ctx, db, a.config.Name)
	return state, true, err
}

func readDatabases(ctx context.Context, db *sql.DB, name string) ([]databaseState, error) {
	rows, err := db.QueryContext(ctx, readDatabasesSQL, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []databaseState
	for rows.Next() {
		var state databaseState
		var takenAt, appliedAt sql.NullTime
		if err := rows.Scan(&state.Name, &state.Stage, &state.SizeBytes, &state.Seq, &state.Stripes, &state.FirstLSN, &state.LastLSN,
			&takenAt, &state.Final, &state.Applied, &state.AppliedLSN, &appliedAt); err != nil {
			return nil, err
		}
		state.TakenAt, state.AppliedAt = nullTime(takenAt), nullTime(appliedAt)
		result = append(result, state)
	}
	return result, rows.Err()
}

func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

// Recover reads the target's state once; it reports false when the target
// could not be read. On an availability group the state is on the replica
// the databases are restored on, which may no longer be the primary.
func (a *Agent) Recover(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	target, err := a.db(a.config.Target)
	if err != nil {
		log.Printf("recover: %v", err)
		return false
	}
	state, found, err := a.readState(ctx, target)
	if err != nil {
		log.Printf("recover: read the target: %s", sqlError(err))
		return false
	}
	// On an availability group the state is on the replica the databases are
	// restored on, and a move of the primary may have left an older one.
	others, err := a.statesOnReplicas(ctx, target)
	if err != nil {
		log.Printf("recover: read the availability group replicas: %s", sqlError(err))
		return false
	}
	for _, other := range others {
		if !found || other.updatedAt.After(state.updatedAt) {
			state, found = other, true
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageRecovering {
		return true // torn down meanwhile
	}
	if !found {
		a.stage = stageIdle
		return true
	}
	options, err := decodeOptions(json.RawMessage(state.options))
	if err != nil {
		a.stage, a.failure = stageRunning, "The migration state on the target cannot be read: "+err.Error()
		return true
	}
	a.options, a.phase, a.paused, a.notes, a.databases = options, state.phase, state.paused, state.notes, state.databases
	a.failure, a.resumable = state.failure, state.resumable
	a.replica = a.config.Target.WithHost(state.replica)
	if state.phase == phaseCompleted {
		a.stage, a.initialSync = stageCompleted, true
		return true
	}
	a.stage = stageRunning
	a.step = phaseStep(state.phase)
	log.Printf("recovered a migration in phase %s on %s", state.phase, a.replica.Address())
	if a.failure == "" {
		a.launchLocked()
	}
	return true
}

// statesOnReplicas reads the migration's state on the replicas of the
// target's availability groups; unreachable replicas are skipped.
func (a *Agent) statesOnReplicas(ctx context.Context, target *sql.DB) ([]persistedState, error) {
	hosts, err := replicaHosts(ctx, target)
	if err != nil {
		return nil, err
	}
	var states []persistedState
	for _, host := range hosts {
		db, err := a.db(a.config.Target.WithHost(host))
		if err != nil {
			continue
		}
		state, found, err := a.readState(ctx, db)
		if err != nil {
			log.Printf("recover: read replica %s: %s", host, sqlError(err))
			continue
		}
		if found {
			states = append(states, state)
		}
	}
	return states, nil
}

// replicaHosts lists the hosts of the server's availability replicas.
func replicaHosts(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT endpoint_url FROM sys.availability_replicas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var endpoint string
		if err := rows.Scan(&endpoint); err != nil {
			return nil, err
		}
		if host := endpointHost(endpoint); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts, rows.Err()
}

// launchLocked starts the background run; a.mu must be held.
func (a *Agent) launchLocked() {
	// A torn-down migration must not recreate what Teardown removed.
	if a.running || a.stage == stageStopped {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.running, a.cancel, a.done = true, cancel, done
	go func() {
		defer close(done)
		a.run(ctx)
	}()
}

// Start runs the preflight, copies the databases and ships their logs in
// the background. Calling it again after the first call does nothing.
func (a *Agent) Start(options Options) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage == stageRecovering {
		return fmt.Errorf("%w: the worker is still reading the migration state from the target", errUnavailable)
	}
	if a.stage != stageIdle {
		return nil // the reconciler retries Start after a lost response
	}
	a.stage, a.phase, a.options = stageRunning, phasePreparing, options
	a.launchLocked()
	return nil
}

// Pause stops taking and restoring log backups. The source keeps its log
// meanwhile, so it grows until the migration resumes.
func (a *Agent) Pause(ctx context.Context) error {
	a.mu.Lock()
	ok := a.stage == stageRunning && a.phase == phaseShipping && a.failure == "" && !a.paused
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: the migration can be paused only while it ships log backups", errConflict)
	}
	if err := a.persistPaused(ctx, true); err != nil {
		return err
	}
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	return nil
}

// Resume continues a paused migration, or retries one that failed for a
// reason that has been fixed.
func (a *Agent) Resume(ctx context.Context) error {
	a.mu.Lock()
	current, paused, failure, resumable := a.stage, a.paused, a.failure, a.resumable
	a.mu.Unlock()
	switch {
	case current != stageRunning:
		return fmt.Errorf("%w: there is no migration to resume", errConflict)
	case failure != "" && !resumable:
		return fmt.Errorf("%w: this failure cannot be resumed; delete the migration and create it again", errConflict)
	case failure != "":
		db, err := a.replicaDB()
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, setFailureSQL, "", true, a.config.Name); err != nil && !isMissingState(err) {
			return fmt.Errorf("record the resume on the target: %s", sqlError(err))
		}
		a.mu.Lock()
		if a.stage != stageRunning || a.failure != failure {
			a.mu.Unlock()
			return fmt.Errorf("%w: the migration changed meanwhile", errConflict)
		}
		a.failure, a.transient = "", ""
		a.launchLocked()
		a.mu.Unlock()
	case paused:
		if err := a.persistPaused(ctx, false); err != nil {
			return err
		}
		a.mu.Lock()
		a.paused = false
		a.mu.Unlock()
	default:
		return fmt.Errorf("%w: the migration is neither paused nor failed", errConflict)
	}
	a.signal()
	return nil
}

// isMissingState tolerates a failure recorded before the state table existed
// (the target could not be reached).
func isMissingState(err error) bool { return hasSQLError(err, 208) }

// phaseStep is the status message of a phase until a step replaces it.
func phaseStep(phase string) string {
	switch phase {
	case phaseCopying:
		return "Copying the databases"
	case phaseShipping:
		return shippingStep
	case phaseCutover:
		return "Taking and restoring the final log backups"
	}
	return ""
}

const shippingStep = "Shipping log backups"

func (a *Agent) persistPaused(ctx context.Context, paused bool) error {
	db, err := a.replicaDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, setPausedSQL, paused, a.config.Name); err != nil {
		return fmt.Errorf("record the pause on the target: %s", sqlError(err))
	}
	return nil
}

// Cutover records the request; the background run then takes a final log
// backup of every database, started after the request so it holds every
// transaction committed before it, restores it and recovers the databases.
func (a *Agent) Cutover(ctx context.Context) error {
	a.mu.Lock()
	current, phase, failure, initialSync := a.stage, a.phase, a.failure, a.initialSync
	a.mu.Unlock()
	if current == stageRunning && phase == phaseCutover {
		return nil
	}
	if current != stageRunning || phase != phaseShipping || failure != "" || !initialSync {
		return fmt.Errorf("%w: cutover is possible once the target has caught up", errConflict)
	}
	db, err := a.replicaDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE `+stateTable+` SET phase = @p1, paused = 0, updated_at = SYSUTCDATETIME() WHERE name = @p2 AND phase = @p3`,
		phaseCutover, a.config.Name, phaseShipping); err != nil {
		return fmt.Errorf("record the cutover on the target: %s", sqlError(err))
	}
	a.mu.Lock()
	a.phase, a.paused = phaseCutover, false
	a.mu.Unlock()
	a.signal()
	return nil
}

// Teardown stops background work and removes everything the migration
// created on the source, the target and the bucket, except the migrated
// databases. Idempotent.
func (a *Agent) Teardown(ctx context.Context) error {
	a.mu.Lock()
	recovering := a.stage == stageRecovering
	a.mu.Unlock()
	if recovering && !a.Recover(ctx) {
		return errors.New("cannot reach the target to read the migration state")
	}
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	done := a.done
	a.stage = stageStopped
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return errors.New("the migration did not stop in time")
		}
	}

	if err := a.dropCredential(ctx, a.config.Source); err != nil {
		return fmt.Errorf("remove the S3 credential on the source: %w", err)
	}
	a.mu.Lock()
	pinned := a.replica
	a.mu.Unlock()
	if err := a.dropCredential(ctx, pinned); err != nil {
		return fmt.Errorf("remove the S3 credential on the target: %w", err)
	}
	replica, err := a.db(pinned)
	if err != nil {
		return err
	}
	if err := a.config.Storage.RemoveAll(ctx); err != nil {
		return fmt.Errorf("remove the backups from the bucket: %w", err)
	}
	if _, err := replica.ExecContext(ctx, deleteStateSQL, a.config.Name); err != nil {
		return fmt.Errorf("remove the migration state on the target: %s", sqlError(err))
	}
	if _, err := replica.ExecContext(ctx, dropEmptyStateSQL); err != nil {
		log.Printf("drop the empty state tables on the target: %s", sqlError(err))
	}
	// A move of the target's primary may have left state on another replica.
	hosts, _ := replicaHosts(ctx, replica)
	for _, host := range hosts {
		if strings.EqualFold(host, pinned.Host) {
			continue
		}
		if db, err := a.db(a.config.Target.WithHost(host)); err == nil {
			if _, err := db.ExecContext(ctx, deleteStateSQL+dropEmptyStateSQL, a.config.Name); err != nil {
				log.Printf("remove the migration state on %s: %s", host, sqlError(err))
			}
		}
	}
	return nil
}

// dropCredential removes the migration's S3 credential from a server and
// every replica of its availability groups: credentials are server objects,
// and the source gets one on whichever replica is the primary.
func (a *Agent) dropCredential(ctx context.Context, endpoint Endpoint) error {
	db, err := a.db(endpoint)
	if err != nil {
		return err
	}
	statement := dropCredentialStatement(a.config.Storage.CredentialName())
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return errors.New(sqlError(err))
	}
	hosts, err := replicaHosts(ctx, db)
	if err != nil {
		return fmt.Errorf("list the availability group replicas: %s", sqlError(err))
	}
	for _, host := range hosts {
		if strings.EqualFold(host, endpoint.Host) {
			continue
		}
		replica, err := a.db(endpoint.WithHost(host))
		if err == nil {
			_, err = replica.ExecContext(ctx, statement)
		}
		if err != nil {
			return fmt.Errorf("replica %s: %s", host, sqlError(err))
		}
	}
	return nil
}

// ensureCredentials creates the S3 credential where it is missing: an
// availability group source fails over to a replica that has none.
func (a *Agent) ensureCredentials(ctx context.Context) error {
	for _, side := range []struct {
		role string
		db   func() (*sql.DB, error)
	}{{"source", a.source}, {"target", a.replicaDB}} {
		db, err := side.db()
		if err != nil {
			return err
		}
		var exists int
		if err := db.QueryRowContext(ctx, credentialExistsSQL, a.config.Storage.CredentialName()).Scan(&exists); err != nil {
			return fmt.Errorf("read the credentials on the %s: %s", side.role, sqlError(err))
		}
		if exists > 0 {
			continue
		}
		log.Printf("creating the S3 credential on the %s", side.role)
		if err := ensureCredential(ctx, db, a.config.Storage); err != nil {
			return fmt.Errorf("create the S3 credential on the %s: %s", side.role, sqlError(err))
		}
	}
	return nil
}

func (a *Agent) Status(ctx context.Context) Status {
	a.mu.Lock()
	current, phase, step, failure, transient := a.stage, a.phase, a.step, a.failure, a.transient
	paused, notes := a.paused, a.notes
	databases := append([]databaseState(nil), a.databases...)
	sourceClock, sourceClockAt := a.sourceClock, a.sourceClockAt
	var copying *copyProgress
	if a.copying != nil {
		progress := *a.copying
		copying = &progress
	}
	a.mu.Unlock()

	switch current {
	case stageRecovering:
		return Status{State: StateStopped, Message: "Connecting to the target to read the migration state"}
	case stageIdle:
		return Status{State: StateIdle}
	case stageStopped:
		return Status{State: StateStopped, Message: "Stopped"}
	}
	total, copied := a.bytes(ctx, databases, copying)
	status := Status{BytesTotal: total, BytesCopied: copied, Message: step, Error: transient}
	status.CopyDone = phase != phasePreparing && phase != phaseCopying
	if status.CopyDone {
		status.LagSeconds = lagSeconds(databases, sourceClock, sourceClockAt, time.Now())
		a.mu.Lock()
		if !a.initialSync && caughtUp(databases, status.LagSeconds) {
			a.initialSync = true
		}
		status.InitialSyncCompleted = a.initialSync
		a.mu.Unlock()
	}
	switch {
	case current == stageCompleted:
		return Status{State: StateCompleted, Message: strings.TrimSpace("Migration completed. " + notes), CopyDone: true,
			InitialSyncCompleted: true, BytesTotal: total, BytesCopied: total}
	case failure != "":
		status.State, status.Error = StateFailed, failure
	case phase == phaseCutover:
		status.State = StateFinalizing
	case paused:
		status.State, status.Message = StatePaused, "Paused: no log backups are taken; the source keeps its log meanwhile"
	default:
		status.State = StateRunning
	}
	return status
}

// caughtUp requires a restored log backup of every database, not only the
// full backup, and a lag within caughtUpLag.
func caughtUp(databases []databaseState, lag int64) bool {
	if len(databases) == 0 {
		return false
	}
	for _, database := range databases {
		if database.Stage == dbCopying || database.Seq < 1 || (!database.Applied && database.Seq == 1) {
			return false
		}
	}
	return time.Duration(lag)*time.Second <= caughtUpLag
}

// lagSeconds is the age, on the source's clock, of the oldest last restored
// backup: everything committed on the source before that backup started is
// on the target.
func lagSeconds(databases []databaseState, sourceClock, sourceClockAt, now time.Time) int64 {
	if sourceClock.IsZero() {
		return 0
	}
	sourceNow := sourceClock.Add(now.Sub(sourceClockAt))
	var lag time.Duration
	for _, database := range databases {
		if database.AppliedAt == nil {
			continue
		}
		lag = max(lag, sourceNow.Sub(*database.AppliedAt))
	}
	return int64(lag.Round(time.Second) / time.Second)
}

// bytes estimates the copy's progress from the databases' data file sizes
// and, for the database being copied, its backup's and restore's progress.
func (a *Agent) bytes(ctx context.Context, databases []databaseState, copying *copyProgress) (uint64, uint64) {
	var total, copied int64
	for _, database := range databases {
		total += database.SizeBytes
		if database.Stage != dbCopying {
			copied += database.SizeBytes
		}
	}
	if copying == nil {
		return uint64(total), uint64(copied)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var backup, restore float64
	if copying.backupDone {
		backup = 100
	} else if source, err := a.source(); err == nil && copying.backupSession != 0 {
		backup = sessionPercent(ctx, source, copying.backupSession)
	}
	if replica, err := a.replicaDB(); err == nil && copying.restoreSession != 0 {
		restore = sessionPercent(ctx, replica, copying.restoreSession)
	}
	copied += int64(float64(copying.size) * (backup + restore) / 200)
	return uint64(total), uint64(min(copied, total))
}

func sessionPercent(ctx context.Context, db *sql.DB, session int64) float64 {
	var percent sql.NullFloat64
	if err := db.QueryRowContext(ctx, `SELECT percent_complete FROM sys.dm_exec_requests WHERE session_id = @p1`, session).Scan(&percent); err != nil {
		return 0
	}
	return percent.Float64
}
