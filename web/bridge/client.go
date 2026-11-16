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
	ProjectID       string `json:"projectId"`
	Read            bool   `json:"read"`
	CreateDraft     bool   `json:"createDraft"`
	EditDraft       bool   `json:"editDraft"`
	AnswerQuestion  bool   `json:"answerQuestion"`
	ApproveAndQueue bool   `json:"approveAndQueue"`
	// AssessPlanChanges and ApproveRoutineRevision are the plan-revision
	// capabilities. Zero values grant nothing, and omitempty keeps a grant
	// that holds neither marshalling exactly as it always did. Both are
	// checked by the authorizer like every capability above: the first admits
	// a claim on one proposal's assessment and the result its claimant
	// delivers, and the second is one of the facts rechecked under the policy
	// lock before a routine clarification is authorized.
	AssessPlanChanges      bool   `json:"assessPlanChanges,omitempty"`
	ApproveRoutineRevision bool   `json:"approveRoutineRevision,omitempty"`
	ApprovalAuthority      string `json:"approvalAuthority"` // none, user_directed, standing
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

// MarshalFactoryAccess is a built-in principal's factory authority, in the
// shape both the store and the connection status report it. Every field is a
// pointer because "unset" and an explicit false are different answers: an
// unset field inherits the layer beneath it, while an explicit false is a
// denial that stays one until it is reset.
//
// The effective default is read-only — read on, every mutation off, no
// approval authority — and a project default then a session/worker override
// replace each field they carry. Nothing here is widened by a role: a worker's
// role and item are recorded, never consulted.
type MarshalFactoryAccess struct {
	Read              *bool   `json:"read,omitempty"`
	CreateDraft       *bool   `json:"createDraft,omitempty"`
	EditDraft         *bool   `json:"editDraft,omitempty"`
	AnswerQuestion    *bool   `json:"answerQuestion,omitempty"`
	ApproveAndQueue   *bool   `json:"approveAndQueue,omitempty"`
	ApprovalAuthority *string `json:"approvalAuthority,omitempty"` // none, user_directed, standing
}

// clone returns a copy that shares no storage with a. Every field is a
// pointer, so without it a caller could reach into a stored value through the
// one it was handed.
func (a MarshalFactoryAccess) clone() MarshalFactoryAccess {
	out := a
	out.Read = copyBool(a.Read)
	out.CreateDraft = copyBool(a.CreateDraft)
	out.EditDraft = copyBool(a.EditDraft)
	out.AnswerQuestion = copyBool(a.AnswerQuestion)
	out.ApproveAndQueue = copyBool(a.ApproveAndQueue)
	if a.ApprovalAuthority != nil {
		authority := *a.ApprovalAuthority
		out.ApprovalAuthority = &authority
	}
	return out
}

// copyBool is a fresh pointer to the same bool, or nil.
func copyBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	return boolPtr(*b)
}

// MarshalSessionBinding is the server-owned association between one
// authenticated built-in session and the project it may act on. It is
// established by the control plane's trusted session association and never by
// a tool argument or a request body. Generation changes on a restart or an
// owner change, so credentials and verified receipts cannot cross one.
type MarshalSessionBinding struct {
	InstanceID string `json:"instanceId"`
	ProjectID  string `json:"projectId"`
	SessionID  string `json:"sessionId"`
	WorkerID   string `json:"workerId,omitempty"`
	ItemID     string `json:"itemId,omitempty"`
	Role       string `json:"role,omitempty"`
	Generation string `json:"generation"`
}

// MarshalConnectionStatus is what the control plane reports about one built-in
// session's factory connection: the state it reached, the identities it
// verified, and the effective access every pointer of which has been resolved.
//
// Source names the layers that contributed and whether each was the stored
// override or the default beneath it; it names no paths and no authority.
type MarshalConnectionStatus struct {
	State      string               `json:"state"` // connected, unavailable, ambiguous, unsupported, stale
	InstanceID string               `json:"instanceId,omitempty"`
	ProjectID  string               `json:"projectId,omitempty"`
	SessionID  string               `json:"sessionId,omitempty"`
	VerifiedAt string               `json:"verifiedAt,omitempty"` // RFC3339 UTC
	Access     MarshalFactoryAccess `json:"access"`
	Source     map[string]string    `json:"source,omitempty"` // project or session
}

// marshalStateConnected is the one connection state this task reports. The
// enrollment states — unavailable, ambiguous, unsupported, stale — belong to
// the session association that establishes a binding.
const marshalStateConnected = "connected"

// marshalFactoryAccessDefault is the effective access of a verified built-in
// session nothing has been stored for: it may read the project's factory and
// change nothing.
func marshalFactoryAccessDefault() MarshalFactoryAccess {
	none := factoryAuthorityNone
	return MarshalFactoryAccess{
		Read:              boolPtr(true),
		CreateDraft:       boolPtr(false),
		EditDraft:         boolPtr(false),
		AnswerQuestion:    boolPtr(false),
		ApproveAndQueue:   boolPtr(false),
		ApprovalAuthority: &none,
	}
}

// resolveMarshalFactoryAccess is the effective access of one built-in
// principal: the read-only default, then each layer in the order it is given —
// project defaults first, the session/worker override second. A field a layer
// leaves unset inherits the layer beneath it; an explicit false is a denial and
// stays one.
func resolveMarshalFactoryAccess(layers ...MarshalFactoryAccess) MarshalFactoryAccess {
	effective := marshalFactoryAccessDefault()
	for _, layer := range layers {
		if layer.Read != nil {
			effective.Read = copyBool(layer.Read)
		}
		if layer.CreateDraft != nil {
			effective.CreateDraft = copyBool(layer.CreateDraft)
		}
		if layer.EditDraft != nil {
			effective.EditDraft = copyBool(layer.EditDraft)
		}
		if layer.AnswerQuestion != nil {
			effective.AnswerQuestion = copyBool(layer.AnswerQuestion)
		}
		if layer.ApproveAndQueue != nil {
			effective.ApproveAndQueue = copyBool(layer.ApproveAndQueue)
		}
		if layer.ApprovalAuthority != nil {
			authority := *layer.ApprovalAuthority
			effective.ApprovalAuthority = &authority
		}
	}
	return effective
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
