package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// The factory tools. Their names are prefixed because the fleet tools already
// own "list" and "status", and a tool name is a contract a client compiled
// against.
const (
	factoryToolProjects        = "factory_projects"
	factoryToolList            = "factory_list"
	factoryToolGet             = "factory_get"
	factoryToolCreateDraft     = "factory_create_draft"
	factoryToolUpdateDraft     = "factory_update_draft"
	factoryToolApproveAndQueue = "factory_approve_and_queue"
	factoryToolAnswerQuestion  = "factory_answer_question"
)

// factoryAuthorityDelegated is the one answer authority that is not a stored
// grant's approval authority: it names the delegation capability instead, and
// it is the only client-supplied value that selects a different check.
const factoryAuthorityDelegated = "delegated"

// Domain failure codes. The envelope carries no other detail than a code and a
// fixed message, so no failure names a project, an item, a path or a version.
const (
	factoryCodeInvalidInput = "invalid_input"
	factoryCodeNotFound     = "not_found"
	factoryCodeConflict     = "conflict"
	factoryCodeUnsupported  = "unsupported"
	factoryCodeUnavailable  = "unavailable"
	factoryCodeDenied       = "denied"
)

// Bounds on the projections. The journal holds up to factoryRecordLimit per
// item; a tool result is a view of it, not a copy, so the list and the history
// it returns are capped.
const (
	factoryToolListLimit    = 100
	factoryToolHistoryLimit = 50
	factoryToolTextLimit    = 4000
)

var (
	// errFactoryToolInput is a request whose shape this bridge refuses: an
	// unknown field, trailing data, a missing required argument, a
	// client-supplied project root or an authority value this tool does not
	// accept.
	errFactoryToolInput = errors.New("invalid factory tool arguments")
	// errFactoryToolAuthority is an approval claiming an authority the grant
	// does not carry. It is a denial: the stored authority is never echoed.
	errFactoryToolAuthority = errors.New("factory approval authority does not match the grant")
)

// factoryToolFailure is the domain failure an MCP factory tool reports inside
// its result. It is a value, not an error string, so a caller can branch on it.
type factoryToolFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// factoryFailureOf classifies one service error into the domain failure a
// client sees. Unauthorised access and a nonexistent indirect id are the same
// answer on purpose: no failure may be used to learn which ids exist.
func factoryFailureOf(err error) factoryToolFailure {
	switch {
	case errors.Is(err, errFactoryDenied), errors.Is(err, ErrFactoryNotFound):
		return factoryToolFailure{Code: factoryCodeNotFound, Message: "not found"}
	case errors.Is(err, errFactoryToolAuthority):
		return factoryToolFailure{Code: factoryCodeDenied, Message: "authority does not match the configured grant"}
	case errors.Is(err, ErrFactoryConflict):
		return factoryToolFailure{Code: factoryCodeConflict, Message: "conflict with the item's current state or version"}
	case errors.Is(err, ErrFactoryUnavailable), errors.Is(err, ErrFactoryLocked), errors.Is(err, ErrFactoryCorrupt):
		return factoryToolFailure{Code: factoryCodeUnavailable, Message: "factory unavailable"}
	case errors.Is(err, errFactoryConfig), errors.Is(err, ErrFactoryForgeUnsupported):
		return factoryToolFailure{Code: factoryCodeUnsupported, Message: "configuration unsupported"}
	default:
		// Everything else is a rejected request — malformed arguments, a plan
		// and specification over the import limit, an identity the draft may
		// not move — and its own text may name fields or state, so it is not
		// passed through.
		return factoryToolFailure{Code: factoryCodeInvalidInput, Message: "invalid request"}
	}
}

