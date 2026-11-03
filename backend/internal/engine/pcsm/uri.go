package pcsm

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// mongoURI is a minimal MongoDB connection-string splitter. net/url rejects
// multi-host authorities, so the URI is split on its structural delimiters.
type mongoURI struct {
	scheme   string
	userinfo string
	hosts    string
	// rest is the path and query, starting with "/" or empty.
	rest string
}

// duplicatePortPattern repairs "host:27017:27017", a known quirk of the
// OpenEverest connection endpoint for some providers.
var duplicatePortPattern = regexp.MustCompile(`:([0-9]+):[0-9]+(,|$)`)

// errInvalidURI never includes the URI because it may contain credentials.
var errInvalidURI = errors.New("must be a mongodb:// or mongodb+srv:// connection string with at least one host")

func parseMongoURI(raw string) (mongoURI, error) {
	raw = strings.TrimSpace(raw)
	scheme, remainder, found := strings.Cut(raw, "://")
	if !found || (scheme != "mongodb" && scheme != "mongodb+srv") {
		return mongoURI{}, errInvalidURI
	}
	authorityEnd := strings.IndexAny(remainder, "/?")
	authority, rest := remainder, ""
	if authorityEnd >= 0 {
		authority, rest = remainder[:authorityEnd], remainder[authorityEnd:]
	}
	if strings.HasPrefix(rest, "?") {
		rest = "/" + rest
	}
	userinfo, hosts := "", authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfo, hosts = authority[:at], authority[at+1:]
	}
	hosts = duplicatePortPattern.ReplaceAllString(hosts, ":$1$2")
	if hosts == "" || strings.HasPrefix(hosts, ",") || strings.HasSuffix(hosts, ",") || strings.ContainsAny(hosts, " \t\n") {
		return mongoURI{}, errInvalidURI
	}
	if scheme == "mongodb+srv" && strings.Contains(hosts, ",") {
		return mongoURI{}, errors.New("mongodb+srv connection strings accept exactly one host")
	}
	return mongoURI{scheme: scheme, userinfo: userinfo, hosts: hosts, rest: rest}, nil
}

func (u mongoURI) String() string {
	var builder strings.Builder
	builder.WriteString(u.scheme)
	builder.WriteString("://")
	if u.userinfo != "" {
		builder.WriteString(u.userinfo)
		builder.WriteByte('@')
	}
	builder.WriteString(u.hosts)
	builder.WriteString(u.rest)
	return builder.String()
}

// withCredentials adds credentials only when the URI carries none, so explicit
// URI credentials always win.
func (u mongoURI) withCredentials(username, password string) mongoURI {
	if u.userinfo != "" || username == "" {
		return u
	}
	u.userinfo = url.UserPassword(username, password).String()
	return u
}

func (u mongoURI) hasOption(name string) bool {
	_, query, _ := strings.Cut(u.rest, "?")
	for _, pair := range strings.Split(query, "&") {
		key, _, _ := strings.Cut(pair, "=")
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func (u mongoURI) withOption(name, value string) mongoURI {
	if u.rest == "" {
		u.rest = "/"
	}
	separator := "?"
	if strings.Contains(u.rest, "?") {
		separator = "&"
		if strings.HasSuffix(u.rest, "?") || strings.HasSuffix(u.rest, "&") {
			separator = ""
		}
	}
	u.rest += separator + url.QueryEscape(name) + "=" + url.QueryEscape(value)
	return u
}

// display is a credential-free summary used in API responses.
func (u mongoURI) display() string {
	if u.scheme == "mongodb+srv" {
		return "mongodb+srv://" + u.hosts
	}
	return u.hosts
}
