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
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

// SSL modes use MySQL client names; the backend normalizes URIs to them.
const (
	sslDisabled       = "DISABLED"
	sslPreferred      = "PREFERRED"
	sslRequired       = "REQUIRED"
	sslVerifyCA       = "VERIFY_CA"
	sslVerifyIdentity = "VERIFY_IDENTITY"
	defaultMySQLPort  = 3306
	connectTimeout    = 10 * time.Second
)

// Endpoint is one MySQL server. It carries credentials: never log it.
type Endpoint struct {
	Host     string
	Port     int
	User     string
	Password string
	SSLMode  string
	// SSLCA is a PEM file mounted into the worker.
	SSLCA string
}

// parseEndpoint reads mysql://user:password@host:port/?ssl-mode=...&ssl-ca=...
// The error never includes the URI because it may contain credentials.
func parseEndpoint(raw string) (Endpoint, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "mysql" || parsed.Hostname() == "" {
		return Endpoint{}, errors.New("must be a mysql:// URI with a host")
	}
	endpoint := Endpoint{Host: parsed.Hostname(), Port: defaultMySQLPort, SSLMode: sslPreferred}
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
	if endpoint.User == "" {
		return Endpoint{}, errors.New("must include a user")
	}
	query := parsed.Query()
	if mode := strings.ToUpper(strings.TrimSpace(query.Get("ssl-mode"))); mode != "" {
		switch mode {
		case sslDisabled, sslPreferred, sslRequired, sslVerifyCA, sslVerifyIdentity:
			endpoint.SSLMode = mode
		default:
			return Endpoint{}, fmt.Errorf("has an unknown ssl-mode %q", mode)
		}
	}
	endpoint.SSLCA = query.Get("ssl-ca")
	if (endpoint.SSLMode == sslVerifyCA || endpoint.SSLMode == sslVerifyIdentity) && endpoint.SSLCA == "" {
		return Endpoint{}, errors.New("needs ssl-ca to verify the server certificate")
	}
	return endpoint, nil
}

// Address is host:port, without credentials.
func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// WithAddress points the endpoint at another server of the same cluster with
// the same credentials. The node's name may not be in the server certificate,
// so hostname verification is relaxed to CA verification.
func (e Endpoint) WithAddress(address string) (Endpoint, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return Endpoint{}, fmt.Errorf("invalid node address %q", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 || host == "" {
		return Endpoint{}, fmt.Errorf("invalid node address %q", address)
	}
	clone := e
	clone.Host, clone.Port = host, port
	if clone.SSLMode == sslVerifyIdentity {
		clone.SSLMode = sslVerifyCA
	}
	return clone, nil
}

var (
	tlsConfigsMu sync.Mutex
	tlsConfigs   = map[string]string{}
)

// DSN builds a go-sql-driver DSN. interpolateParams lets placeholders appear
// in statements MySQL cannot prepare (CHANGE REPLICATION SOURCE, filters);
// the driver escapes them for the server's SQL mode.
func (e Endpoint) DSN() (string, error) {
	config := mysql.NewConfig()
	config.User, config.Passwd = e.User, e.Password
	config.Net, config.Addr = "tcp", e.Address()
	config.InterpolateParams = true
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = connectTimeout
	config.ReadTimeout = 5 * time.Minute
	config.WriteTimeout = time.Minute
	config.Params = map[string]string{"time_zone": "'+00:00'"}
	tlsName, err := e.tlsConfigName()
	if err != nil {
		return "", err
	}
	config.TLSConfig = tlsName
	return config.FormatDSN(), nil
}

func (e Endpoint) tlsConfigName() (string, error) {
	switch e.SSLMode {
	case sslDisabled:
		return "false", nil
	case sslPreferred:
		return "preferred", nil
	case sslRequired:
		return "skip-verify", nil
	}
	key := e.SSLMode + "|" + e.SSLCA + "|" + e.Host
	tlsConfigsMu.Lock()
	defer tlsConfigsMu.Unlock()
	if name, ok := tlsConfigs[key]; ok {
		return name, nil
	}
	pem, err := os.ReadFile(e.SSLCA)
	if err != nil {
		return "", fmt.Errorf("read the CA certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return "", errors.New("the CA certificate is not valid PEM")
	}
	config := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	if e.SSLMode == sslVerifyIdentity {
		config.ServerName = e.Host
	} else {
		// VERIFY_CA: verify the chain, not the host name.
		config.InsecureSkipVerify = true
		config.VerifyPeerCertificate = verifyChain(roots)
	}
	name := fmt.Sprintf("migration-%d", len(tlsConfigs))
	if err := mysql.RegisterTLSConfig(name, config); err != nil {
		return "", err
	}
	tlsConfigs[key] = name
	return name, nil
}

func verifyChain(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("the server sent no certificate")
		}
		certificates := make([]*x509.Certificate, 0, len(raw))
		for _, der := range raw {
			certificate, err := x509.ParseCertificate(der)
			if err != nil {
				return err
			}
			certificates = append(certificates, certificate)
		}
		intermediates := x509.NewCertPool()
		for _, certificate := range certificates[1:] {
			intermediates.AddCert(certificate)
		}
		_, err := certificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
		return err
	}
}

// ShellConnection is the MySQL Shell connection dictionary.
func (e Endpoint) ShellConnection() map[string]any {
	connection := map[string]any{
		"scheme": "mysql", "host": e.Host, "port": e.Port, "user": e.User, "password": e.Password,
		"ssl-mode": e.SSLMode,
	}
	if e.SSLCA != "" {
		connection["ssl-ca"] = e.SSLCA
	}
	return connection
}

// UsesTLS tells whether the target server should connect to the source with
// TLS for replication.
func (e Endpoint) UsesTLS() bool { return e.SSLMode != sslDisabled }
