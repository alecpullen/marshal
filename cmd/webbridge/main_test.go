package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	// Ensure env vars do not leak host state into the defaults test.
	for _, k := range []string{"WEBBRIDGE_ADDR", "WEBBRIDGE_TOKEN", "WEBBRIDGE_MARSHAL_BIN", "WEBBRIDGE_CWD_ROOT"} {
		t.Setenv(k, "")
	}
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:7700" {
		t.Errorf("addr = %q, want 127.0.0.1:7700", cfg.addr)
	}
	if cfg.token != "" {
		t.Errorf("token = %q, want empty (generated later)", cfg.token)
	}
	if cfg.marshalBin != "marshal" {
		t.Errorf("marshalBin = %q, want marshal", cfg.marshalBin)
	}
	if cfg.cwdRoot == "" {
		t.Error("cwdRoot empty; want fallback to working directory")
	}
}

func TestParseConfigEnvAndFlags(t *testing.T) {
	t.Setenv("WEBBRIDGE_ADDR", "127.0.0.1:9999")
	t.Setenv("WEBBRIDGE_TOKEN", "env-token")
	t.Setenv("WEBBRIDGE_CWD_ROOT", "/env/root")

	// Env applies when flags are absent.
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:9999" || cfg.token != "env-token" || cfg.cwdRoot != "/env/root" {
		t.Errorf("env not applied: %+v", cfg)
	}

	// Flags win over env.
	cfg, err = parseConfig([]string{
		"--addr", "127.0.0.1:1234",
		"--token", "flag-token",
		"--marshal-bin", "/bin/custom",
		"--cwd-root", "/flag/root",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:1234" || cfg.token != "flag-token" || cfg.marshalBin != "/bin/custom" || cfg.cwdRoot != "/flag/root" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestParseConfigProjects(t *testing.T) {
	cfg, err := parseConfig([]string{"--project", "/tmp/project"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.projects) != 1 || cfg.projects[0] != "/tmp/project" {
		t.Fatalf("projects = %#v", cfg.projects)
	}
}

func TestParseProjectMountRejectsMalformed(t *testing.T) {
	if _, err := parseProjectMounts([]string{"no-colon"}); err == nil {
		t.Fatal("accepted a mapping with no colon")
	}
	got, err := parseProjectMounts([]string{"/h:/c"})
	if err != nil || len(got) != 1 || got[0].Host != "/h" || got[0].Container != "/c" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestParseConfigRejectsArgs(t *testing.T) {
	if _, err := parseConfig([]string{"bogus"}, io.Discard); err == nil {
		t.Fatal("positional arg: expected error, got nil")
	}
	if _, err := parseConfig([]string{"--nope"}, io.Discard); err == nil {
		t.Fatal("unknown flag: expected error, got nil")
	}
}

func TestAskpassAnswersUsernameAndPassword(t *testing.T) {
	t.Setenv("MARSHAL_ASKPASS_USER", "x-access-token")
	t.Setenv("MARSHAL_ASKPASS_SECRET", "sk-secret")

	if got := askpassResponse("Username for 'https://github.com': "); got != "x-access-token" {
		t.Errorf("username prompt answered %q", got)
	}
	if got := askpassResponse("Password for 'https://github.com': "); got != "sk-secret" {
		t.Errorf("password prompt answered %q", got)
	}
}

func TestAskpassModeShortCircuitsRun(t *testing.T) {
	t.Setenv("MARSHAL_ASKPASS", "1")
	t.Setenv("MARSHAL_ASKPASS_SECRET", "sk-secret")

	var out strings.Builder
	err := run(context.Background(),
		[]string{"Password for 'https://github.com': "},
		&out, io.Discard)
	if err != nil {
		t.Fatalf("run in askpass mode: %v", err)
	}
	if strings.TrimSpace(out.String()) != "sk-secret" {
		t.Fatalf("askpass printed %q, want the secret", out.String())
	}
}

func TestGenToken(t *testing.T) {
	tok, err := genToken()
	if err != nil {
		t.Fatalf("genToken: %v", err)
	}
	if len(tok) != 32 { // 16 bytes hex-encoded
		t.Errorf("token length = %d, want 32", len(tok))
	}
	tok2, err := genToken()
	if err != nil {
		t.Fatalf("genToken: %v", err)
	}
	if tok == tok2 {
		t.Error("two generated tokens are identical")
	}
}

// --- TLS ---

func TestParseConfigTLSAcceptsBothOrNeither(t *testing.T) {
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig(no tls): %v", err)
	}
	if cfg.tlsCert != "" || cfg.tlsKey != "" {
		t.Fatalf("expected no TLS by default, got %q/%q", cfg.tlsCert, cfg.tlsKey)
	}

	cfg, err = parseConfig([]string{"--tls-cert", "/c.pem", "--tls-key", "/k.pem"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig(both): %v", err)
	}
	if cfg.tlsCert != "/c.pem" || cfg.tlsKey != "/k.pem" {
		t.Fatalf("got %q/%q", cfg.tlsCert, cfg.tlsKey)
	}
}

// TestParseConfigTLSRejectsOnlyOne guards the failure mode worth
// designing out: a mistyped flag name silently serving plaintext.
func TestParseConfigTLSRejectsOnlyOne(t *testing.T) {
	for _, args := range [][]string{
		{"--tls-cert", "/c.pem"},
		{"--tls-key", "/k.pem"},
	} {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("parseConfig(%v) succeeded; half-configured TLS must fail loudly", args)
		}
	}
}

// selfSignedCert writes a throwaway cert/key for 127.0.0.1 and returns
// their paths plus a pool that trusts them. Generated in-test so there
// is no fixture on disk to expire.
func selfSignedCert(t *testing.T) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "webbridge-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	pool = x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("could not trust the generated certificate")
	}
	return certPath, keyPath, pool
}

