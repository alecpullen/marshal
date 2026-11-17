package contract

import (
	"encoding/json"
	"time"
)

// The factory's wire types (F6 decision 1): a work item and everything
// `GET /api/factory/items/{id}` returns, plus the command a client posts back
// to `POST /api/factory/items/{id}/commands`.
//
// The declarations are the bridge's own, moved here unchanged so both clients
// are generated from one source; `web/bridge` aliases each name to the type
// below, so no bridge code or journal byte changed with the move. Field
// order matters and is preserved: a journal record is replayed by
// re-marshalling it and comparing bytes.
//
// Two of the types hold a method or a field that only the bridge reads, which
// is why they differ from the aliases they replace:
//
//   - FactoryApproval carries MarshalJSON, which keeps a journal written
//     before subagent settings existed byte-identical. A Go method cannot be
//     declared outside its type's package, so the marshaller lives here; it is
//     the package's only method.
//   - FactoryOutcome.Continuation and FactoryCommand's two freeze fields are
//     bridge-internal carriers (tagged `json:"-"`), never wire fields: they are
//     exported only because the bridge, which sets them, is another package.
//
// Enumeration values (states, command kinds, assessment states) are plain
// strings on the wire and are not declared as named types here: the bridge
// treats them as strings, and a second vocabulary would have to be kept in
// step with it.

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

// SelectedFile is one repository-relative file a snapshot captured. Path is a
// slash path relative to the planner's workspace root, never a host path, and
// Operation is exactly create, modify or delete.
type SelectedFile struct {
	Path          string `json:"path"`
	Operation     string `json:"operation"`
	BaseSHA256    string `json:"baseSha256,omitempty"`
	ContentSHA256 string `json:"contentSha256,omitempty"`
	Executable    bool   `json:"executable"`
}

// EditSnapshot is the hashed core of a snapshot: the base commit it was taken
// against and the files it froze, in the order the caller selected them. Its
// SHA256 is the digest of exactly those two fields, so the same capture always
// hashes the same way, whatever else the record carries.
type EditSnapshot struct {
	BaseCommit string         `json:"baseCommit"`
	Files      []SelectedFile `json:"files"`
	SHA256     string         `json:"sha256"`
}

// ReviewFinding is one finding of a review-findings result.
type ReviewFinding struct {
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	StepNode string `json:"stepNode,omitempty"`
}

