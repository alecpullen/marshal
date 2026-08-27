package bridge

import (
	"errors"
	"fmt"
	"os"
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
	mu    sync.RWMutex
	creds map[string]Credential
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
func (s *CredentialStore) Resolve(ownerID, credRef string) (Credential, error) {
	if credRef == "" {
		return Credential{Kind: "none"}, nil
	}
	s.mu.RLock()
	cred, ok := s.creds[credRef]
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
	case "ssh":
		if cred.KeyPath == "" {
			return Credential{}, fmt.Errorf("credential %q: no key path configured", credRef)
		}
		return cred, nil
	default:
		return Credential{}, fmt.Errorf("credential %q: unknown kind %q", credRef, cred.Kind)
	}
}