// factoryToolCall serves one factory tool, reporting whether the name was one.
func (s *Server) factoryToolCall(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, name string, args json.RawMessage) bool {
	switch name {
	case factoryToolProjects, factoryToolList, factoryToolGet, factoryToolCreateDraft,
		factoryToolUpdateDraft, factoryToolApproveAndQueue, factoryToolAnswerQuestion:
	default:
		return false
	}
	if s.factory == nil {
		// A bridge without a factory answers explicitly: an unavailable domain
		// result, never an unauthenticated or unaccounted path around it.
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeUnavailable, Message: "factory is not enabled on this bridge"})
		return true
	}
	switch name {
	case factoryToolProjects:
		s.factoryToolProjects(w, c, id)
	case factoryToolList:
		s.factoryToolList(w, c, id, args)
	case factoryToolGet:
		s.factoryToolGet(w, c, id, args)
	case factoryToolCreateDraft:
		s.factoryToolCreateDraft(w, c, id, args)
	case factoryToolUpdateDraft:
		s.factoryToolUpdateDraft(w, c, id, args)
	case factoryToolApproveAndQueue:
		s.factoryToolApproveAndQueue(w, r, c, id, args)
	case factoryToolAnswerQuestion:
		s.factoryToolAnswerQuestion(w, r, c, id, args)
	}
	return true
}

// factoryToolProjects lists this client's project ids and the capabilities it
// holds on each. Nothing here is fleet policy: a grant is the only authority.
func (s *Server) factoryToolProjects(w http.ResponseWriter, c MCPClient, id json.RawMessage) {
	client, ok := s.fleet.ws.Client(c.ID)
	if !ok {
		writeFactoryToolFailure(w, id, factoryFailureOf(errFactoryDenied))
		return
	}
	projects := []map[string]any{}
	for _, grant := range client.FactoryGrants {
		root, ok := s.fleet.ws.ProjectRootByID(grant.ProjectID)
		if !ok {
			// A removed project's id no longer resolves, so its grant names
			// nothing and is not listed.
			continue
		}
		capabilities := []string{}
		for _, capability := range []string{
			factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
			factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue,
		} {
			if s.factoryAuth.grantAllows(grant, capability) {
				capabilities = append(capabilities, capability)
			}
		}
		if grant.AnswerQuestion {
			if ps := s.fleet.ws.ProjectSettingsFor(root); ps.PlannerDelegationEnabled && ps.DesignatedMCPClientID == c.ID {
				capabilities = append(capabilities, factoryCapabilityDelegatedAnswer)
			}
		}
		entry := map[string]any{"projectId": grant.ProjectID, "capabilities": capabilities}
		if s.factoryAuth.grantAllows(grant, factoryCapabilityApproveAndQueue) {
			// The authority an approve call must claim, so the assertion can
			// match the configuration instead of guessing.
			entry["approvalAuthority"] = grant.ApprovalAuthority
		}
		projects = append(projects, entry)
	}
	writeFactoryToolResult(w, id, map[string]any{"projects": projects})
}

// factoryToolList returns this project's item summaries.
func (s *Server) factoryToolList(w http.ResponseWriter, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a factoryToolProjectArgs
	if err := decodeFactoryToolArgs(args, &a); err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	root, err := s.factoryToolRoot(c.ID, a.ProjectID)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	items := []factoryToolItemSummary{}
	for _, item := range s.factory.List() {
		if item.ProjectRoot != root {
			continue
		}
		// The grant scopes the project; the client's repository allowlist
		// scopes the items inside it, exactly as the authorizer checks it.
		if err := s.factoryAuth.AuthorizeRead(c.ID, a.ProjectID, item.RepoID); err != nil {
			continue
		}
		items = append(items, s.factoryToolSummary(item))
		if len(items) == factoryToolListLimit {
			break
		}
	}
	writeFactoryToolResult(w, id, map[string]any{"projectId": a.ProjectID, "items": items})
}

// factoryToolGet returns one stored item: its plan, specification, effective
// configuration, version, state, decision, latest outcome and history.
func (s *Server) factoryToolGet(w http.ResponseWriter, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a factoryToolItemArgs
	if err := decodeFactoryToolArgs(args, &a); err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	root, err := s.factoryToolRoot(c.ID, a.ProjectID)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	item, err := s.factoryToolRead(c.ID, a.ProjectID, root, a.ItemID)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, s.factoryToolDetail(a.ProjectID, item))
}

