package mysqlreplication

import (
	"errors"
	"net"
	"net/url"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
	"github.com/openeverest/plugin-migrations/backend/internal/engine/mysqluri"
)

// Keys provider-mariadb publishes for its admin connection.
const (
	propertyAdminUsername    = "admin-username"
	propertyAdminPassword    = "admin-password"
	propertyAdminHost        = "admin-host"
	propertyAdminPort        = "admin-port"
	propertyAdminTLS         = "admin-tls"
	propertyAdminCA          = "admin-ca.crt"
	propertySkipReplicaStart = "admin-skip-replica-start"
)

// adminConnection is the instance's admin connection, which provider-mariadb
// publishes next to the application user's: replication needs privileges
// the application user does not have.
func adminConnection(connection engine.Connection) (engine.Connection, error) {
	properties := connection.Properties
	host, user, password := properties[propertyAdminHost], properties[propertyAdminUsername], properties[propertyAdminPassword]
	if host == "" || user == "" || password == "" {
		return engine.Connection{}, errors.New("does not publish an admin connection; it needs provider-mariadb 0.2.2 or later")
	}
	port := properties[propertyAdminPort]
	if port == "" {
		port = mysqluri.DefaultPort
	}
	uri := url.URL{Scheme: "mysql", Host: net.JoinHostPort(host, port), Path: "/"}
	if properties[propertyAdminTLS] == "true" {
		uri.RawQuery = "tls=true"
	}
	return engine.Connection{URI: uri.String(), Username: user, Password: password, CACert: properties[propertyAdminCA], Properties: properties}, nil
}
