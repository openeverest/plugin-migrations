package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Agent states reported by GET /status.
const (
	StateIdle       = "idle"
	StateRunning    = "running"
	StateFinalizing = "finalizing"
	StateCompleted  = "completed"
	StateFailed     = "failed"
	StateStopped    = "stopped"
)

const (
	// caughtUpLag marks the initial sync complete: changes reach the target
	// close enough to real time for a cutover to be quick.
	caughtUpLag    = 15 * time.Second
	pollInterval   = time.Second
	setupTimeout   = time.Minute
	cutoverTimeout = 30 * time.Second
	// drainTimeout bounds the wait for the last changes at the cutover; the
	// target had caught up (lag ≤ 15s) when it started.
	drainTimeout = 15 * time.Minute
	// maxCopyAttempts limits fresh copies that fail before the copy finishes:
	// every attempt makes the source fork for a snapshot.
	maxCopyAttempts = 5
	maxBackoff      = 2 * time.Minute
)

// Phases stored in the target's state key.
const (
	phaseCopying = "copying"
	// phaseFailed waits for a resume, also after a worker restart.
	phaseFailed = "failed"
	// phaseStopped: the cutover stopped RedisShake; only cleanup remains.
	phaseStopped   = "stopped"
	phaseCompleted = "completed"
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
	WorkDir           string
	ShakeBinary       string
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
	// stageRecovering lasts until the target's state key has been read.
	stageRecovering stage = iota
	stageIdle
	stageRunning
	stageCutover
	stageCompleted
	stageFailed
	stageStopped
)

type Agent struct {
	config Config

	mu      sync.Mutex
	stage   stage
	step    string
	failure string
	options Options
	cancel  context.CancelFunc
	// runDone is closed when the current run loop returns.
	runDone chan struct{}
	// cutoverDone is closed when the cutover goroutine returns.
	cutoverDone chan struct{}
	process     *shakeProcess
	// interruption explains why the copy started again; attempts counts
	// fresh copies since the last one that finished.
	interruption string
	attempts     int
	retryAt      time.Time
	shake        shakeStatus
	shakeRead    bool
	lag          time.Duration
	lagKnown     bool
	initialSync  bool
	// beating is false during the cutover, which writes the last heartbeat.
	beating bool
	// owned is set once the migration recorded itself on the target: from
	// then on the target holds only its data and may be emptied for a new
	// copy. Before, the preflight must accept the target first.
	owned bool
	// slots holds the heartbeat key slots: one per source and target shard
	// pair that exchange data.
	slots []int
	// pendingBeats are heartbeat timestamps written to the source and not
	// yet seen on the target, oldest first.
	pendingBeats []int64
	// beatMu orders heartbeat writes against the cutover marker; lastBeat is
	// the newest value written, so the marker can be newer than all of them.
	beatMu   sync.Mutex
	lastBeat int64

	connMu sync.Mutex
	source *server
	target *server
}

func NewAgent(config Config) *Agent {
	return &Agent{config: config}
}

func (a *Agent) sourceServer(ctx context.Context) (*server, error) {
	return a.connection(ctx, &a.source, a.config.Source)
}

func (a *Agent) targetServer(ctx context.Context) (*server, error) {
	return a.connection(ctx, &a.target, a.config.Target)
}

func (a *Agent) connection(ctx context.Context, slot **server, endpoint Endpoint) (*server, error) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if *slot != nil {
		return *slot, nil
	}
	s, err := connect(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	*slot = s
	return s, nil
}

// state is the target's record of the migration, so a restarted worker
// knows what it was doing.
type state struct {
	Phase   string  `json:"phase"`
	Options Options `json:"options"`
	Error   string  `json:"error,omitempty"`
}

