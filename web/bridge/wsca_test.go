package bridge

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCAFleet(t *testing.T) (*Fleet, SecretProvider) {
	t.Helper()
	f := testFleetWithAudit(t)
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if err := GenerateKeyFile(key); err != nil {
		t.Fatal(err)
	}
	p, err := NewLocalProvider(filepath.Join(root, "state"), key)
	if err != nil {
		t.Fatal(err)
	}
	f.SetSecrets(p)
	return f, p
}

func stubSystemBundle(t *testing.T) []byte {
	t.Helper()
	cert, certPEM, _, err := generateWorkspaceCA("system", time.Now())
	if err != nil || cert == nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(path, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	old := systemBundlePaths
	systemBundlePaths = []string{filepath.Join(t.TempDir(), "missing"), path}
	t.Cleanup(func() { systemBundlePaths = old })
	return certPEM
}

func TestWorkspaceCAVerifiesMintedLeaf(t *testing.T) {
	ctx := context.Background()
	f, _ := testCAFleet(t)
	cert, key, err := f.workspaceCA(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA || cert.MaxPathLen != 0 || !cert.MaxPathLenZero || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("CA constraints wrong: %+v", cert)
	}
	if cert.Subject.CommonName != "marshal workspace dev" || key == nil {
		t.Fatalf("subject %q", cert.Subject.CommonName)
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d < 360*24*time.Hour || d > 370*24*time.Hour {
		t.Fatalf("validity %v", d)
	}
	l, err := f.leafFor(ctx, "dev", "", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	if _, err := l.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "api.example.com"}); err != nil {
		t.Fatalf("leaf does not verify: %v", err)
	}
	if _, err := l.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "evil.example.com"}); err == nil {
		t.Fatal("leaf verified for another host")
	}
	if l2, _ := f.leafFor(ctx, "dev", "", "api.example.com"); l2 != l {
		t.Error("leaf not cached")
	}
	ip, err := f.leafFor(ctx, "dev", "", "127.0.0.1")
	if err != nil || len(ip.Leaf.IPAddresses) != 1 {
		t.Fatalf("IP leaf: %v", err)
	}
}

func TestWorkspaceCAKeyRoundTripsThroughProvider(t *testing.T) {
	ctx := context.Background()
	f, p := testCAFleet(t)
	cert, _, err := f.workspaceCA(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.Get(ctx, DefaultOwnerID, "ca/dev")
	if err != nil {
		t.Fatalf("CA not stored at vault:ca/dev: %v", err)
	}
	c2, _, key2, err := decodeCABundle(raw)
	if err != nil || !c2.Equal(cert) || key2 == nil {
		t.Fatalf("decode: %v", err)
	}
	// A fresh fleet on the same backend reuses the stored CA.
	f2 := testFleetWithAudit(t)
	f2.SetSecrets(p)
	again, _, err := f2.workspaceCA(ctx, "dev")
	if err != nil || again.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatalf("CA not reused: %v", err)
	}
}

