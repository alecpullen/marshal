package bridge

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// systemBundlePaths are the host trust bundles, tried in order. It is a
// variable so tests can point it at a stub.
var systemBundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/cert.pem",
}

const (
	workspaceCAValidity = 365 * 24 * time.Hour
	leafValidity        = 24 * time.Hour
	// leafRefreshMargin is how long before expiry a cached leaf is
	// replaced.
	leafRefreshMargin = time.Hour
)

// caCache is one workspace's CA and the leaves minted from it.
type caCache struct {
	cert    *x509.Certificate
	certPEM []byte
	key     crypto.Signer

	mu     sync.Mutex
	now    func() time.Time
	leaves map[string]*tls.Certificate
}

func newCACache(cert *x509.Certificate, certPEM []byte, key crypto.Signer) *caCache {
	return &caCache{cert: cert, certPEM: certPEM, key: key, now: time.Now, leaves: map[string]*tls.Certificate{}}
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// generateWorkspaceCA makes a fresh P-256 CA that can sign leaves and
// nothing below them (MaxPathLen 0).
func generateWorkspaceCA(name string, now time.Time) (*x509.Certificate, []byte, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "marshal workspace " + name},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(workspaceCAValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key, nil
}

// encodeCABundle packs certificate and PKCS#8 key into one PEM blob, the
// form stored at vault:ca/<name>.
func encodeCABundle(certPEM []byte, key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, certPEM...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})...), nil
}

func decodeCABundle(data []byte) (*x509.Certificate, []byte, crypto.Signer, error) {
	var cert *x509.Certificate
	var certPEM []byte
	var key crypto.Signer
	for {
		var blk *pem.Block
		blk, data = pem.Decode(data)
		if blk == nil {
			break
		}
		switch blk.Type {
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				return nil, nil, nil, err
			}
			cert, certPEM = c, pem.EncodeToMemory(blk)
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
			if err != nil {
				return nil, nil, nil, err
			}
			s, ok := k.(crypto.Signer)
			if !ok {
				return nil, nil, nil, errors.New("CA key cannot sign")
			}
			key = s
		}
	}
	if cert == nil || key == nil {
		return nil, nil, nil, errors.New("CA bundle needs a certificate and a key")
	}
	return cert, certPEM, key, nil
}

func caSecretPath(name string) string { return "ca/" + name }

func validWorkspaceName(name string) error {
	if !idPattern.MatchString(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	return nil
}

// workspaceCA returns the workspace's CA certificate and signer, creating
// and storing one on first use. With a read-only backend a missing CA
// cannot be stored, so it is ErrInjectionUnavailable.
func (f *Fleet) workspaceCA(ctx context.Context, name string) (*x509.Certificate, crypto.Signer, error) {
	c, err := f.caFor(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	return c.cert, c.key, nil
}

// caFor returns the cached CA for name, loading or creating it, and
// (re)writes the public trust files.
func (f *Fleet) caFor(ctx context.Context, name string) (*caCache, error) {
	if err := validWorkspaceName(name); err != nil {
		return nil, err
	}
	f.caMu.Lock()
	defer f.caMu.Unlock()
	if c, ok := f.cas[name]; ok {
		return c, nil
	}
	c, err := f.loadOrCreateCA(ctx, name, false)
	if err != nil {
		return nil, err
	}
	if f.cas == nil {
		f.cas = map[string]*caCache{}
	}
	f.cas[name] = c
	return c, nil
}

// loadOrCreateCA reads vault:ca/<name>, or generates and stores a new CA
// when none exists or force is set. Caller holds f.caMu.
func (f *Fleet) loadOrCreateCA(ctx context.Context, name string, force bool) (*caCache, error) {
	path := caSecretPath(name)
	if !force {
		data, err := f.secrets.Get(ctx, DefaultOwnerID, path)
		switch {
		case err == nil:
			cert, certPEM, key, err := decodeCABundle(data)
			if err != nil {
				return nil, fmt.Errorf("workspace CA %s: %w", name, err)
			}
			c := newCACache(cert, certPEM, key)
			return c, f.writeCAFiles(name, c)
		case !errors.Is(err, ErrSecretNotFound):
			return nil, err
		}
	}
	cert, certPEM, key, err := generateWorkspaceCA(name, time.Now())
	if err != nil {
		return nil, err
	}
	bundle, err := encodeCABundle(certPEM, key)
	if err != nil {
		return nil, err
	}
	if err := f.secrets.Put(ctx, DefaultOwnerID, path, bundle); err != nil {
		if errors.Is(err, ErrSecretsReadOnly) {
			return nil, ErrInjectionUnavailable
		}
		return nil, err
	}
	c := newCACache(cert, certPEM, key)
	return c, f.writeCAFiles(name, c)
}

// writeCAFiles writes <state>/ca/<name>.pem and <name>-bundle.pem. The
// bundle is the host's system roots followed by the workspace CA.
func (f *Fleet) writeCAFiles(name string, c *caCache) error {
	dir := filepath.Join(f.stateDir, "ca")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, name+".pem"), c.certPEM, 0o644); err != nil {
		return err
	}
	var bundle []byte
	for _, p := range systemBundlePaths {
		if b, err := os.ReadFile(p); err == nil {
			bundle = b
			break
		}
	}
	if bundle == nil {
		slog.Default().Warn("webbridge: no system CA bundle found; the workspace bundle holds only its own CA", "workspace", name)
	} else if !bytes.HasSuffix(bundle, []byte("\n")) {
		bundle = append(bundle, '\n')
	}
	bundle = append(bundle, c.certPEM...)
	return writeFileAtomic(filepath.Join(dir, name+"-bundle.pem"), bundle, 0o644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// rotateCA replaces the workspace CA. Agents spawned afterwards trust the
// new one; the old leaves are dropped.
func (f *Fleet) rotateCA(ctx context.Context, name string) (*x509.Certificate, error) {
	if err := validWorkspaceName(name); err != nil {
		return nil, err
	}
	f.caMu.Lock()
	defer f.caMu.Unlock()
	c, err := f.loadOrCreateCA(ctx, name, true)
	if err != nil {
		return nil, err
	}
	if f.cas == nil {
		f.cas = map[string]*caCache{}
	}
	f.cas[name] = c
	f.auditf(AuditEvent{Event: AuditCARotated, OwnerID: DefaultOwnerID, Detail: name})
	return c.cert, nil
}

// leaf mints (or returns the cached) P-256 leaf for host, valid 24h and
// signed by the CA. A cached leaf is replaced an hour before it expires.
func (c *caCache) leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if l, ok := c.leaves[host]; ok && l.Leaf != nil && now.Before(l.Leaf.NotAfter.Add(-leafRefreshMargin)) {
		return l, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	l := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
	c.leaves[host] = l
	return l, nil
}

// leafFor mints a leaf for workspace/host from the workspace's CA.
func (f *Fleet) leafFor(ctx context.Context, workspace, host string) (*tls.Certificate, error) {
	c, err := f.caFor(ctx, workspace)
	if err != nil {
		return nil, err
	}
	return c.leaf(host)
}

func (s *Server) rotateWorkspaceCA(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	name := r.PathValue("name")
	if err := validWorkspaceName(name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cert, err := s.fleet.rotateCA(r.Context(), name)
	switch {
	case errors.Is(err, ErrInjectionUnavailable):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"serial": cert.SerialNumber.Text(16), "notAfter": cert.NotAfter})
	}
}
