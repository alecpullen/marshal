package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"marshal/web/bridge/contract"
)

// The factory tools serve two kinds of authenticated principal, and the same
// tool must authorize whichever one is calling it.
//
// An external MCP client is authorized by its stored grant and repository
// allowlist. A built-in session is authorized by the binding the owner-only
// socket established: its project is the binding's, and its capabilities are
// that session's effective access — the project defaults with the session's or
// worker's override applied — resolved under the policy lock on every call, so
// a changed default or a revocation takes effect at the next operation.
//
// These helpers are where that choice is made, once, so no tool can pick the
// wrong authority by forgetting to branch. The identity is never read from a
// tool argument: it is the client the transport authenticated, resolved against
// the live enrollment the same way authenticateMCPPrincipal resolved it.

// The coordinator conversation's tools. Each is offered only to a verified
// built-in session whose session a coordinated task minted (see
// coordinatorToolDefinitions), so no external client and no ordinary project
// session is shown one, and none carries a task argument a caller could forge:
// the task is resolved from the session the transport authenticated.
const (
	// coordinatorToolAttention is the read-only attention summary.
	coordinatorToolAttention = "coordinator_attention"
	// coordinatorToolAssign dispatches a task's work to its target projects.
	coordinatorToolAssign = "coordinator_assign"
	// coordinatorToolAssignments reads the task's assignments and how far each
	// got.
	coordinatorToolAssignments = "coordinator_assignments"
)

// coordinatorMCPToolDefinitions returns the tools only a coordinator
// conversation is offered.
//
// The list is separate from the fleet and factory tools on purpose. The
// attention read is read-only, and the assignment pair is task-scoped: a client
// that is not a coordinator session is refused the call rather than shown a tool
// it cannot use. None of them carries a task argument — the task is the session
// the transport authenticated.
func coordinatorMCPToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        coordinatorToolAttention,
			"description": "Read what needs a person across this host's accessible projects: pending asks, submissions awaiting confirmation and reviews that are ready, each with where it opens, plus per-source freshness. Read-only: it starts no work and changes nothing. An allClear answer means every source was read and fresh; partial means the answer covers less than the whole scope.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        coordinatorToolAssign,
			"description": "Hand this coordinated task's work to its target projects. Each target names a canonical project id and the instruction for it; a target may instead name an existing thread in that project to link for observation. A target the task's access does not cover fails on its own and its siblings still dispatch. A project whose policy authorises this session to start work begins immediately; otherwise the instruction is stored as a submission awaiting the user's confirmation and the assignment reports pending, never running. Linking is observation: an existing thread is given the instruction only when the user explicitly directed it.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"requestId": map[string]any{"type": "string", "description": "This dispatch's idempotency key. Reuse it for every retry of the same targets."},
					"targets": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"projectId":   map[string]any{"type": "string", "description": "The target project's canonical id"},
								"instruction": map[string]any{"type": "string", "description": "What that project should do"},
								"threadId":    map[string]any{"type": "string", "description": "Link this existing thread in the project instead of starting a worker"},
								"steer":       map[string]any{"type": "boolean", "description": "Give the linked thread the instruction. Only valid when the user directed that thread; the bridge verifies the direction and refuses otherwise."},
							},
							"required": []string{"projectId", "instruction"},
						},
					},
				},
				"required": []string{"requestId", "targets"},
			},
		},
		{
			"name":        coordinatorToolAssignments,
			"description": "Read this coordinated task's assignments: each target project, what it was asked, whether it created a worker or linked an existing thread, its state (dispatching, pending, running, linked, failed or expired), the worker or submission it resolved to, and the revision of its newest observed result. Read-only: it starts no work.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// coordinatorToolDefinitions returns the coordinator tool definitions this
// caller may be offered: none for an external client, and the read tool for a
// verified built-in session whose session a coordinated task minted.
func (s *Server) coordinatorToolDefinitions(principal mcpPrincipal) []map[string]any {
	if !principal.Builtin {
		return nil
	}
	svc := s.coordinator
	if svc == nil || svc.store == nil || svc.openErr != nil {
		return nil
	}
	if _, ok := svc.store.BySession(principal.Binding.SessionID); !ok {
		return nil
	}
	return coordinatorMCPToolDefinitions()
}

// coordinatorToolCall serves one coordinator tool, reporting whether the name
// was one.
//
// The only identity it accepts is the one the transport established: the
// caller's verified built-in binding. Its session is resolved to the task this
// daemon minted for it, and that task supplies the project scope, the session
// the access policy is checked against, and the scope of every assignment
// operation. An external MCP client holds no binding, so it cannot name a task at
// all — there is no task argument to forge, which is what keeps a dispatch from
// being widened by a request body.
func (s *Server) coordinatorToolCall(w http.ResponseWriter, r *http.Request, c MCPClient, id json.RawMessage, name string, args json.RawMessage) bool {
	switch name {
	case coordinatorToolAttention, coordinatorToolAssign, coordinatorToolAssignments:
	default:
		return false
	}
	binding, ok := s.marshalBindingFor(c.ID)
	if !ok {
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeDenied, Message: "not a coordinator session"})
		return true
	}
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeUnavailable, Message: "coordinator unavailable"})
		return true
	}
	task, ok := svc.store.BySession(binding.SessionID)
	if !ok {
		// A bound session no task minted is refused rather than answered with an
		// empty all-clear: there is no scope to read, so there is nothing to
		// report and nothing to dispatch.
		writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeDenied, Message: "this session is not a coordinated task"})
		return true
	}
	task.Host = s.coordinatorHost()
	switch name {
	case coordinatorToolAttention:
		s.coordinatorAttentionTool(w, id, task)
	case coordinatorToolAssign:
		s.coordinatorAssignTool(w, r, id, task, args)
	case coordinatorToolAssignments:
		s.coordinatorAssignmentsTool(w, id, task)
	}
	return true
}

