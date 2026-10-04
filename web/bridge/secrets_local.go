package bridge

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// localProvider keeps secrets in one JSON file, each entry sealed with
// AES-256-GCM. The storage path is the AAD, so an entry copied under
// another path fails to open.
type localProvider struct {
	path string
	key  [32]byte
	mu   sync.Mutex
}

type localEntry struct {
	Nonce string `json:"nonce"`
	CT    string `json:"ct"`
}

// GenerateKeyFile writes 32 random bytes to path with mode 0600. It
// refuses to overwrite an existing file.
func GenerateKeyFile(path string) error {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(key[:]); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// NewLocalProvider loads the key and opens <stateDir>/secrets/store.json.
// It refuses a key file inside stateDir (a backup of the state would then
// carry the key with the ciphertext) and one readable by group or other.
func NewLocalProvider(stateDir, keyFile string) (SecretProvider, error) {
	if keyFile == "" {
		return nil, errors.New("--secrets local needs --secrets-key-file")
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	absKey, err := filepath.Abs(keyFile)
	if err != nil {
		return nil, err
	}
	// Resolve symlinks where possible so a link into the state dir is caught.
	if r, err := filepath.EvalSymlinks(absKey); err == nil {
		absKey = r
	}
	if r, err := filepath.EvalSymlinks(absState); err == nil {
		absState = r
	}
	if rel, err := filepath.Rel(absState, absKey); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("secrets key file %s must not be inside the state dir %s", keyFile, stateDir)
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		return nil, fmt.Errorf("secrets key file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secrets key file %s has mode %o; it must be 0600", keyFile, info.Mode().Perm())
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("secrets key file: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("secrets key file must hold exactly 32 bytes, has %d", len(raw))
	}
	p := &localProvider{path: filepath.Join(stateDir, "secrets", "store.json")}
	copy(p.key[:], raw)
	return p, nil
}

func (p *localProvider) Name() string { return "local" }

func (p *localProvider) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(p.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (p *localProvider) load() (map[string]localEntry, error) {
	m := map[string]localEntry{}
	data, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("secrets store: %w", err)
	}
	return m, nil
}

func (p *localProvider) save(m map[string]localEntry) error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), "store-*.tmp")
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
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p.path)
}

func (p *localProvider) Get(_ context.Context, owner, ref string) ([]byte, error) {
	if err := validateSecretPath(ref); err != nil {
		return nil, err
	}
	sp := storagePath(owner, ref)
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return nil, err
	}
	e, ok := m[sp]
	if !ok {
		return nil, ErrSecretNotFound
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets store: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(e.CT)
	if err != nil {
		return nil, fmt.Errorf("secrets store: %w", err)
	}
	g, err := p.gcm()
	if err != nil {
		return nil, err
	}
	if len(nonce) != g.NonceSize() {
		return nil, errors.New("secrets store: bad nonce")
	}
	pt, err := g.Open(nil, nonce, ct, []byte(sp))
	if err != nil {
		return nil, errors.New("secrets store: cannot open entry (wrong key or tampered)")
	}
	return pt, nil
}

func (p *localProvider) Put(_ context.Context, owner, ref string, value []byte) error {
	if err := validateSecretPath(ref); err != nil {
		return err
	}
	sp := storagePath(owner, ref)
	g, err := p.gcm()
	if err != nil {
		return err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := g.Seal(nil, nonce, value, []byte(sp))
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return err
	}
	m[sp] = localEntry{
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		CT:    base64.StdEncoding.EncodeToString(ct),
	}
	return p.save(m)
}

func (p *localProvider) Delete(_ context.Context, owner, ref string) error {
	if err := validateSecretPath(ref); err != nil {
		return err
	}
	sp := storagePath(owner, ref)
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return err
	}
	if _, ok := m[sp]; !ok {
		return ErrSecretNotFound
	}
	delete(m, sp)
	return p.save(m)
}

func (p *localProvider) List(_ context.Context, owner, prefix string) ([]string, error) {
	root := storagePath(owner, "")
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return nil, err
	}
	var out []string
	for sp := range m {
		rel, ok := strings.CutPrefix(sp, root)
		if !ok || !strings.HasPrefix(rel, prefix) {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}
