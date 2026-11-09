// Package everest calls the OpenEverest API on behalf of the caller, always
// with the caller's own JWT so OpenEverest enforces that user's permissions.
package everest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnauthorized = errors.New("OpenEverest authentication failed")
	ErrForbidden    = errors.New("OpenEverest access denied")
	ErrNotFound     = errors.New("OpenEverest resource not found")
	ErrUpstream     = errors.New("OpenEverest API failure")
)

const (
	requestTimeout       = 10 * time.Second
	discoveryTimeout     = 20 * time.Second
	discoveryConcurrency = 4
	maxResponseBytes     = 4 << 20
)

// InstanceRef identifies an OpenEverest-managed database instance.
type InstanceRef struct {
	K8sCluster string
	Namespace  string
	Name       string
}

// InstanceSummary is credential-free instance metadata for pickers.
type InstanceSummary struct {
	K8sCluster string `json:"k8sCluster"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
}

// ConnectionDetails mirrors the well-known keys of the instance connection
// Secret. Never log it.
type ConnectionDetails struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	URI      string `json:"uri"`
	// CACert is published by providers whose instances use a private CA.
	CACert string `json:"ca.crt"`
	// Properties holds the provider's other string keys (OpenEverest returns
	// them next to the well-known ones), e.g. an admin connection.
	Properties map[string]string `json:"-"`
}

var connectionDetailsKeys = []string{"type", "provider", "host", "port", "username", "password", "uri", "ca.crt"}

func (d *ConnectionDetails) UnmarshalJSON(data []byte) error {
	type wellKnown ConnectionDetails
	if err := json.Unmarshal(data, (*wellKnown)(d)); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	for key, raw := range all {
		var value string
		if slices.Contains(connectionDetailsKeys, key) || json.Unmarshal(raw, &value) != nil {
			continue
		}
		if d.Properties == nil {
			d.Properties = map[string]string{}
		}
		d.Properties[key] = value
	}
	return nil
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: &http.Client{Timeout: requestTimeout}}
}

// APIURLFromEnv prefers an explicit override, then the in-cluster Service
// variables Kubernetes injects for the "everest" Service, then its DNS name.
func APIURLFromEnv() string {
	if value := strings.TrimSpace(os.Getenv("EVEREST_API_URL")); value != "" {
		return value
	}
	if host := os.Getenv("EVEREST_SERVICE_HOST"); host != "" {
		port := os.Getenv("EVEREST_SERVICE_PORT")
		if port == "" {
			port = "8080"
		}
		return "http://" + host + ":" + port
	}
	return "http://everest.everest-system.svc.cluster.local:8080"
}

func instancePath(ref InstanceRef) string {
	return fmt.Sprintf("/v1/clusters/%s/namespaces/%s/instances/%s",
		url.PathEscape(ref.K8sCluster), url.PathEscape(ref.Namespace), url.PathEscape(ref.Name))
}

// GetConnection requires the caller's read-connection permission on the instance.
func (c *Client) GetConnection(ctx context.Context, token string, ref InstanceRef) (ConnectionDetails, error) {
	var details *ConnectionDetails
	if err := c.getJSON(ctx, token, instancePath(ref)+"/connection", &details); err != nil {
		return ConnectionDetails{}, err
	}
	if details == nil {
		return ConnectionDetails{}, fmt.Errorf("%w: connection details must be a JSON object", ErrUpstream)
	}
	return *details, nil
}

// CheckAccess verifies the caller can read the instance without reading secrets.
func (c *Client) CheckAccess(ctx context.Context, token string, ref InstanceRef) error {
	var ignored json.RawMessage
	return c.getJSON(ctx, token, instancePath(ref), &ignored)
}

type listedInstance struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		ProviderRef struct {
			Name string `json:"name"`
		} `json:"providerRef"`
	} `json:"spec"`
}

// ListInstances returns the instances the caller can read in one Kubernetes
// cluster, sorted by namespace and name.
func (c *Client) ListInstances(ctx context.Context, token, k8sCluster string) ([]InstanceSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	basePath := "/v1/clusters/" + url.PathEscape(k8sCluster) + "/namespaces"
	var namespaces []string
	if err := c.getJSON(ctx, token, basePath, &namespaces); err != nil {
		return nil, err
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		firstErr  error
		instances = make([]InstanceSummary, 0)
		slots     = make(chan struct{}, discoveryConcurrency)
	)
	for _, namespace := range namespaces {
		if strings.TrimSpace(namespace) == "" {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			var listed struct {
				Items []listedInstance `json:"items"`
			}
			err := c.getJSON(ctx, token, basePath+"/"+url.PathEscape(namespace)+"/instances", &listed)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			for _, item := range listed.Items {
				if item.Metadata.Name == "" || item.Spec.ProviderRef.Name == "" {
					continue
				}
				instances = append(instances, InstanceSummary{
					K8sCluster: k8sCluster, Namespace: namespace, Name: item.Metadata.Name, Provider: item.Spec.ProviderRef.Name,
				})
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	slices.SortFunc(instances, func(a, b InstanceSummary) int {
		if n := strings.Compare(a.Namespace, b.Namespace); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	return instances, nil
}

func (c *Client) getJSON(ctx context.Context, token, path string, destination any) error {
	if strings.TrimSpace(token) == "" {
		return ErrUnauthorized
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("%w: build request", ErrUpstream)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: request failed", ErrUpstream)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case response.StatusCode == http.StatusForbidden:
		return ErrForbidden
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: HTTP %d", ErrUpstream, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read response", ErrUpstream)
	}
	// Decoder errors can quote upstream values (including credentials).
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("%w: invalid response", ErrUpstream)
	}
	return nil
}