// coordinatorAttentionTool answers the attention read.
func (s *Server) coordinatorAttentionTool(w http.ResponseWriter, id json.RawMessage, task contract.CoordinatorTask) {
	attention := s.coordinatorAttention(task)
	entries := make([]map[string]any, 0, len(attention.Entries))
	for _, entry := range attention.Entries {
		row := map[string]any{
			"kind":        string(entry.Kind),
			"text":        entry.Text,
			"ageSeconds":  entry.AgeSeconds,
			"source":      map[string]any{"projectId": entry.Source.ProjectID, "kind": entry.Source.Kind, "id": entry.Source.ID},
			"destination": entry.Destination,
		}
		entries = append(entries, row)
	}
	sources := make([]map[string]any, 0, len(attention.Sources))
	for _, source := range attention.Sources {
		row := map[string]any{"projectId": source.ProjectID, "state": string(source.State)}
		if source.Reason != "" {
			row["reason"] = source.Reason
		}
		if source.ObservedAt > 0 {
			row["observedAt"] = source.ObservedAt
		}
		sources = append(sources, row)
	}
	writeFactoryToolResult(w, id, map[string]any{
		"taskId":     attention.TaskID,
		"host":       attention.Host,
		"entries":    entries,
		"sources":    sources,
		"allClear":   attention.AllClear,
		"partial":    attention.Partial,
		"observedAt": attention.ObservedAt.Format(time.RFC3339),
	})
}

// coordinatorAssignTool answers one task-scoped dispatch.
//
// The steering receipt travels beside the arguments, in the header the trusted
// transport sets, and is read from there: a model cannot write one, which is why
// a linked thread is only steered when a person actually directed it.
func (s *Server) coordinatorAssignTool(w http.ResponseWriter, r *http.Request, id json.RawMessage, task contract.CoordinatorTask, args json.RawMessage) {
	var req contract.CoordinatorAssignRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			writeFactoryToolFailure(w, id, factoryToolFailure{Code: factoryCodeInvalidInput, Message: "parse arguments"})
			return
		}
	}
	result, err := s.coordinatorDispatch(r.Context(), task, req, marshalUserDirectionReceiptID(r))
	if err != nil {
		writeFactoryToolFailure(w, id, coordinatorAssignmentFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, map[string]any{
		"taskId":      result.TaskID,
		"assignments": coordinatorAssignmentRows(result.Assignments),
	})
}