func (a *Agent) writeState(ctx context.Context, phase string, options Options, failure string) error {
	target, err := a.targetServer(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(state{Phase: phase, Options: options, Error: failure})
	if err != nil {
		return err
	}
	return target.client.Set(ctx, stateKey(a.config.Name), encoded, 0).Err()
}

// RecoverLoop restores the stage after a worker restart, retrying until the
// target answers.
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

// Recover reads the state key once and reports false when the target could
// not be read. A copy that was running starts again from the beginning:
// RedisShake cannot resume one.
func (a *Agent) Recover(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	target, err := a.targetServer(ctx)
	if err != nil {
		log.Printf("recover: connect to the target: %v", err)
		return false
	}
	raw, err := target.client.Get(ctx, stateKey(a.config.Name)).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Printf("recover: read the migration state: %v", err)
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageRecovering {
		return true
	}
	if errors.Is(err, redis.Nil) {
		a.stage = stageIdle
		return true
	}
	var saved state
	if err := json.Unmarshal(raw, &saved); err != nil {
		a.stage, a.failure = stageFailed, "The migration state on the target cannot be read. Delete this migration and create it again."
		return true
	}
	a.options, a.owned = saved.Options, true
	switch saved.Phase {
	case phaseCompleted:
		a.stage, a.initialSync = stageCompleted, true
	case phaseStopped:
		// RedisShake was stopped with every change applied; never copy again.
		a.stage, a.initialSync = stageCompleted, true
		done := make(chan struct{})
		a.cutoverDone = done
		go func() {
			defer close(done)
			if err := a.finishCutover(saved.Options); err != nil {
				log.Printf("recover: finish the cutover: %v", err)
			}
		}()
	case phaseFailed:
		a.stage, a.failure = stageFailed, saved.Error
	default:
		log.Print("recovered a running migration: copying again from the beginning")
		a.interruption = "the migration worker restarted"
		_ = a.startRunLocked(true)
	}
	return true
}

// Start runs the preflight and the migration in the background. Calling it
// again after the first call does nothing.
func (a *Agent) Start(options Options) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.stage {
	case stageRecovering:
		return fmt.Errorf("%w: the worker is still reading the migration state from the target", errUnavailable)
	case stageIdle:
	default:
		return nil // the reconciler retries Start after a lost response
	}
	a.options = options
	return a.startRunLocked(false)
}

// Resume starts a failed migration again, with a fresh copy. A migration
// that failed its preflight runs the preflight again.
func (a *Agent) Resume() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage != stageFailed {
		return fmt.Errorf("%w: only a failed migration can be resumed", errConflict)
	}
	a.interruption = ""
	return a.startRunLocked(a.owned)
}

// Pause is not supported: RedisShake cannot continue a stopped run, so
// resuming would copy everything again.
func (a *Agent) Pause() error {
	return fmt.Errorf("%w: this engine cannot pause a migration", errConflict)
}