func TestServeHTTPUsesTLSWhenCertificatesAreSupplied(t *testing.T) {
	certPath, keyPath, pool := selfSignedCert(t)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = serveHTTP(srv, ln, certPath, keyPath) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("HTTPS request failed; ServeTLS was not wired: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	// A plaintext request must not reach the handler. Go's TLS server
	// answers it with a valid HTTP 400 ("Client sent an HTTP request to
	// an HTTPS server"), so the transport error is nil — the assertion
	// has to be that the request was rejected, not that it failed.
	plain, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		return // connection refused is also an acceptable rejection
	}
	defer plain.Body.Close()
	if plain.StatusCode == http.StatusOK {
		t.Fatal("plain HTTP reached the handler on a TLS listener")
	}
	body, _ := io.ReadAll(plain.Body)
	if string(body) == "ok" {
		t.Fatal("the handler served a plaintext request on a TLS listener")
	}
}

func TestServeHTTPStaysPlaintextWithoutCertificates(t *testing.T) {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = serveHTTP(srv, ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })

	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("plain HTTP failed: %v", err)
	}
	defer resp.Body.Close()
}

func TestParseConfigStateVolume(t *testing.T) {
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.stateVolume != "marshal-state" {
		t.Fatalf("default = %q, want marshal-state", cfg.stateVolume)
	}
	cfg, err = parseConfig([]string{"--state-volume", "prod-state"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.stateVolume != "prod-state" {
		t.Fatalf("got %q", cfg.stateVolume)
	}
}

func TestParseConfigLimitFlags(t *testing.T) {
	cfg, err := parseConfig([]string{"--max-concurrent", "9", "--max-disk-mb", "100", "--max-clone-mb", "50"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxConcurrent != 9 || cfg.maxDiskMB != 100 || cfg.maxCloneMB != 50 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestWebbridgeReportsItsVersion(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), []string{"--version"},
		&out, io.Discard); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if !strings.Contains(out.String(), "webbridge") {
		t.Fatalf("--version output does not contain 'webbridge': %q", out.String())
	}
}