// coordinatorAssignmentsTool answers the task's assignment list.
func (s *Server) coordinatorAssignmentsTool(w http.ResponseWriter, id json.RawMessage, task contract.CoordinatorTask) {
	assignments, err := s.coordinatorTaskAssignments(context.Background(), task)
	if err != nil {
		writeFactoryToolFailure(w, id, coordinatorAssignmentFailureOf(err))
		return
	}
	writeFactoryToolResult(w, id, map[string]any{
		"taskId":      task.ID,
		"host":        task.Host,
		"assignments": coordinatorAssignmentRows(assignments),
	})
}

// toolPrincipal is the identity a tool call from this client acts as: a built-in
// session when the client holds a live enrollment, and an external client
// otherwise.
func (s *Server) toolPrincipal(c MCPClient) mcpPrincipal {
	if binding, ok := s.marshalBindingFor(c.ID); ok {
		return mcpPrincipal{Client: c, Binding: binding, Builtin: true}
	}
	return mcpPrincipal{Client: c}
}

// The coordinator attention read.
//
// This is the one place Marshal's own conversation answers "what needs me?". It
// is a pure read of what the fleet already holds: it starts no worker, prompts
// no agent, and calls no mutation path, so an attention-only turn costs a
// projection of state and nothing else.
//
// The task is the identity. Its daemon-minted coordinator session is what the
// access policy is resolved for on every read, so a revoked grant arrives here
// as an unauthorized source — never as a quiet project. A source that could not
// be read (unavailable or unauthorized) or whose evidence has aged (stale) can
// never produce an all-clear: "nothing needs you" would then be a claim the
// evidence does not support, and the caller gets Partial instead.

// coordinatorNow is the clock the coordinator reads use, so a test can age
// evidence without sleeping.
func (s *Server) coordinatorNow() time.Time {
	if s != nil && s.coordinator != nil && s.coordinator.now != nil {
		return s.coordinator.now().UTC()
	}
	return time.Now().UTC()
}

// coordinatorAttention is the read-only attention summary for one task.
func (s *Server) coordinatorAttention(task contract.CoordinatorTask) contract.CoordinatorAttention {
	now := s.coordinatorNow()
	out := contract.CoordinatorAttention{
		TaskID:     task.ID,
		Host:       task.Host,
		Entries:    []contract.CoordinatorAttentionEntry{},
		Sources:    []contract.CoordinatorSource{},
		ObservedAt: now,
	}
	if s.fleet == nil || s.fleet.ws == nil {
		// There is no fleet to read, so no source was read: that is incomplete
		// coverage, never an all-clear.
		out.Partial = true
		return out
	}

	byRoot, observed := s.coordinatorEvidence(now)
	for _, projectID := range s.coordinatorScope(task) {
		source := contract.CoordinatorSource{ProjectID: projectID, State: contract.CoordinatorSourceFresh}
		root, ok := s.fleet.ws.ProjectRootByID(projectID)
		if !ok {
			source.State, source.Reason = contract.CoordinatorSourceUnavailable, "this host does not hold the project"
			out.Sources = append(out.Sources, source)
			out.Partial = true
			continue
		}
		if !s.coordinatorReadAllowed(task, projectID) {
			source.State, source.Reason = contract.CoordinatorSourceUnauthorized, "the task's read access to the project is not granted"
			out.Sources = append(out.Sources, source)
			out.Partial = true
			continue
		}
		if at, ok := observed[root]; ok {
			source.ObservedAt = at.UnixMilli()
			if now.Sub(at) > coordinatorFreshWindow {
				source.State, source.Reason = contract.CoordinatorSourceStale, "the newest evidence is older than the freshness window"
				out.Partial = true
			}
		}
		// An entry only reaches a caller whose read the policy still allows,
		// and its destination is built here rather than by the caller: a link
		// to a source this task cannot read would leak what it points at.
		out.Entries = append(out.Entries, byRoot[root]...)
		out.Sources = append(out.Sources, source)
	}
	// Oldest first, so the ask that has waited longest is what a reader sees
	// first. The order is stable, so equal ages keep their source order.
	sort.SliceStable(out.Entries, func(i, j int) bool {
		return out.Entries[i].AgeSeconds > out.Entries[j].AgeSeconds
	})
	if len(out.Sources) == 0 {
		// No project could be named: nothing was read, so nothing is clear.
		out.Partial = true
	}
	out.AllClear = len(out.Entries) == 0 && !out.Partial
	return out
}

