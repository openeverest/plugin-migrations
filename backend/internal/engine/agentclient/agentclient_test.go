package agentclient

import (
	"testing"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

func TestMapStatus(t *testing.T) {
	tests := []struct {
		in        AgentStatus
		phase     engine.Phase
		cutoverOK bool
	}{
		{AgentStatus{State: "idle"}, engine.PhaseStarting, false},
		{AgentStatus{State: "running"}, engine.PhaseFullLoad, false},
		{AgentStatus{State: "running", CopyDone: true}, engine.PhaseCatchingUp, false},
		{AgentStatus{State: "running", CopyDone: true, InitialSyncCompleted: true}, engine.PhaseReplicating, true},
		{AgentStatus{State: "paused", CopyDone: true, InitialSyncCompleted: true}, engine.PhasePaused, true},
		{AgentStatus{State: "finalizing", CopyDone: true, InitialSyncCompleted: true}, engine.PhaseFinalizing, false},
		{AgentStatus{State: "completed"}, engine.PhaseCompleted, false},
		{AgentStatus{State: "failed", Error: "boom"}, engine.PhaseFailed, false},
	}
	for _, tc := range tests {
		status, err := MapStatus(tc.in)
		if err != nil || status.Phase != tc.phase || status.Progress.ReadyForCutover != tc.cutoverOK {
			t.Errorf("%+v → %+v, %v", tc.in, status, err)
		}
	}
	if _, err := MapStatus(AgentStatus{State: "exploded"}); engine.KindOf(err) != engine.KindUnavailable {
		t.Fatalf("unknown state: %v", err)
	}
}