// factoryToolCreateDraft proposes a draft. The order is the approved one:
// decode strictly, prepare outside the policy lock (resolution, normalisation
// and preflight may do external work), then authorize and append under it.
func (s *Server) factoryToolCreateDraft(w http.ResponseWriter, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a factoryToolCreateArgs
	if err := decodeFactoryToolArgs(args, &a); err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	if strings.TrimSpace(a.RequestID) == "" {
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "requestId is required"})
		return
	}
	req, err := s.factoryToolProposal(a.ProjectID, a.RequestID, a.Title, a.Plan, a.Spec, a.Config)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	resolved, err := s.factory.PrepareDraft(req)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	item, err := s.factoryAuth.WithAuthorizedMutation(c.ID, a.ProjectID, req.Config.RepoID, factoryCapabilityCreateDraft, func() (FactoryItem, error) {
		return s.factory.CreateDraftAuthorized(req, resolved,
			factoryToolPrincipal(c.ID, a.ProjectID, factoryCapabilityCreateDraft, a.RequestID, ""))
	})
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, factoryToolStored(a.ProjectID, s.factoryToolLink(item.ID), item))
}

// factoryToolUpdateDraft replaces one unapproved draft's content.
func (s *Server) factoryToolUpdateDraft(w http.ResponseWriter, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a factoryToolUpdateArgs
	if err := decodeFactoryToolArgs(args, &a); err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	switch {
	case strings.TrimSpace(a.ItemID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "itemId is required"})
		return
	case strings.TrimSpace(a.RequestID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "requestId is required"})
		return
	case a.ExpectedVersion == 0:
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "expectedVersion is required"})
		return
	}
	req, err := s.factoryToolProposal(a.ProjectID, a.RequestID, a.Title, a.Plan, a.Spec, a.Config)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	resolved, err := s.factory.PrepareDraft(req)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	item, err := s.factoryAuth.WithAuthorizedMutation(c.ID, a.ProjectID, req.Config.RepoID, factoryCapabilityEditDraft, func() (FactoryItem, error) {
		// Only this project's items are reachable: the store refuses a moved
		// identity, and this refuses to look across projects at all.
		if err := s.factoryToolOwned(a.ItemID, req.Config.ProjectRoot); err != nil {
			return FactoryItem{}, err
		}
		return s.factory.ReplaceDraftAuthorized(a.ItemID, resolved, a.ExpectedVersion,
			factoryToolPrincipal(c.ID, a.ProjectID, factoryCapabilityEditDraft, a.RequestID, ""))
	})
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, factoryToolStored(a.ProjectID, s.factoryToolLink(item.ID), item))
}

// factoryToolApproveAndQueue records provenance and queues the exact stored
// draft through the existing approve command.
func (s *Server) factoryToolApproveAndQueue(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a factoryToolApproveArgs
	if err := decodeFactoryToolArgs(args, &a); err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	switch {
	case strings.TrimSpace(a.ItemID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "itemId is required"})
		return
	case strings.TrimSpace(a.RequestID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "requestId is required"})
		return
	case a.ExpectedVersion == 0:
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "expectedVersion is required"})
		return
	}
	authority := strings.TrimSpace(a.Authority)
	if authority != factoryAuthorityUserDirected && authority != factoryAuthorityStanding {
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "authority must be user_directed or standing"})
		return
	}
	root, err := s.factoryToolMutationRoot(a.ProjectID)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	item, err := s.factoryToolActionable(a.ItemID, root)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	cmd := FactoryCommand{RequestID: a.RequestID, Kind: "approve", ExpectedVersion: a.ExpectedVersion}
	saved, err := s.factoryAuth.WithAuthorizedMutationAuthority(c.ID, a.ProjectID, item.RepoID, factoryCapabilityApproveAndQueue,
		func(stored string) (FactoryItem, error) {
			// An approval is journalled under the authority that admitted it,
			// so the client's assertion has to be the configured authority. A
			// mismatch is a denial, not a silent downgrade.
			if stored != authority {
				return FactoryItem{}, errFactoryToolAuthority
			}
			return s.factory.CommandAuthorized(r.Context(), a.ItemID, cmd,
				factoryToolPrincipal(c.ID, a.ProjectID, factoryCapabilityApproveAndQueue, a.RequestID, stored))
		})
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, factoryToolStored(a.ProjectID, s.factoryToolLink(saved.ID), saved))
}

