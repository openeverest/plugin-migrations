package mssqllogshipping

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const defaultPort = "1433"

// sqlServerURI is a parsed sqlserver://user:password@host:port URI. TLS
// settings use go-mssqldb's names (encrypt, TrustServerCertificate).
type sqlServerURI struct {
	host, port, user, password, database, encrypt string
	// trust skips certificate verification; set unless the URI asks for it.
	trust bool
}

// parseURI applies the connection's credentials, which take precedence over
// userinfo in the URI. Errors never include the URI: it may hold credentials.
func parseURI(raw string, connection engine.Connection) (sqlServerURI, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "sqlserver" || parsed.Hostname() == "" {
		return sqlServerURI{}, errors.New("must be a sqlserver:// connection URI with a host")
	}
	if strings.Contains(parsed.Host, ",") {
		return sqlServerURI{}, errors.New("must name a single host")
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return sqlServerURI{}, errors.New("must not name an instance in the path; use the port")
	}
	uri := sqlServerURI{host: parsed.Hostname(), port: parsed.Port(), encrypt: "true", trust: true}
	if uri.port == "" {
		uri.port = defaultPort
	}
	if parsed.User != nil {
		uri.user = parsed.User.Username()
		uri.password, _ = parsed.User.Password()
	}
	if connection.Username != "" {
		uri.user, uri.password = connection.Username, connection.Password
	}
	if uri.user == "" {
		return sqlServerURI{}, errors.New("must include a user")
	}
	query := lowerKeys(parsed.Query())
	uri.database = strings.TrimSpace(query.Get("database"))
	switch encrypt := strings.ToLower(strings.TrimSpace(query.Get("encrypt"))); encrypt {
	case "", "true", "yes", "mandatory":
	case "strict":
		uri.encrypt = "strict"
	case "disable":
		uri.encrypt = "disable"
	case "false", "no", "optional":
		// go-mssqldb then encrypts only the login; the worker encrypts all.
	default:
		return sqlServerURI{}, errors.New("has an unknown encrypt value")
	}
	switch strings.ToLower(strings.TrimSpace(query.Get("trustservercertificate"))) {
	case "", "true", "yes":
	case "false", "no":
		uri.trust = false
	default:
		return sqlServerURI{}, errors.New("has an unknown TrustServerCertificate value")
	}
	return uri, nil
}

// lowerKeys makes query keys case-insensitive, as SQL Server clients treat them.
func lowerKeys(values url.Values) url.Values {
	result := url.Values{}
	for key, value := range values {
		result[strings.ToLower(key)] = value
	}
	return result
}

// workerURI is what the worker connects with. A CA makes the worker verify
// the server's certificate; without one it trusts the certificate unless
// the URI asked to verify it against the system roots.
func (u sqlServerURI) workerURI(caPath string) string {
	query := url.Values{}
	query.Set("encrypt", u.encrypt)
	switch {
	case u.encrypt == "disable":
	case caPath != "":
		query.Set("certificate", caPath)
	case u.trust:
		query.Set("TrustServerCertificate", "true")
	}
	result := url.URL{Scheme: "sqlserver", User: url.UserPassword(u.user, u.password), Host: u.endpoint(), RawQuery: query.Encode()}
	return result.String()
}

// endpoint identifies the server, ignoring credentials and options.
func (u sqlServerURI) endpoint() string { return net.JoinHostPort(strings.ToLower(u.host), u.port) }
