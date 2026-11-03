package everest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeEverest(t *testing.T, routes map[string]func(http.ResponseWriter)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler, ok := routes[r.URL.EscapedPath()]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handler(w)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL + "/")
}

func respond(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestGetConnectionDecodesCA(t *testing.T) {
	client := fakeEverest(t, map[string]func(http.ResponseWriter){
		"/v1/clusters/main/namespaces/shop/instances/orders/connection": respond(200,
			`{"type":"mongodb","provider":"percona-server-mongodb","uri":"mongodb://u:p@h/admin?tls=true","ca.crt":"PEM"}`),
	})
	details, err := client.GetConnection(context.Background(), "jwt", InstanceRef{"main", "shop", "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if details.Type != "mongodb" || details.CACert != "PEM" || details.URI == "" {
		t.Fatalf("details = %+v", details)
	}
}

func TestStatusErrors(t *testing.T) {
	ref := InstanceRef{"main", "shop", "orders"}
	tests := map[int]error{401: ErrUnauthorized, 403: ErrForbidden, 404: ErrNotFound, 503: ErrUpstream}
	for status, want := range tests {
		client := fakeEverest(t, map[string]func(http.ResponseWriter){
			"/v1/clusters/main/namespaces/shop/instances/orders": respond(status, `{"secret":"s3cret"}`),
		})
		err := client.CheckAccess(context.Background(), "jwt", ref)
		if !errors.Is(err, want) || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("status %d → %v, want %v", status, err, want)
		}
	}
	client := fakeEverest(t, nil)
	if err := client.CheckAccess(context.Background(), "", ref); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty token → %v", err)
	}
}

func TestInvalidJSONDoesNotLeakBody(t *testing.T) {
	client := fakeEverest(t, map[string]func(http.ResponseWriter){
		"/v1/clusters/main/namespaces/shop/instances/orders/connection": respond(200, `{"password": s3cret}`),
	})
	_, err := client.GetConnection(context.Background(), "jwt", InstanceRef{"main", "shop", "orders"})
	if !errors.Is(err, ErrUpstream) || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
}

func TestListInstances(t *testing.T) {
	client := fakeEverest(t, map[string]func(http.ResponseWriter){
		"/v1/clusters/main/namespaces":                     respond(200, `["shop","analytics","shop"]`),
		"/v1/clusters/main/namespaces/shop/instances":      respond(200, `{"items":[{"metadata":{"name":"orders"},"spec":{"providerRef":{"name":"percona-server-mongodb"}}},{"metadata":{"name":"broken"},"spec":{}}]}`),
		"/v1/clusters/main/namespaces/analytics/instances": respond(200, `{"items":[{"metadata":{"name":"events"},"spec":{"providerRef":{"name":"provider-percona-postgresql"}}}]}`),
	})
	instances, err := client.ListInstances(context.Background(), "jwt", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 || instances[0].Namespace != "analytics" || instances[1].Name != "orders" {
		t.Fatalf("instances = %+v", instances)
	}

	failing := fakeEverest(t, map[string]func(http.ResponseWriter){
		"/v1/clusters/main/namespaces":                respond(200, `["shop"]`),
		"/v1/clusters/main/namespaces/shop/instances": respond(403, ``),
	})
	if _, err := failing.ListInstances(context.Background(), "jwt", "main"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
}