// coordinatorScope is the projects one task may read: those it was bound to, or
// every project this host registered when it named none. Both are project IDs,
// never paths, and the list is sorted so a read is reproducible.
func (s *Server) coordinatorScope(task contract.CoordinatorTask) []string {
	if len(task.Projects) > 0 {
		out := append([]string(nil), task.Projects...)
		sort.Strings(out)
		return out
	}
	roots := s.fleet.ws.Projects()
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if id, ok := s.fleet.ws.ProjectID(root); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// coordinatorReadAllowed checks one project's read capability for the task's
// bound session, through the one policy the built-in tools use: the project's
// stored defaults, then any override for this session. Resolved on every read,
// so a revocation needs no invalidation to take effect.
func (s *Server) coordinatorReadAllowed(task contract.CoordinatorTask, projectID string) bool {
	status, err := s.coordinatorTargetStatus(task, projectID)
	if err != nil {
		return false
	}
	return marshalAccessAllows(status.Access, factoryCapabilityRead)
}

// coordinatorEvidence projects what the fleet already holds into attention
// entries, keyed by the project root each one belongs to, together with the
// newest evidence each root contributed.
//
// It reads the same projections the Studio's Needs you screen does — the inbox
// queue (a pending agent, an unconfirmed submission, a fix that could not
// finish) and the pull records, including a review whose checks have settled —
// so the conversation and the screen cannot disagree about what is waiting.
// A source that cannot be attributed to a registered project is left out: a
// scoped read never widens to reach it.
func (s *Server) coordinatorEvidence(now time.Time) (map[string][]contract.CoordinatorAttentionEntry, map[string]time.Time) {
	byRoot := map[string][]contract.CoordinatorAttentionEntry{}
	observed := map[string]time.Time{}
	add := func(root string, at, evidence time.Time, entry contract.CoordinatorAttentionEntry) {
		if root == "" {
			return
		}
		if id, ok := s.fleet.ws.ProjectID(root); ok {
			entry.Source.ProjectID = id
		} else {
			return
		}
		entry.AgeSeconds = int64(now.Sub(at).Seconds())
		if entry.AgeSeconds < 0 {
			entry.AgeSeconds = 0
		}
		byRoot[root] = append(byRoot[root], entry)
		if evidence.After(observed[root]) {
			observed[root] = evidence
		}
	}

	// A pending agent ask. The registry is what says it is still outstanding,
	// so the evidence here is this read.
	for _, st := range s.fleet.Snapshot() {
		if st.Pending == nil {
			continue
		}
		at := st.UpdatedAt
		if st.Pending.At > 0 {
			at = time.UnixMilli(st.Pending.At).UTC()
		}
		add(st.Project, at, now, contract.CoordinatorAttentionEntry{
			Source:      contract.CoordinatorThreadRef{Kind: "agent", ID: st.ID},
			Kind:        contract.CoordinatorAttentionAsk,
			Text:        coordinatorAgentAskText(st),
			Destination: "/thread/" + st.ID,
		})
	}
	// An intake submission awaiting confirmation. It names a repo, which this
	// host maps to a project; one it cannot attribute is not reported.
	for _, p := range s.fleet.ws.Pending() {
		// A coordinated assignment's submission names the project it targets
		// directly (task 6); every other submission names the repository it
		// clones, which this host maps to a project.
		root := p.Project
		if root == "" {
			root = s.fleet.ws.ProjectRootByRepo(p.RepoID)
		}
		add(root, p.CreatedAt, now, contract.CoordinatorAttentionEntry{
			Source:      contract.CoordinatorThreadRef{Kind: "intake", ID: p.ID},
			Kind:        contract.CoordinatorAttentionAsk,
			Text:        fmt.Sprintf("Submission %q is waiting for confirmation", p.Title),
			Destination: "/inbox",
		})
	}
	// A fix that could not finish leaves the ask the Inbox carries. Its
	// evidence is when the pull request was last observed, which is what makes
	// an answer that has aged visibly stale.
	for _, ask := range s.fleet.ws.PullAsks() {
		rec, ok := s.fleet.pull(ask.Pull)
		if !ok {
			continue
		}
		at := pullUpdated(rec)
		if ask.At > 0 {
			at = time.UnixMilli(ask.At).UTC()
		}
		add(rec.ProjectRoot, at, pullEvidence(rec), contract.CoordinatorAttentionEntry{
			Source:      contract.CoordinatorThreadRef{Kind: "pull", ID: rec.ID},
			Kind:        contract.CoordinatorAttentionAsk,
			Text:        coordinatorPullAskText(rec, ask),
			Destination: "/pulls",
		})
	}
	// A review ready for a person: its checks have settled on the head the
	// record holds. A pull request already carrying an ask is not counted
	// twice.
	asked := map[string]bool{}
	for _, ask := range s.fleet.ws.PullAsks() {
		asked[ask.Pull] = true
	}
	for _, rec := range s.fleet.pulls() {
		if rec.State != contract.PullReady || asked[rec.ID] {
			continue
		}
		add(rec.ProjectRoot, pullUpdated(rec), pullEvidence(rec), contract.CoordinatorAttentionEntry{
			Source:      contract.CoordinatorThreadRef{Kind: "review", ID: rec.ID},
			Kind:        contract.CoordinatorAttentionReview,
			Text:        coordinatorReviewText(rec),
			Destination: "/pulls",
		})
	}
	return byRoot, observed
}

// pullUpdated is when a pull request record itself last changed.
func pullUpdated(rec contract.PullRecord) time.Time {
	return time.UnixMilli(rec.UpdatedAt).UTC()
}

// pullEvidence is when a pull request's evidence was last read: the observation
// time when the bridge has one, else the record's own update.
func pullEvidence(rec contract.PullRecord) time.Time {
	if rec.ObservedAt > 0 {
		return time.UnixMilli(rec.ObservedAt).UTC()
	}
	return pullUpdated(rec)
}

// coordinatorAgentAskText is the display text for a pending agent ask.
func coordinatorAgentAskText(st AgentStatus) string {
	label := firstNonEmpty(st.Title, st.Name, st.ID)
	if st.Pending.Kind == contract.PendingApproval {
		return fmt.Sprintf("%s is waiting for approval", label)
	}
	return fmt.Sprintf("%s is waiting for an answer", label)
}

// coordinatorPullAskText is the display text for a fix that could not finish.
func coordinatorPullAskText(rec contract.PullRecord, ask contract.PullAsk) string {
	label := firstNonEmpty(rec.Title, "#"+strconv.Itoa(rec.Number))
	if strings.TrimSpace(ask.Check) == "" {
		return fmt.Sprintf("%s needs a decision", label)
	}
	return fmt.Sprintf("%s: check %q needs a decision", label, ask.Check)
}

// coordinatorReviewText is the display text for a review that is ready.
func coordinatorReviewText(rec contract.PullRecord) string {
	return fmt.Sprintf("Pull request #%d %s is ready for review", rec.Number, firstNonEmpty(rec.Title, rec.HeadRef))
}

// authorizeToolRoot resolves the project root one read may act on and checks the
// read capability that gates it. A built-in session may name only the project
// its binding names; an external client only a project its grant covers. An
// unknown project and a project the principal holds nothing on are one answer,
// and no root leaves this function.
func (s *Server) authorizeToolRoot(clientID, projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", errFactoryToolInput
	}
	if s.factoryAuth == nil || s.fleet == nil || s.fleet.ws == nil {
		return "", ErrFactoryUnavailable
	}
	if binding, ok := s.marshalBindingFor(clientID); ok {
		if projectID != binding.ProjectID {
			// A session is scoped by the project its binding names; a tool
			// argument may not move it to another one.
			return "", errFactoryDenied
		}
		if err := s.factoryAuth.AuthorizeMarshalCapability(binding, "", factoryCapabilityRead); err != nil {
			return "", err
		}
		root, ok := s.fleet.ws.ProjectRootByID(projectID)
		if !ok {
			return "", errFactoryDenied
		}
		return root, nil
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

// authorizeToolRead checks the read capability for one item's repository. The
// project is checked with it, so a built-in session cannot read another
// project's item through an id it guessed.
func (s *Server) authorizeToolRead(clientID, projectID, repoID string) error {
	if binding, ok := s.marshalBindingFor(clientID); ok {
		if projectID != binding.ProjectID {
			return errFactoryDenied
		}
		return s.factoryAuth.AuthorizeMarshalCapability(binding, repoID, factoryCapabilityRead)
	}
	return s.factoryAuth.AuthorizeRead(clientID, projectID, repoID)
}

// withAuthorizedToolMutation authorizes one durable mutation for whichever
// principal this client is and, only when it holds, runs fn while the policy
// lock is still held — so the journal append is the linearization point for a
// grant or an access change, and a revocation lands entirely before the
// operation was admitted or entirely after it committed.
//
// fn receives the principal to journal under. For an external client that is
// its client identity and claimed authority; for a built-in session it is the
// instance, project, session and worker the binding verified.
func (s *Server) withAuthorizedToolMutation(c MCPClient, projectID, repoID, capability, requestID, authority string, fn func(factoryPrincipal) (FactoryItem, error)) (FactoryItem, error) {
	if binding, ok := s.marshalBindingFor(c.ID); ok {
		if projectID != binding.ProjectID {
			return FactoryItem{}, errFactoryDenied
		}
		return s.factoryAuth.WithAuthorizedMarshalMutation(binding, repoID, capability, func(p factoryPrincipal) (FactoryItem, error) {
			p.RequestID = requestID
			return fn(p)
		})
	}
	return s.factoryAuth.WithAuthorizedMutation(c.ID, projectID, repoID, capability, func() (FactoryItem, error) {
		return fn(factoryToolPrincipal(c.ID, projectID, capability, requestID, authority))
	})
}

// factoryToolProjectsBuiltin lists the one project a verified built-in session
// acts on and the capabilities its effective access holds there.
//
// The project is the binding's and the capabilities are resolved from the stored
// default plus this session's override, under the policy lock, so what a session
// is told it may do is what the next call will enforce. A session whose access
// is being changed therefore sees the change at its next listing.
func (s *Server) factoryToolProjectsBuiltin(w http.ResponseWriter, principal mcpPrincipal, id json.RawMessage) {
	status, err := s.factoryAuth.marshalStatus(principal.Binding)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	projects := []map[string]any{}
	if status.ProjectID != "" {
		capabilities := []string{}
		for _, capability := range []string{
			factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
			factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue,
		} {
			if marshalAccessAllows(status.Access, capability) {
				capabilities = append(capabilities, capability)
			}
		}
		entry := map[string]any{"projectId": status.ProjectID, "capabilities": capabilities}
		if marshalAccessAllows(status.Access, factoryCapabilityApproveAndQueue) && status.Access.ApprovalAuthority != nil {
			// The authority an approval acts under, so a caller can see it
			// without guessing. It is the stored policy, never the request's.
			entry["approvalAuthority"] = *status.Access.ApprovalAuthority
		}
		projects = append(projects, entry)
	}
	writeFactoryToolResult(w, id, map[string]any{"projects": projects})
}
