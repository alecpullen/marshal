package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

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
}

type FactoryConfig struct {
	RepoID                   string            `json:"repoId"`
	ProjectRoot              string            `json:"projectRoot"`
	HostID                   string            `json:"hostId"`
	TargetBranch             string            `json:"targetBranch"`
	Workspace                string            `json:"workspace"`
	Native                   bool              `json:"native"`
	ImplementerRouting       json.RawMessage   `json:"implementerRouting"`
	ReviewerRouting          json.RawMessage   `json:"reviewerRouting"`
	ReviewerInstructions     string            `json:"reviewerInstructions"`
	ImplementerAuto          bool              `json:"implementerAuto"`
	ExpectedChecks           []FactoryCheckKey `json:"expectedChecks"`
	NoRequiredCIAcknowledged bool              `json:"noRequiredCiAcknowledged"`
	MaxRounds                int               `json:"maxRounds"`
	CIPollSeconds            int               `json:"ciPollSeconds"`
	CIWaitSeconds            int               `json:"ciWaitSeconds"`
}

type FactoryCreate struct {
	RequestID string        `json:"requestId"`
	Title     string        `json:"title"`
	Plan      string        `json:"plan"`
	Spec      string        `json:"spec"`
	Config    FactoryConfig `json:"config"`
	// EditSnapshot is the reviewed selection a planner handoff bound to this
	// draft. It is optional and absent on every draft a Studio surface creates,
	// so those journals keep their bytes. The field is a pointer because
	// "no selection" and "an empty selection" are different approvals.
	EditSnapshot *EditSnapshot `json:"editSnapshot,omitempty"`
}

type FactoryApproval struct {
	Revision uint64 `json:"revision"`
	Plan     string `json:"plan"`
	Spec     string `json:"spec"`
	// PlanHash binds the exact plan, spec and selection this revision was
	// approved with (see factoryApprovalHash). An approval without a snapshot
	// holds the digest it always has.
	PlanHash     string        `json:"planHash"`
	Config       FactoryConfig `json:"config"`
	EditSnapshot *EditSnapshot `json:"editSnapshot,omitempty"`
	ApprovedAt   time.Time     `json:"approvedAt"`
}

type FactoryGrant struct {
	ID               string    `json:"id"`
	Reason           string    `json:"reason"`
	AdditionalRounds int       `json:"additionalRounds"`
	At               time.Time `json:"at"`
}

type FactoryWorker struct {
	AgentID       string `json:"agentId"`
	SessionID     string `json:"sessionId"`
	Role          string `json:"role"`
	SpawnEffectID string `json:"spawnEffectId"`
	Parked        bool   `json:"parked"`
}

