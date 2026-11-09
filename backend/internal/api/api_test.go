package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/everest"
	"github.com/openeverest/plugin-migrations/backend/internal/migration"
)

type stubService struct {
	err       error
	lastToken string
	lastID    string
	action    migration.Action
	created   migration.CreateRequest
	lastForce bool
}

func (s *stubService) Engines() []engine.Descriptor {
	return []engine.Descriptor{{ID: "pcsm", DisplayName: "PCSM"}}
}
func (s *stubService) Create(_ context.Context, token string, request migration.CreateRequest) (migration.View, error) {
	s.lastToken, s.created = token, request
	return migration.View{ID: "mig-1", AllowedActions: []migration.Action{migration.ActionDelete}}, s.err
}
func (s *stubService) List(_ context.Context, token string) ([]migration.View, error) {
	s.lastToken = token
	return []migration.View{}, s.err
}
func (s *stubService) Get(_ context.Context, token, id string) (migration.View, error) {
	s.lastToken, s.lastID = token, id
	return migration.View{ID: id}, s.err
}
func (s *stubService) Act(_ context.Context, token, id string, action migration.Action) (migration.View, error) {
	s.lastToken, s.lastID, s.action = token, id, action
	return migration.View{ID: id}, s.err
}
func (s *stubService) Delete(_ context.Context, token, id string, force bool) error {
	s.lastToken, s.lastID, s.lastForce = token, id, force
	return s.err
}

type stubInstances struct{}

func (stubInstances) ListInstances(_ context.Context, _, k8sCluster string) ([]everest.InstanceSummary, error) {
	return []everest.InstanceSummary{{K8sCluster: k8sCluster, Namespace: "shop", Name: "orders", Provider: "percona-server-mongodb"}}, nil
}

func (stubInstances) ListBackupStorages(_ context.Context, _, _, namespace string) ([]everest.BackupStorageSummary, error) {
	return []everest.BackupStorageSummary{{Name: namespace + "-s3", Type: "s3", Bucket: "b", Endpoint: "https://s3"}}, nil
}

func serve(service *stubService, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	New(service, stubInstances{}).RegisterRoutes(mux)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

var bearer = map[string]string{"Authorization": "Bearer user-jwt"}

func errorMessage(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %s", recorder.Body.String())
	}
	return body.Error
}

func TestTokenExtraction(t *testing.T) {
	service := &stubService{}
	if recorder := serve(service, http.MethodGet, "/api/migrations", "", nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", recorder.Code)
	}
	serve(service, http.MethodGet, "/api/migrations", "", bearer)
	if service.lastToken != "user-jwt" {
		t.Fatalf("bearer token = %q", service.lastToken)
	}
	serve(service, http.MethodGet, "/api/migrations", "", map[string]string{"X-Everest-User": "host-jwt", "Authorization": "Bearer user-jwt"})
	if service.lastToken != "host-jwt" {
		t.Fatalf("X-Everest-User must take precedence, got %q", service.lastToken)
	}
}

func TestStatusIsPublicAndListsEngines(t *testing.T) {
	recorder := serve(&stubService{}, http.MethodGet, "/api/status", "", nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"engines":[{"id":"pcsm"`) {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateDecoding(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"unknown field", `{"name":"a","sourceUri":"mongodb://u:s3cret@h"}`, `unknown field "sourceUri"`},
		{"wrong type", `{"name":"a","source":{"uri":42}}`, "has the wrong type"},
		{"trailing object", `{"name":"a"}{"name":"b"}`, "single JSON object"},
		{"not JSON", `mongodb://u:s3cret@h`, "must be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serve(&stubService{}, http.MethodPost, "/api/migrations", test.body, bearer)
			message := errorMessage(t, recorder)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(message, test.want) {
				t.Fatalf("got %d %q, want 400 containing %q", recorder.Code, message, test.want)
			}
			if strings.Contains(message, "s3cret") {
				t.Fatal("decode error echoed request contents")
			}
		})
	}

	service := &stubService{}
	body := `{"name":"orders","engine":"pcsm","source":{"type":"external","uri":"mongodb://h"},"target":{"type":"instance","k8sCluster":"main","namespace":"shop","name":"orders"},"options":{"pauseOnInitialSync":true}}`
	recorder := serve(service, http.MethodPost, "/api/migrations", body, bearer)
	if recorder.Code != http.StatusCreated || service.created.Source.URI != "mongodb://h" || string(service.created.Options) != `{"pauseOnInitialSync":true}` {
		t.Fatalf("create = %d %+v", recorder.Code, service.created)
	}
}

func TestServiceErrorMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
	}{
		{&migration.Error{Kind: migration.KindInvalid, Message: "bad name"}, http.StatusBadRequest},
		{&migration.Error{Kind: migration.KindNotFound, Message: "migration not found"}, http.StatusNotFound},
		{&migration.Error{Kind: migration.KindConflict, Message: "busy"}, http.StatusConflict},
		{&engine.Error{Kind: engine.KindConflict, Message: "cannot pause: not running"}, http.StatusConflict},
		{&engine.Error{Kind: engine.KindUnavailable, Message: "PCSM worker is unreachable"}, http.StatusBadGateway},
		{everest.ErrUnauthorized, http.StatusUnauthorized},
		{fmt.Errorf("wrapped: %w", everest.ErrForbidden), http.StatusForbidden},
		{everest.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("%w: HTTP 500", everest.ErrUpstream), http.StatusBadGateway},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{errors.New("database password=s3cret rejected"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		recorder := serve(&stubService{err: test.err}, http.MethodGet, "/api/migrations/mig-1", "", bearer)
		if recorder.Code != test.status {
			t.Errorf("%v → %d, want %d", test.err, recorder.Code, test.status)
		}
		if strings.Contains(errorMessage(t, recorder), "s3cret") {
			t.Errorf("internal error details leaked: %s", recorder.Body.String())
		}
	}
}

func TestMigrationActions(t *testing.T) {
	service := &stubService{}
	recorder := serve(service, http.MethodPost, "/api/migrations/mig-7/cutover", "", bearer)
	if recorder.Code != http.StatusAccepted || service.lastID != "mig-7" || service.action != migration.ActionCutover {
		t.Fatalf("cutover = %d id=%s action=%s", recorder.Code, service.lastID, service.action)
	}
	if recorder := serve(service, http.MethodPost, "/api/migrations/mig-7/delete", "", bearer); recorder.Code != http.StatusNotFound {
		t.Fatalf("delete must use DELETE, got %d", recorder.Code)
	}
	if recorder := serve(service, http.MethodDelete, "/api/migrations/mig-7", "", bearer); recorder.Code != http.StatusNoContent || service.lastForce {
		t.Fatalf("delete = %d force=%v", recorder.Code, service.lastForce)
	}
	if recorder := serve(service, http.MethodDelete, "/api/migrations/mig-7?force=true", "", bearer); recorder.Code != http.StatusNoContent || !service.lastForce {
		t.Fatalf("forced delete = %d force=%v", recorder.Code, service.lastForce)
	}
}

func TestListInstancesRequiresCluster(t *testing.T) {
	if recorder := serve(&stubService{}, http.MethodGet, "/api/instances", "", bearer); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing cluster = %d", recorder.Code)
	}
	recorder := serve(&stubService{}, http.MethodGet, "/api/instances?k8sCluster=main", "", bearer)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"provider":"percona-server-mongodb"`) {
		t.Fatalf("instances = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestListBackupStorages(t *testing.T) {
	if recorder := serve(&stubService{}, http.MethodGet, "/api/backup-storages?k8sCluster=main", "", bearer); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing namespace = %d", recorder.Code)
	}
	recorder := serve(&stubService{}, http.MethodGet, "/api/backup-storages?k8sCluster=main&namespace=shop", "", bearer)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"backupStorages":[{"name":"shop-s3"`) {
		t.Fatalf("storages = %d %s", recorder.Code, recorder.Body.String())
	}
}
