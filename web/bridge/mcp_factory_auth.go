package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Factory capabilities. Four are the grant's own booleans, and the fifth is
// answerQuestion exercised as the project's designated planner: it is the one
// capability that also consults the project's delegation settings, which is
// the only reason it is named apart from the boolean it requires.
const (
	factoryCapabilityRead            = "read"
	factoryCapabilityCreateDraft     = "createDraft"
	factoryCapabilityEditDraft       = "editDraft"
	factoryCapabilityAnswerQuestion  = "answerQuestion"
	factoryCapabilityDelegatedAnswer = "answerQuestion:delegated"
	factoryCapabilityApproveAndQueue = "approveAndQueue"
	// The plan-revision capabilities. assessPlanChanges admits a claim on one
	// proposal's assessment and the result the claiming client delivers;
	// approveRoutineRevision is one of the four facts that must hold, under
	// the policy lock, before a routine clarification is authorized.
	factoryCapabilityAssessPlanChanges      = "assessPlanChanges"
	factoryCapabilityApproveRoutineRevision = "approveRoutineRevision"
)

// factoryAuthorityRoutineRevision is the authority an assessment result is
// journalled under when the client delivering it may carry routine planner
// authorization. It is never supplied by a caller: the authorizer sets it on
// the server-authenticated principal while the policy lock is held, after
// rechecking the grant, the project's delegation toggle and the designated
// client.
const factoryAuthorityRoutineRevision = "delegated_routine"

// Approval authority values a stored grant may carry. The zero value is
// none: a grant written without an authority authorizes no approval.
const (
	factoryAuthorityNone         = "none"
	factoryAuthorityUserDirected = "user_directed"
	factoryAuthorityStanding     = "standing"
)

var (
	// errFactoryDenied is the answer to every "no": an unknown project ID, a
	// project the client was never granted, a capability that is off and a
	// revoked grant are one value with one message, so an error cannot be
	// used to learn which IDs exist.
	errFactoryDenied = errors.New("factory access denied")
	// errFactoryCapabilityUnsupported is a caller bug: a capability this
	// bridge does not define. It names no identity.
	errFactoryCapabilityUnsupported = errors.New("unsupported factory capability")
	// errFactoryGrantInvalid is a management write that does not describe a
	// valid grant.
	errFactoryGrantInvalid = errors.New("invalid factory grant")
	// errFactoryUnknownPair is a management read or write naming a client or
	// project this store does not hold. One value for both, and the message
	// names neither, so the route cannot be used to probe IDs.
	errFactoryUnknownPair = errors.New("unknown client or project")
)

// factoryMCPAuthorizer serializes factory policy: grant reads and writes,
// client deletion, project removal, delegation settings, and the authority
// check that guards a durable factory mutation.
//
// One mutex, because two rules need it. A revocation that returns before a
// mutation is admitted must prevent it, and a revocation that arrives after
// admission waits for the journal append instead of landing between admission
// and commit. The mutex is the policy lock; the lock order is policy ->
// workspace -> factory store, nothing here reenters policy, and no forge,
// network or model I/O runs while it is held.
type factoryMCPAuthorizer struct {
	mu sync.Mutex
	ws *Workspace
	// userDirection holds the receipts this bridge minted for built-in
	// operations that act on a person's instruction and that no attempt has
	// consumed yet, in mint order. now is the clock their expiry is read
	// against; a test injects a deterministic one. Both are guarded by mu,
	// exactly like the policy they are consulted with.
	userDirection []marshalUserDirectionReceipt
	now           func() time.Time
}

func newFactoryMCPAuthorizer(ws *Workspace) *factoryMCPAuthorizer {
	return &factoryMCPAuthorizer{ws: ws, now: time.Now}
}

// WithAuthorizedMutation authorizes one capability and, only when it holds,
// runs fn while still holding the policy lock. fn is the local validation and
// the journal append — the durable commit is the linearization point — so a
// grant change or a client deletion either lands entirely before this
// operation was admitted or entirely after it committed.
//
// The client and its grant are reloaded here rather than taken from the
// caller: what authorized the request is what the store holds now. An
// authority revoke therefore takes effect for the next operation without any
// cache to invalidate.
func (a *factoryMCPAuthorizer) WithAuthorizedMutation(clientID, projectID, repoID, capability string, fn func() (FactoryItem, error)) (FactoryItem, error) {
	if fn == nil {
		return FactoryItem{}, fmt.Errorf("%w: no mutation to authorize", errFactoryDenied)
	}
	return a.withAuthorizedMutation(clientID, projectID, repoID, capability, func(string) (FactoryItem, error) { return fn() })
}