// startRunLocked starts the run loop; owned says the target holds only this
// migration's data, so it may be emptied without the preflight's consent.
func (a *Agent) startRunLocked(owned bool) error {
	for _, done := range []chan struct{}{a.runDone, a.cutoverDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		default:
			return fmt.Errorf("%w: the previous run is still stopping; try again in a moment", errConflict)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.stage, a.failure, a.step, a.cancel, a.runDone = stageRunning, "", "Checking the source and target", cancel, done
	a.attempts, a.initialSync, a.lagKnown, a.shakeRead, a.beating = 0, false, false, false, true
	a.pendingBeats = nil
	options := a.options
	go func() {
		defer close(done)
		err := a.run(ctx, options, owned)
		if ctx.Err() != nil || err == nil {
			return
		}
		log.Printf("migration failed: %v", err)
		a.recordFailure(err.Error())
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.stage == stageRunning {
			a.stage, a.failure = stageFailed, err.Error()
		}
	}()
	return nil
}

// recordFailure keeps a failure across worker restarts, so a restarted
// worker waits for a resume instead of emptying the target and copying.
func (a *Agent) recordFailure(failure string) {
	a.mu.Lock()
	owned, options := a.owned, a.options
	a.mu.Unlock()
	if !owned {
		return // the preflight failed: the target is not the migration's
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	if err := a.writeState(ctx, phaseFailed, options, failure); err != nil {
		log.Printf("record the failure on the target: %v", err)
	}
}

// run copies and replicates until the context ends. Interrupted runs start
// over with an empty target.
func (a *Agent) run(ctx context.Context, options Options, owned bool) error {
	if !owned {
		if err := a.preflight(ctx, options); err != nil {
			return err
		}
	}
	// Once RedisShake has written to the target, every new copy must start
	// from an empty target, or keys deleted on the source meanwhile stay.
	emptyTarget := owned
	for {
		a.mu.Lock()
		a.attempts++
		attempt := a.attempts
		a.mu.Unlock()
		if attempt > maxCopyAttempts {
			return fmt.Errorf("The copy failed %d times in a row; last error: %s. Check the source's client-output-buffer-limit "+
				"for replicas and repl-timeout, which can close the connection during a large copy, then resume.",
				maxCopyAttempts, a.lastInterruption())
		}
		err := a.runOnce(ctx, options, emptyTarget)
		emptyTarget = true
		if ctx.Err() != nil {
			return nil
		}
		a.mu.Lock()
		running := a.stage == stageRunning
		a.mu.Unlock()
		if !running || !transientError(err.Error()) {
			return fmt.Errorf("Migration stopped: %s", err)
		}
		backoff := min(maxBackoff, time.Duration(5<<min(attempt-1, 5))*time.Second)
		log.Printf("interrupted: %v; copying again in %s", err, backoff)
		a.mu.Lock()
		a.interruption, a.retryAt, a.shakeRead, a.lagKnown, a.initialSync = err.Error(), time.Now().Add(backoff), false, false, false
		a.pendingBeats = nil
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

func (a *Agent) lastInterruption() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.interruption
}

func (a *Agent) setStep(step string) {
	a.mu.Lock()
	a.step = step
	a.mu.Unlock()
	log.Print(step)
}

func (a *Agent) preflight(ctx context.Context, options Options) error {
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	source, err := a.sourceServer(ctx)
	if err != nil {
		return fmt.Errorf("Cannot connect to the source: %v", err)
	}
	target, err := a.targetServer(ctx)
	if err != nil {
		return fmt.Errorf("Cannot connect to the target: %v", err)
	}
	sourceFacts, err := source.inspect(ctx)
	if err != nil {
		return fmt.Errorf("Cannot read the source: %v", err)
	}
	targetFacts, err := target.inspect(ctx)
	if err != nil {
		return fmt.Errorf("Cannot read the target: %v", err)
	}
	return checkPreflight(a.config.Source, sourceFacts, targetFacts, target.cluster, options)
}

// checkPreflight lists every problem at once so they can be fixed together.
func checkPreflight(sourceEndpoint Endpoint, source, target serverFacts, targetCluster bool, options Options) error {
	var problems []string
	if source.Role != "master" {
		problems = append(problems, fmt.Sprintf("the source is a %s; connect to the primary, which the migration writes a heartbeat key to", source.Role))
	}
	if !source.ClusterOK {
		problems = append(problems, "the source cluster is not healthy (cluster_state is not ok)")
	}
	if !target.ClusterOK {
		problems = append(problems, "the target cluster is not healthy (cluster_state is not ok)")
	}
	if sourceEndpoint.Cert != "" && sourceEndpoint.CA == "" {
		problems = append(problems, "a client certificate for the source needs its CA certificate too")
	}
	if targetCluster {
		var databases []string
		for _, db := range slices.Sorted(maps.Keys(source.Keys)) {
			if db != 0 && source.Keys[db] > 0 {
				databases = append(databases, strconv.Itoa(db))
			}
		}
		if len(databases) > 0 {
			problems = append(problems, "the source has keys in database "+strings.Join(databases, ", ")+
				"; a Valkey cluster has only database 0, move them there or delete them")
		}
	}
	if source.VolatileItems > 0 {
		switch {
		case source.Valkey != target.Valkey:
			problems = append(problems, fmt.Sprintf("the source has %d hash(es) with field expiration, which cannot be copied from %s to %s yet; "+
				"remove the field expirations or migrate between the same server type", source.VolatileItems, source.product(), target.product()))
		case target.Valkey && !versionAtLeast(target.Version, 9):
			problems = append(problems, fmt.Sprintf("the source has hashes with field expiration, which %s does not support", target.product()))
		}
	}
	if keys := target.totalKeys(); keys > 0 && !options.FlushTarget {
		problems = append(problems, fmt.Sprintf("the target already has %d key(s); empty it or enable \"Delete all keys on the target\"", keys))
	}
	for _, id := range source.RunIDs {
		if id != "" && slices.Contains(target.RunIDs, id) {
			problems = append(problems, "the source and the target are the same server")
			break
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("Cannot start the migration: " + strings.Join(problems, "; ") + ".")
}

func versionAtLeast(version string, major int) bool {
	first, _, _ := strings.Cut(version, ".")
	number, err := strconv.Atoi(first)
	return err == nil && number >= major
}

// runOnce empties the target if needed, starts RedisShake and supervises it
// until it exits or the context ends.
func (a *Agent) runOnce(ctx context.Context, options Options, emptyTarget bool) error {
	setupCtx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	source, err := a.sourceServer(setupCtx)
	if err != nil {
		return fmt.Errorf("connect to the source: %v", err)
	}
	target, err := a.targetServer(setupCtx)
	if err != nil {
		return fmt.Errorf("connect to the target: %v", err)
	}
	if emptyTarget || options.FlushTarget {
		a.setStep("Deleting all keys on the target")
		if err := target.flushAll(setupCtx); err != nil {
			return fmt.Errorf("empty the target: %v", err)
		}
	}
	if err := a.writeState(setupCtx, phaseCopying, options, ""); err != nil {
		return fmt.Errorf("record the migration on the target: %v", err)
	}
	a.mu.Lock()
	a.owned = true
	a.mu.Unlock()
	sourceShards, err := source.shardRanges(setupCtx)
	if err != nil {
		return fmt.Errorf("read the source shards: %v", err)
	}
	targetShards, err := target.shardRanges(setupCtx)
	if err != nil {
		return fmt.Errorf("read the target shards: %v", err)
	}
	slots := heartbeatSlots(sourceShards, targetShards)
	a.mu.Lock()
	a.slots = slots
	a.mu.Unlock()
	// The heartbeat keys must exist before the snapshot, so the copy brings
	// them to the target.
	now, err := source.nowMillis(setupCtx)
	if err == nil {
		a.beatMu.Lock()
		err = a.beat(setupCtx, source, now)
		a.beatMu.Unlock()
	}
	if err != nil {
		return fmt.Errorf("write the heartbeat on the source: %v", err)
	}

	a.setStep("Copying data")
	dir := filepath.Join(a.config.WorkDir, "shake")
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clean the work directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the work directory: %w", err)
	}
	configPath := filepath.Join(a.config.WorkDir, "shake.toml")
	config := shakeConfig(a.config.Source, a.config.Target, source.cluster, target.cluster, options, dir)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write the RedisShake configuration: %w", err)
	}
	process, err := startShake(a.config.ShakeBinary, configPath)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.process = process
	a.mu.Unlock()
	defer process.stop()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if exited, exitErr, lastError := process.exited(); exited {
			if lastError == "" {
				lastError = fmt.Sprintf("RedisShake exited (%v)", exitErr)
			}
			return errors.New(lastError)
		}
		pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		status, err := fetchShakeStatus(pollCtx)
		cancel()
		if err != nil {
			continue // not listening yet
		}
		a.mu.Lock()
		a.shake, a.shakeRead = status, true
		if status.copyDone() && a.attempts > 0 {
			// A finished copy resets the limit on failed attempts.
			a.attempts, a.step = 0, ""
		}
		a.mu.Unlock()
	}
}

// beat writes at, a source timestamp, into the heartbeat key of every
// source shard. The caller holds beatMu.
func (a *Agent) beat(ctx context.Context, source *server, at int64) error {
	a.mu.Lock()
	slots := slices.Clone(a.slots)
	a.mu.Unlock()
	for _, slot := range slots {
		if err := source.client.Set(ctx, heartbeatKey(a.config.Name, slot), at, 0).Err(); err != nil {
			return err
		}
	}
	a.lastBeat = max(a.lastBeat, at)
	return nil
}

// oldestBeat is the oldest heartbeat the target has, or false if one is
// missing.
func (a *Agent) oldestBeat(ctx context.Context, target *server) (int64, bool, error) {
	a.mu.Lock()
	slots := slices.Clone(a.slots)
	a.mu.Unlock()
	var oldest int64
	for i, slot := range slots {
		value, err := target.client.Get(ctx, heartbeatKey(a.config.Name, slot)).Int64()
		if errors.Is(err, redis.Nil) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		if i == 0 || value < oldest {
			oldest = value
		}
	}
	return oldest, len(slots) > 0, nil
}

// RunSupervisor writes the heartbeats and measures the lag.
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
		active := a.stage == stageRunning && a.beating && a.shakeRead
		a.mu.Unlock()
		if !active {
			continue
		}
		tickCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		a.measure(tickCtx)
		cancel()
	}
}

