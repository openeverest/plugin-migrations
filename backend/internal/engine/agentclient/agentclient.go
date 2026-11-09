// Package agentclient drives the migration agents built in this repository
// (workers/postgres-logical, workers/mysql-replication). They share one HTTP
// API: GET /status, POST /start, /pause, /resume, /cutover and /teardown.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const maxResponseBytes = 1 << 20

// StartBody turns the stored engine options into the agent's /start body.
type StartBody func(options json.RawMessage) (any, error)

// Client implements engine.Controller and engine.Teardowner.
type Client struct {
	baseURL        string
	httpClient     *http.Client
	teardownClient *http.Client
	startBody      StartBody
}

// New returns a client for the agent at baseURL. Teardown gets its own,
// usually longer, timeout.
func New(baseURL string, httpClient, teardownClient *http.Client, startBody StartBody) *Client {
	return &Client{baseURL: baseURL, httpClient: httpClient, teardownClient: teardownClient, startBody: startBody}
}

// AgentStatus is the agent's GET /status body.
type AgentStatus struct {
	State                string `json:"state"`
	Message              string `json:"message"`
	Error                string `json:"error"`
	CopyDone             bool   `json:"copyDone"`
	InitialSyncCompleted bool   `json:"initialSyncCompleted"`
	BytesTotal           uint64 `json:"bytesTotal"`
	BytesCopied          uint64 `json:"bytesCopied"`
	LagSeconds           int64  `json:"lagSeconds"`
}

func (c *Client) Start(ctx context.Context, options json.RawMessage) error {
	body, err := c.startBody(options)
	if err != nil {
		return err
	}
	return c.do(ctx, c.httpClient, http.MethodPost, "/start", body, nil)
}

func (c *Client) Pause(ctx context.Context) error {
	return c.do(ctx, c.httpClient, http.MethodPost, "/pause", nil, nil)
}

func (c *Client) Resume(ctx context.Context) error {
	return c.do(ctx, c.httpClient, http.MethodPost, "/resume", nil, nil)
}

func (c *Client) Cutover(ctx context.Context) error {
	return c.do(ctx, c.httpClient, http.MethodPost, "/cutover", nil, nil)
}

func (c *Client) Teardown(ctx context.Context) error {
	return c.do(ctx, c.teardownClient, http.MethodPost, "/teardown", nil, nil)
}

func (c *Client) Status(ctx context.Context) (engine.Status, error) {
	var response AgentStatus
	if err := c.do(ctx, c.httpClient, http.MethodGet, "/status", nil, &response); err != nil {
		return engine.Status{}, err
	}
	return MapStatus(response)
}

// MapStatus maps the agent's states onto the shared phases.
func MapStatus(response AgentStatus) (engine.Status, error) {
	status := engine.Status{
		Message:              response.Message,
		Error:                response.Error,
		InitialSyncCompleted: response.InitialSyncCompleted,
		Progress: engine.Progress{
			BytesTotal:  response.BytesTotal,
			BytesCopied: response.BytesCopied,
			LagSeconds:  response.LagSeconds,
		},
	}
	switch response.State {
	case "idle":
		status.Phase = engine.PhaseStarting
		status.Idle = true
	case "running":
		switch {
		case !response.CopyDone:
			status.Phase = engine.PhaseFullLoad
		case !response.InitialSyncCompleted:
			status.Phase = engine.PhaseCatchingUp
		default:
			status.Phase = engine.PhaseReplicating
		}
	case "paused":
		status.Phase = engine.PhasePaused
	case "finalizing":
		status.Phase = engine.PhaseFinalizing
	case "completed":
		status.Phase = engine.PhaseCompleted
	case "failed":
		status.Phase = engine.PhaseFailed
	case "stopped":
		status.Phase = engine.PhaseUnknown
	default:
		return engine.Status{}, &engine.Error{Kind: engine.KindUnavailable, Message: "the migration worker reported an unknown state"}
	}
	status.Progress.ReadyForCutover = status.InitialSyncCompleted &&
		(status.Phase == engine.PhaseReplicating || status.Phase == engine.PhasePaused)
	return status, nil
}

func (c *Client) do(ctx context.Context, client *http.Client, method, path string, body, destination any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode migration worker request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("build migration worker request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return &engine.Error{Kind: engine.KindUnavailable, Message: "the migration worker is unreachable"}
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return &engine.Error{Kind: engine.KindUnavailable, Message: "failed to read the migration worker response"}
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &failure)
		message := failure.Error
		if message == "" {
			message = fmt.Sprintf("the migration worker returned HTTP %d", response.StatusCode)
		}
		kind := engine.KindUnavailable
		switch response.StatusCode {
		case http.StatusConflict:
			kind = engine.KindConflict
		case http.StatusBadRequest:
			kind = engine.KindInvalidRequest
		}
		return &engine.Error{Kind: kind, Message: message}
	}
	if destination == nil {
		return nil
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return &engine.Error{Kind: engine.KindUnavailable, Message: "the migration worker returned an invalid response"}
	}
	return nil
}
