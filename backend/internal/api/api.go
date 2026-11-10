// Package api exposes the migration service over HTTP (docs/architecture.md §8).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

const maxRequestBytes = 128 << 10

type MigrationService interface {
	Engines() []engine.Descriptor
	Create(ctx context.Context, token string, request migration.CreateRequest) (migration.View, error)
	List(ctx context.Context, token string) ([]migration.View, error)
	Get(ctx context.Context, token, id string) (migration.View, error)
	Act(ctx context.Context, token, id string, action migration.Action) (migration.View, error)
	Delete(ctx context.Context, token, id string, force bool) error
}

// InstanceLister lists the OpenEverest resources the create form picks from.
type InstanceLister interface {
	ListInstances(ctx context.Context, token, k8sCluster string) ([]everest.InstanceSummary, error)
	ListBackupStorages(ctx context.Context, token, k8sCluster, namespace string) ([]everest.BackupStorageSummary, error)
}

type API struct {
	migrations MigrationService
	instances  InstanceLister
}

func New(migrations MigrationService, instances InstanceLister) *API {
	return &API{migrations: migrations, instances: instances}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("GET /api/instances", a.withToken(a.handleListInstances))
	mux.HandleFunc("GET /api/backup-storages", a.withToken(a.handleListBackupStorages))
	mux.HandleFunc("GET /api/migrations", a.withToken(a.handleListMigrations))
	mux.HandleFunc("POST /api/migrations", a.withToken(a.handleCreateMigration))
	mux.HandleFunc("GET /api/migrations/{id}", a.withToken(a.handleGetMigration))
	mux.HandleFunc("DELETE /api/migrations/{id}", a.withToken(a.handleDeleteMigration))
	mux.HandleFunc("POST /api/migrations/{id}/{action}", a.withToken(a.handleMigrationAction))
}

type tokenHandler func(w http.ResponseWriter, r *http.Request, token string)

// withToken prefers the host's X-Everest-User JWT and falls back to the
// forwarded Authorization bearer token; both are accepted by OpenEverest.
func (a *API) withToken(next tokenHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.Header.Get("X-Everest-User"))
		if token == "" {
			scheme, value, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
			if found && strings.EqualFold(scheme, "Bearer") {
				token = strings.TrimSpace(value)
			}
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "authentication is required")
			return
		}
		next(w, r, token)
	}
}

func (a *API) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ready",
		"storage": "kubernetes",
		"engines": a.migrations.Engines(),
	})
}

func (a *API) handleListInstances(w http.ResponseWriter, r *http.Request, token string) {
	k8sCluster := strings.TrimSpace(r.URL.Query().Get("k8sCluster"))
	if k8sCluster == "" {
		writeError(w, http.StatusBadRequest, "k8sCluster is required")
		return
	}
	instances, err := a.instances.ListInstances(r.Context(), token, k8sCluster)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": instances})
}

func (a *API) handleListBackupStorages(w http.ResponseWriter, r *http.Request, token string) {
	k8sCluster := strings.TrimSpace(r.URL.Query().Get("k8sCluster"))
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if k8sCluster == "" || namespace == "" {
		writeError(w, http.StatusBadRequest, "k8sCluster and namespace are required")
		return
	}
	storages, err := a.instances.ListBackupStorages(r.Context(), token, k8sCluster, namespace)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backupStorages": storages})
}

func (a *API) handleListMigrations(w http.ResponseWriter, r *http.Request, token string) {
	views, err := a.migrations.List(r.Context(), token)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"migrations": views})
}

func (a *API) handleCreateMigration(w http.ResponseWriter, r *http.Request, token string) {
	var request migration.CreateRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := a.migrations.Create(r.Context(), token, request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (a *API) handleGetMigration(w http.ResponseWriter, r *http.Request, token string) {
	view, err := a.migrations.Get(r.Context(), token, r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *API) handleDeleteMigration(w http.ResponseWriter, r *http.Request, token string) {
	force := r.URL.Query().Get("force") == "true"
	if err := a.migrations.Delete(r.Context(), token, r.PathValue("id"), force); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMigrationAction(w http.ResponseWriter, r *http.Request, token string) {
	action := migration.Action(r.PathValue("action"))
	switch action {
	case migration.ActionPause, migration.ActionResume, migration.ActionCutover:
	default:
		writeError(w, http.StatusNotFound, "unknown action")
		return
	}
	view, err := a.migrations.Act(r.Context(), token, r.PathValue("id"), action)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

// decodeJSON returns client-safe errors that never echo request values,
// because the body may contain a connection string with credentials.
func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesErr *http.MaxBytesError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &maxBytesErr):
			return errors.New("request body is too large")
		case errors.As(err, &typeErr) && typeErr.Field != "":
			return errors.New("field " + typeErr.Field + " has the wrong type")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return errors.New(strings.TrimPrefix(err.Error(), "json: "))
		}
		return errors.New("request body must be a JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, everest.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "OpenEverest rejected the credentials")
		return
	case errors.Is(err, everest.ErrForbidden):
		writeError(w, http.StatusForbidden, "you do not have access to the selected instance")
		return
	case errors.Is(err, everest.ErrNotFound):
		writeError(w, http.StatusNotFound, "instance not found")
		return
	case errors.Is(err, everest.ErrUpstream):
		writeError(w, http.StatusBadGateway, "OpenEverest API request failed")
		return
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "request timed out")
		return
	}
	var message string
	var migrationErr *migration.Error
	var engineErr *engine.Error
	if errors.As(err, &migrationErr) {
		message = migrationErr.Message
	} else if errors.As(err, &engineErr) {
		message = engineErr.Message
	}
	switch migration.KindOf(err) {
	case migration.KindInvalid:
		writeError(w, http.StatusBadRequest, message)
	case migration.KindNotFound:
		writeError(w, http.StatusNotFound, message)
	case migration.KindConflict:
		writeError(w, http.StatusConflict, message)
	case migration.KindUnavailable:
		writeError(w, http.StatusBadGateway, message)
	default:
		log.Printf("internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}
