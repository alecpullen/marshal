package bridge

import (
	"encoding/json"
	"time"
)

// FactorySubagentOverrides is one role's subagent settings as a draft or a
// project's factory defaults state them. Every field is a pointer because the
// resolution chain distinguishes "unset" from "stated": a missing override
// inherits the next source, and an explicit false or an explicit empty model
// is a choice that is preserved.
//
// A nil Model or a Model pointing at "" both mean normal child routing: only
// an explicit override is ever written here, so an empty string is a stated
// choice rather than an absence.
type FactorySubagentOverrides struct {
	Enabled     *bool   `json:"enabled,omitempty"`
	Model       *string `json:"model,omitempty"`
	Concurrency *int    `json:"concurrency,omitempty"`
}

// FactorySubagentSettings is one role's resolved subagent settings. It is what
// the resolution chain produced for a draft, and what an approval revision
// freezes: a later default change cannot reach an active revision through it.
//
// An approval journalled before subagent settings existed carries the zero
// value, which is why a zero Concurrency means "no snapshot" rather than a
// cap of zero.
type FactorySubagentSettings struct {
	Enabled     bool   `json:"enabled"`
	Model       string `json:"model,omitempty"`
	Concurrency int    `json:"concurrency"`
}

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
	// ImplementerSubagents and ReviewerSubagents hold only the work item's
	// explicit subagent overrides: nil means inherit. Nothing is copied onto
	// the draft from the project's defaults, so a draft follows a project
	// default it never stated while an override it does state stays fixed.
	// They are omitted when nil, so a journal written before they existed
	// keeps its bytes.
	ImplementerSubagents *FactorySubagentOverrides `json:"implementerSubagents,omitempty"`
	ReviewerSubagents    *FactorySubagentOverrides `json:"reviewerSubagents,omitempty"`
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
	// ImplementerSubagents and ReviewerSubagents are the subagent settings
	// this revision froze: runtime defaults, then the project's override, then
	// the draft's override, resolved when the approval was written. They are
	// values, not pointers, because an approval states what was approved.
	// An approval journalled before they existed carries the zero value and
	// keeps its bytes; nothing re-derives them for it.
	ImplementerSubagents FactorySubagentSettings `json:"implementerSubagents"`
	ReviewerSubagents    FactorySubagentSettings `json:"reviewerSubagents"`
	ApprovedAt           time.Time               `json:"approvedAt"`
}

// legacyFactoryApproval is the approval object a journal written before
// subagent settings carried: the same fields, in the same order, without the
// two settings snapshots.
type legacyFactoryApproval struct {
	Revision     uint64        `json:"revision"`
	Plan         string        `json:"plan"`
	Spec         string        `json:"spec"`
	PlanHash     string        `json:"planHash"`
	Config       FactoryConfig `json:"config"`
	EditSnapshot *EditSnapshot `json:"editSnapshot,omitempty"`
	ApprovedAt   time.Time     `json:"approvedAt"`
}

// MarshalJSON keeps a journal written before subagent settings existed
// byte-identical. The journal is replayed by re-marshalling each record and
// comparing it with the stored bytes, so an approval that froze nothing has to
// write exactly the keys it always did: the two settings snapshots are omitted
// when both are zero, which is also the value a legacy record decodes to.
//
// An approval that froze settings writes them, and only an approval that froze
// settings can be enforced.
func (a FactoryApproval) MarshalJSON() ([]byte, error) {
	if a.ImplementerSubagents == (FactorySubagentSettings{}) && a.ReviewerSubagents == (FactorySubagentSettings{}) {
		return json.Marshal(legacyFactoryApproval{
			Revision:     a.Revision,
			Plan:         a.Plan,
			Spec:         a.Spec,
			PlanHash:     a.PlanHash,
			Config:       a.Config,
			EditSnapshot: a.EditSnapshot,
			ApprovedAt:   a.ApprovedAt,
		})
	}
	// The alias keeps this function off the value's own method set.
	type plain FactoryApproval
	return json.Marshal(plain(a))
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
	// ApprovalRevision and Subagents bind the snapshot this worker was started
	// with: the approval revision it came from and that revision's settings for
	// this worker's own role. A worker started before they existed carries the
	// zero value for both, and FactoryWorker.MarshalJSON keeps that worker's
	// record byte-identical so an older journal still replays.
	ApprovalRevision uint64                  `json:"approvalRevision,omitempty"`
	Subagents        FactorySubagentSettings `json:"subagents"`
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
type factoryPrincipal struct {
	ClientID  string
	ProjectID string
	Action    string
	RequestID string
	Authority string
}

// provenance renders the attribution a journal entry carries. decisionID names
// the question an answer resolved and is empty for every other action.
func (p *factoryPrincipal) provenance(decisionID string) *FactoryProvenance {
	if p == nil {
		return nil
	}
	return &FactoryProvenance{
		ClientID:   p.ClientID,
		ProjectID:  p.ProjectID,
		Action:     p.Action,
		RequestID:  p.RequestID,
		Authority:  p.Authority,
		DecisionID: decisionID,
	}
}

// event is one journaled entry attributed to p. A nil principal produces
// exactly the entry the Studio surfaces have always written, so a legacy
// journal keeps its bytes.
func (p *factoryPrincipal) event(kind, message, decisionID string, now time.Time) FactoryEvent {
	return FactoryEvent{At: now, Kind: kind, Message: message, Provenance: p.provenance(decisionID)}
}

// scoped is the journal's request identity for a request this client sent.
func (p *factoryPrincipal) scoped(requestID string) string {
	return factoryScopedRequestID(p.ClientID, requestID)
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
	// SettingsVersion is the settings version the review surface displayed:
	// the project's settings ETag and the session's reported defaults digest,
	// as one string. An approval that carries it is refused when either side
	// has changed since review; an approval that omits it — a legacy client,
	// or a command replayed from an older journal — proceeds as it always did.
	SettingsVersion string `json:"settingsVersion,omitempty"`
	// subagents is the resolved subagent pair an approval freezes, computed
	// outside the journal's lock and never serialised: it rides the same
	// append as the approval it belongs to, and a journal replays the item it
	// stored rather than the command.
	subagents *factorySubagentFreeze
}

// FactoryPlanHash binds exact plan and spec bytes, including whitespace.
func FactoryPlanHash(plan, spec string) string { return factoryPlanHash(plan, spec) }
