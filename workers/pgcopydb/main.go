// Command pgcopydb-agent runs one pgcopydb migration and exposes an HTTP API
// the plugin-migrations backend uses to start, observe and finish it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"
)

const (
	defaultPort              = "2242"
	defaultWorkDir           = "/work"
	defaultHeartbeatInterval = 5 * time.Second
	maxRequestBytes          = 1 << 16
	teardownTimeout          = 2 * time.Minute
)

// slotNamePattern matches what PostgreSQL accepts for slot and origin names.
var slotNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,63}$`)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	config, err := configFromEnv()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	agent := NewAgent(config)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	agent.DetectLostState(ctx)
	go agent.RunHeartbeats(ctx)

	server := &http.Server{Addr: ":" + envOr("PORT", defaultPort), Handler: newHandler(agent), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("pgcopydb agent listening on %s (slot %s)", server.Addr, config.SlotName)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func configFromEnv() (Config, error) {
	config := Config{
		SourceURI:         os.Getenv("PGCOPYDB_SOURCE_PGURI"),
		TargetURI:         os.Getenv("PGCOPYDB_TARGET_PGURI"),
		TargetAdminURI:    os.Getenv("TARGET_ADMIN_PGURI"),
		TargetDatabase:    os.Getenv("TARGET_DATABASE"),
		SlotName:          os.Getenv("SLOT_NAME"),
		WorkDir:           envOr("WORK_DIR", defaultWorkDir),
		HeartbeatInterval: defaultHeartbeatInterval,
	}
	if config.SourceURI == "" || config.TargetURI == "" || config.TargetAdminURI == "" || config.TargetDatabase == "" {
		return Config{}, errors.New("PGCOPYDB_SOURCE_PGURI, PGCOPYDB_TARGET_PGURI, TARGET_ADMIN_PGURI and TARGET_DATABASE are required")
	}
	if !slotNamePattern.MatchString(config.SlotName) {
		return Config{}, errors.New("SLOT_NAME must be 1-63 lowercase letters, digits or underscores")
	}
	if raw := os.Getenv("HEARTBEAT_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval < time.Second {
			return Config{}, fmt.Errorf("HEARTBEAT_INTERVAL must be a duration of at least 1s")
		}
		config.HeartbeatInterval = interval
	}
	return config, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type startRequest struct {
	Options json.RawMessage `json:"options"`
}

func newHandler(agent *Agent) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, agent.Status(r.Context()))
	})
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		var request startRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes)).Decode(&request); err != nil {
			writeError(w, fmt.Errorf("%w: body must be a JSON object", errInvalidBody))
			return
		}
		options, err := decodeOptions(request.Options)
		if err != nil {
			writeError(w, fmt.Errorf("%w: %v", errInvalidBody, err))
			return
		}
		writeResult(w, agent.Start(r.Context(), options))
	})
	mux.HandleFunc("POST /pause", func(w http.ResponseWriter, r *http.Request) { writeResult(w, agent.Pause(r.Context())) })
	mux.HandleFunc("POST /resume", func(w http.ResponseWriter, r *http.Request) { writeResult(w, agent.Resume(r.Context())) })
	mux.HandleFunc("POST /cutover", func(w http.ResponseWriter, r *http.Request) { writeResult(w, agent.Cutover(r.Context())) })
	mux.HandleFunc("POST /teardown", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), teardownTimeout)
		defer cancel()
		writeResult(w, agent.Teardown(ctx))
	})
	return mux
}

func writeResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, errConflict):
		status = http.StatusConflict
	case errors.Is(err, errInvalidBody):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