type FactoryPR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	NodeID  string `json:"nodeId"`
	HeadRef string `json:"headRef"`
	BaseRef string `json:"baseRef"`
	HeadSHA string `json:"headSha"`
	BaseSHA string `json:"baseSha"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
}

type FactoryCheckKey struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	AppID int64  `json:"appId"`
}

type FactoryCheck struct {
	Key        FactoryCheckKey `json:"key"`
	ID         string          `json:"id"`
	SHA        string          `json:"sha"`
	Status     string          `json:"status"`
	Conclusion string          `json:"conclusion"`
	URL        string          `json:"url"`
	LogTail    string          `json:"logTail"`
	ObservedAt time.Time       `json:"observedAt"`
}

type FactoryCI struct {
	HeadSHA    string         `json:"headSha"`
	Checks     []FactoryCheck `json:"checks"`
	Complete   bool           `json:"complete"`
	ObservedAt time.Time      `json:"observedAt"`
}

type FactoryReport struct {
	OperationID      string            `json:"operationId"`
	Outcome          string            `json:"outcome"`
	Summary          string            `json:"summary"`
	Question         string            `json:"question"`
	Reason           string            `json:"reason"`
	BaseSHA          string            `json:"baseSha"`
	HeadSHA          string            `json:"headSha"`
	InputsAccessible bool              `json:"inputsAccessible"`
	Findings         []ReviewFinding   `json:"findings"`
	Narration        *FactoryNarration `json:"narration,omitempty"`
}

// FactoryNarration is the engineer's account of the change: the issue and the
// proposed solution, the reasoning behind the implementation, and the
// verification that backs it.
//
// The sections are the approved list. Constraints are narrated inside the
// approach and the implementation choice, because they are the reasons those
// two sections take the shape they do.
type FactoryNarration struct {
	Issue             string           `json:"issue"`
	Approach          string           `json:"approach"`
	ExistingBehaviour string           `json:"existingBehaviour"`
	Alternatives      string           `json:"alternatives"`
	Tradeoffs         string           `json:"tradeoffs"`
	Choice            string           `json:"choice"`
	Verification      string           `json:"verification"`
	Limitations       string           `json:"limitations"`
	Excerpts          []FactoryExcerpt `json:"excerpts,omitempty"`
}

// FactoryExcerpt is a selected piece of code that supports the narration. It
// is a selection, not a diff: the narration avoids narrating every changed
// line.
type FactoryExcerpt struct {
	Path string `json:"path"`
	Lang string `json:"lang,omitempty"`
	Code string `json:"code"`
}

type FactoryRound struct {
	Number            int            `json:"number"`
	ApprovalRevision  uint64         `json:"approvalRevision"`
	ImplementEffectID string         `json:"implementEffectId"`
	BaseSHA           string         `json:"baseSha"`
	HeadSHA           string         `json:"headSha"`
	TreeSHA           string         `json:"treeSha"`
	CI                *FactoryCI     `json:"ci"`
	Implementation    *FactoryReport `json:"implementation"`
	Review            *FactoryReport `json:"review"`
	StartedAt         time.Time      `json:"startedAt"`
	FinishedAt        time.Time      `json:"finishedAt"`
}

type FactoryEffect struct {
	// Outcome retains accepted stop reasons and confirmed adapter evidence.
	Outcome          *FactoryOutcome `json:"outcome"`
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	WorkerRole       string          `json:"workerRole"`
	Round            int             `json:"round"`
	ApprovalRevision uint64          `json:"approvalRevision"`
	Worker           *FactoryWorker  `json:"worker"`
	ExpectedHead     string          `json:"expectedHead"`
	ExpectedBase     string          `json:"expectedBase"`
	CreatedAt        time.Time       `json:"createdAt"`
}

type FactoryOutcome struct {
	EffectID   string         `json:"effectId"`
	StopReason string         `json:"stopReason"`
	Error      string         `json:"error"`
	HeadSHA    string         `json:"headSha"`
	TreeSHA    string         `json:"treeSha"`
	BaseSHA    string         `json:"baseSha"`
	Worker     *FactoryWorker `json:"worker"`
	PR         *FactoryPR     `json:"pr"`
	Report     *FactoryReport `json:"report"`
	Confirmed  bool           `json:"confirmed"`
}

type FactoryObservation struct {
	Workers         []FactoryWorker `json:"workers"`
	RuntimeKnown    bool            `json:"runtimeKnown"`
	TurnIdle        bool            `json:"turnIdle"`
	ActiveWorkKnown bool            `json:"activeWorkKnown"`
	WorkIdle        bool            `json:"workIdle"`
	HeadSHA         string          `json:"headSha"`
	TreeSHA         string          `json:"treeSha"`
	BaseSHA         string          `json:"baseSha"`
	RemoteHead      string          `json:"remoteHead"`
	Dirty           bool            `json:"dirty"`
	PR              *FactoryPR      `json:"pr"`
	CI              *FactoryCI      `json:"ci"`
	Error           string          `json:"error"`
	ObservedAt      time.Time       `json:"observedAt"`
}

type FactoryDecision struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Question  string    `json:"question"`
	Reason    string    `json:"reason"`
	EffectID  string    `json:"effectId"`
	CreatedAt time.Time `json:"createdAt"`
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

// FactoryProvenance attributes one journaled factory mutation to the MCP
// client that caused it. It is optional: an entry written before provenance
// existed still replays, and an ordinary Studio mutation records none.
//
// Action is the capability the mutation was admitted under, RequestID is the
// client's own request identity (the journal's is namespaced), and Authority
// is the grant's approvalAuthority or the answer mode the client claimed. It
// records what the server accepted, which for a user-directed action is a
// client assertion rather than a verified human instruction.
type FactoryProvenance struct {
	ClientID   string `json:"clientId"`
	ProjectID  string `json:"projectId"`
	Action     string `json:"action"`
	RequestID  string `json:"requestId"`
	Authority  string `json:"authority,omitempty"`
	DecisionID string `json:"decisionId,omitempty"`
	// The authenticated built-in principal, when the mutation came from a
	// session association rather than an MCP client. Every field is optional
	// and appended last, so a record written before session principals existed
	// — and a client's own provenance — marshals byte for byte as it always
	// did. AuthorityOrigin says where Authority came from; it is never claimed
	// by a caller.
	InstanceID      string `json:"instanceId,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	WorkerID        string `json:"workerId,omitempty"`
	Role            string `json:"role,omitempty"`
	AuthorityOrigin string `json:"authorityOrigin,omitempty"`
}