func TestWorkspaceCABundleHoldsSystemRootsAndCA(t *testing.T) {
	ctx := context.Background()
	sys := stubSystemBundle(t)
	f, _ := testCAFleet(t)
	cert, _, err := f.workspaceCA(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(f.stateDir, "ca", "dev-bundle.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(bundle, sys) {
		t.Fatal("bundle does not start with the system bundle")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle holds no certificates")
	}
	only, _ := os.ReadFile(filepath.Join(f.stateDir, "ca", "dev.pem"))
	blk, _ := pem.Decode(only)
	if blk == nil || !bytes.Equal(blk.Bytes, cert.Raw) {
		t.Fatal("dev.pem is not the CA certificate")
	}
	if !bytes.HasSuffix(bundle, only) {
		t.Fatal("bundle does not end with the CA certificate")
	}
	// With no system bundle the bundle is the CA alone.
	systemBundlePaths = []string{filepath.Join(t.TempDir(), "none")}
	if _, _, err := f.workspaceCA(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	alone, _ := os.ReadFile(filepath.Join(f.stateDir, "ca", "other-bundle.pem"))
	one, _ := os.ReadFile(filepath.Join(f.stateDir, "ca", "other.pem"))
	if !bytes.Equal(alone, one) {
		t.Fatal("fallback bundle should be the CA alone")
	}
}

func TestWorkspaceCAEnvProviderUnavailable(t *testing.T) {
	f := testFleetWithAudit(t) // env backend
	if _, _, err := f.workspaceCA(context.Background(), "dev"); !errors.Is(err, ErrInjectionUnavailable) {
		t.Fatalf("err = %v", err)
	}
	s := NewServer(f, "")
	if c := doReq(t, s, http.MethodPost, "/api/workspaces/dev/ca/rotate", nil, nil).Code; c != http.StatusConflict {
		t.Fatalf("rotate on env = %d", c)
	}
}

func TestWorkspaceCARotationChangesSerial(t *testing.T) {
	ctx := context.Background()
	f, _ := testCAFleet(t)
	before, _, err := f.workspaceCA(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	oldLeaf, _ := f.leafFor(ctx, "dev", "", "a.example.com")
	s := NewServer(f, "")
	rec := doReq(t, s, http.MethodPost, "/api/workspaces/dev/ca/rotate", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body)
	}
	after, _, _ := f.workspaceCA(ctx, "dev")
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Fatal("serial unchanged")
	}
	file, _ := os.ReadFile(filepath.Join(f.stateDir, "ca", "dev.pem"))
	if blk, _ := pem.Decode(file); blk == nil || !bytes.Equal(blk.Bytes, after.Raw) {
		t.Fatal("trust file not rewritten")
	}
	newLeaf, _ := f.leafFor(ctx, "dev", "", "a.example.com")
	if newLeaf == oldLeaf {
		t.Fatal("leaf cache survived rotation")
	}
	roots := x509.NewCertPool()
	roots.AddCert(before)
	if _, err := newLeaf.Leaf.Verify(x509.VerifyOptions{Roots: roots}); err == nil {
		t.Fatal("new leaf still verifies under the old CA")
	}
	if findEvent(auditTail(t, f), AuditCARotated) == nil {
		t.Fatal("no ca_rotated audit entry")
	}
	if c := doReq(t, s, http.MethodPost, "/api/workspaces/..%2Fx/ca/rotate", nil, nil).Code; c == http.StatusOK {
		t.Fatal("bad name rotated")
	}
}

func TestWorkspaceCALeafRefreshesBeforeExpiry(t *testing.T) {
	ctx := context.Background()
	f, _ := testCAFleet(t)
	c, err := f.caFor(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c.now = func() time.Time { return clock }
	a, _ := c.leaf("h.example.com")
	clock = clock.Add(22 * time.Hour)
	if b, _ := c.leaf("h.example.com"); b != a {
		t.Fatal("refreshed too early")
	}
	clock = clock.Add(90 * time.Minute)
	if b, _ := c.leaf("h.example.com"); b == a {
		t.Fatal("not refreshed within the last hour")
	}
}

func TestWorkspaceCARotationKeepsRunningAgentGeneration(t *testing.T) {
	ctx := context.Background()
	f, _ := testCAFleet(t)
	if err := os.MkdirAll(filepath.Join(f.stateDir, egressSubpath), 0o700); err != nil {
		t.Fatal(err)
	}
	h := newEgressHost(f)
	f.egress = h
	old, _, err := f.workspaceCA(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	oldID := old.SerialNumber.Text(16)
	h.agents["a1"] = &egressAgent{workspace: "dev"}
	h.setCA("a1", oldID)

	if _, err := f.rotateCA(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	// The running agent still gets leaves from the generation it trusts.
	l, err := f.leafFor(ctx, "dev", oldID, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(old)
	if _, err := l.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "a.example.com"}); err != nil {
		t.Fatalf("old-generation leaf does not verify under the old CA: %v", err)
	}
	// The trust files carry both generations, so the agent trusts either.
	bundle, _ := os.ReadFile(filepath.Join(f.stateDir, "ca", "dev-bundle.pem"))
	if !bytes.Contains(bundle, pemOf(old)) {
		t.Fatal("bundle dropped the generation a running agent uses")
	}
	// Once the agent is gone the retired generation is pruned.
	h.remove("a1")
	bundle, _ = os.ReadFile(filepath.Join(f.stateDir, "ca", "dev-bundle.pem"))
	if bytes.Contains(bundle, pemOf(old)) {
		t.Fatal("retired generation outlived its last agent")
	}
	if _, err := f.leafFor(ctx, "dev", oldID, "a.example.com"); err == nil {
		t.Fatal("pruned generation still signs")
	}
}

func pemOf(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}
