package pcsm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const maxResponseBytes = 1 << 20

type controller struct {
	baseURL    string
	httpClient *http.Client
}

// statusResponse is the subset of PCSM's GET /status body this engine reads.
type statusResponse struct {
	Ok                   bool   `json:"ok"`
	Error                string `json:"error"`
	State                string `json:"state"`
	Info                 string `json:"info"`
	LagTimeSeconds       int64  `json:"lagTimeSeconds"`
	EventsApplied        int64  `json:"eventsApplied"`
	LastReplicatedOpTime *struct {
		ISODate string `json:"isoDate"`
	} `json:"lastReplicatedOpTime"`
	InitialSync *struct {
		EstimatedCloneSizeBytes uint64 `json:"estimatedCloneSizeBytes"`
		ClonedSizeBytes         uint64 `json:"clonedSizeBytes"`
		Completed               bool   `json:"completed"`
		CloneCompleted          bool   `json:"cloneCompleted"`
	} `json:"initialSync"`
}

// actionResponse is PCSM's generic {ok, error} reply for POST endpoints.
type actionResponse struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

func (c *controller) Start(ctx context.Context, rawOptions json.RawMessage) error {
	options, err := decodeOptions(rawOptions)
	if err != nil {
		return err
	}
	err = c.action(ctx, "/start", options.startRequest())
	// The reconciler may retry after a lost response; a started run is success.
	if err != nil && strings.Contains(err.Error(), "already running") {
		return nil
	}
	return err
}

func (c *controller) Pause(ctx context.Context) error {
	return c.action(ctx, "/pause", nil)
}

func (c *controller) Resume(ctx context.Context) error {
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	return c.action(ctx, "/resume", map[string]bool{"fromFailure": status.Phase == engine.PhaseFailed})
}

func (c *controller) Cutover(ctx context.Context) error {
	return c.action(ctx, "/finalize", nil)
}

func (c *controller) Status(ctx context.Context) (engine.Status, error) {
	var response statusResponse
	if err := c.do(ctx, http.MethodGet, "/status", nil, &response); err != nil {
		return engine.Status{}, err
	}
	return mapStatus(response)
}

func mapStatus(response statusResponse) (engine.Status, error) {
	status := engine.Status{Message: response.Info, Error: response.Error}
	status.Progress.LagSeconds = response.LagTimeSeconds
	status.Progress.EventsApplied = response.EventsApplied
	if response.LastReplicatedOpTime != nil {
		if at, err := time.Parse(time.RFC3339, response.LastReplicatedOpTime.ISODate); err == nil {
			status.Progress.LastReplicatedAt = &at
		}
	}
	cloneCompleted := false
	if sync := response.InitialSync; sync != nil {
		status.Progress.BytesTotal = sync.EstimatedCloneSizeBytes
		status.Progress.BytesCopied = sync.ClonedSizeBytes
		status.InitialSyncCompleted = sync.Completed
		cloneCompleted = sync.CloneCompleted
	}

	switch response.State {
	case "idle":
		status.Phase = engine.PhaseStarting
		status.Idle = true
	case "running":
		switch {
		case !cloneCompleted:
			status.Phase = engine.PhaseFullLoad
		case !status.InitialSyncCompleted:
			status.Phase = engine.PhaseCatchingUp
		default:
			status.Phase = engine.PhaseReplicating
		}
	case "paused":
		status.Phase = engine.PhasePaused
	case "finalizing":
		status.Phase = engine.PhaseFinalizing
	case "finalized":
		status.Phase = engine.PhaseCompleted
	case "failed":
		status.Phase = engine.PhaseFailed
	default:
		return engine.Status{}, &engine.Error{Kind: engine.KindUnavailable, Message: "PCSM reported an unknown state"}
	}
	status.Progress.ReadyForCutover = status.InitialSyncCompleted &&
		(status.Phase == engine.PhaseReplicating || status.Phase == engine.PhasePaused)
	return status, nil
}

func (c *controller) action(ctx context.Context, path string, body any) error {
	var response actionResponse
	if err := c.do(ctx, http.MethodPost, path, body, &response); err != nil {
		return err
	}
	if !response.Ok {
		message := response.Error
		if message == "" {
			message = "PCSM rejected the request"
		}
		return &engine.Error{Kind: engine.KindConflict, Message: message}
	}
	return nil
}

func (c *controller) do(ctx context.Context, method, path string, body any, destination any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode PCSM request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("build PCSM request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return &engine.Error{Kind: engine.KindUnavailable, Message: "PCSM worker is unreachable"}
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return &engine.Error{Kind: engine.KindUnavailable, Message: "failed to read PCSM response"}
	}
	switch {
	case response.StatusCode == http.StatusConflict:
		// HA standby or a tenure that is not open yet; the caller should retry.
		return &engine.Error{Kind: engine.KindUnavailable, Message: "PCSM worker is not active yet"}
	case response.StatusCode != http.StatusOK:
		return &engine.Error{Kind: engine.KindUnavailable, Message: fmt.Sprintf("PCSM returned HTTP %d", response.StatusCode)}
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return &engine.Error{Kind: engine.KindUnavailable, Message: "PCSM returned an invalid response"}
	}
	return nil
}