// WithAuthorizedMutationAuthority is WithAuthorizedMutation for the one
// mutation that journals the authority it was admitted under. An approval
// records the grant's approvalAuthority as it stood under the policy lock, so
// the entry names the authority that admitted it rather than one the caller
// claimed.
func (a *factoryMCPAuthorizer) WithAuthorizedMutationAuthority(clientID, projectID, repoID, capability string, fn func(authority string) (FactoryItem, error)) (FactoryItem, error) {
	if fn == nil {
		return FactoryItem{}, fmt.Errorf("%w: no mutation to authorize", errFactoryDenied)
	}
	return a.withAuthorizedMutation(clientID, projectID, repoID, capability, fn)
}

// WithAuthorizedRevisionMutation is WithAuthorizedMutation for a plan-revision
// mutation that hands back plan text (a claim). The assessment capability alone
// would let a grant that cannot read the project's plans claim an assignment
// carrying the approved and proposed text, so both capabilities are checked
// under the policy lock, in one admission.
func (a *factoryMCPAuthorizer) WithAuthorizedRevisionMutation(clientID, projectID, repoID string, fn func() (FactoryItem, error)) (FactoryItem, error) {
	if fn == nil {
		return FactoryItem{}, fmt.Errorf("%w: no mutation to authorize", errFactoryDenied)
	}
	return a.withAuthorizedMutation(clientID, projectID, repoID, factoryCapabilityAssessPlanChanges,
		func(string) (FactoryItem, error) {
			if err := a.authorizeLocked(clientID, projectID, repoID, factoryCapabilityRead); err != nil {
				return FactoryItem{}, err
			}
			return fn()
		})
}