// measure reads which heartbeats reached the target, then writes the next
// one. The lag is the age of the oldest heartbeat still on its way, or 0.
func (a *Agent) measure(ctx context.Context) {
	source, err := a.sourceServer(ctx)
	if err != nil {
		return
	}
	target, err := a.targetServer(ctx)
	if err != nil {
		return
	}
	arrived, found, err := a.oldestBeat(ctx, target)
	if err != nil {
		log.Printf("heartbeat: read the target: %v", err)
		return
	}
	now, err := source.nowMillis(ctx)
	if err != nil {
		return
	}
	a.mu.Lock()
	if found {
		a.pendingBeats = slices.DeleteFunc(a.pendingBeats, func(beat int64) bool { return beat <= arrived })
		a.lag, a.lagKnown = 0, true
		if len(a.pendingBeats) > 0 {
			a.lag = max(0, time.Duration(now-a.pendingBeats[0])*time.Millisecond)
		}
		if a.shake.copyDone() && a.lag <= caughtUpLag {
			a.initialSync = true
		}
	}
	a.mu.Unlock()
	a.beatMu.Lock()
	defer a.beatMu.Unlock()
	a.mu.Lock()
	beating := a.beating && a.stage == stageRunning
	a.mu.Unlock()
	if !beating {
		return // the cutover writes the last heartbeat
	}
	if err := a.beat(ctx, source, now); err != nil {
		log.Printf("heartbeat: %v", err)
		return
	}
	a.mu.Lock()
	a.pendingBeats = append(a.pendingBeats, now)
	a.mu.Unlock()
}

