package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const defaultRedisPort = 6379

// Endpoint is one Redis or Valkey server (or any node of a cluster). It
// carries credentials: never log it.
type Endpoint struct {
	Host     string
	Port     int
	User     string
	Password string
	TLS      bool
	// CA, Cert and Key are PEM files mounted into the worker.
	CA   string
	Cert string
	Key  string
}

// parseEndpoint reads redis[s]:// or valkey[s]:// URIs, with the TLS files as
// query parameters (ca, cert, key). The error never includes the URI.
func parseEndpoint(raw string) (Endpoint, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return Endpoint{}, errors.New("must be a redis:// or rediss:// URI with a host")
	}
	endpoint := Endpoint{Host: parsed.Hostname(), Port: defaultRedisPort}
	switch parsed.Scheme {
	case "redis", "valkey":
	case "rediss", "valkeys":
		endpoint.TLS = true
	default:
		return Endpoint{}, errors.New("must be a redis:// or rediss:// URI with a host")
	}
	if portText := parsed.Port(); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return Endpoint{}, errors.New("has an invalid port")
		}
		endpoint.Port = port
	}
	if parsed.User != nil {
		endpoint.User = parsed.User.Username()
		endpoint.Password, _ = parsed.User.Password()
	}
	query := parsed.Query()
	endpoint.CA, endpoint.Cert, endpoint.Key = query.Get("ca"), query.Get("cert"), query.Get("key")
	if (endpoint.Cert == "") != (endpoint.Key == "") {
		return Endpoint{}, errors.New("needs both cert and key for a client certificate")
	}
	if !endpoint.TLS && (endpoint.CA != "" || endpoint.Cert != "") {
		return Endpoint{}, errors.New("sets TLS files without TLS; use rediss://")
	}
	return endpoint, nil
}

// Address is host:port, without credentials.
func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// tlsConfig is what the agent's own connections use. With a CA they verify
// the server against the endpoint's host name, also when they reach cluster
// nodes by IP.
func (e Endpoint) tlsConfig() (*tls.Config, error) {
	if !e.TLS {
		return nil, nil
	}
	config := &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	if e.Cert != "" {
		cert, err := tls.LoadX509KeyPair(e.Cert, e.Key)
		if err != nil {
			return nil, fmt.Errorf("load the client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}
	if e.CA == "" {
		config.InsecureSkipVerify = true
		return config, nil
	}
	pem, err := os.ReadFile(e.CA)
	if err != nil {
		return nil, fmt.Errorf("read the CA certificate: %w", err)
	}
	config.RootCAs = x509.NewCertPool()
	if !config.RootCAs.AppendCertsFromPEM(pem) {
		return nil, errors.New("the CA file contains no PEM certificate")
	}
	return config, nil
}
