package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// ErrUnknownCredential is returned when a credential reference cannot be
// resolved — either it does not exist, or it belongs to a different
// owner. Callers must treat both cases identically so they cannot probe
// which credential IDs exist.
var ErrUnknownCredential = errors.New("bridge: unknown credential")

// Credential is an immutable, serialisable description of how to
// authenticate to a remote. The secret itself (literal) is deliberately
// unexported and populated only at use time from the configured
// environment variable, so a Credential value can be persisted and
// marshalled to JSON without ever embedding a token.
type Credential struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	EnvVar  string `json:"envVar,omitempty"`
	KeyPath string `json:"keyPath,omitempty"`
	OwnerID string `json:"ownerId"`
	User    string `json:"user,omitempty"`
	// Ref is the "vault:<path>" reference for kind "vault". The value
	// itself lives in the secret backend, never here.
	Ref string `json:"ref,omitempty"`

	// literal holds the resolved PAT for "pat" credentials. It is not
	// exported, so encoding/json and any reflection-based logging never
	// see it.
	literal string
}

// username returns the username git should present during askpass, or a
// neutral default. A PAT carries its own identity, so the token user is
// not required.
func (c Credential) username() string {
	if c.User != "" {
		return c.User
	}
	return "x-access-token"
}

// CredentialStore resolves credential references against a fixed roster.
// It is safe for concurrent use: lookups take a read lock, and the map
// is immutable after construction.
type CredentialStore struct {
	mu      sync.RWMutex
	creds   map[string]Credential
	secrets SecretProvider
}

// SetProvider installs the backend that "vault" credentials read from.
func (s *CredentialStore) SetProvider(p SecretProvider) {
	s.mu.Lock()
	s.secrets = p
	s.mu.Unlock()
}

// Put adds or replaces a credential.
func (s *CredentialStore) Put(c Credential) {
	s.mu.Lock()
	s.creds[c.ID] = c
	s.mu.Unlock()
}

// Remove drops a credential.
func (s *CredentialStore) Remove(id string) {
	s.mu.Lock()
	delete(s.creds, id)
	s.mu.Unlock()
}

// List returns every credential, ordered by ID.
func (s *CredentialStore) List() []Credential {
	s.mu.RLock()
	out := make([]Credential, 0, len(s.creds))
	for _, c := range s.creds {
		out = append(out, c)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one credential without resolving its secret.
func (s *CredentialStore) Get(id string) (Credential, bool) {
	s.mu.RLock()
	c, ok := s.creds[id]
	s.mu.RUnlock()
	return c, ok
}

// NewCredentialStore builds a store keyed by credential ID. A nil or
// empty roster is valid and simply resolves nothing.
func NewCredentialStore(creds []Credential) *CredentialStore {
	m := make(map[string]Credential, len(creds))
	for _, c := range creds {
		m[c.ID] = c
	}
	return &CredentialStore{creds: m}
}

// Resolve looks up credRef for the given owner and returns a fully
// populated Credential. An empty credRef denotes an anonymous clone and
// resolves to a "none" credential. The actual secret for "pat"
// credentials is read from the environment at resolve time — never at
// store construction — so a token added or rotated in the shell is seen
// immediately and never persists in memory longer than one git call.
func (s *CredentialStore) Resolve(ctx context.Context, ownerID, credRef string) (Credential, error) {
	if credRef == "" {
		return Credential{Kind: "none"}, nil
	}
	s.mu.RLock()
	cred, ok := s.creds[credRef]
	provider := s.secrets
	s.mu.RUnlock()
	if !ok || cred.OwnerID != ownerID {
		return Credential{}, ErrUnknownCredential
	}

	switch cred.Kind {
	case "none":
		// Pass-through: anonymous clone.
		return cred, nil
	case "pat":
		if cred.EnvVar == "" {
			return Credential{}, fmt.Errorf("credential %q: no env var configured", credRef)
		}
		secret, ok := os.LookupEnv(cred.EnvVar)
		if !ok || secret == "" {
			return Credential{}, fmt.Errorf("credential %q: env var %s is unset", credRef, cred.EnvVar)
		}
		cred.literal = secret
		return cred, nil
	case "vault":
		// A vault credential resolves to a PAT-shaped value: the secret
		// is fetched now and flows through the same askpass path.
		path, err := parseCredentialRef(cred.Ref)
		if err != nil {
			return Credential{}, fmt.Errorf("credential %q: %w", credRef, err)
		}
		if provider == nil {
			return Credential{}, fmt.Errorf("credential %q: no secrets backend configured", credRef)
		}
		value, err := provider.Get(ctx, ownerID, path)
		if err != nil {
			return Credential{}, fmt.Errorf("credential %q: %w", credRef, err)
		}
		secret := strings.TrimSpace(string(value))
		if secret == "" {
			return Credential{}, fmt.Errorf("credential %q: secret %s is empty", credRef, cred.Ref)
		}
		cred.Kind = "pat"
		cred.literal = secret
		return cred, nil
	case "ssh":
		if cred.KeyPath == "" {
			return Credential{}, fmt.Errorf("credential %q: no key path configured", credRef)
		}
		return cred, nil
	default:
		return Credential{}, fmt.Errorf("credential %q: unknown kind %q", credRef, cred.Kind)
	}
}