// withAuthorizedMutation is the shared body of both entry points: authorize
// under the policy lock, then run the caller's local commit while holding it.
func (a *factoryMCPAuthorizer) withAuthorizedMutation(clientID, projectID, repoID, capability string, fn func(authority string) (FactoryItem, error)) (FactoryItem, error) {
	if a == nil {
		return FactoryItem{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.authorizeLocked(clientID, projectID, repoID, capability); err != nil {
		return FactoryItem{}, err
	}
	return fn(a.authorityLocked(clientID, projectID))
}

// authorityLocked is the approval authority the stored grant carries. The
// caller holds the policy lock, so this is the value that admitted the
// operation.
func (a *factoryMCPAuthorizer) authorityLocked(clientID, projectID string) string {
	if a == nil || a.ws == nil {
		return ""
	}
	state, ok := a.ws.FactoryGrant(clientID, projectID)
	if !ok {
		return ""
	}
	return state.Grant.ApprovalAuthority
}

// AuthorizeRead checks the read capability for one client/project/repo.
func (a *factoryMCPAuthorizer) AuthorizeRead(clientID, projectID, repoID string) error {
	return a.AuthorizeCapability(clientID, projectID, repoID, factoryCapabilityRead)
}

// AuthorizeAssessPlanChanges checks the plan-revision assessment capability for
// one client/project/repo, the way the read-only revision views check it.
func (a *factoryMCPAuthorizer) AuthorizeAssessPlanChanges(clientID, projectID, repoID string) error {
	return a.AuthorizeCapability(clientID, projectID, repoID, factoryCapabilityAssessPlanChanges)
}

// AuthorizeCapability checks one capability under the policy lock, so a grant
// change that arrives first is respected and one that arrives second waits.
func (a *factoryMCPAuthorizer) AuthorizeCapability(clientID, projectID, repoID, capability string) error {
	if a == nil {
		return fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authorizeLocked(clientID, projectID, repoID, capability)
}

// authorizeLocked resolves the pair and checks one capability. The caller
// holds the policy lock, so what it reads is what the following journal append
// acts on.
func (a *factoryMCPAuthorizer) authorizeLocked(clientID, projectID, repoID, capability string) error {
	if err := a.validCapability(capability); err != nil {
		return err
	}
	if a == nil || a.ws == nil {
		return fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	// The project ID mapping is the only way in: an ID the store did not mint
	// names no project, and a removed project's ID no longer resolves, so a
	// re-registered project never revives an old grant.
	root, ok := a.ws.ProjectRootByID(projectID)
	if !ok {
		return errFactoryDenied
	}
	client, ok := a.ws.Client(clientID)
	if !ok {
		return errFactoryDenied
	}
	state, ok := a.ws.FactoryGrant(clientID, projectID)
	if !ok {
		return errFactoryDenied
	}
	if !a.grantAllows(state.Grant, capability) {
		return errFactoryDenied
	}
	// A grant scopes factory authority; it never widens the client's existing
	// fleet policy, and it names no repository that is not registered.
	if !repoAllowed(client, repoID) {
		return errFactoryDenied
	}
	if _, ok := a.ws.Repo(repoID); !ok {
		return errFactoryDenied
	}
	if capability == factoryCapabilityDelegatedAnswer {
		// Delegation is a project setting, read here and not from a cache: the
		// toggle and the designated client can change between two answers.
		ps := a.ws.ProjectSettingsFor(root)
		if !ps.PlannerDelegationEnabled || ps.DesignatedMCPClientID != clientID {
			return errFactoryDenied
		}
	}
	return nil
}

// validCapability rejects a capability this bridge does not define. It is kept
// apart from a denial on purpose: it describes the caller, not the client's
// authority, and it names no identity.
func (a *factoryMCPAuthorizer) validCapability(capability string) error {
	switch capability {
	case factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
		factoryCapabilityAnswerQuestion, factoryCapabilityDelegatedAnswer, factoryCapabilityApproveAndQueue,
		factoryCapabilityAssessPlanChanges, factoryCapabilityApproveRoutineRevision:
		return nil
	}
	return fmt.Errorf("%w: %q", errFactoryCapabilityUnsupported, capability)
}

// grantAllows reports whether one stored grant carries a capability. Every
// check is positive: a grant record that names a project grants nothing by
// existing.
func (a *factoryMCPAuthorizer) grantAllows(g MCPFactoryProjectGrant, capability string) bool {
	switch capability {
	case factoryCapabilityRead:
		return g.Read
	case factoryCapabilityCreateDraft:
		return g.CreateDraft
	case factoryCapabilityEditDraft:
		return g.EditDraft
	case factoryCapabilityAnswerQuestion:
		return g.AnswerQuestion
	case factoryCapabilityDelegatedAnswer:
		return g.AnswerQuestion
	case factoryCapabilityApproveAndQueue:
		// Approval additionally needs a configured authority: a grant that says
		// yes without one cannot approve, and an authority this bridge does not
		// know (a hand-edited file) fails closed rather than being trusted.
		return g.ApproveAndQueue && (g.ApprovalAuthority == factoryAuthorityUserDirected || g.ApprovalAuthority == factoryAuthorityStanding)
	case factoryCapabilityAssessPlanChanges:
		return g.AssessPlanChanges
	case factoryCapabilityApproveRoutineRevision:
		return g.ApproveRoutineRevision
	}
	return false
}

// WithAuthorizedRevisionAssessment authorizes one plan-revision assessment and
// reports, while still holding the policy lock, whether that client may also
// carry routine authorization for the result it delivers.
//
// Both answers come from the store as it stands under the lock. Grant writes
// take that same lock, so a revoked grant lands entirely before this operation
// was admitted or entirely after its journal append committed. Delegation
// settings are the weaker case: PutProjectSettingsIfMatch serializes on the
// workspace, not on this lock, so a toggle flipped while an admitted delivery
// is in flight does not cancel it. At most one proposal is authorized under the
// settings the admission read, the journal records who authorized it, and every
// later operation reads the new settings. Nothing is cached and nothing is
// taken from the caller.
func (a *factoryMCPAuthorizer) WithAuthorizedRevisionAssessment(clientID, projectID, repoID string, fn func(routine bool) (FactoryItem, error)) (FactoryItem, error) {
	if fn == nil {
		return FactoryItem{}, fmt.Errorf("%w: no mutation to authorize", errFactoryDenied)
	}
	return a.withAuthorizedMutation(clientID, projectID, repoID, factoryCapabilityAssessPlanChanges,
		func(string) (FactoryItem, error) {
			// Delivering an assessment names the plan text this client was
			// handed when it claimed the attempt, so it needs read as well.
			if err := a.authorizeLocked(clientID, projectID, repoID, factoryCapabilityRead); err != nil {
				return FactoryItem{}, err
			}
			return fn(a.routineRevisionLocked(clientID, projectID))
		})
}

// routineRevisionLocked reports whether routine planner authorization is
// delegated to this client right now: its grant carries approveRoutineRevision,
// the project turns planner delegation on, and the project names this very
// client as the designated one. The caller holds the policy lock.
func (a *factoryMCPAuthorizer) routineRevisionLocked(clientID, projectID string) bool {
	if a == nil || a.ws == nil || strings.TrimSpace(clientID) == "" {
		return false
	}
	state, ok := a.ws.FactoryGrant(clientID, projectID)
	if !ok || !a.grantAllows(state.Grant, factoryCapabilityApproveRoutineRevision) {
		return false
	}
	root, ok := a.ws.ProjectRootByID(projectID)
	if !ok {
		return false
	}
	ps := a.ws.ProjectSettingsFor(root)
	return ps.PlannerDelegationEnabled && ps.DesignatedMCPClientID == clientID
}

// Built-in sessions.
//
// A built-in session is not an MCP client: it holds no client record and no
// MCPFactoryProjectGrant. Its authority is a project's defaults plus, at most,
// one override for the session (or for one worker of it). The binding that
// names it is established by the control plane's trusted session association —
// never by a tool argument or a request body — so no caller can mint one.
//
// The lock order and the linearization point are unchanged: policy ->
// workspace -> factory store. An access write takes this same policy lock, so
// a revocation lands entirely before a mutation was admitted or entirely after
// that mutation's journal append committed, and every admission re-reads the
// store instead of trusting a cache.
//
// Nothing here widens an external client's authority, and no built-in default
// ever reaches one: AuthorizeCapability and WithAuthorizedMutation still read
// the stored grant and nothing else.

// AuthorizeMarshalRead checks the read capability for one verified built-in
// binding, the way the read-only factory views check it.
func (a *factoryMCPAuthorizer) AuthorizeMarshalRead(binding MarshalSessionBinding, repoID string) error {
	return a.AuthorizeMarshalCapability(binding, repoID, factoryCapabilityRead)
}

// AuthorizeMarshalCapability checks one capability for a verified built-in
// binding under the policy lock, so an access change that arrives first is
// respected and one that arrives second waits.
func (a *factoryMCPAuthorizer) AuthorizeMarshalCapability(binding MarshalSessionBinding, repoID, capability string) error {
	if a == nil {
		return fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authorizeMarshalLocked(binding, capability)
}

// WithAuthorizedMarshalMutation authorizes one capability for a verified
// built-in binding and, only when it holds, runs fn while still holding the
// policy lock. fn is the local validation and the journal append — the durable
// commit is the linearization point — so an access change either lands
// entirely before this operation was admitted or entirely after it committed.
//
// fn receives the principal to journal under: the scope its request identity is
// namespaced by and the attribution the entry carries.
//
// repoID is the repository the mutation acts on. It is accepted for symmetry
// with the client path and is deliberately not a gate: a built-in principal is
// scoped by the project its binding names, which is checked here, and its
// ceiling is the effective access — the project defaults, then the session or
// worker override. A worker's role and item name what it is working on; neither
// grants anything.
func (a *factoryMCPAuthorizer) WithAuthorizedMarshalMutation(binding MarshalSessionBinding, repoID, capability string, fn func(factoryPrincipal) (FactoryItem, error)) (FactoryItem, error) {
	if fn == nil {
		return FactoryItem{}, fmt.Errorf("%w: no mutation to authorize", errFactoryDenied)
	}
	if a == nil {
		return FactoryItem{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	principal, err := a.admitMarshalLocked(binding, capability)
	if err != nil {
		return FactoryItem{}, err
	}
	return fn(principal)
}

// marshalStatus reports one verified binding's effective access under the
// policy lock, with every pointer resolved: the answer a status surface shows.
func (a *factoryMCPAuthorizer) marshalStatus(binding MarshalSessionBinding) (MarshalConnectionStatus, error) {
	if a == nil {
		return MarshalConnectionStatus{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.marshalStatusLocked(binding)
}

// authorizeMarshalLocked is the admission for one built-in capability. The
// caller holds the policy lock, so the access it reads is the access the
// following journal append runs under.
func (a *factoryMCPAuthorizer) authorizeMarshalLocked(binding MarshalSessionBinding, capability string) error {
	_, err := a.admitMarshalLocked(binding, capability)
	return err
}

// admitMarshalLocked resolves one binding's effective access, checks the
// capability against it, and renders the principal the operation is journalled
// under. Resolution and check are one step on purpose: a second read would be a
// second answer. The caller holds the policy lock.
func (a *factoryMCPAuthorizer) admitMarshalLocked(binding MarshalSessionBinding, capability string) (factoryPrincipal, error) {
	// A capability this bridge does not define is a caller bug, not an
	// authority question, so it is reported apart from a denial exactly as it
	// is for an MCP client.
	if err := a.validCapability(capability); err != nil {
		return factoryPrincipal{}, err
	}
	status, err := a.marshalStatusLocked(binding)
	if err != nil {
		return factoryPrincipal{}, err
	}
	if !marshalAccessAllows(status.Access, capability) {
		return factoryPrincipal{}, errFactoryDenied
	}
	return binding.principal(capability, status.Access), nil
}

// marshalStatusLocked resolves the effective access one verified binding holds
// and reports it. The caller holds the policy lock.
//
// The default is read-only, and each layer replaces only the fields it carries:
// an override reset to unset inherits the layer beneath it again, while an
// explicit false keeps denying.
func (a *factoryMCPAuthorizer) marshalStatusLocked(binding MarshalSessionBinding) (MarshalConnectionStatus, error) {
	if a == nil || a.ws == nil {
		return MarshalConnectionStatus{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	if err := a.validMarshalBindingLocked(binding); err != nil {
		return MarshalConnectionStatus{}, err
	}
	project, stored := a.ws.marshalAccessDefault(binding.ProjectID)
	effective := resolveMarshalFactoryAccess(project.Access)
	source := map[string]string{"project": "default"}
	if stored {
		source["project"] = "override"
	}
	if override, ok := a.ws.marshalAccessOverride(binding.ProjectID, binding.SessionID); ok && marshalOverrideApplies(override, binding) {
		effective = resolveMarshalFactoryAccess(project.Access, override.Access)
		source["session"] = "override"
	}
	return MarshalConnectionStatus{
		State:      marshalStateConnected,
		InstanceID: binding.InstanceID,
		ProjectID:  binding.ProjectID,
		SessionID:  binding.SessionID,
		VerifiedAt: time.Now().UTC().Format(time.RFC3339),
		Access:     effective,
		Source:     source,
	}, nil
}

// validMarshalBindingLocked refuses a binding the server has not established.
// Every identity the association is made of must be present: a partly verified
// binding is a denial, not a default, and it says nothing about which field was
// missing.
func (a *factoryMCPAuthorizer) validMarshalBindingLocked(binding MarshalSessionBinding) error {
	if a == nil || a.ws == nil {
		return fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	for _, field := range []string{binding.InstanceID, binding.ProjectID, binding.SessionID, binding.Generation} {
		if strings.TrimSpace(field) == "" {
			return errFactoryDenied
		}
	}
	// The project ID mapping is the only way in, exactly as it is for a client:
	// an ID this store did not mint names no project.
	if _, ok := a.ws.ProjectRootByID(binding.ProjectID); !ok {
		return errFactoryDenied
	}
	return nil
}

// marshalOverrideApplies reports whether a stored session override applies to
// this binding. A record that names no worker is the session's own; one that
// names a worker is that worker's, and a sibling worker inherits the project
// default instead.
func marshalOverrideApplies(override marshalFactoryAccessState, binding MarshalSessionBinding) bool {
	return override.WorkerID == "" || override.WorkerID == binding.WorkerID
}

// marshalAccessAllows reports whether one effective access value carries a
// capability. Every check is positive, and the planner capabilities a
// designated MCP client carries are denied rather than derived: a built-in
// session has no designation, so nothing here can be claimed by a role.
func marshalAccessAllows(access MarshalFactoryAccess, capability string) bool {
	switch capability {
	case factoryCapabilityRead:
		return access.Read != nil && *access.Read
	case factoryCapabilityCreateDraft:
		return access.CreateDraft != nil && *access.CreateDraft
	case factoryCapabilityEditDraft:
		return access.EditDraft != nil && *access.EditDraft
	case factoryCapabilityAnswerQuestion:
		return access.AnswerQuestion != nil && *access.AnswerQuestion
	case factoryCapabilityApproveAndQueue:
		// Approval additionally needs a configured authority, exactly as a
		// grant does: an authority this bridge does not know (a hand-edited
		// file) fails closed rather than being trusted.
		return access.ApproveAndQueue != nil && *access.ApproveAndQueue &&
			access.ApprovalAuthority != nil &&
			(*access.ApprovalAuthority == factoryAuthorityUserDirected || *access.ApprovalAuthority == factoryAuthorityStanding)
	}
	return false
}

// User direction receipts.
//
// An approval or an answer that acts on a person's instruction needs evidence
// that a person gave it, and the evidence has to be the bridge's, not the
// caller's: a model's "authority: user_directed" argument proves nothing, and a
// built-in session carries a credential its model can reach. So the bridge mints
// an opaque, expiring, single-use receipt for one exact operation, answers the
// attempt that has none with that receipt, and treats a later attempt that
// presents it as the person's direction. The receipt travels in a header, beside
// the tool arguments, so no argument and no model content can mint one.
const (
	// MarshalUserDirectionHeader carries a user-direction receipt beside the
	// MCP request. No tool decodes a receipt from its JSON arguments, so a
	// model cannot supply one by writing one; the trusted transport sets this
	// header from the session's own confirmed receipt.
	MarshalUserDirectionHeader = "X-Marshal-User-Direction"
	// marshalUserDirectionTTL bounds how long a minted receipt stays
	// presentable. It is short on purpose: a receipt is a confirmation of one
	// operation happening now.
	marshalUserDirectionTTL = 5 * time.Minute
)

// errMarshalUserDirectionRequired is what a built-in operation that acts on a
// person's instruction reports when it has none to show: the bridge minted a
// receipt and the caller must present it after a trusted confirmation.
var errMarshalUserDirectionRequired = errors.New("user direction required")

// marshalUserDirectionChallenge is that answer: the opaque receipt id and when
// it lapses. It is a value the caller is told and never one it can choose.
type marshalUserDirectionChallenge struct {
	Receipt   string
	ExpiresAt time.Time
}

func (c *marshalUserDirectionChallenge) Error() string {
	return errMarshalUserDirectionRequired.Error() + ": the user must confirm this operation and present the receipt"
}

// Is makes errors.Is(err, errMarshalUserDirectionRequired) hold for the value
// the authorizer returns, so a caller classifies the failure by its sentinel
// while still reading the receipt from it.
func (c *marshalUserDirectionChallenge) Is(target error) bool {
	return target == errMarshalUserDirectionRequired
}

// marshalUserDirectionReceipt is one minted receipt: the exact operation it
// covers and when it lapses. Every field is bound when it is minted, from the
// authenticated binding and the validated arguments, so presenting it for any
// other instance, project, session, worker, action, target or request matches
// nothing.
type marshalUserDirectionReceipt struct {
	id         string
	expiresAt  time.Time
	instanceID string
	projectID  string
	sessionID  string
	workerID   string
	action     string
	target     string
	requestID  string
}

// covers reports whether this receipt was minted for exactly this operation.
func (r marshalUserDirectionReceipt) covers(binding MarshalSessionBinding, action, target, requestID string) bool {
	return r.instanceID == binding.InstanceID &&
		r.projectID == binding.ProjectID &&
		r.sessionID == binding.SessionID &&
		r.workerID == binding.WorkerID &&
		r.action == action &&
		r.target == target &&
		r.requestID == requestID
}

// marshalUserDirectionTarget is the exact operation a receipt covers, built
// from the arguments the tool already validated: the item, the version the
// request was made against, and — for an answer — the decision it resolves. A
// receipt minted for one operation is not evidence for another.
func marshalUserDirectionTarget(itemID string, expectedVersion uint64, decisionID string) string {
	return strings.Join([]string{itemID, strconv.FormatUint(expectedVersion, 10), decisionID}, "\x00")
}

// WithAuthorizedMarshalUserDirection authorizes one built-in operation that
// acts on a person's instruction and, only when it holds, runs fn while still
// holding the policy lock.
//
// What admits the operation is the store's effective approval authority, read
// under the lock — never the caller's claim. Standing authority is the
// project's own configured policy and owes no confirmation. User-directed
// authority needs a person: a receipt this bridge minted for exactly this
// (instance, project, session, worker, action, target, request) that no other
// attempt consumed. Without one nothing is admitted, and the answer is a
// challenge carrying the receipt to present after the trusted confirmation; a
// receipt already minted for the same exact operation is handed back rather than
// asking the person a second time.
//
// A presented receipt is consumed by the attempt that presents it, so it can
// never be replayed: a consumed, expired, mismatched or generation-stale receipt
// is a denial like every other, and nothing is journalled for it.
func (a *factoryMCPAuthorizer) WithAuthorizedMarshalUserDirection(binding MarshalSessionBinding, repoID, capability, receiptID, target, requestID string, fn func(factoryPrincipal) (FactoryItem, error)) (FactoryItem, error) {
	if fn == nil {
		return FactoryItem{}, fmt.Errorf("%w: no mutation to authorize", errFactoryDenied)
	}
	if a == nil {
		return FactoryItem{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	principal, err := a.admitMarshalLocked(binding, capability)
	if err != nil {
		return FactoryItem{}, err
	}
	switch principal.Authority {
	case factoryAuthorityStanding:
		// The store's own policy, configured separately: the standing
		// authority a person set up in advance owes no confirmation per
		// operation, and it is journalled as standing.
		return fn(principal)
	case factoryAuthorityUserDirected:
	default:
		// Nothing configured admits an operation that acts on a person's
		// instruction, so no receipt exists that could.
		return FactoryItem{}, errFactoryDenied
	}
	if strings.TrimSpace(receiptID) != "" {
		if !a.consumeUserDirectionLocked(receiptID, binding, capability, target, requestID) {
			return FactoryItem{}, errFactoryDenied
		}
		principal.AuthorityOrigin = factoryAuthorityOriginVerifiedUserDirected
		return fn(principal)
	}
	return FactoryItem{}, a.challengeUserDirectionsLocked(binding, capability, target, requestID)
}

// consumeUserDirectionLocked verifies one presented receipt against exactly
// this operation and, when it covers it, consumes it. An expired receipt is
// dropped rather than kept, so a stale one cannot linger; a receipt minted for
// anything else is left in place and matches nothing. The caller holds the
// policy lock.
func (a *factoryMCPAuthorizer) consumeUserDirectionLocked(id string, binding MarshalSessionBinding, action, target, requestID string) bool {
	for n := range a.userDirection {
		receipt := a.userDirection[n]
		if receipt.id != id {
			continue
		}
		if !a.timestamp().Before(receipt.expiresAt) {
			a.userDirection = append(a.userDirection[:n], a.userDirection[n+1:]...)
			return false
		}
		if !receipt.covers(binding, action, target, requestID) {
			return false
		}
		a.userDirection = append(a.userDirection[:n], a.userDirection[n+1:]...)
		return true
	}
	return false
}

// challengeUserDirectionsLocked returns the receipt for exactly this operation,
// minting one only when no unspent receipt already covers it: two attempts at
// one operation must not ask a person twice, and only one confirmation is owed.
// The caller holds the policy lock.
func (a *factoryMCPAuthorizer) challengeUserDirectionsLocked(binding MarshalSessionBinding, action, target, requestID string) error {
	now := a.timestamp()
	kept := a.userDirection[:0]
	for _, receipt := range a.userDirection {
		if !now.Before(receipt.expiresAt) {
			// An expired receipt is not evidence and is not kept.
			continue
		}
		kept = append(kept, receipt)
	}
	a.userDirection = kept
	for _, receipt := range a.userDirection {
		if receipt.covers(binding, action, target, requestID) {
			return &marshalUserDirectionChallenge{Receipt: receipt.id, ExpiresAt: receipt.expiresAt}
		}
	}
	id, err := newMarshalUserDirectionID()
	if err != nil {
		// Nothing can be presented that this bridge would recognize, so the
		// operation is refused rather than admitted unverified.
		return fmt.Errorf("%w: no receipt could be minted", errFactoryDenied)
	}
	receipt := marshalUserDirectionReceipt{
		id:         id,
		expiresAt:  now.Add(marshalUserDirectionTTL),
		instanceID: binding.InstanceID,
		projectID:  binding.ProjectID,
		sessionID:  binding.SessionID,
		workerID:   binding.WorkerID,
		action:     action,
		target:     target,
		requestID:  requestID,
	}
	a.userDirection = append(a.userDirection, receipt)
	return &marshalUserDirectionChallenge{Receipt: receipt.id, ExpiresAt: receipt.expiresAt}
}

// pendingUserDirections reports how many unspent receipts this bridge holds. It
// is the observable a test asserts "one confirmation, one receipt" with.
func (a *factoryMCPAuthorizer) pendingUserDirections() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.userDirection)
}

// timestamp is the clock the receipt ledger reads. A zero authorizer built in a
// test falls back to the wall clock rather than expiring everything at once.
func (a *factoryMCPAuthorizer) timestamp() time.Time {
	if a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

// newMarshalUserDirectionID mints one opaque receipt id. It is random and
// carries no identity: reading one tells a caller nothing about the operation it
// covers.
func newMarshalUserDirectionID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// marshalUserDirectionReceiptID is the receipt presented with this request. It
// is read from the header the trusted transport sets and from nowhere else: no
// tool argument carries a receipt, so no tool argument can supply one.
func marshalUserDirectionReceiptID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Header.Get(MarshalUserDirectionHeader))
}

// putMarshalAccessDefault stores one project's built-in defaults under the
// policy lock, so an access change and an authorized mutation cannot
// interleave. An unknown project is refused without saying which ID was wrong.
func (a *factoryMCPAuthorizer) putMarshalAccessDefault(projectID string, access MarshalFactoryAccess, expected string) (bool, marshalFactoryAccessState, error) {
	if a == nil || a.ws == nil {
		return false, marshalFactoryAccessState{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	var (
		wrote bool
		state marshalFactoryAccessState
	)
	err := a.withPolicy(func() error {
		if _, ok := a.ws.ProjectRootByID(projectID); !ok {
			return errFactoryUnknownPair
		}
		next, current, err := a.ws.putMarshalAccessDefaultIfMatch(projectID, access, expected)
		wrote, state = next, current
		return err
	})
	if err != nil {
		return false, state, err
	}
	return wrote, state, nil
}

// putMarshalAccessOverride stores one session's override under the same policy
// lock, so a revocation of it cannot land inside an admitted mutation.
func (a *factoryMCPAuthorizer) putMarshalAccessOverride(projectID, sessionID, workerID string, access MarshalFactoryAccess, expected string) (bool, marshalFactoryAccessState, error) {
	if a == nil || a.ws == nil {
		return false, marshalFactoryAccessState{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	var (
		wrote bool
		state marshalFactoryAccessState
	)
	err := a.withPolicy(func() error {
		if _, ok := a.ws.ProjectRootByID(projectID); !ok {
			return errFactoryUnknownPair
		}
		next, current, err := a.ws.putMarshalAccessOverrideIfMatch(projectID, sessionID, workerID, access, expected)
		wrote, state = next, current
		return err
	})
	if err != nil {
		return false, state, err
	}
	return wrote, state, nil
}

// withPolicy runs one durable policy mutation under the authorizer's mutex, on
// the same lock every grant write and authorized factory mutation takes.
func (a *factoryMCPAuthorizer) withPolicy(fn func() error) error {
	if a == nil {
		// A missing authorizer is a wiring fault, not permission to run a
		// policy mutation unlocked: every other method here fails closed.
		return fmt.Errorf("%w: no authorizer", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return fn()
}

// validPairLocked reports whether clientID and projectID name a client and a
// registered project this store holds. The caller holds the policy lock.
func (a *factoryMCPAuthorizer) validPairLocked(clientID, projectID string) error {
	if a == nil || a.ws == nil {
		return fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	if _, ok := a.ws.Client(clientID); !ok {
		return errFactoryUnknownPair
	}
	if _, ok := a.ws.ProjectRootByID(projectID); !ok {
		return errFactoryUnknownPair
	}
	return nil
}

// grantState reads one pair's stored grant under the policy lock. A valid pair
// with nothing stored reports the zero grant and the stable version of it, so
// the editor's first write can be conditional on what it was shown.
func (a *factoryMCPAuthorizer) grantState(clientID, projectID string) (FactoryGrantState, error) {
	if a == nil {
		return FactoryGrantState{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.validPairLocked(clientID, projectID); err != nil {
		return FactoryGrantState{}, err
	}
	if state, ok := a.ws.FactoryGrant(clientID, projectID); ok {
		return state, nil
	}
	return factoryGrantState(clientID, projectID, MCPFactoryProjectGrant{ProjectID: projectID}), nil
}

// putGrant stores one grant only when the caller's version is still current.
// It reports whether it wrote and the pair's state either way.
func (a *factoryMCPAuthorizer) putGrant(clientID, projectID string, grant MCPFactoryProjectGrant, expected string) (bool, FactoryGrantState, error) {
	if a == nil {
		return false, FactoryGrantState{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.validPairLocked(clientID, projectID); err != nil {
		return false, FactoryGrantState{}, err
	}
	// The identities and the grant are rechecked under the lock: the no-lock
	// pass in the handler only saves the caller a lock for an obvious mistake.
	if grant.ProjectID != projectID {
		return false, FactoryGrantState{}, fmt.Errorf("%w: projectId must match the path", errFactoryGrantInvalid)
	}
	if err := validateFactoryGrant(grant); err != nil {
		return false, FactoryGrantState{}, err
	}
	return a.ws.PutFactoryGrantIfMatch(clientID, grant, expected)
}

// validateFactoryGrant checks a management write. An unknown authority is
// refused rather than stored and later ignored: a grant the bridge cannot
// interpret must not sit in the file looking like authority.
func validateFactoryGrant(g MCPFactoryProjectGrant) error {
	switch g.ApprovalAuthority {
	case factoryAuthorityNone, factoryAuthorityUserDirected, factoryAuthorityStanding:
	case "":
		return fmt.Errorf("%w: approvalAuthority is required (none, user_directed or standing)", errFactoryGrantInvalid)
	default:
		return fmt.Errorf("%w: approvalAuthority must be none, user_directed or standing", errFactoryGrantInvalid)
	}
	if g.ApproveAndQueue && g.ApprovalAuthority == factoryAuthorityNone {
		return fmt.Errorf("%w: approveAndQueue requires user_directed or standing authority", errFactoryGrantInvalid)
	}
	return nil
}

// getFactoryGrant answers GET /api/clients/{clientId}/factory-grants/{projectId}.
func (s *Server) getFactoryGrant(w http.ResponseWriter, r *http.Request) {
	state, err := s.factoryAuth.grantState(r.PathValue("clientId"), r.PathValue("projectId"))
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// putFactoryGrant answers PUT on the same path. The body carries the whole
// grant and the version it was edited from, so two editors naming one version
// cannot silently overwrite each other.
func (s *Server) putFactoryGrant(w http.ResponseWriter, r *http.Request) {
	clientID, projectID := r.PathValue("clientId"), r.PathValue("projectId")
	var body struct {
		Grant   MCPFactoryProjectGrant `json:"grant"`
		Version string                 `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	grant := body.Grant
	if grant.ProjectID != projectID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "grant projectId must match the path"})
		return
	}
	if grant.ApprovalAuthority == "" {
		grant.ApprovalAuthority = factoryAuthorityNone
	}
	if err := validateFactoryGrant(grant); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(body.Version) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version is required"})
		return
	}
	wrote, state, err := s.factoryAuth.putGrant(clientID, projectID, grant, body.Version)
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	if !wrote {
		// The grant moved since the caller read it. The response names the
		// value that won, so the editor can show the conflict and keep its
		// unsaved edit instead of re-reading blind.
		writeJSON(w, http.StatusConflict, state)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// factoryAuthError maps the management API's failures onto status codes. An
// unknown client and an unknown project are one answer, and it names neither:
// the route is authenticated, but its errors still must not become an oracle
// for which IDs exist. A persistence failure is reported without the
// workspace path.
func factoryAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errFactoryUnknownPair):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errFactoryUnknownPair.Error()})
	case errors.Is(err, errFactoryGrantInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrFactoryUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "factory grants unavailable"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "factory grant could not be saved"})
	}
}
