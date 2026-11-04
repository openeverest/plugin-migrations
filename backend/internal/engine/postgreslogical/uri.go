package postgreslogical

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

// errInvalidURI never includes the URI because it may contain credentials.
var errInvalidURI = errors.New("must be a postgres:// or postgresql:// connection URI with a host")

// postgresURI is a parsed libpq connection URI. Only single-host URIs are
// supported: replication needs the writable primary on each side.
type postgresURI struct {
	url *url.URL
}

// parsePostgresURI parses raw and applies connection's credentials, which take
// precedence over userinfo in the URI. If credentials are provided separately,
// a malformed (not percent-encoded) userinfo is discarded instead of rejected.
func parsePostgresURI(raw string, connection engine.Connection) (postgresURI, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil && connection.Username != "" {
		parsed, err = url.Parse(withoutUserinfo(raw))
	}
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
		return postgresURI{}, errInvalidURI
	}
	if strings.Contains(parsed.Host, ",") {
		return postgresURI{}, errors.New("must name a single host; multi-host URIs are not supported")
	}
	if connection.Username != "" {
		parsed.User = url.UserPassword(connection.Username, connection.Password)
	}
	parsed.Fragment = ""
	return postgresURI{url: parsed}, nil
}

func withoutUserinfo(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	authorityEnd := strings.IndexAny(rest, "/?#")
	if authorityEnd < 0 {
		authorityEnd = len(rest)
	}
	if at := strings.LastIndex(rest[:authorityEnd], "@"); at >= 0 {
		rest = rest[at+1:]
	}
	return scheme + "://" + rest
}

// database returns the database named in the path, or "".
func (u postgresURI) database() string {
	name, _ := url.PathUnescape(strings.TrimPrefix(u.url.Path, "/"))
	return name
}

func (u postgresURI) withDatabase(database string) postgresURI {
	clone := *u.url
	clone.Path = "/" + database
	clone.RawPath = ""
	return postgresURI{url: &clone}
}

// withCA verifies the server against a CA file mounted into the worker. An
// explicit sslmode in the URI wins; otherwise the CA implies verify-ca.
func (u postgresURI) withCA(caPath string) postgresURI {
	clone := *u.url
	query := clone.Query()
	query.Set("sslrootcert", caPath)
	if query.Get("sslmode") == "" {
		query.Set("sslmode", "verify-ca")
	}
	clone.RawQuery = query.Encode()
	return postgresURI{url: &clone}
}

func (u postgresURI) String() string { return u.url.String() }

// endpoint identifies the server and port, ignoring credentials and options.
func (u postgresURI) endpoint() string {
	port := u.url.Port()
	if port == "" {
		port = "5432"
	}
	return strings.ToLower(u.url.Hostname()) + ":" + port
}

// display is a credential-free summary such as "db1:5432/shop".
func (u postgresURI) display() string {
	return fmt.Sprintf("%s/%s", u.endpoint(), u.database())
}

// subscriptionConninfo is the source URI the target server itself connects
// with. A CA file mounted in the worker does not exist on the target server,
// so a verifying sslmode becomes require (encrypted, server not verified).
func (u postgresURI) subscriptionConninfo(hasCA bool) postgresURI {
	clone := *u.url
	query := clone.Query()
	query.Del("sslrootcert")
	if mode := query.Get("sslmode"); hasCA && (mode == "" || strings.HasPrefix(mode, "verify-")) {
		query.Set("sslmode", "require")
	}
	clone.RawQuery = query.Encode()
	return postgresURI{url: &clone}
}
