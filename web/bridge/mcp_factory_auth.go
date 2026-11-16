package bridge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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
}

func newFactoryMCPAuthorizer(ws *Workspace) *factoryMCPAuthorizer {
	return &factoryMCPAuthorizer{ws: ws}
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
