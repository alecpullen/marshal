package fleetprofile

import (
	"crypto/tls"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCertificate writes raw DER bytes to dir/name as a PEM file and answers
// its path.
func writeCertificate(t *testing.T, path string, der []byte) {
	t.Helper()
	var buf strings.Builder
	if err := pem.Encode(&stringWriter{&buf}, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode the certificate: %v", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// stringWriter lets pem.Encode write into a strings.Builder.
type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func TestTLSConfigTrustsTheCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	// Without the certificate, the same dial fails verification.
	untrusting := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}}
	resp, err := untrusting.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a client without the ca_file verified the server's certificate")
	}

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	writeCertificate(t, caPath, srv.Certificate().Raw)

	cfg, err := TLSConfig(Server{Name: "home", URL: srv.URL, CAFile: caPath})
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	resp, err = client.Get(srv.URL)
	if err != nil {
		t.Fatalf("a client with the ca_file was refused: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Proto != "HTTP/1.1" {
		t.Fatalf("proto = %q, want HTTP/1.1", resp.Proto)
	}
}

func TestTLSConfigOffersHTTP1Only(t *testing.T) {
	cfg, err := TLSConfig(Server{Name: "home", URL: "https://fleet.example.com:8443"})
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("an https profile has no TLS configuration")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Fatalf("NextProtos = %v, want only http/1.1", cfg.NextProtos)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("the configuration skips verification")
	}

	plain, err := TLSConfig(Server{Name: "here", URL: "http://127.0.0.1:7700"})
	if err != nil {
		t.Fatalf("TLSConfig(loopback http): %v", err)
	}
	if plain != nil {
		t.Fatalf("a loopback http profile has a TLS configuration: %+v", plain)
	}
}

func TestACAFileMustHoldACertificate(t *testing.T) {
	srv := Server{Name: "home", URL: "https://fleet.example.com"}

	srv.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := TLSConfig(srv); err == nil {
		t.Fatal("a missing ca_file was accepted")
	} else if !strings.Contains(err.Error(), "missing.pem") {
		t.Fatalf("error = %q, want it to name the ca_file", err)
	}

	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("# no certificate here\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", empty, err)
	}
	srv.CAFile = empty
	if _, err := TLSConfig(srv); err == nil {
		t.Fatal("a ca_file holding no certificate was accepted")
	} else if !strings.Contains(err.Error(), "empty.pem") {
		t.Fatalf("error = %q, want it to name the ca_file", err)
	}
}