// factoryToolAnswerQuestion answers the open decision on one item. A
// user-directed answer is the client's own assertion; a delegated one is only
// admitted for the project's designated client while the toggle is on.
func (s *Server) factoryToolAnswerQuestion(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, args json.RawMessage) {
	var a factoryToolAnswerArgs
	if err := decodeFactoryToolArgs(args, &a); err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	switch {
	case strings.TrimSpace(a.ItemID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "itemId is required"})
		return
	case strings.TrimSpace(a.RequestID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "requestId is required"})
		return
	case a.ExpectedVersion == 0:
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "expectedVersion is required"})
		return
	case strings.TrimSpace(a.DecisionID) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "decisionId is required"})
		return
	case strings.TrimSpace(a.Answer) == "":
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "answer is required"})
		return
	}
	authority := strings.TrimSpace(a.Authority)
	capability := ""
	switch authority {
	case factoryAuthorityUserDirected:
		capability = factoryCapabilityAnswerQuestion
	case factoryAuthorityDelegated:
		capability = factoryCapabilityDelegatedAnswer
	default:
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "authority must be user_directed or delegated"})
		return
	}
	root, err := s.factoryToolMutationRoot(a.ProjectID)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	item, err := s.factoryToolActionable(a.ItemID, root)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	cmd := FactoryCommand{
		RequestID:       a.RequestID,
		Kind:            "answer",
		DecisionID:      a.DecisionID,
		Answer:          a.Answer,
		ExpectedVersion: a.ExpectedVersion,
	}
	saved, err := s.factoryAuth.WithAuthorizedMutation(c.ID, a.ProjectID, item.RepoID, capability, func() (FactoryItem, error) {
		return s.factory.CommandAuthorized(r.Context(), a.ItemID, cmd,
			factoryToolPrincipal(c.ID, a.ProjectID, capability, a.RequestID, authority))
	})
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, factoryToolStored(a.ProjectID, s.factoryToolLink(saved.ID), saved))
}

// factoryToolRoot resolves the project a read names and checks the read
// capability that gates it. An unknown project and a project this client holds
// nothing on are one answer, and no root leaves this function.
func (s *Server) factoryToolRoot(clientID, projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", errFactoryToolInput
	}
	if s.factoryAuth == nil || s.fleet == nil || s.fleet.ws == nil {
		return "", ErrFactoryUnavailable
	}
	state, err := s.factoryAuth.grantState(clientID, projectID)
	if err != nil || !s.factoryAuth.grantAllows(state.Grant, factoryCapabilityRead) {
		return "", errFactoryDenied
	}
	root, ok := s.fleet.ws.ProjectRootByID(projectID)
	if !ok {
		return "", errFactoryDenied
	}
	return root, nil
}

// factoryToolMutationRoot resolves the project a mutation names. The write
// capability is checked under the policy lock at the append itself; only the
// mapping is read here, because the project root a draft is stored against is
// resolved server-side and never supplied by the client.
func (s *Server) factoryToolMutationRoot(projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", errFactoryToolInput
	}
	if s.fleet == nil || s.fleet.ws == nil {
		return "", ErrFactoryUnavailable
	}
	root, ok := s.fleet.ws.ProjectRootByID(projectID)
	if !ok {
		return "", errFactoryDenied
	}
	return root, nil
}

// factoryToolProposal assembles the draft a create or update proposes.
func (s *Server) factoryToolProposal(projectID, requestID, title, plan, spec string, rawConfig json.RawMessage) (FactoryCreate, error) {
	root, err := s.factoryToolMutationRoot(projectID)
	if err != nil {
		return FactoryCreate{}, err
	}
	config, err := decodeFactoryToolConfig(rawConfig)
	if err != nil {
		return FactoryCreate{}, err
	}
	config.ProjectRoot = root
	return FactoryCreate{RequestID: requestID, Title: title, Plan: plan, Spec: spec, Config: config}, nil
}

// factoryToolRead resolves one item the calling client may read.
func (s *Server) factoryToolRead(clientID, projectID, root, itemID string) (FactoryItem, error) {
	if strings.TrimSpace(itemID) == "" {
		return FactoryItem{}, errFactoryToolInput
	}
	item, err := s.factory.Get(itemID)
	if err != nil {
		return FactoryItem{}, err
	}
	if item.ProjectRoot != root {
		return FactoryItem{}, ErrFactoryNotFound
	}
	if err := s.factoryAuth.AuthorizeRead(clientID, projectID, item.RepoID); err != nil {
		return FactoryItem{}, err
	}
	return item, nil
}

