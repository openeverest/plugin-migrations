// Command tidb-dm-agent migrates MySQL databases into TiDB with TiDB Data
// Migration (DM). It runs a dm-master and a dm-worker in the same pod, drives
// them through dm-master's HTTP API and exposes the HTTP API the
// plugin-migrations backend drives.
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
	"sync"
	"syscall"
	"time"
)

const (
	defaultPort              = "2243"
	defaultDataDir           = "/data"
	defaultBinDir            = "/"
	defaultHeartbeatInterval = 5 * time.Second
	maxRequestBytes          = 1 << 16
	teardownTimeout          = 2 * time.Minute
)

// migrationNamePattern keeps names valid as DM source and task names and in
// database names: "openeverest_" + name fits in 64 characters.
var migrationNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,52}$`)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	config, err := configFromEnv()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	dm := NewDM(config.BinDir, config.DataDir)
	agent := NewAgent(config, dm)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		if err := dm.Run(ctx); err != nil {
			log.Fatalf("run DM: %v", err)
		}
	}()
	go agent.RecoverLoop(ctx)
	go agent.RunSupervisor(ctx)

	server := &http.Server{Addr: ":" + envOr("PORT", defaultPort), Handler: newHandler(agent), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("tidb-dm agent listening on %s (migration %s)", server.Addr, config.Name)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	background.Wait()
}

func configFromEnv() (Config, error) {
	config := Config{
		Name:              os.Getenv("MIGRATION_NAME"),
		DataDir:           envOr("DATA_DIR", defaultDataDir),
		BinDir:            envOr("DM_BIN_DIR", defaultBinDir),
		HeartbeatInterval: defaultHeartbeatInterval,
	}
	var err error
	if config.Source, err = parseEndpoint(os.Getenv("SOURCE_URI")); err != nil {
		return Config{}, fmt.Errorf("SOURCE_URI %w", err)
	}
	if config.Target, err = parseEndpoint(os.Getenv("TARGET_URI")); err != nil {
		return Config{}, fmt.Errorf("TARGET_URI %w", err)
	}
	if !migrationNamePattern.MatchString(config.Name) {
		return Config{}, errors.New("MIGRATION_NAME must be 1-52 lowercase letters, digits or underscores")
	}
	if raw := os.Getenv("HEARTBEAT_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval < time.Second {
			return Config{}, errors.New("HEARTBEAT_INTERVAL must be a duration of at least 1s")
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
		writeResult(w, agent.Start(options))
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
	case errors.Is(err, errUnavailable):
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
