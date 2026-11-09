package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type supervision int

const (
	superviseNothing supervision = iota
	// superviseStart (re)applies the filters and starts the channel.
	superviseStart
	// superviseSkip checks whether the failed transaction only touches a
	// database that is not migrated, and skips it if so.
	superviseSkip
)

const (
	// startInterval spaces out attempts to start the channel.
	startInterval = 10 * time.Second
	// restartBackoff limits how often a channel stopped by an error is restarted.
	restartBackoff = time.Minute
)

// planSupervision decides how to keep a channel that should be running
// healthy. A restarted server (skip_replica_start) leaves the channel stopped
// and without filters; filters must be in place before it starts again.
func planSupervision(channel channelState, options Options, name string, sinceLastStart time.Duration) supervision {
	if !channel.Exists || sinceLastStart < startInterval {
		return superviseNothing
	}
	if !samePatterns(channel.WildDo, options.doPatterns(name)) || !samePatterns(channel.IgnoreTable, options.ignoreTables()) {
		return superviseStart
	}
	if channel.SQLRunning != "Yes" && channel.SQLErrno == errUnknownDatabase {
		return superviseSkip
	}
	if channel.stopped() && channel.IOErrno == 0 && channel.SQLErrno == 0 {
		return superviseStart
	}
	if (channel.SQLRunning != "Yes" || channel.IORunning == "No") && sinceLastStart >= restartBackoff {
		return superviseStart
	}
	return superviseNothing
}

var unknownDatabasePattern = regexp.MustCompile(`Unknown database '([^']+)'`)

// skippableDatabase returns the database a failed statement referenced when
// the statement can be skipped: it failed because that database does not
// exist on the target and is not migrated, so it cannot change migrated
// data. With row-based replication (required), data changes never fail this
// way; this is DDL such as CREATE PROCEDURE other.p, which passes the table
// filters because it touches no table.
func skippableDatabase(failure workerError, options Options) (string, bool) {
	if failure.Number != errUnknownDatabase || failure.Transaction == "" {
		return "", false
	}
	match := unknownDatabasePattern.FindStringSubmatch(failure.Message)
	if match == nil || options.migrates(match[1]) || match[1] == migrationDatabase {
		return "", false
	}
	return match[1], true
}

// snapshot is the replication state read from the replica node and the source.
type snapshot struct {
	Channel   channelState
	Worker    workerError
	BeatAt    *time.Time
	SourceNow time.Time
	Paused    bool
}

// deriveStatus maps a snapshot onto agent states. initialSync latches: once
// the target has caught up it stays true.
func deriveStatus(cutover bool, failure string, snap snapshot, initialSync bool, copied uint64) Status {
	status := Status{State: StateRunning, CopyDone: true, BytesTotal: copied, BytesCopied: copied}
	if snap.BeatAt != nil && !snap.SourceNow.IsZero() {
		lag := snap.SourceNow.Sub(*snap.BeatAt)
		if lag < 0 {
			lag = 0
		}
		status.LagSeconds = int64(lag.Seconds())
		initialSync = initialSync || lag <= caughtUpLag
	}
	status.InitialSyncCompleted = initialSync

	switch {
	case failure != "":
		status.State, status.Error = StateFailed, failure
		return status
	case !snap.Channel.Exists:
		status.State, status.Error = StateFailed, "The replication channel no longer exists on the target. Delete this migration and create it again."
		return status
	case cutover:
		status.State, status.Message = StateFinalizing, "Applying the last changes before the cutover"
	case snap.Paused:
		status.State, status.Message = StatePaused, "Replication is paused"
	case !initialSync:
		status.Message = "Catching up with changes made during the copy"
	default:
		status.Message = "Replicating changes"
	}
	if problem := channelProblem(snap.Channel, snap.Worker, snap.Paused); problem != "" {
		status.Error = problem
	}
	return status
}

// channelProblem explains why changes are not flowing, or returns "".
func channelProblem(channel channelState, worker workerError, paused bool) string {
	switch {
	case channel.IOErrno == errSourceFatalRead:
		return "The source no longer has the binary logs this migration needs (" + channel.IOError + "). " +
			"Increase the binary log retention on the source, then delete this migration and create it again."
	case channel.SQLRunning != "Yes" && channel.SQLErrno != 0:
		message := channel.SQLError
		if worker.Message != "" {
			message = worker.Message
		}
		return fmt.Sprintf("Applying changes on the target failed (error %d): %s. Fix the cause; the migration worker retries every minute.",
			channel.SQLErrno, strings.TrimSpace(message))
	case channel.IOErrno != 0 && channel.IORunning != "Yes":
		return "The target cannot read changes from the source (" + channel.IOError + "). It keeps retrying."
	case !paused && channel.stopped():
		return "Replication is stopped on the target; the migration worker restarts it."
	}
	return ""
}