// factoryToolActionable resolves one item a mutation names, before the lock.
func (s *Server) factoryToolActionable(itemID, root string) (FactoryItem, error) {
	if strings.TrimSpace(itemID) == "" {
		return FactoryItem{}, errFactoryToolInput
	}
	item, err := s.factory.Get(itemID)
	if err != nil {
		return FactoryItem{}, err
	}
	if item.ProjectRoot != root {
		return FactoryItem{}, ErrFactoryNotFound
	}
	return item, nil
}

// factoryToolOwned reports whether itemID names an item of the authorized
// project. A missing item and another project's item are the same answer, so
// an item id can never be used to learn what else this bridge holds.
func (s *Server) factoryToolOwned(itemID, root string) error {
	item, err := s.factory.Get(itemID)
	if err != nil || item.ProjectRoot != root {
		return ErrFactoryNotFound
	}
	return nil
}

// factoryToolPrincipal is the server-authenticated identity of one MCP call.
// The request id it carries is the caller's own; the service scopes it into
// the journal namespace itself.
func factoryToolPrincipal(clientID, projectID, action, requestID, authority string) factoryPrincipal {
	return factoryPrincipal{ClientID: clientID, ProjectID: projectID, Action: action, RequestID: requestID, Authority: authority}
}

// factoryToolLink is the Studio URL for one item: the operator's view of this
// same bridge, and it names nothing on disk.
func (s *Server) factoryToolLink(itemID string) string {
	return s.publicURL("/#factory/" + url.PathEscape(itemID))
}

// factoryToolItemSummary is the list view of one item: no plan, no
// specification and no configuration, because a list is a navigation surface.
type factoryToolItemSummary struct {
	ItemID    string    `json:"itemId"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Stage     string    `json:"stage,omitempty"`
	Version   uint64    `json:"version"`
	RepoID    string    `json:"repoId"`
	Branch    string    `json:"branch,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Link      string    `json:"link"`
}

// factoryToolDraftView is a stored draft with the server-resolved project root
// removed: it is a host path, and it is not the client's to see.
type factoryToolDraftView struct {
	Title  string        `json:"title"`
	Plan   string        `json:"plan"`
	Spec   string        `json:"spec"`
	Config FactoryConfig `json:"config"`
}