// Cutover waits, in the background, until every change made on the source
// before the request has been written to the target, then stops RedisShake.
func (a *Agent) Cutover() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stage == stageCutover {
		return nil
	}
	if a.stage != stageRunning || !a.initialSync || a.failure != "" {
		return fmt.Errorf("%w: cutover is possible once the target has caught up", errConflict)
	}
	a.stage, a.beating = stageCutover, false
	runCancel, runDone := a.cancel, a.runDone
	done := make(chan struct{})
	a.cutoverDone = done
	go func() {
		defer close(done)
		err := a.cutover(runCancel, runDone)
		if err != nil {
			// Never leave RedisShake or the run loop behind a failed cutover.
			runCancel()
			<-runDone
			log.Printf("cutover: %v", err)
		}
		a.mu.Lock()
		stopped := a.stage == stageStopped
		a.mu.Unlock()
		switch {
		case stopped:
		case err != nil:
			a.recordFailure("Cutover failed: " + err.Error())
			a.mu.Lock()
			a.stage, a.failure = stageFailed, "Cutover failed: "+err.Error()
			a.mu.Unlock()
		default:
			a.mu.Lock()
			a.stage = stageCompleted
			a.mu.Unlock()
			log.Print("migration completed")
		}
	}()
	return nil
}

func (a *Agent) cutover(runCancel context.CancelFunc, runDone chan struct{}) error {
	ctx := context.Background()
	source, err := a.sourceServer(ctx)
	if err != nil {
		return err
	}
	target, err := a.targetServer(ctx)
	if err != nil {
		return err
	}
	// Under beatMu no heartbeat is in flight, and none follows (beating is
	// off). The marker is newer than every heartbeat written before, also
	// when the source shards' clocks differ.
	markCtx, cancel := context.WithTimeout(ctx, cutoverTimeout)
	a.beatMu.Lock()
	marker, err := source.nowMillis(markCtx)
	if err == nil {
		marker = max(marker, a.lastBeat+1)
		err = a.beat(markCtx, source, marker)
	}
	a.beatMu.Unlock()
	cancel()
	if err != nil {
		return fmt.Errorf("write the cutover marker on the source: %v", err)
	}
	// The marker in every source/target shard pair proves that every change
	// made before it was applied: each pair is one ordered connection.
	deadline := time.Now().Add(drainTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("the last changes did not reach the target within %s", drainTimeout)
		}
		time.Sleep(pollInterval)
		a.mu.Lock()
		failed := a.stage != stageCutover
		a.mu.Unlock()
		if failed {
			return errors.New("the migration was stopped")
		}
		if exited, _, lastError := a.currentProcessExited(); exited {
			return fmt.Errorf("RedisShake stopped before the last changes were written: %s", lastError)
		}
		pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		oldest, found, err := a.oldestBeat(pollCtx, target)
		cancel()
		if err == nil && found && oldest >= marker {
			break
		}
	}
	runCancel()
	<-runDone
	a.mu.Lock()
	options, stopped := a.options, a.stage == stageStopped
	a.mu.Unlock()
	if stopped {
		return nil // torn down meanwhile; teardown removes the keys
	}
	// From here on a restarted worker must not copy again: every change is
	// on the target, which the application may already use.
	for attempt := 1; ; attempt++ {
		stateCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		err = a.writeState(stateCtx, phaseStopped, options, "")
		cancel()
		if err == nil || attempt == 6 {
			break
		}
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	if err != nil {
		log.Printf("record the stopped copy on the target: %v", err)
	}
	if err := a.finishCutover(options); err != nil {
		// Not a failure: the data is complete. A restarted worker finishes it.
		log.Printf("cutover cleanup: %v", err)
	}
	return nil
}

