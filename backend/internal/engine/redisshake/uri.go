package redisshake

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const defaultPort = "6379"

// Keys OpenEverest Valkey instances publish next to the well-known ones.
const (
	propertySSL           = "ssl"
	propertyClientCert    = "tls.crt"
	propertyClientKey     = "tls.key"
	errorURIMustBeRedis   = "must be a redis://, rediss://, valkey:// or valkeys:// URI with a host"
	errorURIOnlyDatabase0 = "must use database 0: a Valkey cluster has no other databases"
)

// redisURI is a parsed redis[s]:// or valkey[s]:// URI.
type redisURI struct {
	host, port, user, password string
	tls                        bool
}

// parseRedisURI applies connection's credentials, which take precedence over
// userinfo in the URI. Errors never include the URI: it may hold credentials.
func parseRedisURI(connection engine.Connection) (redisURI, error) {
	parsed, err := url.Parse(strings.TrimSpace(connection.URI))
	if err != nil || parsed.Hostname() == "" {
		return redisURI{}, errors.New(errorURIMustBeRedis)
	}
	uri := redisURI{host: parsed.Hostname(), port: parsed.Port()}
	switch parsed.Scheme {
	case "redis", "valkey":
	case "rediss", "valkeys":
		uri.tls = true
	default:
		return redisURI{}, errors.New(errorURIMustBeRedis)
	}
	if strings.Contains(parsed.Host, ",") {
		return redisURI{}, errors.New("must name a single host; for a cluster, any node")
	}
	if database := strings.Trim(parsed.Path, "/"); database != "" && database != "0" {
		return redisURI{}, errors.New(errorURIOnlyDatabase0)
	}
	if uri.port == "" {
		uri.port = defaultPort
	}
	if parsed.User != nil {
		uri.user = parsed.User.Username()
		uri.password, _ = parsed.User.Password()
	}
	if connection.Username != "" || connection.Password != "" {
		uri.user, uri.password = connection.Username, connection.Password
	}
	if connection.Properties[propertySSL] == "true" {
		uri.tls = true
	}
	return uri, nil
}

// workerURI is what the worker connects with; TLS files are passed as paths.
func (u redisURI) workerURI(ca, cert, key string) (string, error) {
	if !u.tls && (ca != "" || cert != "") {
		return "", errors.New("has a CA or client certificate but does not use TLS (rediss://)")
	}
	scheme := "redis"
	if u.tls {
		scheme = "rediss"
	}
	query := url.Values{}
	for name, path := range map[string]string{"ca": ca, "cert": cert, "key": key} {
		if path != "" {
			query.Set(name, path)
		}
	}
	result := url.URL{Scheme: scheme, Host: u.endpoint(), Path: "/", RawQuery: query.Encode()}
	if u.user != "" || u.password != "" {
		result.User = url.UserPassword(u.user, u.password)
	}
	return result.String(), nil
}

// endpoint identifies the server, ignoring credentials and options.
func (u redisURI) endpoint() string { return net.JoinHostPort(strings.ToLower(u.host), u.port) }
