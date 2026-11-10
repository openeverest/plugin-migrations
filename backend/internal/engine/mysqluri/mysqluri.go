// Package mysqluri parses the MySQL connection URIs of migration endpoints
// and turns them into the URIs the MySQL-protocol workers (mysql-replication,
// tidb-dm) read. TLS is normalized to MySQL client ssl-mode names.
package mysqluri

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const DefaultPort = "3306"

// SSL modes use MySQL client names, which the workers understand.
const (
	SSLDisabled       = "DISABLED"
	SSLPreferred      = "PREFERRED"
	SSLRequired       = "REQUIRED"
	SSLVerifyCA       = "VERIFY_CA"
	SSLVerifyIdentity = "VERIFY_IDENTITY"
)

// URI is a parsed ******host:port/database URI. TLS is read
// from ssl-mode (MySQL client names) or tls (Go driver names, which
// OpenEverest providers publish).
type URI struct {
	Host, Port, User, Password, Database, SSLMode string
}

// Parse applies connection's credentials, which take precedence over
// userinfo in the URI. Errors never include the URI: it may hold credentials.
func Parse(raw string, connection engine.Connection, schemes []string) (URI, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !slices.Contains(schemes, parsed.Scheme) || parsed.Hostname() == "" {
		return URI{}, fmt.Errorf("must be a %s:// connection URI with a host", strings.Join(schemes, ":// or "))
	}
	if strings.Contains(parsed.Host, ",") {
		return URI{}, errors.New("must name a single host; multi-host URIs are not supported")
	}
	uri := URI{Host: parsed.Hostname(), Port: parsed.Port(), SSLMode: SSLPreferred}
	if uri.Port == "" {
		uri.Port = DefaultPort
	}
	if parsed.User != nil {
		uri.User = parsed.User.Username()
		uri.Password, _ = parsed.User.Password()
	}
	if connection.Username != "" {
		uri.User, uri.Password = connection.Username, connection.Password
	}
	if uri.User == "" {
		return URI{}, errors.New("must include a user")
	}
	uri.Database, _ = url.PathUnescape(strings.Trim(parsed.Path, "/"))
	query := parsed.Query()
	switch mode := strings.ToUpper(strings.TrimSpace(query.Get("ssl-mode"))); mode {
	case "":
		switch strings.ToLower(strings.TrimSpace(query.Get("tls"))) {
		case "", "preferred":
		case "false":
			uri.SSLMode = SSLDisabled
		case "true":
			uri.SSLMode = SSLVerifyIdentity
		case "skip-verify":
			uri.SSLMode = SSLRequired
		default:
			return URI{}, errors.New("has an unsupported tls value; use ssl-mode instead")
		}
	case SSLDisabled, SSLPreferred, SSLRequired, SSLVerifyCA, SSLVerifyIdentity:
		uri.SSLMode = mode
	default:
		return URI{}, errors.New("has an unknown ssl-mode")
	}
	return uri, nil
}

// WorkerURI is what the worker connects with. A CA makes the worker verify
// the server unless TLS is disabled or the URI asks for host verification.
func (u URI) WorkerURI(caPath string) (string, error) {
	query := url.Values{}
	mode := u.EffectiveSSLMode(caPath != "")
	if caPath != "" && mode != SSLDisabled {
		query.Set("ssl-ca", caPath)
	}
	if (mode == SSLVerifyCA || mode == SSLVerifyIdentity) && caPath == "" {
		return "", errors.New("verifies the server certificate (ssl-mode VERIFY_CA/VERIFY_IDENTITY or tls=true) but no CA certificate was provided")
	}
	query.Set("ssl-mode", mode)
	result := url.URL{Scheme: "mysql", User: url.UserPassword(u.User, u.Password), Host: u.Endpoint(), Path: "/", RawQuery: query.Encode()}
	return result.String(), nil
}

// EffectiveSSLMode is the mode the worker uses: a CA upgrades PREFERRED and
// REQUIRED to VERIFY_CA.
func (u URI) EffectiveSSLMode(hasCA bool) string {
	if hasCA && u.SSLMode != SSLDisabled && u.SSLMode != SSLVerifyIdentity {
		return SSLVerifyCA
	}
	return u.SSLMode
}

// Endpoint identifies the server, ignoring credentials and options.
func (u URI) Endpoint() string { return net.JoinHostPort(strings.ToLower(u.Host), u.Port) }

// MountCA adds a CA to the worker's secret files and returns its path, or ""
// without a CA.
func MountCA(caCert, key string, worker *engine.WorkerSpec) string {
	if caCert == "" {
		return ""
	}
	worker.SecretData[key] = []byte(caCert)
	worker.SecretFiles[key] = key
	return engine.SecretMountPath + "/" + key
}