// finishCutover removes the heartbeat keys and records the completion.
func (a *Agent) finishCutover(options Options) error {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	a.removeHeartbeats(ctx)
	a.mu.Lock()
	stopped := a.stage == stageStopped
	a.mu.Unlock()
	if stopped {
		return nil
	}
	if err := a.writeState(ctx, phaseCompleted, options, ""); err != nil {
		return fmt.Errorf("record the completion on the target: %v", err)
	}
	return nil
}

func (a *Agent) currentProcessExited() (bool, error, string) {
	a.mu.Lock()
	process := a.process
	a.mu.Unlock()
	if process == nil {
		return false, nil, ""
	}
	return process.exited()
}

// removeHeartbeats deletes this migration's heartbeat keys on both sides.
// They are found by name, so a changed shard layout does not leave any.
func (a *Agent) removeHeartbeats(ctx context.Context) {
	pattern := keyPrefix + "{*}:" + a.config.Name + ":heartbeat"
	for _, get := range []func(context.Context) (*server, error){a.sourceServer, a.targetServer} {
		s, err := get(ctx)
		if err != nil {
			log.Printf("remove the heartbeats: %v", err)
			continue
		}
		keys, err := s.scanKeys(ctx, pattern)
		if err == nil {
			err = s.deleteKeys(ctx, keys)
		}
		if err != nil {
			log.Printf("remove the heartbeats on %s: %v", s.endpoint.Address(), err)
		}
	}
}

// Teardown stops RedisShake and removes everything the migration created on
// the source and target except the migrated data. Idempotent.
func (a *Agent) Teardown(ctx context.Context) error {
	a.mu.Lock()
	recovering := a.stage == stageRecovering
	a.mu.Unlock()
	if recovering && !a.Recover(ctx) {
		return errors.New("cannot reach the target to remove the migration state")
	}
	a.mu.Lock()
	cancel, done, cutoverDone := a.cancel, a.runDone, a.cutoverDone
	a.stage = stageStopped
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if cutoverDone != nil {
		<-cutoverDone
	}
	a.removeHeartbeats(ctx)
	target, err := a.targetServer(ctx)
	if err != nil {
		return fmt.Errorf("connect to the target: %v", err)
	}
	if err := target.client.Del(ctx, stateKey(a.config.Name)).Err(); err != nil {
		return fmt.Errorf("remove the migration state on the target: %v", err)
	}
	return nil
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.stage {
	case stageRecovering:
		return Status{State: StateStopped, Message: "Connecting to the target to read the migration state"}
	case stageIdle:
		return Status{State: StateIdle}
	case stageStopped:
		return Status{State: StateStopped, Message: "Stopped"}
	case stageFailed:
		return Status{State: StateFailed, Error: a.failure}
	case stageCompleted:
		return Status{State: StateCompleted, Message: "Migration completed.", CopyDone: true, InitialSyncCompleted: true}
	}
	status := Status{State: StateRunning, InitialSyncCompleted: a.initialSync}
	if a.lagKnown {
		status.LagSeconds = int64(a.lag.Seconds())
	}
	if a.shakeRead {
		status.CopyDone = a.shake.copyDone()
		status.BytesTotal, status.BytesCopied = a.shake.copyBytes()
	}
	switch {
	case a.stage == stageCutover:
		status.State, status.Message = StateFinalizing, "Writing the last changes to the target"
	case !a.retryAt.IsZero() && time.Now().Before(a.retryAt):
		status.Message = fmt.Sprintf("Interrupted (%s). Copying again from the beginning in %s.",
			a.interruption, time.Until(a.retryAt).Round(time.Second))
	case !status.CopyDone && a.interruption != "":
		status.Message = fmt.Sprintf("Copying again from the beginning after an interruption (%s)", a.interruption)
	case !status.CopyDone:
		status.Message = a.step
	case !a.initialSync:
		status.Message = "Catching up with changes made during the copy"
	default:
		status.Message = "Replicating changes"
	}
	return status
}
