package main

import (
	"sync"
	"time"
)

// maxHeartbeats bounds memory while the target is far behind (about a day of
// beats at the default interval). Older beats are dropped; lag then reads as at
// least the age of the oldest kept beat.
const maxHeartbeats = 20000

type heartbeat struct {
	lsn uint64
	at  time.Time
}

// LagTracker derives replication lag in seconds from heartbeat writes on the
// source: the lag is the age of the oldest heartbeat the target has not
// replayed yet, which is how long ago the first unapplied change happened.
type LagTracker struct {
	mu    sync.Mutex
	beats []heartbeat
}

func (t *LagTracker) Record(lsn uint64, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.beats = append(t.beats, heartbeat{lsn: lsn, at: at})
	if len(t.beats) > maxHeartbeats {
		t.beats = t.beats[len(t.beats)-maxHeartbeats:]
	}
}

// Lag returns the lag at now for a target that replayed up to replayLSN, and
// forgets beats that were replayed. ok is false before the first heartbeat.
func (t *LagTracker) Lag(replayLSN uint64, now time.Time) (lag time.Duration, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	replayed := 0
	for replayed < len(t.beats) && t.beats[replayed].lsn <= replayLSN {
		replayed++
	}
	if replayed == len(t.beats) {
		if replayed == 0 {
			return 0, false
		}
		t.beats = t.beats[replayed-1:]
		return 0, true
	}
	if replayed > 0 {
		t.beats = t.beats[replayed-1:]
		replayed = 1
	}
	lag = now.Sub(t.beats[replayed].at)
	if lag < 0 {
		lag = 0
	}
	return lag, true
}
