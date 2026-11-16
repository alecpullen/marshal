package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"marshal/web/bridge/contract"
)

// The factory item and its parts are contract types: web/bridge/contract
// declares them and both clients are generated from that declaration (F6
// decision 1). Each name below is an alias, so bridge code is unchanged and
// the journal's bytes stay exactly as they were — the declarations preserve
// field order, tags and the approval marshaller.
type (
	// FactorySubagentOverrides is contract.FactorySubagentOverrides.
	FactorySubagentOverrides = contract.FactorySubagentOverrides
	// FactorySubagentSettings is contract.FactorySubagentSettings.
	FactorySubagentSettings = contract.FactorySubagentSettings
	// FactoryConfig is contract.FactoryConfig.
	FactoryConfig = contract.FactoryConfig
	// FactoryCreate is contract.FactoryCreate.
	FactoryCreate = contract.FactoryCreate
	// FactoryApproval is contract.FactoryApproval.
	FactoryApproval = contract.FactoryApproval
	// FactoryGrant is contract.FactoryGrant.
	FactoryGrant = contract.FactoryGrant
	// FactoryWorker is contract.FactoryWorker.
	FactoryWorker = contract.FactoryWorker
	// FactoryPR is contract.FactoryPR.
	FactoryPR = contract.FactoryPR
	// FactoryCheckKey is contract.FactoryCheckKey.
	FactoryCheckKey = contract.FactoryCheckKey
	// FactoryCheck is contract.FactoryCheck.
	FactoryCheck = contract.FactoryCheck
	// FactoryCI is contract.FactoryCI.
	FactoryCI = contract.FactoryCI
	// FactoryReport is contract.FactoryReport.
	FactoryReport = contract.FactoryReport
	// FactoryNarration is contract.FactoryNarration.
	FactoryNarration = contract.FactoryNarration
	// FactoryExcerpt is contract.FactoryExcerpt.
	FactoryExcerpt = contract.FactoryExcerpt
	// FactoryRound is contract.FactoryRound.
	FactoryRound = contract.FactoryRound
	// FactoryDelegatedTurn is contract.FactoryDelegatedTurn.
	FactoryDelegatedTurn = contract.FactoryDelegatedTurn
	// FactoryEffect is contract.FactoryEffect.
	FactoryEffect = contract.FactoryEffect
	// FactoryOutcome is contract.FactoryOutcome.
	FactoryOutcome = contract.FactoryOutcome
	// FactoryDecision is contract.FactoryDecision.
	FactoryDecision = contract.FactoryDecision
	// FactoryProvenance is contract.FactoryProvenance.
	FactoryProvenance = contract.FactoryProvenance
	// FactoryEvent is contract.FactoryEvent.
	FactoryEvent = contract.FactoryEvent
	// FactoryItem is contract.FactoryItem.
	FactoryItem = contract.FactoryItem
	// FactoryCommand is contract.FactoryCommand.
	FactoryCommand = contract.FactoryCommand
)

// factorySubagentFreeze is the resolved pair an approval revision records. It
// is computed outside the journal's lock and carried on the command that
// writes the revision, so the settings land in the same append as the
// approval they belong to.
type factorySubagentFreeze struct {
	implementer FactorySubagentSettings
	reviewer    FactorySubagentSettings
}

