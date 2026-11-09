package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openeverest/plugin-migrations/backend/internal/api"
	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/mysqlreplication"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/pcsm"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/postgreslogical"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
	"github.com/openeverest/plugin-migrations/backend/internal/kube"
	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

// The frontend bundle is copied here by the Docker build; the repository keeps
// a placeholder so the backend builds before the frontend.
//
//go:embed dist/main.js
var distFS embed.FS

//go:embed dist/icon.svg
var iconData []byte

const shutdownTimeout = 15 * time.Second

// compiledEngines lists every engine built into this binary. Configuration
// only enables them and selects their images.
var compiledEngines = map[string]func() engine.Engine{
	pcsm.ID:             pcsm.New,
	postgreslogical.ID:  postgreslogical.New,
	mysqlreplication.ID: mysqlreplication.New,
}

// engineConfig is one entry of the ENGINES JSON rendered from Helm values.
type engineConfig struct {
	Enabled bool `json:"enabled"`
	Image   struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		PullPolicy string `json:"pullPolicy"`
	} `json:"image"`
}

func engineRegistryFromEnv() (*engine.Registry, error) {
	var configs map[string]engineConfig
	if err := decodeJSONEnv("ENGINES", &configs); err != nil {
		return nil, err
	}
	registrations := make([]engine.Registration, 0, len(configs))
	for _, id := range slices.Sorted(maps.Keys(configs)) {
		config := configs[id]
		if !config.Enabled {
			continue
		}
		constructor, ok := compiledEngines[id]
		if !ok {
			return nil, fmt.Errorf("engine %q is enabled but not compiled into this build", id)
		}
		if config.Image.Repository == "" || config.Image.Tag == "" {
			return nil, fmt.Errorf("engine %q requires image.repository and image.tag", id)
		}
		pullPolicy := config.Image.PullPolicy
		if pullPolicy == "" {
			pullPolicy = "IfNotPresent"
		}
		registrations = append(registrations, engine.Registration{
			Engine: constructor(),
			Runner: engine.RunnerConfig{Image: config.Image.Repository + ":" + config.Image.Tag, ImagePullPolicy: pullPolicy},
		})
	}
	return engine.NewRegistry(registrations...)
}

func workerConfigFromEnv() (kube.WorkerConfig, error) {
	var config kube.WorkerConfig
	for key, target := range map[string]any{
		"WORKER_RESOURCES":          &config.Resources,
		"WORKER_NODE_SELECTOR":      &config.NodeSelector,
		"WORKER_TOLERATIONS":        &config.Tolerations,
		"WORKER_IMAGE_PULL_SECRETS": &config.ImagePullSecrets,
	} {
		if err := decodeJSONEnv(key, target); err != nil {
			return kube.WorkerConfig{}, err
		}
	}
	return config, nil
}

// decodeJSONEnv leaves target unchanged when the variable is unset and rejects
// unknown fields so misspelled Helm values fail at startup.
func decodeJSONEnv(key string, target any) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" || value == "null" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	if decoder.More() {
		return fmt.Errorf("parse %s: unexpected data after JSON value", key)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func run() error {
	registry, err := engineRegistryFromEnv()
	if err != nil {
		return fmt.Errorf("initialize engines: %w", err)
	}
	workerConfig, err := workerConfigFromEnv()
	if err != nil {
		return err
	}
	reconcileInterval, err := time.ParseDuration(envOrDefault("RECONCILE_INTERVAL", "5s"))
	if err != nil || reconcileInterval < time.Second {
		return errors.New("RECONCILE_INTERVAL must be a duration of at least 1s")
	}
	namespace := envOrDefault("WORKLOAD_NAMESPACE", os.Getenv("POD_NAMESPACE"))
	if namespace == "" {
		return errors.New("WORKLOAD_NAMESPACE or POD_NAMESPACE is required")
	}
	port := envOrDefault("PORT", "8080")
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("PORT must be a number: %w", err)
	}

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("load in-cluster Kubernetes config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}

	everestClient := everest.NewClient(everest.APIURLFromEnv())
	service := migration.NewService(registry, kube.NewStore(clientset, namespace), kube.NewWorkloads(clientset, namespace, workerConfig), everestClient)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go service.RunReconciler(ctx, reconcileInterval)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /main.js", handleBundle)
	mux.HandleFunc("GET /icon.svg", handleIcon)
	mux.HandleFunc("GET /healthz", handleHealth)
	api.New(service, everestClient).RegisterRoutes(mux)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("plugin-migrations listening on :%s (workload namespace %s, engines %d)", port, namespace, len(registry.Descriptors()))
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

var bundle = loadBundle()

type bundleData struct {
	content []byte
	etag    string
}

// loadBundle reads the embedded bundle once; a new release ships a new image.
func loadBundle() bundleData {
	content, err := distFS.ReadFile("dist/main.js")
	if err != nil {
		return bundleData{}
	}
	sum := sha256.Sum256(content)
	return bundleData{content: content, etag: `"` + hex.EncodeToString(sum[:]) + `"`}
}

// handleBundle serves the stable /main.js URL with a content ETag so browsers
// revalidate cheaply and still pick up new releases immediately.
func handleBundle(w http.ResponseWriter, r *http.Request) {
	if bundle.content == nil {
		http.Error(w, "frontend bundle not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", bundle.etag)
	if r.Header.Get("If-None-Match") == bundle.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(bundle.content)
}

func handleIcon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(iconData)
}
