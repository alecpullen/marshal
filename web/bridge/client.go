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
	AllowedRepos []string `json:"allowedRepos,omitempty"`
	// FactoryGrants are this client's explicit factory authority, one
	// record per project. It is additive and optional: a client
	// registered before grants existed holds none, and holding none is
	// the same as holding no authority at all.
	FactoryGrants []MCPFactoryProjectGrant `json:"factoryGrants,omitempty"`
	OwnerID       string                   `json:"ownerId"`
	CreatedAt     time.Time                `json:"createdAt"`
}

// MCPFactoryProjectGrant is one client's factory authority over one
// project. Every capability is independent and defaults to refused, so a
// grant that exists but names nothing grants nothing. Nothing here is
// implied by Autonomous or AllowedRepos: those are fleet-spawn policy, and
// a client that may spawn agents may still propose no factory work.
type MCPFactoryProjectGrant struct {
	ProjectID         string `json:"projectId"`
	Read              bool   `json:"read"`
	CreateDraft       bool   `json:"createDraft"`
	EditDraft         bool   `json:"editDraft"`
	AnswerQuestion    bool   `json:"answerQuestion"`
	ApproveAndQueue   bool   `json:"approveAndQueue"`
	ApprovalAuthority string `json:"approvalAuthority"` // none, user_directed, standing
}

// FactoryGrantState is one client/project pair's stored grant and the
// opaque version that guards writes to it. Version is a hash of the
// canonical grant JSON, so an editor that writes from a stale read is
// refused rather than silently overwriting someone else's edit.
type FactoryGrantState struct {
	ClientID  string                 `json:"clientId"`
	ProjectID string                 `json:"projectId"`
	Grant     MCPFactoryProjectGrant `json:"grant"`
	Version   string                 `json:"version"`
}

// clone returns a copy that shares no mutable storage with c, so a caller
// cannot reach into the store through the value it was handed.
func (c MCPClient) clone() MCPClient {
	out := c
	out.AllowedRepos = append([]string(nil), c.AllowedRepos...)
	out.FactoryGrants = append([]MCPFactoryProjectGrant(nil), c.FactoryGrants...)
	return out
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
