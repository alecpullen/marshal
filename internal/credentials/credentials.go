// Package credentials provides a byte-oriented secret key/value store backed by
// the operating system's credential manager: the macOS Keychain, the Windows
// Credential Manager, or the Linux Secret Service.
//
// The package is deliberately ignorant of what callers store. Keys are opaque
// strings -- callers conventionally use a "marshal:<subsystem>:<name>" form such
// as "marshal:mcp:github" -- and values are opaque byte slices, typically JSON
// blobs. Nothing here knows about MCP, JSON, or any other schema.
//
// The store never falls back to plaintext on-disk storage. When no OS
// credential backend is available (for example a headless Linux box with no
// Secret Service), [New] fails with an error wrapping [ErrKeyringUnavailable]
// so callers can degrade gracefully rather than leak secrets onto disk.
package credentials

import (
	"errors"
	"fmt"
	"sync"

	"github.com/99designs/keyring"
)

// Store is a byte-oriented secret key/value store.
//
// Implementations must be safe for concurrent use. Get reports a missing key by
// returning an error that satisfies errors.Is(err, ErrNotFound).
type Store interface {
	// Get returns the value stored under key. It returns an error wrapping
	// ErrNotFound when key is not present, which callers should treat as a
	// normal "nothing stored yet" state rather than a failure.
	Get(key string) ([]byte, error)

	// Set stores value under key, replacing any existing value.
	Set(key string, value []byte) error

	// Delete removes key. It is idempotent: deleting a key that is not
	// present is not an error.
	Delete(key string) error
}

var (
	// ErrKeyringUnavailable is returned by New when no OS credential backend
	// could be opened. Callers should detect it with errors.Is and fall back
	// to a weaker (or disabled) secret path rather than writing secrets to
	// plaintext files.
	ErrKeyringUnavailable = errors.New("credentials: no OS keyring backend available")

	// ErrNotFound is wrapped by Store.Get when the requested key does not
	// exist. Detect it with errors.Is(err, ErrNotFound); callers should treat
	// it as a normal empty state, not a failure.
	ErrNotFound = errors.New("credentials: key not found")
)

// secureBackends is the whitelist of OS credential backends this package is
// willing to open. It intentionally excludes keyring's plaintext file backend,
// the "pass" command backend, and the volatile kernel keyctl store, so that a
// secret is never persisted unencrypted just because a stronger backend was
// missing.
var secureBackends = []keyring.BackendType{
	keyring.KeychainBackend,
	keyring.WinCredBackend,
	keyring.SecretServiceBackend,
}

// keyringStore adapts a keyring.Keyring to the Store interface.
type keyringStore struct {
	ring keyring.Keyring
}

var _ Store = (*keyringStore)(nil)

// New opens the operating system credential manager under the given service
// name and returns a Store backed by it.
//
// If no secure backend can be opened, New returns (nil, err) where err wraps
// ErrKeyringUnavailable. New never returns (nil, nil).
func New(service string) (Store, error) {
	if service == "" {
		return nil, errors.New("credentials: service name must not be empty")
	}

	ring, err := keyring.Open(keyring.Config{
		ServiceName:     service,
		AllowedBackends: secureBackends,
	})
	if err != nil {
		return nil, fmt.Errorf("credentials: opening keyring for service %q: %w", service, ErrKeyringUnavailable)
	}
	if ring == nil {
		// Defensive: the keyring package never does this, but New must never
		// hand back a nil Store with a nil error.
		return nil, fmt.Errorf("credentials: opening keyring for service %q: %w", service, ErrKeyringUnavailable)
	}

	return &keyringStore{ring: ring}, nil
}

// Get returns the value stored under key.
func (s *keyringStore) Get(key string) ([]byte, error) {
	item, err := s.ring.Get(key)
	if err != nil {
		if errors.Is(err, keyring.ErrKeyNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("credentials: reading %q: %w", key, err)
	}
	return item.Data, nil
}

// Set stores value under key, replacing any existing value.
func (s *keyringStore) Set(key string, value []byte) error {
	err := s.ring.Set(keyring.Item{
		Key:         key,
		Data:        value,
		Label:       key,
		Description: "marshal secret",
	})
	if err != nil {
		return fmt.Errorf("credentials: writing %q: %w", key, err)
	}
	return nil
}

// Delete removes key. It is idempotent: removing a key that is not present is
// not an error.
func (s *keyringStore) Delete(key string) error {
	err := s.ring.Remove(key)
	if err != nil {
		if errors.Is(err, keyring.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("credentials: deleting %q: %w", key, err)
	}
	return nil
}

// MemStore is an in-memory Store implementation for tests and for consumers
// that need a stand-in when no OS keyring is available. It is safe for
// concurrent use and stores defensive copies of values.
type MemStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

var _ Store = (*MemStore)(nil)

// NewMemStore returns an empty in-memory Store.
func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string][]byte)}
}

// Get returns the value stored under key, or an error wrapping ErrNotFound.
func (m *MemStore) Get(key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	value, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	out := make([]byte, len(value))
	copy(out, value)
	return out, nil
}

// Set stores a copy of value under key.
func (m *MemStore) Set(key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	m.data[key] = stored
	return nil
}

// Delete removes key. It is idempotent.
func (m *MemStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, key)
	return nil
}
