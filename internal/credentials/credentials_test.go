package credentials

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/99designs/keyring"
)

// fakeStore is a minimal in-memory Store used to pin the interface contract that
// consumers rely on. It deliberately does not reuse MemStore so that a change to
// MemStore cannot silently change what the interface promises.
type fakeStore struct {
	data map[string][]byte
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string][]byte{}} }

func (f *fakeStore) Get(key string) ([]byte, error) {
	v, ok := f.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (f *fakeStore) Set(key string, value []byte) error {
	f.data[key] = value
	return nil
}

func (f *fakeStore) Delete(key string) error {
	delete(f.data, key)
	return nil
}

// Compile-time proof that an arbitrary consumer can implement Store.
var _ Store = (*fakeStore)(nil)

func TestStoreInterfaceRoundTrip(t *testing.T) {
	var s Store = newFakeStore()

	blob := []byte(`{"access_token":"tok","refresh_token":"ref"}`)
	if err := s.Set("marshal:mcp:github", blob); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("marshal:mcp:github")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("Get = %q, want %q", got, blob)
	}

	if err := s.Delete("marshal:mcp:github"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("marshal:mcp:github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete Get error = %v, want ErrNotFound", err)
	}
}

func TestMemStoreRoundTrip(t *testing.T) {
	s := NewMemStore()

	if _, err := s.Get("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(absent) error = %v, want ErrNotFound", err)
	}

	value := []byte("secret")
	if err := s.Set("marshal:mcp:github", value); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Mutating the caller's slice must not corrupt the stored value.
	value[0] = 'X'
	got, err := s.Get("marshal:mcp:github")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("Get = %q, want %q", got, "secret")
	}

	// Mutating the returned slice must not corrupt the stored value either.
	got[0] = 'Y'
	again, err := s.Get("marshal:mcp:github")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(again) != "secret" {
		t.Fatalf("Get = %q, want %q", again, "secret")
	}

	// Delete is idempotent.
	if err := s.Delete("marshal:mcp:github"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("marshal:mcp:github"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrNotFound, ErrKeyringUnavailable) || errors.Is(ErrKeyringUnavailable, ErrNotFound) {
		t.Fatal("ErrNotFound and ErrKeyringUnavailable must be distinct")
	}
}

// errRing is a keyring.Keyring stub whose Get/Remove behaviour is configurable,
// letting us assert how keyringStore translates backend errors.
type errRing struct {
	getErr    error
	removeErr error
	item      keyring.Item
}

func (e *errRing) Get(string) (keyring.Item, error)             { return e.item, e.getErr }
func (e *errRing) GetMetadata(string) (keyring.Metadata, error) { return keyring.Metadata{}, nil }
func (e *errRing) Set(keyring.Item) error                       { return nil }
func (e *errRing) Remove(string) error                          { return e.removeErr }
func (e *errRing) Keys() ([]string, error)                      { return nil, nil }

var _ keyring.Keyring = (*errRing)(nil)

func TestKeyringStoreGetMapsNotFound(t *testing.T) {
	s := &keyringStore{ring: &errRing{getErr: keyring.ErrKeyNotFound}}

	_, err := s.Get("marshal:mcp:github")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestKeyringStoreGetReturnsValue(t *testing.T) {
	s := &keyringStore{ring: &errRing{item: keyring.Item{Data: []byte("tok")}}}

	got, err := s.Get("marshal:mcp:github")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "tok" {
		t.Fatalf("Get = %q, want %q", got, "tok")
	}
}

func TestKeyringStoreGetPropagatesOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	s := &keyringStore{ring: &errRing{getErr: boom}}

	_, err := s.Get("marshal:mcp:github")
	if !errors.Is(err, boom) {
		t.Fatalf("Get error = %v, want to wrap %v", err, boom)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("backend failure must not be reported as ErrNotFound: %v", err)
	}
}

func TestKeyringStoreDeleteIsIdempotent(t *testing.T) {
	s := &keyringStore{ring: &errRing{removeErr: keyring.ErrKeyNotFound}}

	if err := s.Delete("marshal:mcp:github"); err != nil {
		t.Fatalf("Delete of missing key = %v, want nil", err)
	}
}

func TestKeyringStoreDeletePropagatesOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	s := &keyringStore{ring: &errRing{removeErr: boom}}

	if err := s.Delete("marshal:mcp:github"); !errors.Is(err, boom) {
		t.Fatalf("Delete error = %v, want to wrap %v", err, boom)
	}
}

// TestNewNeverReturnsNilStore is the core constructor contract: on every
// platform, New either hands back a usable Store or a non-nil error wrapping
// ErrKeyringUnavailable. It must never panic and never return (nil, nil).
//
// It deliberately does not touch the OS credential store, so it stays hermetic
// and cannot hang on a locked or headless keychain.
func TestNewNeverReturnsNilStore(t *testing.T) {
	store, err := New("marshal-test")

	switch {
	case err == nil && store == nil:
		t.Fatal("New returned (nil, nil)")
	case err == nil:
		// A backend is available: the contract is satisfied as long as the
		// Store is non-nil. No value is written to the real credential store.
		var _ Store = store
	case !errors.Is(err, ErrKeyringUnavailable):
		t.Fatalf("New error = %v, want errors.Is(err, ErrKeyringUnavailable)", err)
	}
}

// TestLiveKeyringRoundTrip exercises a real OS credential store end to end. It
// is opt-in because it writes to the developer's keychain and would otherwise
// fail or block on a headless machine. Enable with:
//
//	MARSHAL_CREDENTIALS_LIVE_TEST=1 go test ./internal/credentials/...
func TestLiveKeyringRoundTrip(t *testing.T) {
	if os.Getenv("MARSHAL_CREDENTIALS_LIVE_TEST") == "" {
		t.Skip("set MARSHAL_CREDENTIALS_LIVE_TEST=1 to exercise the real OS keyring")
	}

	store, err := New("marshal-test")
	if errors.Is(err, ErrKeyringUnavailable) {
		t.Skipf("no OS keyring backend available: %v", err)
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	key := "marshal:credentials:selftest"
	value := []byte(`{"token":"value"}`)
	t.Cleanup(func() { _ = store.Delete(key) })

	if err := store.Set(key, value); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get = %q, want %q", got, value)
	}
	if err := store.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete error = %v, want ErrNotFound", err)
	}
}

func TestNewRejectsEmptyService(t *testing.T) {
	store, err := New("")
	if err == nil {
		t.Fatal("New(\"\") returned nil error")
	}
	if store != nil {
		t.Fatal("New(\"\") returned a non-nil Store alongside an error")
	}
}

func TestNewDoesNotUsePlaintextBackend(t *testing.T) {
	for _, b := range secureBackends {
		if b == keyring.FileBackend {
			t.Fatal("plaintext file backend must not be in the allowed backend list")
		}
	}
}
