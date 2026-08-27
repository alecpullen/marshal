package bridge

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"
)

// MCPClient is one registered MCP consumer.
//
// The token is shown once at creation and stored only as a hash: a
// readable fleet.json must not yield working credentials. Client
// identity exists because per-client autonomy is impossible without it,
// and because a client holding the shared API bearer could bypass MCP
// and drive the REST API directly.
type MCPClient struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"tokenHash"`
	// Autonomous skips the confirmation gate for this client.
	Autonomous bool `json:"autonomous,omitempty"`
	// MaxConcurrent and MaxPerDay bound this client specifically, so a
	// runaway loop in someone's editor cannot spawn fifty agents
	// overnight. Zero means unbounded.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	MaxPerDay     int `json:"maxPerDay,omitempty"`
	// AllowedRepos restricts which registered repos this client may
	// target. Empty means every registered repo.
	AllowedRepos []string  `json:"allowedRepos,omitempty"`
	OwnerID      string    `json:"ownerId"`
	CreatedAt    time.Time `json:"createdAt"`
}

// tokenBytes is the entropy in a client token. 32 bytes is well beyond
// guessing range and keeps the hex form a manageable 64 characters.
const tokenBytes = 32

// NewClientToken mints a token, returning the plaintext to show the
// operator once and the hash to persist.
func NewClientToken() (plain, hash string, err error) {
	var b [tokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("bridge: generate client token: %w", err)
	}
	plain = hex.EncodeToString(b[:])
	return plain, HashToken(plain), nil
}

// HashToken is the one-way transform applied before storage.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// MatchClient finds the client a presented token belongs to.
//
// Every candidate is compared even after a match, and the comparison is
// constant-time, so neither the elapsed time nor the position in the
// list reveals which client matched — or whether any did.
func MatchClient(clients []MCPClient, presented string) (MCPClient, bool) {
	want := []byte(HashToken(presented))
	var found MCPClient
	matched := 0
	for _, c := range clients {
		if subtle.ConstantTimeCompare([]byte(c.TokenHash), want) == 1 {
			found = c
			matched++
		}
	}
	return found, matched == 1
}