// factoryToolPRView is the part of a pull request a client acts on.
type factoryToolPRView struct {
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`
	Draft  bool   `json:"draft"`
}

// factoryToolOutcomeView is the latest settled outcome, summarised: the error
// text a failed effect carries can name hosts and endpoints, so it is not
// projected.
type factoryToolOutcomeView struct {
	EffectID   string             `json:"effectId"`
	StopReason string             `json:"stopReason,omitempty"`
	Confirmed  bool               `json:"confirmed"`
	HeadSHA    string             `json:"headSha,omitempty"`
	PR         *factoryToolPRView `json:"pr,omitempty"`
	Outcome    string             `json:"outcome,omitempty"`
	Summary    string             `json:"summary,omitempty"`
	Question   string             `json:"question,omitempty"`
	Reason     string             `json:"reason,omitempty"`
}

// factoryToolDetail is the get view of one item.
type factoryToolDetail struct {
	ProjectID string                  `json:"projectId"`
	ItemID    string                  `json:"itemId"`
	Title     string                  `json:"title"`
	State     string                  `json:"state"`
	Stage     string                  `json:"stage,omitempty"`
	Version   uint64                  `json:"version"`
	RepoID    string                  `json:"repoId"`
	Branch    string                  `json:"branch,omitempty"`
	Approvals int                     `json:"approvals"`
	CreatedAt time.Time               `json:"createdAt"`
	UpdatedAt time.Time               `json:"updatedAt"`
	Draft     factoryToolDraftView    `json:"draft"`
	Decision  *FactoryDecision        `json:"decision,omitempty"`
	Outcome   *factoryToolOutcomeView `json:"outcome,omitempty"`
	History   []FactoryEvent          `json:"history"`
	Link      string                  `json:"link"`
}

// factoryToolStoredItem is the result of a mutation: what was stored, its id
// and version, and where the operator can look at it.
type factoryToolStoredItem struct {
	ProjectID string               `json:"projectId"`
	ItemID    string               `json:"itemId"`
	Title     string               `json:"title"`
	State     string               `json:"state"`
	Stage     string               `json:"stage,omitempty"`
	Version   uint64               `json:"version"`
	Draft     factoryToolDraftView `json:"draft"`
	Link      string               `json:"link"`
}

// factoryToolSummary renders the list view of one item.
func (s *Server) factoryToolSummary(item FactoryItem) factoryToolItemSummary {
	return factoryToolItemSummary{
		ItemID: item.ID, Title: item.Title, State: item.State, Stage: item.Stage,
		Version: item.Version, RepoID: item.RepoID, Branch: item.Branch,
		UpdatedAt: item.UpdatedAt, Link: s.factoryToolLink(item.ID),
	}
}

// factoryToolDetail renders the get view of one item.
func (s *Server) factoryToolDetail(projectID string, item FactoryItem) factoryToolDetail {
	view := factoryToolDetail{
		ProjectID: projectID, ItemID: item.ID, Title: item.Title, State: item.State,
		Stage: item.Stage, Version: item.Version, RepoID: item.RepoID, Branch: item.Branch,
		Approvals: len(item.Approvals), CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		Draft:    factoryToolDraftView{Title: item.Draft.Title, Plan: item.Draft.Plan, Spec: item.Draft.Spec, Config: factoryToolConfigView(item.Draft.Config)},
		Decision: item.Decision,
		Outcome:  factoryToolOutcome(item),
		History:  factoryToolHistory(item.History),
		Link:     s.factoryToolLink(item.ID),
	}
	return view
}

// factoryToolStored renders the mutation result for one item.
func factoryToolStored(projectID, link string, item FactoryItem) factoryToolStoredItem {
	return factoryToolStoredItem{
		ProjectID: projectID, ItemID: item.ID, Title: item.Title, State: item.State,
		Stage: item.Stage, Version: item.Version,
		Draft: factoryToolDraftView{Title: item.Draft.Title, Plan: item.Draft.Plan, Spec: item.Draft.Spec, Config: factoryToolConfigView(item.Draft.Config)},
		Link:  link,
	}
}

// factoryToolConfigView removes the fields a client may not see.
func factoryToolConfigView(c FactoryConfig) FactoryConfig {
	c.ProjectRoot = ""
	return c
}

// factoryToolOutcome summarises the latest effect that settled with an
// outcome. An effect still in flight contributes nothing.
func factoryToolOutcome(item FactoryItem) *factoryToolOutcomeView {
	for n := len(item.Effects) - 1; n >= 0; n-- {
		e := item.Effects[n]
		if e.Outcome == nil {
			continue
		}
		o := e.Outcome
		view := &factoryToolOutcomeView{
			EffectID: e.ID, StopReason: factoryToolClip(o.StopReason, factoryToolTextLimit),
			Confirmed: o.Confirmed, HeadSHA: o.HeadSHA,
		}
		if o.PR != nil {
			view.PR = &factoryToolPRView{Number: o.PR.Number, URL: o.PR.URL, State: o.PR.State, Draft: o.PR.Draft}
		}
		if o.Report != nil {
			view.Outcome = factoryToolClip(o.Report.Outcome, factoryToolTextLimit)
			view.Summary = factoryToolClip(o.Report.Summary, factoryToolTextLimit)
			view.Question = factoryToolClip(o.Report.Question, factoryToolTextLimit)
			view.Reason = factoryToolClip(o.Report.Reason, factoryToolTextLimit)
		}
		return view
	}
	return nil
}

// factoryToolHistory renders the tail of an item's journal, so a long-running
// item cannot turn one tool result into its whole history.
func factoryToolHistory(events []FactoryEvent) []FactoryEvent {
	if len(events) > factoryToolHistoryLimit {
		events = events[len(events)-factoryToolHistoryLimit:]
	}
	out := make([]FactoryEvent, 0, len(events))
	for _, e := range events {
		out = append(out, FactoryEvent{
			Sequence: e.Sequence, At: e.At, Kind: e.Kind,
			Message:  factoryToolClip(e.Message, factoryToolTextLimit),
			EffectID: e.EffectID, Provenance: e.Provenance,
		})
	}
	return out
}

// factoryToolClip bounds one string without splitting a rune.
func factoryToolClip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// factoryToolProjectArgs, factoryToolItemArgs, factoryToolCreateArgs,
// factoryToolUpdateArgs, factoryToolApproveArgs and factoryToolAnswerArgs are
// the argument shapes of the seven tools. They are separate types because a
// field one tool does not take must be refused by it, not ignored.
type factoryToolProjectArgs struct {
	ProjectID string `json:"projectId"`
}

type factoryToolItemArgs struct {
	ProjectID string `json:"projectId"`
	ItemID    string `json:"itemId"`
}

type factoryToolCreateArgs struct {
	ProjectID string          `json:"projectId"`
	RequestID string          `json:"requestId"`
	Title     string          `json:"title"`
	Plan      string          `json:"plan"`
	Spec      string          `json:"spec"`
	Config    json.RawMessage `json:"config"`
}

type factoryToolUpdateArgs struct {
	ProjectID       string          `json:"projectId"`
	ItemID          string          `json:"itemId"`
	RequestID       string          `json:"requestId"`
	ExpectedVersion uint64          `json:"expectedVersion"`
	Title           string          `json:"title"`
	Plan            string          `json:"plan"`
	Spec            string          `json:"spec"`
	Config          json.RawMessage `json:"config"`
}

type factoryToolApproveArgs struct {
	ProjectID       string `json:"projectId"`
	ItemID          string `json:"itemId"`
	RequestID       string `json:"requestId"`
	ExpectedVersion uint64 `json:"expectedVersion"`
	Authority       string `json:"authority"`
}

type factoryToolAnswerArgs struct {
	ProjectID       string `json:"projectId"`
	ItemID          string `json:"itemId"`
	RequestID       string `json:"requestId"`
	ExpectedVersion uint64 `json:"expectedVersion"`
	DecisionID      string `json:"decisionId"`
	Answer          string `json:"answer"`
	Authority       string `json:"authority"`
}

// decodeFactoryToolArgs decodes one tool's arguments strictly: unknown fields,
// trailing data and an oversized argument object are refused rather than
// trimmed. The size bound is the MCP envelope limit, which the transport also
// enforces on the whole request.
func decodeFactoryToolArgs(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || len(trimmed) > factoryRequestLimit {
		return errFactoryToolInput
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errFactoryToolInput
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return errFactoryToolInput
	}
	return nil
}

// decodeFactoryToolConfig decodes the config argument. projectRoot is resolved
// server-side from the authorized project, so a client that supplies it is
// refused rather than quietly overridden, and every other unknown field is
// refused with it.
func decodeFactoryToolConfig(raw json.RawMessage) (FactoryConfig, error) {
	var config FactoryConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return config, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return FactoryConfig{}, errFactoryToolInput
	}
	if _, ok := fields["projectRoot"]; ok {
		return FactoryConfig{}, errFactoryToolInput
	}
	if err := decodeFactoryToolArgs(trimmed, &config); err != nil {
		return FactoryConfig{}, err
	}
	return config, nil
}

// writeFactoryToolResult writes one standard MCP tool result: a single text
// content block holding the JSON projection.
func writeFactoryToolResult(w http.ResponseWriter, id json.RawMessage, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeUnavailable, Message: "factory unavailable"})
		return
	}
	writeRPC(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(raw)}},
		"isError": false,
	}, nil)
}

// writeFactoryToolFailure writes one domain failure as a standard MCP tool
// result with isError set; the code and message stay inside the text block.
func writeFactoryToolFailure(w http.ResponseWriter, id json.RawMessage, failure factoryToolFailure) {
	raw, err := json.Marshal(failure)
	if err != nil {
		raw = []byte(`{"code":"unavailable","message":"factory unavailable"}`)
	}
	writeRPC(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(raw)}},
		"isError": true,
	}, nil)
}

// factoryMCPToolDefinitions returns the seven factory tools. Each accepts only
// the arguments it is documented with: the schema is what a client validates
// against, and the decoder refuses anything else.
func factoryMCPToolDefinitions() []map[string]any {
	projectID := map[string]any{"type": "string", "description": "Opaque project id from factory_projects"}
	itemID := map[string]any{"type": "string", "description": "Opaque work item id"}
	requestID := map[string]any{"type": "string", "description": "Caller-generated nonempty request id; a replay of the same request returns the original result"}
	expectedVersion := map[string]any{"type": "integer", "minimum": 1, "description": "The item version this request was made against"}
	draftArgs := map[string]any{
		"title": map[string]any{"type": "string"},
		"plan":  map[string]any{"type": "string", "description": "Markdown plan (plan and spec together stay under 1 MiB)"},
		"spec":  map[string]any{"type": "string"},
		"config": map[string]any{
			"type":                 "object",
			"description":          "Execution choices; the project root is resolved server-side and may not be supplied",
			"additionalProperties": false,
			"properties": map[string]any{
				"repoId":                   map[string]any{"type": "string", "description": "A registered repository this client may target"},
				"hostId":                   map[string]any{"type": "string"},
				"targetBranch":             map[string]any{"type": "string"},
				"workspace":                map[string]any{"type": "string"},
				"native":                   map[string]any{"type": "boolean"},
				"implementerRouting":       map[string]any{"type": "object"},
				"reviewerRouting":          map[string]any{"type": "object"},
				"reviewerInstructions":     map[string]any{"type": "string"},
				"implementerAuto":          map[string]any{"type": "boolean"},
				"expectedChecks":           map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"kind": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "appId": map[string]any{"type": "integer"}}, "required": []string{"kind", "name"}}},
				"noRequiredCiAcknowledged": map[string]any{"type": "boolean"},
				"maxRounds":                map[string]any{"type": "integer", "minimum": 0},
				"ciPollSeconds":            map[string]any{"type": "integer", "minimum": 0},
				"ciWaitSeconds":            map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []string{"repoId"},
		},
	}
	return []map[string]any{
		{
			"name":        factoryToolProjects,
			"description": "List the opaque project ids this client holds factory authority on, with the capabilities it holds on each.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			"name":        factoryToolList,
			"description": "List one project's work items and their versions. Requires the read capability.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"projectId": projectID},
				"required":             []string{"projectId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        factoryToolGet,
			"description": "Read one work item: its plan, spec, effective configuration, version, state, decision, latest outcome and recent history. Requires the read capability.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"projectId": projectID, "itemId": itemID},
				"required":             []string{"projectId", "itemId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        factoryToolCreateDraft,
			"description": "Create a draft work item from a plan and spec. Requires the createDraft capability. Returns the stored draft, its id and version, and a Studio link.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"projectId": projectID, "requestId": requestID,
					"title": draftArgs["title"], "plan": draftArgs["plan"], "spec": draftArgs["spec"],
					"config": draftArgs["config"],
				},
				"required":             []string{"projectId", "requestId", "title", "plan", "spec", "config"},
				"additionalProperties": false,
			},
		},
		{
			"name":        factoryToolUpdateDraft,
			"description": "Replace an unapproved draft's content after normal validation. Requires the editDraft capability. The item's identity, project and repository cannot move, and a queued item cannot be edited.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"projectId": projectID, "itemId": itemID, "requestId": requestID,
					"expectedVersion": expectedVersion,
					"title":           draftArgs["title"], "plan": draftArgs["plan"], "spec": draftArgs["spec"],
					"config": draftArgs["config"],
				},
				"required":             []string{"projectId", "itemId", "requestId", "expectedVersion", "title", "plan", "spec", "config"},
				"additionalProperties": false,
			},
		},
		{
			"name":        factoryToolApproveAndQueue,
			"description": "Approve the exact stored draft and queue it, recording this client's provenance. Requires the approveAndQueue capability and the configured approval authority.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"projectId": projectID, "itemId": itemID, "requestId": requestID,
					"expectedVersion": expectedVersion,
					"authority":       map[string]any{"type": "string", "enum": []string{factoryAuthorityUserDirected, factoryAuthorityStanding}, "description": "Must match the authority configured on this client's grant"},
				},
				"required":             []string{"projectId", "itemId", "requestId", "expectedVersion", "authority"},
				"additionalProperties": false,
			},
		},
		{
			"name":        factoryToolAnswerQuestion,
			"description": "Answer the open question on one item. A user_directed answer asserts the operator's instruction; a delegated answer is only admitted while the project's delegation toggle is on and this client is the designated one.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"projectId": projectID, "itemId": itemID, "requestId": requestID,
					"expectedVersion": expectedVersion,
					"decisionId":      map[string]any{"type": "string"},
					"answer":          map[string]any{"type": "string"},
					"authority":       map[string]any{"type": "string", "enum": []string{factoryAuthorityUserDirected, factoryAuthorityDelegated}},
				},
				"required":             []string{"projectId", "itemId", "requestId", "expectedVersion", "decisionId", "answer", "authority"},
				"additionalProperties": false,
			},
		},
	}
}
