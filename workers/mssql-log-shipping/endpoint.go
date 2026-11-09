package main

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const defaultSQLPort = "1433"

// Endpoint is a SQL Server connection. It holds credentials: never log it.
type Endpoint struct {
	Host, Port, User, Password string
	// Encrypt is go-mssqldb's encrypt value: "true", "strict" or "disable".
	Encrypt string
	// TrustServerCertificate skips certificate verification.
	TrustServerCertificate bool
	// CertificateFile is a CA bundle that verifies the server.
	CertificateFile string
	// HostNameInCertificate is the name the certificate must carry; set when
	// the endpoint is pinned to one replica of the server it was given for.
	HostNameInCertificate string
}

// parseEndpoint reads the sqlserver:// URI the backend builds. Errors never
// include the URI: it holds credentials.
func parseEndpoint(raw string) (Endpoint, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "sqlserver" || parsed.Hostname() == "" || parsed.User == nil {
		return Endpoint{}, errors.New("must be a sqlserver://user:password@host:port URI")
	}
	endpoint := Endpoint{Host: parsed.Hostname(), Port: parsed.Port(), User: parsed.User.Username(), Encrypt: "true"}
	endpoint.Password, _ = parsed.User.Password()
	if endpoint.Port == "" {
		endpoint.Port = defaultSQLPort
	}
	query := parsed.Query()
	switch encrypt := strings.ToLower(query.Get("encrypt")); encrypt {
	case "", "true":
	case "strict", "disable":
		endpoint.Encrypt = encrypt
	default:
		return Endpoint{}, errors.New("encrypt must be true, strict or disable")
	}
	endpoint.TrustServerCertificate = strings.EqualFold(query.Get("TrustServerCertificate"), "true")
	endpoint.CertificateFile = query.Get("certificate")
	endpoint.HostNameInCertificate = query.Get("hostNameInCertificate")
	if endpoint.User == "" {
		return Endpoint{}, errors.New("must include a user")
	}
	return endpoint, nil
}

func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, e.Port) }

// WithHost pins the endpoint to another host of the same server (an
// availability group replica). The certificate keeps being checked against
// the original name.
func (e Endpoint) WithHost(host string) Endpoint {
	if host == "" || strings.EqualFold(host, e.Host) {
		return e
	}
	if e.HostNameInCertificate == "" {
		e.HostNameInCertificate = e.Host
	}
	e.Host = host
	return e
}

// dsn is the go-mssqldb connection string. "connection timeout" also bounds
// queries in go-mssqldb, so it stays 0: callers bound work with contexts,
// and backups and restores may run for hours.
func (e Endpoint) dsn() string {
	query := url.Values{}
	query.Set("database", "master")
	query.Set("app name", "openeverest-migration")
	query.Set("encrypt", e.Encrypt)
	query.Set("connection timeout", "0")
	query.Set("dial timeout", "15")
	if e.TrustServerCertificate {
		query.Set("TrustServerCertificate", "true")
	}
	if e.CertificateFile != "" {
		query.Set("certificate", e.CertificateFile)
	}
	if e.HostNameInCertificate != "" {
		query.Set("hostNameInCertificate", e.HostNameInCertificate)
	}
	return (&url.URL{Scheme: "sqlserver", User: url.UserPassword(e.User, e.Password), Host: e.Address(), RawQuery: query.Encode()}).String()
}