// FactoryDefaults are a project's default factory configuration. A handoff
// inherits every field it leaves unset.
//
// The type is the rule: a field that is not here can never be inherited.
// RepoID and ProjectRoot are absent because they say what the work item is
// about and which project it belongs to. Native, ImplementerAuto and
// NoRequiredCIAcknowledged are absent because their false is a meaningful
// choice, and the handoff's plain FactoryConfig cannot tell "unset" from
// "explicitly false".
type FactoryDefaults struct {
	TargetBranch         string            `json:"targetBranch,omitempty"`
	Workspace            string            `json:"workspace,omitempty"`
	HostID               string            `json:"hostId,omitempty"`
	ImplementerRouting   json.RawMessage   `json:"implementerRouting,omitempty"`
	ReviewerRouting      json.RawMessage   `json:"reviewerRouting,omitempty"`
	ReviewerInstructions string            `json:"reviewerInstructions,omitempty"`
	ExpectedChecks       []FactoryCheckKey `json:"expectedChecks,omitempty"`
	MaxRounds            int               `json:"maxRounds,omitempty"`
	CIPollSeconds        int               `json:"ciPollSeconds,omitempty"`
	CIWaitSeconds        int               `json:"ciWaitSeconds,omitempty"`
	// ImplementerSubagents and ReviewerSubagents are the project's subagent
	// overrides. They are pointers for the same reason the handoff's are: an
	// absent field inherits the session's reported defaults, and an explicit
	// false is not an absence.
	ImplementerSubagents *FactorySubagentOverrides `json:"implementerSubagents,omitempty"`
	ReviewerSubagents    *FactorySubagentOverrides `json:"reviewerSubagents,omitempty"`
}

// The delegated turn's states. A waiting turn is not an outcome: the report it
// wrote is early. A continuation is journaled as prepared before anything is
// sent and as dispatched only after the journal says so, and the prompt itself
// is the last step — so a crash between the two writes leaves evidence the
// recovery path can read instead of a prompt that ran unrecorded.
const (
	factoryDelegatedWaiting    = "waiting"
	factoryDelegatedPrepared   = "prepared"
	factoryDelegatedDispatched = "dispatched"
	factoryDelegatedUncertain  = "uncertain"
)

// FactoryChild is one child of a worker session as that session's own status
// projects it. Its identity is the pair, never the id alone: child ids are
// session-local, so two worker sessions can both contain child 1 and must never
// be conflated.
type FactoryChild struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	// Terminal is true only for a state the projection names as finished.
	Terminal bool `json:"terminal"`
}

// FactoryChildSession is one worker session's child projection: the children it
// reported, and whether it reported at all. Known is false when the bridge
// could not read the session's children — a missing session, an unsupported
// projection, or an ambiguous child identity — because an unread projection can
// never be read as a settled empty set.
type FactoryChildSession struct {
	SessionID string         `json:"sessionId"`
	Known     bool           `json:"known"`
	Children  []FactoryChild `json:"children,omitempty"`
}

type FactoryObservation struct {
	Workers         []FactoryWorker `json:"workers"`
	RuntimeKnown    bool            `json:"runtimeKnown"`
	TurnIdle        bool            `json:"turnIdle"`
	ActiveWorkKnown bool            `json:"activeWorkKnown"`
	WorkIdle        bool            `json:"workIdle"`
	// Children is each live worker session's own child projection, keyed by the
	// session that reported it: child ids are session-local, so a child is the
	// pair and never the id alone. A session whose projection could not be read
	// is present with Known false, which is unknown work rather than an empty
	// settled set.
	Children   []FactoryChildSession `json:"children,omitempty"`
	HeadSHA    string                `json:"headSha"`
	TreeSHA    string                `json:"treeSha"`
	BaseSHA    string                `json:"baseSha"`
	RemoteHead string                `json:"remoteHead"`
	Dirty      bool                  `json:"dirty"`
	PR         *FactoryPR            `json:"pr"`
	CI         *FactoryCI            `json:"ci"`
	Error      string                `json:"error"`
	ObservedAt time.Time             `json:"observedAt"`
}

// Approval authority origin values, recorded in provenance so an entry says
// where the authority it acted under came from. Standing authority is what the
// store itself holds — a grant's approvalAuthority, or a built-in project or
// session default; verified_user_directed is a built-in operation admitted with
// a receipt this bridge minted for exactly that operation and no other attempt
// consumed, which is the only way a person's direction becomes evidence here.
//
// An external client's claim has no origin value: its entry names the client id
// and claims no more than the grant it acted under, so the absence of an origin
// is what distinguishes a client assertion from verified direction.
const (
	factoryAuthorityOriginStanding             = "standing"
	factoryAuthorityOriginVerifiedUserDirected = "verified_user_directed"
)

