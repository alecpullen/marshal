package fleetprofile

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
)

// TLSConfig is s's client TLS configuration (decision 5), nil for a loopback
// http URL.
//
// The system roots are kept and every certificate in ca_file is added to them.
// TLS 1.2 is the minimum, http/1.1 the only protocol offered, and there is no
// option to skip verification.
func TLSConfig(s Server) (*tls.Config, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, fmt.Errorf("fleet.toml: servers.%s.url: %q is not a URL: %v", s.Name, s.URL, err)
	}
	if u.Scheme != "https" {
		// http is only allowed on a loopback host (decision 2), where the
		// transport dials plain. Load enforces that, but a Server built by
		// hand reaches here too, so the rule is checked again rather than
		// left to carry a token in clear to a host off this machine.
		if !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("fleet.toml: servers.%s.url: %q is http on %s, which is not loopback: use https", s.Name, s.URL, u.Hostname())
		}
		return nil, nil
	}

	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		ServerName: u.Hostname(),
	}
	if s.CAFile == "" {
		return cfg, nil
	}

	pem, err := os.ReadFile(s.CAFile)
	if err != nil {
		return nil, fmt.Errorf("fleet.toml: servers.%s.ca_file: %s: %w", s.Name, s.CAFile, err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("fleet.toml: servers.%s.ca_file: %s holds no certificate", s.Name, s.CAFile)
	}
	cfg.RootCAs = roots
	return cfg, nil
}