type FactoryEvent struct {
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Message  string    `json:"message"`
	EffectID string    `json:"effectId"`
	// Provenance is absent from every entry the Studio surfaces write, and
	// from every record journalled before it existed, so those bytes are
	// unchanged.
	Provenance *FactoryProvenance `json:"provenance,omitempty"`
}

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

type FactoryItem struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	State       string            `json:"state"`
	Stage       string            `json:"stage"`
	RepoID      string            `json:"repoId"`
	ProjectRoot string            `json:"projectRoot"`
	HostID      string            `json:"hostId"`
	Branch      string            `json:"branch"`
	BaseSHA     string            `json:"baseSha"`
	Version     uint64            `json:"version"`
	Draft       FactoryCreate     `json:"draft"`
	Approvals   []FactoryApproval `json:"approvals"`
	// Proposals is the append-only history of plan/specification revisions
	// proposed against this item. It is optional, so an item journalled
	// before proposals existed replays unchanged.
	Proposals  []FactoryRevisionProposal `json:"proposals,omitempty"`
	Grants     []FactoryGrant            `json:"grants"`
	Rounds     []FactoryRound            `json:"rounds"`
	Workers    []FactoryWorker           `json:"workers"`
	PRs        []FactoryPR               `json:"prs"`
	Effects    []FactoryEffect           `json:"effects"`
	Decision   *FactoryDecision          `json:"decision"`
	History    []FactoryEvent            `json:"history"`
	CreatedAt  time.Time                 `json:"createdAt"`
	ApprovedAt time.Time                 `json:"approvedAt"`
	UpdatedAt  time.Time                 `json:"updatedAt"`
}

type FactoryCommand struct {
	RequestID                    string          `json:"requestId"`
	Kind                         string          `json:"kind"`
	DecisionID                   string          `json:"decisionId"`
	Answer                       string          `json:"answer"`
	Reason                       string          `json:"reason"`
	ExpectedVersion              uint64          `json:"expectedVersion"`
	AdditionalRounds             int             `json:"additionalRounds"`
	Replacement                  *FactoryCreate  `json:"replacement"`
	ImplementerRouting           json.RawMessage `json:"implementerRouting"`
	ReviewerRouting              json.RawMessage `json:"reviewerRouting"`
	ConfirmPreviousWorkerStopped bool            `json:"confirmPreviousWorkerStopped"`
	// Revision is the structured payload of a plan-revision command:
	// revision_propose, revision_supersede, revision_reject,
	// revision_assess_request, revision_assess_claim, revision_assess_result,
	// revision_assess_retry, revision_authorize, revision_delivery_choice and
	// revision_follow_up_confirm read it, and every other kind leaves it nil,
	// so a command those kinds do not carry marshals as it always did.
	Revision *FactoryRevisionCommand `json:"revision,omitempty"`
}

// FactoryPlanHash binds exact plan and spec bytes, including whitespace.
func FactoryPlanHash(plan, spec string) string { return factoryPlanHash(plan, spec) }