// factoryPrincipal is the server-authenticated MCP client behind a factory
// mutation. It is an argument threaded by the caller, never a field on a
// request type: neither the REST payload nor an MCP argument can name it, and
// the authority it carries is read from the stored grant while the policy lock
// is held.
//
// RequestID is the identity the client sent, not the journal's: every service
// entry point scopes it into the journal namespace and keeps the original here
// so provenance names what the client asked for.
//
// A built-in session's principal fills the instance, session, worker and role
// instead of ClientID, and its authority origin says the store held it. Every
// one of those fields is empty for a client, so its scope and its provenance
// are the ones it has always had.
type factoryPrincipal struct {
	ClientID  string
	ProjectID string
	Action    string
	RequestID string
	Authority string

	InstanceID      string
	SessionID       string
	WorkerID        string
	Role            string
	AuthorityOrigin string
}

// provenance renders the attribution a journal entry carries. decisionID names
// the question an answer resolved and is empty for every other action.
func (p *factoryPrincipal) provenance(decisionID string) *FactoryProvenance {
	if p == nil {
		return nil
	}
	return &FactoryProvenance{
		ClientID:        p.ClientID,
		ProjectID:       p.ProjectID,
		Action:          p.Action,
		RequestID:       p.RequestID,
		Authority:       p.Authority,
		DecisionID:      decisionID,
		InstanceID:      p.InstanceID,
		SessionID:       p.SessionID,
		WorkerID:        p.WorkerID,
		Role:            p.Role,
		AuthorityOrigin: p.AuthorityOrigin,
	}
}

// event is one journaled entry attributed to p. A nil principal produces
// exactly the entry the Studio surfaces have always written, so a legacy
// journal keeps its bytes.
func (p *factoryPrincipal) event(kind, message, decisionID string, now time.Time) FactoryEvent {
	return FactoryEvent{At: now, Kind: kind, Message: message, Provenance: p.provenance(decisionID)}
}

// scoped is the journal's request identity for a request this principal sent.
//
// A built-in session is scoped by the association that authenticated it —
// instance, project, session and, when it names one, worker — so two sessions
// in one project can use the same request id without colliding, while one
// session retrying is still one operation. An MCP client keeps exactly the
// identity it has always had.
func (p *factoryPrincipal) scoped(requestID string) string {
	if p.InstanceID != "" || p.SessionID != "" {
		return marshalScopedRequestID(p.InstanceID, p.ProjectID, p.SessionID, p.WorkerID, requestID)
	}
	return factoryScopedRequestID(p.ClientID, requestID)
}

// marshalScopedRequestID is the journal identity of one built-in session's
// request. The separator is a NUL, so no concatenation of two different scopes
// can produce one identity.
func marshalScopedRequestID(instanceID, projectID, sessionID, workerID, requestID string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{instanceID, projectID, sessionID, workerID, requestID}, "\x00")))
	return "marshal:" + hex.EncodeToString(h[:])
}

// principal is the journaled identity of one mutation a verified built-in
// session performed: the scope its request identity is namespaced by and the
// attribution it is recorded under. Its authority is the effective approval
// authority and its origin is standing, because the store — not the caller —
// holds that authority.
func (b MarshalSessionBinding) principal(action string, access MarshalFactoryAccess) factoryPrincipal {
	authority := ""
	if access.ApprovalAuthority != nil {
		authority = *access.ApprovalAuthority
	}
	return factoryPrincipal{
		InstanceID:      b.InstanceID,
		ProjectID:       b.ProjectID,
		SessionID:       b.SessionID,
		WorkerID:        b.WorkerID,
		Role:            b.Role,
		Action:          action,
		Authority:       authority,
		AuthorityOrigin: factoryAuthorityOriginStanding,
	}
}

// FactoryPlanHash binds exact plan and spec bytes, including whitespace.
func FactoryPlanHash(plan, spec string) string { return factoryPlanHash(plan, spec) }