type FactoryConfig struct {
	RepoID               string            `json:"repoId"`
	ProjectRoot          string            `json:"projectRoot"`
	HostID               string            `json:"hostId"`
	TargetBranch         string            `json:"targetBranch"`
	Workspace            string            `json:"workspace"`
	Native               bool              `json:"native"`
	ImplementerRouting   json.RawMessage   `json:"implementerRouting"`
	ReviewerRouting      json.RawMessage   `json:"reviewerRouting"`
	ReviewerInstructions string            `json:"reviewerInstructions"`
	ImplementerAuto      bool              `json:"implementerAuto"`
	ExpectedChecks       []FactoryCheckKey `json:"expectedChecks"`
	// The tag's spelling is pre-move and load-bearing; see factory_tags_test.go.
	NoRequiredCIAcknowledged bool `json:"noRequiredCiAcknowledged"`
	MaxRounds                int  `json:"maxRounds"`
	CIPollSeconds            int  `json:"ciPollSeconds"`
	CIWaitSeconds            int  `json:"ciWaitSeconds"`
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

// legacyFactoryWorker is the worker object a journal written before approved
// subagent settings carried: the same fields, in the same order, without the
// snapshot a worker now binds.
type legacyFactoryWorker struct {
	AgentID       string `json:"agentId"`
	SessionID     string `json:"sessionId"`
	Role          string `json:"role"`
	SpawnEffectID string `json:"spawnEffectId"`
	Parked        bool   `json:"parked"`
}

// MarshalJSON keeps a worker that bound nothing byte-identical to the record it
// always wrote. The journal is replayed by re-marshalling each record and
// comparing it with the stored bytes, so a record written before a worker bound
// its snapshot has to write exactly the keys it always did. It is the same rule
// FactoryApproval.MarshalJSON follows, for the same reason.
func (w FactoryWorker) MarshalJSON() ([]byte, error) {
	if w.ApprovalRevision == 0 && w.Subagents == (FactorySubagentSettings{}) {
		return json.Marshal(legacyFactoryWorker{w.AgentID, w.SessionID, w.Role, w.SpawnEffectID, w.Parked})
	}
	// The alias keeps this function off the value's own method set.
	type plain FactoryWorker
	return json.Marshal(plain(w))
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
	// zero value for both, and FactoryWorker's marshalling keeps that worker's
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

// FactoryDelegatedTurn is one worker turn's delegated child work: the children
// the turn launched, and how far they have been proven. It is journalled beside
// the effect that dispatched the turn, so the item keeps the effect and the
// candidate identity it was dispatched with until its children are settled.
// The waiting state is not an outcome: the report the turn wrote before those
// children settled is early, and is never the effect's result.
type FactoryDelegatedTurn struct {
	EffectID       string  `json:"effectId"`
	WorkerID       string  `json:"workerId"`
	SessionID      string  `json:"sessionId"`
	ParentTurnID   string  `json:"parentTurnId"`
	ChildIDs       []int64 `json:"childIds"`
	ContinuationID string  `json:"continuationId,omitempty"`
	State          string  `json:"state"` // waiting, prepared, dispatched, settled, uncertain
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
	// Delegated is the turn's launched child work while it is not proven
	// settled, and the waiting state Studio shows beside the effect's status.
	// It is omitted when nil, so a record journalled before delegated turns
	// existed keeps its bytes.
	Delegated *FactoryDelegatedTurn `json:"delegated,omitempty"`
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
	// Delegated is the waiting turn a worker effect hands back instead of a
	// result: the turn launched children it did not prove settled, so the report
	// it wrote before they settled is early and is never applied. It is omitted
	// when nil, so a completed outcome keeps the bytes it always wrote.
	Delegated *FactoryDelegatedTurn `json:"delegated,omitempty"`
	// Continuation is the iteration this result was produced for. It is a
	// bridge-internal carrier and never crosses the wire: the delegated turn
	// beside the effect is what persists, and a recovered outcome is proved
	// from the session's own transcript instead.
	Continuation string `json:"-"`
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
	Proposals []FactoryRevisionProposal `json:"proposals,omitempty"`
	Grants    []FactoryGrant            `json:"grants"`
	Rounds    []FactoryRound            `json:"rounds"`
	Workers   []FactoryWorker           `json:"workers"`
	PRs       []FactoryPR               `json:"prs"`
	Effects   []FactoryEffect           `json:"effects"`
	Decision  *FactoryDecision          `json:"decision"`
	History   []FactoryEvent            `json:"history"`
	// Completion is the durable business completion status, which is
	// independent of State: the work counts as done as far as the user is
	// concerned, whatever the run happens to be doing. It is optional, so an
	// item journalled before completion existed replays unchanged, and a nil
	// record (or one whose status is empty) is open.
	Completion *FactoryCompletion `json:"completion,omitempty"`
	CreatedAt  time.Time          `json:"createdAt"`
	ApprovedAt time.Time          `json:"approvedAt"`
	UpdatedAt  time.Time          `json:"updatedAt"`
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
	// Completion is the structured payload of the completion commands:
	// link_pr reads the pull request it names, refresh_pr_evidence carries
	// nothing of its own, and the merge kinds read the reviewed set, the
	// strategy, the completion intent and the run choice. Every other kind
	// leaves it nil, so a command those kinds do not carry marshals as it
	// always did.
	Completion *FactoryCompletionCommand `json:"completion,omitempty"`
	// SettingsVersion is the settings version the review surface displayed:
	// the project's settings ETag and the session's reported defaults digest,
	// as one string. An approval that carries it is refused when either side
	// has changed since review; an approval that omits it — a legacy client,
	// or a command replayed from an older journal — proceeds as it always did.
	SettingsVersion string `json:"settingsVersion,omitempty"`
	// ImplementerFreeze and ReviewerFreeze carry the resolved subagent pair an
	// approval freezes, computed outside the journal's lock and never
	// serialised: they ride the same append as the approval they belong to,
	// and a journal replays the item it stored rather than the command. Both
	// are set together, or neither is.
	ImplementerFreeze *FactorySubagentSettings `json:"-"`
	ReviewerFreeze    *FactorySubagentSettings `json:"-"`
}

// FactoryRevisionAssessment is a planner's recorded judgement about one
// proposed revision. It is advisory: it names the classification it asserts,
// the work it believes is affected, the routing it recommends and whether the
// proposal can wait for a safe boundary or needs the running turn interrupted.
//
// Classification and strategy are assertions by a trusted planner, not proofs:
// the journal records what was claimed, and authorization rechecks who may act
// on it.
type FactoryRevisionAssessment struct {
	Classification   string `json:"classification"` // routine_clarification | scope_change | needs_user
	Reason           string `json:"reason"`
	AffectedEffectID string `json:"affectedEffectId,omitempty"`
	AffectedWorkerID string `json:"affectedWorkerId,omitempty"`
	Recommendation   string `json:"recommendation"` // amend | follow_up
	Strategy         string `json:"strategy"`       // wait_safe | interrupt | hold
	AssessorID       string `json:"assessorId"`
	CandidateHash    string `json:"candidateHash"`
	BaseRevision     uint64 `json:"baseRevision"`
}

// FactoryRevisionDelivery is the recorded intent and evidence for handing an
// authorized revision to the worker that is running, or will run, the item.
//
// It is evidence, not a request log: a state here was observed, or the outcome
// of the attempt is explicitly unknown. "delivered" means the runtime accepted
// the input, never that the agent followed it.
type FactoryRevisionDelivery struct {
	Strategy      string `json:"strategy"` // wait_safe | interrupt | hold
	State         string `json:"state"`    // pending | dispatching | accepted | interrupting | waiting_safe_boundary | waiting_for_resume | delivered | undelivered | uncertain
	SessionID     string `json:"sessionId,omitempty"`
	TurnID        string `json:"turnId,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	CandidateHash string `json:"candidateHash"`
}

// FactoryRevisionProposal is one immutable plan/specification revision
// proposed against an approved factory revision.
//
// The candidate bytes, the base it was proposed against, its hash and its
// creator are journal history: an edit is a new proposal, and replacing an
// unresolved one explicitly supersedes it rather than rewriting it. Only
// state, assessment, authority and delivery move afterwards, and every one of
// those moves bumps Version so a caller writing from a stale read is refused.
//
// While an amendment is authorized but not yet activated, the item's active
// approval stays the last entry of FactoryItem.Approvals: an in-flight worker
// keeps the revision it was started under.
type FactoryRevisionProposal struct {
	ID                  string                     `json:"id"`
	Version             uint64                     `json:"version"`
	BaseRevision        uint64                     `json:"baseRevision"`
	BaseHash            string                     `json:"baseHash"`
	Plan                string                     `json:"plan"`
	Spec                string                     `json:"spec"`
	CandidateHash       string                     `json:"candidateHash"`
	CreatedBy           string                     `json:"createdBy"`
	AutoAssess          bool                       `json:"autoAssess"`
	Status              string                     `json:"status"`          // proposed | awaiting_authorization | authorized | activated | rejected | superseded | follow_up_pending | followed_up
	AssessmentState     string                     `json:"assessmentState"` // not_requested | pending | assessing | assessed | unavailable | uncertain | failed
	AssessmentAttemptID string                     `json:"assessmentAttemptId,omitempty"`
	Assessment          *FactoryRevisionAssessment `json:"assessment,omitempty"`
	AuthorizedBy        string                     `json:"authorizedBy,omitempty"`
	Authority           string                     `json:"authority,omitempty"`
	Routing             string                     `json:"routing,omitempty"`    // amend | follow_up
	PauseState          string                     `json:"pauseState,omitempty"` // requested | stopping | paused | uncertain
	PauseRequestID      string                     `json:"pauseRequestId,omitempty"`
	Delivery            *FactoryRevisionDelivery   `json:"delivery,omitempty"`
	FollowUpRequestID   string                     `json:"followUpRequestId,omitempty"`
	FollowUpItemID      string                     `json:"followUpItemId,omitempty"`
	CreatedAt           time.Time                  `json:"createdAt"`
	UpdatedAt           time.Time                  `json:"updatedAt"`
}

// FactoryRevisionCommand is one plan-revision command's structured payload.
//
// A candidate is named by its exact bytes and the hash that binds them, and
// every mutation names the proposal and the version the caller read, so a
// stale command conflicts instead of silently rebasing. BaseRevision and
// BaseHash name the approved revision the candidate was written against.
type FactoryRevisionCommand struct {
	ProposalID      string                     `json:"proposalId,omitempty"`
	ProposalVersion uint64                     `json:"proposalVersion,omitempty"`
	BaseRevision    uint64                     `json:"baseRevision,omitempty"`
	BaseHash        string                     `json:"baseHash,omitempty"`
	Plan            string                     `json:"plan,omitempty"`
	Spec            string                     `json:"spec,omitempty"`
	CandidateHash   string                     `json:"candidateHash,omitempty"`
	AttemptID       string                     `json:"attemptId,omitempty"`
	Assessment      *FactoryRevisionAssessment `json:"assessment,omitempty"`
	Routing         string                     `json:"routing,omitempty"`
	Strategy        string                     `json:"strategy,omitempty"`
	FollowUp        *FactoryCreate             `json:"followUp,omitempty"`
}

// FactoryCompletionCommand is the structured payload of the completion
// commands. Each kind reads only the fields it needs, so a command those
// kinds do not carry marshals as it always did.
type FactoryCompletionCommand struct {
	// Link is the pull request a link_pr command names: a number or a URL.
	Link string `json:"link,omitempty"`
	// PRs is the reviewed, ordered set a merge_pr or merge_and_complete
	// command authorizes.
	PRs []FactoryCompletionPR `json:"prs,omitempty"`
	// Strategy is the reviewed merge strategy.
	Strategy string `json:"strategy,omitempty"`
	// CompleteAfter completes the item once every reviewed merge confirms.
	CompleteAfter bool `json:"completeAfter,omitempty"`
	// RunChoice is continue or request_stop when the item's own run is
	// active or its state cannot be established.
	RunChoice string `json:"runChoice,omitempty"`
}

// FactoryCompletionPR is one pull request a user reviewed for a merge batch.
type FactoryCompletionPR struct {
	RepoID  string `json:"repoId"`
	Number  int    `json:"number"`
	NodeID  string `json:"nodeId"`
	HeadSHA string `json:"headSha"`
	BaseRef string `json:"baseRef"`
}

type FactoryPRLink struct {
	RepoID     string    `json:"repoId"`
	PR         FactoryPR `json:"pr"`
	Origin     string    `json:"origin"` // factory or user
	ObservedAt time.Time `json:"observedAt"`
	Fresh      bool      `json:"fresh"`
}

type FactoryAssessment struct {
	Status      string    `json:"status"` // pending, available, failed, unavailable
	Summary     string    `json:"summary,omitempty"`
	Source      string    `json:"source,omitempty"`
	InputDigest string    `json:"inputDigest"`
	ObservedAt  time.Time `json:"observedAt"`
	// Fresh is derived, never stored: the server recomputes it on every item
	// it serves by comparing InputDigest with the item's current input digest,
	// so a result computed against inputs that have since moved is shown with
	// its summary intact and fresh false. The journalled value is never
	// trusted on read.
	Fresh bool `json:"fresh"`
}

type FactoryCompletionRecord struct {
	ID         string             `json:"id"`
	At         time.Time          `json:"at"`
	Actor      string             `json:"actor"`
	Goal       string             `json:"goal"`
	Criteria   string             `json:"criteria"`
	Assessment *FactoryAssessment `json:"assessment,omitempty"`
	Links      []FactoryPRLink    `json:"links"`
	RunChoice  string             `json:"runChoice,omitempty"` // continue or request_stop
}

type FactoryMergeStep struct {
	RepoID   string `json:"repoId"`
	Number   int    `json:"number"`
	NodeID   string `json:"nodeId"`
	HeadSHA  string `json:"headSha"`
	BaseRef  string `json:"baseRef"`
	Strategy string `json:"strategy"`
	State    string `json:"state"` // unattempted, prepared, dispatched, confirmed, failed, uncertain
	Result   string `json:"result,omitempty"`
}

type FactoryMergeBatch struct {
	ID            string             `json:"id"`
	RequestID     string             `json:"requestId"`
	State         string             `json:"state"` // active, stopped, complete
	CompleteAfter bool               `json:"completeAfter"`
	RunChoice     string             `json:"runChoice,omitempty"`
	Steps         []FactoryMergeStep `json:"steps"`
}

type FactoryCompletion struct {
	Status     string                    `json:"status"` // open or complete; empty means open
	Links      []FactoryPRLink           `json:"links,omitempty"`
	Assessment *FactoryAssessment        `json:"assessment,omitempty"`
	Records    []FactoryCompletionRecord `json:"records,omitempty"`
	Batch      *FactoryMergeBatch        `json:"batch,omitempty"`
	StopState  string                    `json:"stopState,omitempty"` // requested, stopped, failed, unknown
}
