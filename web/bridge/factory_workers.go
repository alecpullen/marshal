package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type factoryWorkers struct{ fleet *Fleet }

func newFactoryWorkers(fleet *Fleet) *factoryWorkers { return &factoryWorkers{fleet: fleet} }

// ErrFactoryUnsupported reports the one thing that makes an approved snapshot
// unenforceable: a runtime that cannot bind a session's child settings. The
// worker is refused before it starts rather than run under the runtime's
// global settings, which is not what was approved.
var ErrFactoryUnsupported = errors.New("factory worker settings unsupported")

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

// factorySubagentSettingsSupported reports whether the runtime binds and
// enforces a session's child-settings snapshot. It reads the same control
// session's session/status that factoryAPI.factorySessionSubagents reads, so
// the capability costs no session of its own. An agent that does not answer the
// method, answers {}, or reports settings false cannot enforce a snapshot, and
// says so here rather than at the first child it fails to limit.
func (f *Fleet) factorySubagentSettingsSupported(ctx context.Context) bool {
	if f == nil {
		return false
	}
	raw, err := f.controlCall(ctx, "session/status", nil, true)
	if err != nil {
		return false
	}
	var status struct {
		SubagentControls *struct {
			Settings bool `json:"settings"`
		} `json:"subagentControls"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return false
	}
	return status.SubagentControls != nil && status.SubagentControls.Settings
}

func (f *Fleet) SpawnFactoryWorker(ctx context.Context, item FactoryItem, role, effectID string) (FactoryWorker, error) {
	if item.ID == "" || effectID == "" || (role != "implementer" && role != "reviewer") {
		return FactoryWorker{}, fmt.Errorf("factory: invalid worker identity")
	}
	if len(item.Approvals) == 0 {
		return FactoryWorker{}, fmt.Errorf("factory: missing approval")
	}
	approval := item.Approvals[len(item.Approvals)-1]
	conf := approval.Config
	if conf.Native && conf.Workspace != "" {
		return FactoryWorker{}, fmt.Errorf("factory: native and workspace cannot both be selected")
	}
	if conf.RepoID == "" || item.BaseSHA == "" {
		return FactoryWorker{}, fmt.Errorf("factory: repository and base SHA required")
	}
	ref := item.BaseSHA
	if role == "implementer" && len(item.PRs) > 0 && len(item.Rounds) > 0 && item.Rounds[len(item.Rounds)-1].HeadSHA != "" {
		ref = item.Rounds[len(item.Rounds)-1].HeadSHA
	}
	mode := "act"
	auto := conf.ImplementerAuto
	routing := conf.ImplementerRouting
	// The snapshot a worker runs under is the one its own role's approval
	// froze. The other role's snapshot is neither this worker's to run under
	// nor this worker's to change.
	subagents := approval.ImplementerSubagents
	if role == "reviewer" {
		mode = "ask"
		auto = false
		routing = conf.ReviewerRouting
		subagents = approval.ReviewerSubagents
		if len(item.Rounds) == 0 || item.Rounds[len(item.Rounds)-1].HeadSHA == "" {
			return FactoryWorker{}, fmt.Errorf("factory: missing reviewer candidate head")
		}
		ref = item.Rounds[len(item.Rounds)-1].HeadSHA
	}
	// An approval that froze settings states what the worker must run under; a
	// runtime that cannot bind them is refused before any worker starts. A
	// legacy approval carries the zero value and sends nothing, which is the
	// behaviour it always had.
	var snapshot *FactorySubagentSettings
	if subagents != (FactorySubagentSettings{}) {
		if !f.factorySubagentSettingsSupported(ctx) {
			return FactoryWorker{}, fmt.Errorf("%w: the runtime cannot bind the approved %s subagent settings", ErrFactoryUnsupported, role)
		}
		snapshot = &subagents
	}
	opts := SpawnOptions{Name: "Factory " + role + " " + item.ID, Mode: mode, Auto: &auto, RepoID: conf.RepoID, Ref: ref, Routing: routing, Subagents: snapshot, Workspace: conf.Workspace}
	id, err := f.spawn(ctx, conf.ProjectRoot, opts, &factorySpawnTag{itemID: item.ID, role: role, effectID: effectID, native: conf.Native})
	if err != nil {
		return FactoryWorker{}, err
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.SessionID == "" {
		return FactoryWorker{}, fmt.Errorf("factory: spawned worker has no saved session")
	}
	return FactoryWorker{AgentID: id, SessionID: a.SessionID, Role: role, SpawnEffectID: effectID, ApprovalRevision: approval.Revision, Subagents: subagents}, nil
}

func (w *factoryWorkers) Execute(ctx context.Context, item FactoryItem, effect FactoryEffect) (result FactoryOutcome, err error) {
	result = FactoryOutcome{EffectID: effect.ID}
	dispatched := false
	defer func() {
		if err != nil && !errors.Is(err, ErrFactoryRefused) && !errors.Is(err, ErrFactoryUncertain) && !errors.Is(err, ErrFactoryInvalidResult) && !errors.Is(err, ErrFactoryWorkerFailed) {
			if dispatched {
				err = fmt.Errorf("%w: %v", ErrFactoryUncertain, err)
			} else {
				err = fmt.Errorf("%w: %v", ErrFactoryRefused, err)
			}
		}
	}()
	if effect.Kind != "implement" && effect.Kind != "review" {
		return result, fmt.Errorf("factory: unsupported worker effect %s", effect.Kind)
	}
	if effect.Worker == nil {
		return result, fmt.Errorf("factory: effect has no worker")
	}
	worker := *effect.Worker
	if worker.Role != map[string]string{"implement": "implementer", "review": "reviewer"}[effect.Kind] {
		return result, fmt.Errorf("factory: worker role mismatch")
	}
	a, ok := w.fleet.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.FactoryParked {
		return result, fmt.Errorf("factory: worker ownership or state mismatch")
	}
	rt, err := w.fleet.runtimeForAgent(worker.AgentID)
	if err != nil {
		return result, err
	}
	if worker.SessionID == "" || worker.SessionID != w.fleet.sessionIDFor(rt) {
		return result, fmt.Errorf("factory: worker session mismatch")
	}
	prompt, err := BuildFactoryPrompt(item, effect)
	if err != nil {
		return result, err
	}
	before, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read pre-turn stack: %w", err)
	}
	oldFinal, _, _ := factoryFinal(before)
	dispatched = true
	turn, err := rt.reg.PromptResult(ctx, worker.SessionID, prompt)
	result.StopReason = turn.StopReason
	if err != nil {
		return result, err
	}
	if turn.StopReason != "end_turn" {
		return result, fmt.Errorf("%w: worker turn ended with %q", ErrFactoryWorkerFailed, turn.StopReason)
	}
	after, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read post-turn stack: %w", err)
	}
	// A turn that launched children is not finished when its own report arrives:
	// the children are the work, so until they are proven settled the report is
	// early. The effect keeps its dispatch and its candidate identity, and the
	// delegated turn beside it is what a continuation resolves.
	if delegated, waiting := w.delegatedTurn(ctx, rt, worker, effect, before, after); waiting {
		result.Delegated = delegated
		return result, nil
	}
	final, message, hasMessage := factoryFinal(after)
	if final == "" || final == oldFinal || !hasMessage || strings.TrimSpace(message) == "" {
		return result, fmt.Errorf("%w: no new final report in turn", ErrFactoryInvalidResult)
	}
	report, err := ParseFactoryReport(message, effect.ID, worker.Role, effect.ExpectedBase, effect.ExpectedHead)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrFactoryInvalidResult, err)
	}
	result.Report = &report
	result.Worker = &worker
	if report.Outcome == "needs_input" {
		result.Confirmed = true
		return result, nil
	}
	obs, err := w.Observe(ctx, item)
	if err != nil {
		return result, err
	}
	if obs.Dirty && worker.Role == "reviewer" {
		return result, fmt.Errorf("factory: reviewer checkout changed")
	}
	result.HeadSHA, result.TreeSHA, result.BaseSHA = obs.HeadSHA, obs.TreeSHA, item.BaseSHA
	if obs.HeadSHA == "" || obs.TreeSHA == "" {
		return result, fmt.Errorf("factory: checkout Git state unavailable")
	}
	if worker.Role == "reviewer" && obs.HeadSHA != effect.ExpectedHead {
		return result, fmt.Errorf("factory: reviewer checkout head changed")
	}
	result.Confirmed = true
	return result, nil
}

func factoryFinal(raw json.RawMessage) (id, message string, hasMessage bool) {
	var tree struct {
		Nodes []struct {
			ID, Kind string
			Message  *struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &tree) != nil {
		return "", "", false
	}
	for i := len(tree.Nodes) - 1; i >= 0; i-- {
		if tree.Nodes[i].Kind == "final" {
			if tree.Nodes[i].Message == nil {
				return tree.Nodes[i].ID, "", false
			}
			return tree.Nodes[i].ID, tree.Nodes[i].Message.Content, true
		}
	}
	return "", "", false
}

// EndWork.Items is omitempty in the existing ACP wire contract: {} is a
// successful empty snapshot. A present items field must be an array; null or
// an unrelated object cannot establish idleness.
func decodeFactoryWork(raw json.RawMessage) (known, idle bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false, false
	}
	if len(fields) == 0 {
		return true, true
	}
	items, ok := fields["items"]
	if !ok || len(fields) != 1 {
		return false, false
	}
	items = []byte(strings.TrimSpace(string(items)))
	if len(items) == 0 || items[0] != '[' {
		return false, false
	}
	var entries []json.RawMessage
	if json.Unmarshal(items, &entries) != nil {
		return false, false
	}
	return true, len(entries) == 0
}

// factoryStackChildren names the children a session/stack snapshot shows: the
// subagent a tool row started, and the subagent a completion closed. It is how a
// dispatched turn's own children are told from the work an earlier turn left,
// because the pre-turn stack already shows every child the session had.
func factoryStackChildren(raw json.RawMessage) map[int64]bool {
	var tree struct {
		Nodes []struct {
			Tool *struct {
				SubagentID int64 `json:"subagentId"`
			} `json:"tool"`
			Completion *struct {
				SubagentID int64 `json:"subagentId"`
			} `json:"completion"`
		} `json:"nodes"`
	}
	out := make(map[int64]bool)
	if json.Unmarshal(raw, &tree) != nil {
		return out
	}
	for _, n := range tree.Nodes {
		if n.Tool != nil && n.Tool.SubagentID > 0 {
			out[n.Tool.SubagentID] = true
		}
		if n.Completion != nil && n.Completion.SubagentID > 0 {
			out[n.Completion.SubagentID] = true
		}
	}
	return out
}

// factoryStackTurn names the newest turn a session/stack snapshot shows, which
// after a prompt answer is the turn that just ran.
func factoryStackTurn(raw json.RawMessage) string {
	var tree struct {
		Roots []string `json:"roots"`
	}
	if json.Unmarshal(raw, &tree) != nil || len(tree.Roots) == 0 {
		return ""
	}
	return tree.Roots[len(tree.Roots)-1]
}

// factoryChildTerminal reports whether a child state the session's projection
// names is finished. done and failed are terminal; running is not, and a state
// the projection does not name is not terminal either, so it counts as unknown
// work rather than finished work.
func factoryChildTerminal(status string) bool {
	return status == "done" || status == "failed"
}

// factoryChildProjection decodes one worker session's session/status answer into
// its children. Known is false for an agent that does not project its children
// at all: it advertises no observation control, or answers a shape this cannot
// read. A status the session sends without that control is not a claim that the
// session has no children, so it is never read as a settled empty set.
//
// A child whose id is not a positive number, or one a second entry claims, is an
// ambiguous identity: the projection then cannot say which child is which, so it
// names none of them.
func factoryChildProjection(raw json.RawMessage) ([]FactoryChild, bool) {
	var status struct {
		SubagentControls *struct {
			Observe bool `json:"observe"`
		} `json:"subagentControls"`
		Subagents []struct {
			ID     int64  `json:"id"`
			Label  string `json:"label"`
			Status string `json:"status"`
		} `json:"subagents"`
	}
	if json.Unmarshal(raw, &status) != nil || status.SubagentControls == nil || !status.SubagentControls.Observe {
		return nil, false
	}
	seen := make(map[int64]bool, len(status.Subagents))
	out := make([]FactoryChild, 0, len(status.Subagents))
	for _, s := range status.Subagents {
		if s.ID <= 0 || seen[s.ID] {
			return nil, false
		}
		seen[s.ID] = true
		out = append(out, FactoryChild{ID: s.ID, Label: s.Label, Status: s.Status, Terminal: factoryChildTerminal(s.Status)})
	}
	return out, true
}

// sessionChildren reads one worker session's child projection from that session's
// own status. A session the bridge cannot ask, or one that does not project its
// children, contributes an unknown projection: nothing in it counts as settled.
func (w *factoryWorkers) sessionChildren(ctx context.Context, rt *agentRuntime, sessionID string) FactoryChildSession {
	out := FactoryChildSession{SessionID: sessionID}
	if rt == nil || rt.child == nil || sessionID == "" {
		return out
	}
	raw, err := rt.child.Request(ctx, "session/status", map[string]any{"sessionId": sessionID})
	if err != nil {
		return out
	}
	children, known := factoryChildProjection(raw)
	if !known {
		return out
	}
	out.Known = true
	for _, child := range children {
		child.SessionID = sessionID
		out.Children = append(out.Children, child)
	}
	return out
}

// factoryParentTurn names the turn a delegated turn belongs to. The post-turn
// stack's newest root is the turn that just ran, which is an identity the bridge
// already holds; a stack that names no turn falls back to the effect's own
// identity and the dispatch time rather than to a request the bridge would
// otherwise not make.
func factoryParentTurn(after json.RawMessage, effectID string) string {
	if turn := factoryStackTurn(after); turn != "" {
		return turn
	}
	return effectID + "@" + time.Now().UTC().Format(time.RFC3339Nano)
}

// delegatedTurn decides whether a dispatched turn is finished. A turn that
// launched children owns their work: until each one is proven settled by the
// worker session's own projection, the report the turn wrote is early and the
// turn waits. Children the pre-turn stack already showed belong to an earlier
// turn and are never this turn's, and a turn that launched no child has nothing
// to wait for, so it keeps the normal path.
//
// The effect and its candidate identity are untouched: the caller persists the
// returned turn beside them instead of completing the effect.
func (w *factoryWorkers) delegatedTurn(ctx context.Context, rt *agentRuntime, worker FactoryWorker, effect FactoryEffect, before, after json.RawMessage) (*FactoryDelegatedTurn, bool) {
	prior := factoryStackChildren(before)
	children := w.sessionChildren(ctx, rt, worker.SessionID)
	launched := make(map[int64]bool)
	for id := range factoryStackChildren(after) {
		if !prior[id] {
			launched[id] = true
		}
	}
	for _, child := range children.Children {
		if !prior[child.ID] {
			launched[child.ID] = true
		}
	}
	if len(launched) == 0 {
		return nil, false
	}
	// A child the projection does not name, a state it does not name as
	// finished, and a projection the bridge could not read are all unknown, and
	// unknown work never counts as settled.
	terminal := make(map[int64]bool, len(children.Children))
	for _, child := range children.Children {
		terminal[child.ID] = child.Terminal
	}
	settled := children.Known
	ids := make([]int64, 0, len(launched))
	for id := range launched {
		ids = append(ids, id)
		if finished, ok := terminal[id]; !ok || !finished {
			settled = false
		}
	}
	if settled {
		return nil, false
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return &FactoryDelegatedTurn{
		EffectID:     effect.ID,
		WorkerID:     worker.AgentID,
		SessionID:    worker.SessionID,
		ParentTurnID: factoryParentTurn(after, effect.ID),
		ChildIDs:     ids,
		State:        factoryDelegatedWaiting,
	}, true
}

// factoryEffectRole is the worker role an effect kind belongs to.
func factoryEffectRole(kind string) string {
	switch kind {
	case "implement":
		return "implementer"
	case "review":
		return "reviewer"
	}
	return ""
}

// factoryItemWorker is one recorded worker by its agent identity.
func factoryItemWorker(i FactoryItem, agentID string) (FactoryWorker, bool) {
	for _, w := range i.Workers {
		if w.AgentID == agentID {
			return w, true
		}
	}
	return FactoryWorker{}, false
}

// workerRuntime resolves the live runtime that runs one effect's own worker,
// under the ownership checks every worker call makes. A parked, replaced or
// foreign worker is not this item's to prompt.
func (w *factoryWorkers) workerRuntime(item FactoryItem, worker FactoryWorker) (*agentRuntime, error) {
	if w == nil || w.fleet == nil {
		return nil, fmt.Errorf("%w: factory runtime unavailable", ErrFactoryRefused)
	}
	a, ok := w.fleet.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.FactoryParked {
		return nil, fmt.Errorf("factory: worker ownership or state mismatch")
	}
	rt, err := w.fleet.runtimeForAgent(worker.AgentID)
	if err != nil {
		return nil, err
	}
	if worker.SessionID == "" || worker.SessionID != w.fleet.sessionIDFor(rt) {
		return nil, fmt.Errorf("factory: worker session mismatch")
	}
	return rt, nil
}

// factoryDelegatedChildList renders the children a continuation reports on, in
// the order the delegated turn recorded them, so the same settled set always
// produces the same prompt.
func factoryDelegatedChildList(d FactoryDelegatedTurn, projection FactoryChildSession) []FactoryChild {
	byID := make(map[int64]FactoryChild, len(projection.Children))
	for _, child := range projection.Children {
		byID[child.ID] = child
	}
	out := make([]FactoryChild, 0, len(d.ChildIDs))
	for _, id := range d.ChildIDs {
		child, ok := byID[id]
		if !ok {
			child = FactoryChild{SessionID: d.SessionID, ID: id, Status: "unknown"}
		}
		out = append(out, child)
	}
	return out
}

// BuildFactoryContinuationPrompt asks a worker for the report its first turn
// wrote too early. The continuation is a second turn of the same effect: it
// names the iteration and the children that settled since, and it asks for a
// fresh report for the original operation and candidate, so the earlier final
// message can never be read as this turn's result.
func BuildFactoryContinuationPrompt(item FactoryItem, effect FactoryEffect, delegated FactoryDelegatedTurn, children []FactoryChild) (string, error) {
	if effect.ID == "" || (effect.Kind != "implement" && effect.Kind != "review") {
		return "", fmt.Errorf("factory: unsupported continuation effect")
	}
	if delegated.ContinuationID == "" || delegated.EffectID != effect.ID {
		return "", fmt.Errorf("factory: continuation identity mismatch")
	}
	if len(item.Approvals) == 0 {
		return "", fmt.Errorf("factory: missing approval")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Factory operation ID: %s\nContinuation ID: %s\n", effect.ID, delegated.ContinuationID)
	fmt.Fprintf(&b, "Task: %s\n", item.Title)
	b.WriteString("The child work this turn started has finished. Read each child's report, incorporate what it established, and do not repeat work a child already completed.\n")
	for _, child := range children {
		label := child.Label
		if label == "" {
			label = "child"
		}
		fmt.Fprintf(&b, "Child %d (%s): %s\n", child.ID, label, child.Status)
	}
	b.WriteString("Assess every child that failed or was stopped: state what it did not finish and what that means for this work.\n")
	if effect.Kind == "review" {
		fmt.Fprintf(&b, "Review base SHA: %s\nCandidate head SHA: %s\nUse ask mode; do not edit files.\n", effect.ExpectedBase, effect.ExpectedHead)
	} else {
		b.WriteString("Stay inside the approved scope; do not push or merge.\n")
	}
	fmt.Fprintf(&b, "Emit a fresh report for the original operation %s and the same candidate. The report written before the children settled is not this result.\n", effect.ID)
	fmt.Fprintf(&b, "Finish with a fenced JSON block containing continuationId (%q), operationId, outcome (complete or needs_input), summary, question, reason, baseSha, headSha, inputsAccessible, and findings.\n", delegated.ContinuationID)
	if effect.Kind == "implement" {
		b.WriteString("A complete report must also include narration with issue, approach, existingBehaviour, alternatives, tradeoffs, choice, verification, limitations, and excerpts, each excerpt carrying path, lang and code.\n")
	}
	return b.String(), nil
}

// factoryReportContinuationID reads the continuation identity a report names.
// An empty result is a report that does not say which iteration it answers,
// which is never evidence for a continuation.
func factoryReportContinuationID(message string) string {
	block, ok := lastJSONBlock(message)
	if !ok {
		return ""
	}
	var wire struct {
		ContinuationID string `json:"continuationId"`
	}
	if json.Unmarshal([]byte(block), &wire) != nil {
		return ""
	}
	return wire.ContinuationID
}

// Continue runs the one continuation a delegated turn takes once its children
// settle. It is a second turn of the same effect, never a new effect: the
// prompt asks for a fresh report for the original operation and candidate, and
// the result is bound to the continuation identity that asked for it.
func (f *factoryEffects) Continue(ctx context.Context, item FactoryItem, effect FactoryEffect) (FactoryOutcome, error) {
	if f == nil || f.workers == nil {
		return FactoryOutcome{EffectID: effect.ID}, fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	return f.workers.Continue(ctx, item, effect)
}

// ObserveChildren reads one delegated turn's session projection. It costs one
// session/status call and no checkout work: a tick that asks only whether the
// children settled does not need the rest of an observation.
func (f *factoryEffects) ObserveChildren(ctx context.Context, item FactoryItem, delegated FactoryDelegatedTurn) (FactoryChildSession, error) {
	if f == nil || f.workers == nil {
		return FactoryChildSession{SessionID: delegated.SessionID}, fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	return f.workers.observeChildren(ctx, item, delegated)
}

// CancelChildren stops the children one delegated turn owns.
func (f *factoryEffects) CancelChildren(ctx context.Context, item FactoryItem) error {
	if f == nil || f.workers == nil {
		return fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	return f.workers.cancelChildren(ctx, item)
}

// ContinuationEvidence reads what the worker session's own transcript proves
// about a delegated continuation.
func (f *factoryEffects) ContinuationEvidence(ctx context.Context, item FactoryItem, effect FactoryEffect) (factoryContinuationEvidence, error) {
	if f == nil || f.workers == nil {
		return factoryContinuationEvidence{}, fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	return f.workers.continuationEvidence(ctx, item, effect)
}

// Continue is the worker-turn half of a continuation. Nothing is sent until the
// continuation's children are proven settled again, and the report is accepted
// only when it is a new final message that names this iteration, so the turn
// that wrote the early report can never answer for the continuation.
func (w *factoryWorkers) Continue(ctx context.Context, item FactoryItem, effect FactoryEffect) (result FactoryOutcome, err error) {
	result = FactoryOutcome{EffectID: effect.ID}
	dispatched := false
	defer func() {
		if err != nil && !errors.Is(err, ErrFactoryRefused) && !errors.Is(err, ErrFactoryUncertain) && !errors.Is(err, ErrFactoryInvalidResult) && !errors.Is(err, ErrFactoryWorkerFailed) {
			if dispatched {
				err = fmt.Errorf("%w: %v", ErrFactoryUncertain, err)
			} else {
				err = fmt.Errorf("%w: %v", ErrFactoryRefused, err)
			}
		}
	}()
	d := effect.Delegated
	if d == nil || d.ContinuationID == "" {
		return result, fmt.Errorf("factory: no continuation to run")
	}
	if effect.Worker == nil {
		return result, fmt.Errorf("factory: effect has no worker")
	}
	worker := *effect.Worker
	if worker.Role != factoryEffectRole(effect.Kind) || worker.Role == "" {
		return result, fmt.Errorf("factory: worker role mismatch")
	}
	if d.EffectID != effect.ID || d.WorkerID != worker.AgentID || d.SessionID != worker.SessionID {
		return result, fmt.Errorf("factory: continuation identity mismatch")
	}
	rt, err := w.workerRuntime(item, worker)
	if err != nil {
		return result, err
	}
	children := w.sessionChildren(ctx, rt, worker.SessionID)
	if !factoryDelegatedSettled(*d, FactoryObservation{Children: []FactoryChildSession{children}}) {
		// The children are not proven settled: nothing is sent, and the turn
		// waits for a projection that says otherwise.
		waiting := *d
		waiting.State = factoryDelegatedWaiting
		result.Delegated = &waiting
		return result, nil
	}
	prompt, err := BuildFactoryContinuationPrompt(item, effect, *d, factoryDelegatedChildList(*d, children))
	if err != nil {
		return result, err
	}
	before, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read pre-continuation stack: %w", err)
	}
	oldFinal, _, _ := factoryFinal(before)
	dispatched = true
	turn, err := rt.reg.PromptResult(ctx, worker.SessionID, prompt)
	result.StopReason = turn.StopReason
	if err != nil {
		return result, err
	}
	if turn.StopReason != "end_turn" {
		return result, fmt.Errorf("%w: worker turn ended with %q", ErrFactoryWorkerFailed, turn.StopReason)
	}
	after, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read post-continuation stack: %w", err)
	}
	// A continuation that launched children of its own is waiting again: the
	// cycle repeats with a fresh identity, and no report is accepted while
	// those children run.
	if delegated, waiting := w.delegatedTurn(ctx, rt, worker, effect, before, after); waiting {
		result.Delegated = delegated
		return result, nil
	}
	final, message, hasMessage := factoryFinal(after)
	if final == "" || final == oldFinal || !hasMessage || strings.TrimSpace(message) == "" {
		return result, fmt.Errorf("%w: no new final report in the continuation turn", ErrFactoryInvalidResult)
	}
	if got := factoryReportContinuationID(message); got != d.ContinuationID {
		return result, fmt.Errorf("%w: continuation report names %q, want %q", ErrFactoryInvalidResult, got, d.ContinuationID)
	}
	report, err := ParseFactoryReport(message, effect.ID, worker.Role, effect.ExpectedBase, effect.ExpectedHead)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrFactoryInvalidResult, err)
	}
	result.Report = &report
	result.Worker = &worker
	result.continuation = d.ContinuationID
	if report.Outcome == "needs_input" {
		result.Confirmed = true
		return result, nil
	}
	obs, err := w.Observe(ctx, item)
	if err != nil {
		return result, err
	}
	if obs.Dirty && worker.Role == "reviewer" {
		return result, fmt.Errorf("factory: reviewer checkout changed")
	}
	result.HeadSHA, result.TreeSHA, result.BaseSHA = obs.HeadSHA, obs.TreeSHA, item.BaseSHA
	if obs.HeadSHA == "" || obs.TreeSHA == "" {
		return result, fmt.Errorf("factory: checkout Git state unavailable")
	}
	if worker.Role == "reviewer" && obs.HeadSHA != effect.ExpectedHead {
		return result, fmt.Errorf("factory: reviewer checkout head changed")
	}
	result.Confirmed = true
	return result, nil
}

// observeChildren reads one delegated turn's session projection.
func (w *factoryWorkers) observeChildren(ctx context.Context, item FactoryItem, delegated FactoryDelegatedTurn) (FactoryChildSession, error) {
	worker, ok := factoryItemWorker(item, delegated.WorkerID)
	if !ok {
		return FactoryChildSession{SessionID: delegated.SessionID}, fmt.Errorf("factory: worker ownership unconfirmed")
	}
	rt, err := w.workerRuntime(item, worker)
	if err != nil {
		return FactoryChildSession{SessionID: delegated.SessionID}, err
	}
	return w.sessionChildren(ctx, rt, delegated.SessionID), nil
}

// cancelChildren asks every child the delegated turn owns to stop, through the
// existing session/subagent_cancel control. The pair (session, child) is what
// names a child, so it is sent exactly as the turn recorded it: child ids are
// session-local and a bare id would name another session's work in a second
// session. An acknowledgement is not a terminal outcome, which is why nothing
// here reports a child stopped.
func (w *factoryWorkers) cancelChildren(ctx context.Context, item FactoryItem) error {
	e, ok := factoryDelegatedEffect(item)
	if !ok || e.Delegated == nil || len(e.Delegated.ChildIDs) == 0 {
		return nil
	}
	d := *e.Delegated
	worker, ok := factoryItemWorker(item, d.WorkerID)
	if !ok {
		return fmt.Errorf("%w: child ownership unconfirmed", ErrFactoryUncertain)
	}
	rt, err := w.workerRuntime(item, worker)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFactoryUncertain, err)
	}
	for _, id := range d.ChildIDs {
		if _, err := rt.child.Request(ctx, "session/subagent_cancel", map[string]any{"sessionId": d.SessionID, "subagentId": id}); err != nil {
			return fmt.Errorf("%w: child %d cancel: %v", ErrFactoryUncertain, id, err)
		}
	}
	return nil
}

// continuationEvidence reads what the worker session's own transcript proves
// about one delegated continuation: whether a turn after the delegated parent
// turn exists at all, and the fresh final report that names this iteration.
//
// A transcript the bridge cannot read proves nothing, and nothing is ever
// inferred from its absence: the caller marks the turn uncertain instead of
// sending a second prompt.
func (w *factoryWorkers) continuationEvidence(ctx context.Context, item FactoryItem, effect FactoryEffect) (factoryContinuationEvidence, error) {
	d := effect.Delegated
	if d == nil || d.ContinuationID == "" || effect.Worker == nil {
		return factoryContinuationEvidence{}, fmt.Errorf("factory: no continuation to reconcile")
	}
	rt, err := w.workerRuntime(item, *effect.Worker)
	if err != nil {
		return factoryContinuationEvidence{}, err
	}
	raw, err := rt.reg.Stack(ctx, d.SessionID, 0)
	if err != nil {
		return factoryContinuationEvidence{}, err
	}
	var tree struct {
		Roots []string `json:"roots"`
		Nodes []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Parent  string `json:"parent"`
			Message *struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &tree) != nil {
		return factoryContinuationEvidence{}, fmt.Errorf("factory: unreadable worker transcript")
	}
	// A transcript that names no turn cannot say the dispatch is absent, and an
	// unread one says even less.
	ev := factoryContinuationEvidence{Known: len(tree.Roots) > 0}
	for _, root := range tree.Roots {
		if root != "" && root != d.ParentTurnID {
			ev.Dispatched = true
		}
	}
	for _, node := range tree.Nodes {
		if node.Kind != "final" || node.Message == nil {
			continue
		}
		if factoryReportContinuationID(node.Message.Content) != d.ContinuationID {
			continue
		}
		ev.Dispatched = true
		if node.Parent != "" && node.Parent != d.ParentTurnID {
			ev.Accepted = true
			ev.Report = node.Message.Content
			ev.Turn = node.Parent
		}
	}
	return ev, nil
}

func (w *factoryWorkers) Observe(ctx context.Context, item FactoryItem) (FactoryObservation, error) {
	obs := FactoryObservation{ObservedAt: time.Now().UTC(), BaseSHA: item.BaseSHA}
	// Every matched worker contributes to the idle conjunction below, but a
	// parked worker has no runtime, so the loop skips it and contributes
	// nothing. When every matched worker is parked there is then no runtime to
	// be busy: the turn and the active work are known-idle. Leaving the flags
	// unknown there made a fully parked item — which is what a ready item
	// always is, its implementer parked after the last publish and its reviewer
	// after the review — unable to reach a safe boundary at all.
	matched, live, unaccounted := 0, 0, 0
	for _, worker := range item.Workers {
		a, ok := w.fleet.ws.Agent(worker.AgentID)
		if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID {
			// A worker the registry cannot account for is unobserved work. The
			// item's own record may still say it is parked — that is how a row
			// survives its agent being pruned — in which case it contributes no
			// runtime. A row the journal does not already record as parked may
			// be running, so the shortcut below cannot speak for it.
			if !worker.Parked {
				unaccounted++
			}
			continue
		}
		matched++
		if !a.FactoryParked {
			live++
		}
	}
	if matched > 0 && live == 0 && unaccounted == 0 {
		obs.RuntimeKnown, obs.TurnIdle, obs.ActiveWorkKnown, obs.WorkIdle = true, true, true, true
	}
	for _, worker := range item.Workers {
		a, ok := w.fleet.ws.Agent(worker.AgentID)
		if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID {
			continue
		}
		worker.SessionID = a.SessionID
		worker.Parked = a.FactoryParked
		obs.Workers = append(obs.Workers, worker)
		if worker.Parked {
			continue
		}
		// Git evidence comes from the retained checkout, not the runtime, so it
		// stays available while a worker is interrupted or reattaching.
		if w.fleet.git != nil {
			out, err := w.fleet.git.runCtx(ctx, a.Project, Credential{}, "rev-parse", "HEAD")
			if err == nil {
				obs.HeadSHA = strings.TrimSpace(string(out))
			}
			out, err = w.fleet.git.runCtx(ctx, a.Project, Credential{}, "rev-parse", "HEAD^{tree}")
			if err == nil {
				obs.TreeSHA = strings.TrimSpace(string(out))
			}
			out, err = w.fleet.git.runCtx(ctx, a.Project, Credential{}, "status", "--porcelain")
			if err == nil {
				obs.Dirty = strings.TrimSpace(string(out)) != ""
			}
			if a.SourceKind == "git" {
				if repo, ok := w.fleet.ws.Repo(item.RepoID); ok {
					if cred, e := w.fleet.creds.Resolve(ctx, DefaultOwnerID, repo.CredRef); e == nil {
						obs.RemoteHead, _ = newFactoryGit(w.fleet).remoteHead(ctx, a.Project, item.Branch, cred)
					}
				}
			}
		}
		rt, err := w.fleet.runtimeForAgent(worker.AgentID)
		if err != nil {
			continue
		}
		obs.RuntimeKnown = true
		if info, ok := rt.reg.lookup(worker.SessionID); ok {
			obs.TurnIdle = !info.Busy && rt.reg.Pending(worker.SessionID) == ""
		}
		if raw, err := rt.child.Request(ctx, "session/end_work", map[string]any{"sessionId": worker.SessionID}); err == nil {
			obs.ActiveWorkKnown, obs.WorkIdle = decodeFactoryWork(raw)
		}
		// The children are a separate projection from the work snapshot above:
		// end_work's boolean idle says nothing about which children a turn
		// started, so every live session contributes its own child list.
		obs.Children = append(obs.Children, w.sessionChildren(ctx, rt, worker.SessionID))
	}
	return obs, nil
}

// revisionTarget resolves the live runtime that runs one item's worker, which
// is what a delivery or a pause reaches. The ownership checks mirror Observe: a
// parked, replaced or foreign worker is not this item's to steer.
// revisionTarget takes the newest live worker, and deliberately ignores an
// assessment's AffectedWorkerID: the id is a per-proposal fact, while this
// method is reached through a per-item seam that status, send and pause share,
// so honouring it in one of them alone would journal one worker's session and
// steer another. Two live workers would be needed for the order to matter, and
// a worker parks once its effect completes, so the item's effects serialize
// them; when that stops holding, the fix is to resolve the session once and
// pass it to every method rather than to prefer a worker here.
func (w *factoryWorkers) revisionTarget(item FactoryItem) (*Registry, string, error) {
	if w == nil || w.fleet == nil {
		return nil, "", fmt.Errorf("%w: factory runtime unavailable", ErrFactoryRefused)
	}
	for n := len(item.Workers) - 1; n >= 0; n-- {
		worker := item.Workers[n]
		if worker.Parked || worker.SessionID == "" {
			continue
		}
		a, ok := w.fleet.ws.Agent(worker.AgentID)
		if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.FactoryParked {
			continue
		}
		rt, err := w.fleet.runtimeForAgent(worker.AgentID)
		if err != nil || rt.reg == nil {
			continue
		}
		if w.fleet.sessionIDFor(rt) != worker.SessionID {
			continue
		}
		if _, ok := rt.reg.lookup(worker.SessionID); !ok {
			continue
		}
		return rt.reg, worker.SessionID, nil
	}
	return nil, "", fmt.Errorf("%w: no live worker runtime for item %s", ErrFactoryRefused, item.ID)
}

// RevisionSession is the session a delivery or a pause is aimed at.
func (w *factoryWorkers) RevisionSession(_ context.Context, item FactoryItem) (string, error) {
	_, session, err := w.revisionTarget(item)
	return session, err
}

// RevisionSteeringStatus reads the worker session's Send now snapshot.
func (w *factoryWorkers) RevisionSteeringStatus(ctx context.Context, item FactoryItem) (RevisionSteeringState, error) {
	reg, session, err := w.revisionTarget(item)
	if err != nil {
		return RevisionSteeringState{}, err
	}
	return reg.RevisionSteeringStatus(ctx, session)
}

// RevisionSendNow submits one Send now request to the worker session.
func (w *factoryWorkers) RevisionSendNow(ctx context.Context, item FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	reg, session, err := w.revisionTarget(item)
	if err != nil {
		return RevisionSteeringReceipt{}, err
	}
	return reg.RevisionSendNow(ctx, session, turnID, requestID, text)
}

// RevisionPause asks the worker session to stop through the one existing cancel
// path. It is not the factory's terminal stop: nothing is parked or discarded.
func (w *factoryWorkers) RevisionPause(ctx context.Context, item FactoryItem) error {
	reg, session, err := w.revisionTarget(item)
	if err != nil {
		return err
	}
	return reg.Cancel(ctx, session)
}

// RevisionReceipt reads the newest receipt this registry observed for a request.
func (w *factoryWorkers) RevisionReceipt(item FactoryItem, requestID string) (RevisionSteeringReceipt, bool) {
	reg, session, err := w.revisionTarget(item)
	if err != nil {
		return RevisionSteeringReceipt{}, false
	}
	return reg.revisionReceipt(session, requestID)
}

func (f *Fleet) ParkFactoryWorker(ctx context.Context, worker FactoryWorker) error {
	a, ok := f.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID == "" || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.SessionID == "" || a.SessionID != worker.SessionID || a.FactoryParked {
		return fmt.Errorf("factory: worker ownership or saved session unconfirmed")
	}
	rt, err := f.runtimeForAgent(worker.AgentID)
	if err != nil || rt.child == nil {
		return fmt.Errorf("factory: runtime unavailable for parking: %v", err)
	}
	if f.sessionIDFor(rt) != worker.SessionID {
		return fmt.Errorf("factory: live session mismatch")
	}
	info, ok := rt.reg.lookup(worker.SessionID)
	if !ok || info.Busy || rt.reg.Pending(worker.SessionID) != "" {
		return fmt.Errorf("factory: turn is not confirmed idle")
	}
	raw, err := rt.child.Request(ctx, "session/end_work", map[string]any{"sessionId": worker.SessionID})
	if err != nil {
		return fmt.Errorf("factory: active work unknown: %w", err)
	}
	known, idle := decodeFactoryWork(raw)
	if !known || !idle {
		return fmt.Errorf("factory: active work is unknown or non-idle")
	}
	if err := rt.child.stopConfirmed(); err != nil {
		return fmt.Errorf("factory: runtime stop unconfirmed: %w", err)
	}
	if c, ok := rt.child.Transport.(*containerTransport); ok {
		names, err := c.listAgentContainers()
		if err != nil {
			return fmt.Errorf("factory: container exit unconfirmed: %w", err)
		}
		for _, n := range names {
			if n == c.cfg.Name {
				return fmt.Errorf("factory: container still running")
			}
		}
	}
	a.FactoryParked = true
	if err := f.ws.PutAgent(a); err != nil {
		return fmt.Errorf("factory: persist parked worker: %w", err)
	}
	f.closeTerminals(worker.AgentID)
	rt.stopTimeout()
	f.mu.Lock()
	if f.runtimes[worker.AgentID] == rt {
		delete(f.runtimes, worker.AgentID)
		delete(f.sessionAgent, worker.SessionID)
	}
	f.mu.Unlock()
	f.slots.release()
	return nil
}

// stopFactoryRuntimeForRetry removes only the newly started compute after a
// saved-session load failure. The parked workspace and session record survive.
func (f *Fleet) stopFactoryRuntimeForRetry(rt *agentRuntime, a Agent) error {
	if rt == nil || rt.child == nil {
		return fmt.Errorf("factory: missing runtime to stop")
	}
	if err := rt.child.stopConfirmed(); err != nil {
		return err
	}
	if c, ok := rt.child.Transport.(*containerTransport); ok {
		names, err := c.listAgentContainers()
		if err != nil {
			return err
		}
		for _, name := range names {
			if name == c.cfg.Name {
				return fmt.Errorf("factory: resumed container still running")
			}
		}
	}
	f.closeTerminals(a.ID)
	rt.stopTimeout()
	if f.egress != nil {
		f.egress.remove(a.ID)
	}
	f.mu.Lock()
	if f.runtimes[a.ID] != rt {
		f.mu.Unlock()
		return fmt.Errorf("factory: runtime bookkeeping changed during failed resume")
	}
	delete(f.runtimes, a.ID)
	delete(f.sessionAgent, a.SessionID)
	f.mu.Unlock()
	f.slots.release()
	return nil
}
