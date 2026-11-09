package mysqlreplication

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

// errInvalidURI never includes the URI because it may contain credentials.
var errInvalidURI = errors.New("must be a mysql:// connection URI with a host")

const defaultPort = "3306"

// SSL modes use MySQL client names, which the worker understands.
const (
	sslDisabled       = "DISABLED"
	sslPreferred      = "PREFERRED"
	sslRequired       = "REQUIRED"
	sslVerifyCA       = "VERIFY_CA"
	sslVerifyIdentity = "VERIFY_IDENTITY"
)

// mysqlURI is a parsed mysql://user:password@host:port/database URI. TLS is
// read from ssl-mode (MySQL client names) or tls (Go driver names, which
// OpenEverest providers publish).
type mysqlURI struct {
	host, port, user, password, database, sslMode string
}

// parseMySQLURI applies connection's credentials, which take precedence over
// userinfo in the URI.
func parseMySQLURI(raw string, connection engine.Connection) (mysqlURI, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "mysql" || parsed.Hostname() == "" {
		return mysqlURI{}, errInvalidURI
	}
	if strings.Contains(parsed.Host, ",") {
		return mysqlURI{}, errors.New("must name a single host; multi-host URIs are not supported")
	}
	uri := mysqlURI{host: parsed.Hostname(), port: parsed.Port(), sslMode: sslPreferred}
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
		return mysqlURI{}, errors.New("must include a user")
	}
	uri.database, _ = url.PathUnescape(strings.Trim(parsed.Path, "/"))
	query := parsed.Query()
	switch mode := strings.ToUpper(strings.TrimSpace(query.Get("ssl-mode"))); mode {
	case "":
		switch strings.ToLower(strings.TrimSpace(query.Get("tls"))) {
		case "", "preferred":
		case "false":
			uri.sslMode = sslDisabled
		case "true":
			uri.sslMode = sslVerifyIdentity
		case "skip-verify":
			uri.sslMode = sslRequired
		default:
			return mysqlURI{}, errors.New("has an unsupported tls value; use ssl-mode instead")
		}
	case sslDisabled, sslPreferred, sslRequired, sslVerifyCA, sslVerifyIdentity:
		uri.sslMode = mode
	default:
		return mysqlURI{}, errors.New("has an unknown ssl-mode")
	}
	return uri, nil
}

// workerURI is what the worker connects with. A CA makes the worker verify
// the server unless TLS is disabled or the URI asks for host verification.
func (u mysqlURI) workerURI(caPath string) (string, error) {
	query := url.Values{}
	mode := u.sslMode
	if caPath != "" && mode != sslDisabled {
		query.Set("ssl-ca", caPath)
		if mode != sslVerifyIdentity {
			mode = sslVerifyCA
		}
	}
	if (mode == sslVerifyCA || mode == sslVerifyIdentity) && caPath == "" {
		return "", errors.New("verifies the server certificate (ssl-mode VERIFY_CA/VERIFY_IDENTITY or tls=true) but no CA certificate was provided")
	}
	query.Set("ssl-mode", mode)
	result := url.URL{Scheme: "mysql", User: url.UserPassword(u.user, u.password), Host: u.endpoint(), Path: "/", RawQuery: query.Encode()}
	return result.String(), nil
}

// endpoint identifies the server, ignoring credentials and options.
func (u mysqlURI) endpoint() string { return net.JoinHostPort(strings.ToLower(u.host), u.port) }
