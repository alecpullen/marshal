package factorysubagents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge"
)

// agentConfig is the worker child's whole configuration: the fixture provider
// is the only one, one local-only preset carries the route, the implementer
// runs in Act with Auto on (a factory implementer worker's approved starting
// tuple), and the side calls that would consume a scripted reply are off. No
// endpoint, credential or model outside this test is reachable through it.
func agentConfig(baseURL string) string {
	return fmt.Sprintf(`[providers.fixture]
type = "openai_compatible"
base_url = %q
tool_calling = true

[models.presets.coder]
provider = "fixture"
model = %q
context_window = 32768
temperature = 0.0
local_only = true

[profile]
default = "local"

[agent_profiles.local]
implementer = "coder"
reviewer = "coder"

[agent]
mode = "act"
auto = true
narration_prompt = false
intent_nudge = false

[titling]
enabled = false

[postmortem]
on_exit = "never"
`, baseURL+"/v1", workerModel)
}

const (
	fixtureRepoID   = "repo"
	fixtureBranch   = "main"
	fixtureCredID   = "fixture-credential"
	fixtureTokenEnv = "FACTORY_SUBAGENTS_FIXTURE_TOKEN"

	// steerInstruction is the user's direct instruction to the child. Every
	// assertion about delivery counts this exact string.
	steerInstruction = "report the empty-input case before you finish"
	steerRequestID   = "steer-1"
)

// harness is the whole end-to-end fixture: a real bridge over a real fleet,
// whose workers are real `marshal acp` children running the production agent
// runtime against the scripted provider.
type harness struct {
	t          *testing.T
	ctx        context.Context
	prov       *provider
	gits       *gitFixtures
	forge      *forgeStub
	project    string
	ws         *bridge.Workspace
	fleet      *bridge.Fleet
	store      *bridge.FactoryStore
	controller *bridge.FactoryController
	server     *httptest.Server
	itemID     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t}
	// The children inherit this process's environment, so the fixture's
	// config directory and the marker that makes this test binary the `acp`
	// program are set here.
	h.prov = newProvider(t)
	h.gits = newGitFixtures(t)
	// One project directory, registered once: it is the trusted project the
	// factory item belongs to, the checkout the unrelated agent works on, and
	// nothing else.
	h.project = h.gits.project()
	project := h.project
	h.forge = newForgeStub(t, h.gits, "", fixtureBranch)

	configDir := filepath.Join(t.TempDir(), "config")
	writeFile(t, filepath.Join(configDir, "config.toml"), agentConfig(h.prov.URL()))
	t.Setenv("MARSHAL_CONFIG_DIR", configDir)
	t.Setenv(childEnv, "1")
	// A PAT kind is what a factory preflight asks a repository for; the
	// fixture forge ignores it and the Git server is anonymous.
	t.Setenv(fixtureTokenEnv, "fixture-token")

	ws := bridge.NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	h.ws = ws
	if err := ws.AddProject(project); err != nil {
		t.Fatalf("register project: %v", err)
	}
	if err := ws.PutCredential(bridge.Credential{ID: fixtureCredID, Kind: "pat", EnvVar: fixtureTokenEnv, OwnerID: bridge.DefaultOwnerID}); err != nil {
		t.Fatalf("register credential: %v", err)
	}
	// The factory records one URL for both Git and the forge, so the fixture
	// repository is served over git's smart HTTP protocol and the API base
	// points the GitHub client at the fixture forge.
	if err := ws.PutRepo(bridge.Repo{ID: fixtureRepoID, URL: h.gits.url, Branch: fixtureBranch, Forge: "github", APIBase: h.forge.URL(), CredRef: fixtureCredID, OwnerID: bridge.DefaultOwnerID}); err != nil {
		t.Fatalf("register repo: %v", err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("find this test binary: %v", err)
	}
	h.fleet = bridge.NewFleet(ws, executable, nil, t.TempDir(), bridge.Limits{}, "factory-subagents-test", nil, "")
	h.fleet.SetHostProcessesOnly()
	t.Cleanup(h.fleet.Close)

	h.store, err = bridge.OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("open factory store: %v", err)
	}
	t.Cleanup(func() { _ = h.store.Close() })

	h.controller = bridge.NewFactoryController(h.store, bridge.NewFactoryEffects(h.fleet), time.Now)
	h.fleet.SetFactoryController(h.controller)
	h.server = httptest.NewServer(bridge.NewServer(h.fleet, "", "", bridge.NewFactoryAPI(h.store, h.controller, h.fleet)))
	t.Cleanup(h.server.Close)

	h.ctx, _ = context.WithTimeout(context.Background(), 4*time.Minute)

	// Admission is opened by the recovery pass, exactly as a bridge starting
	// against an existing journal does, and the loop is the production one.
	if err := h.controller.Recover(context.Background()); err != nil {
		t.Fatalf("recover factory: %v", err)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	t.Cleanup(func() {
		// Quiesce runs before the store closes (cleanups are LIFO): the
		// loop stops journaling and settles what it dispatched.
		stopRun()
		_ = h.controller.Quiesce()
	})
	go func() { _ = h.controller.Run(runCtx) }()
	return h
}

// item is the factory item as the journal holds it.
func (h *harness) item() bridge.FactoryItem {
	h.t.Helper()
	item, err := h.store.Get(h.itemID)
	if err != nil {
		h.t.Fatalf("read factory item: %v", err)
	}
	return item
}

// wake asks the controller to run a tick now. It is the same door a command
// knocks on; polling never waits out the loop's own CI delay.
func (h *harness) wake() { h.controller.Wake() }

// until polls cond, waking the factory on every pass, until it holds or the
// test's deadline passes. The ordering the assertions rely on is caused by the
// fixture's gates, never by a sleep. An item that stopped on a decision can
// never advance again on its own, so that ends the wait immediately with the
// journal's own words.
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	for {
		if cond() {
			return
		}
		item := h.item()
		if item.Decision != nil {
			h.t.Fatalf("waiting for %s: the factory stopped on a %q decision: %s\n%s",
				what, item.Decision.Kind, item.Decision.Reason, h.dump(item))
		}
		select {
		case <-h.ctx.Done():
			h.t.Fatalf("timed out waiting for %s\n%s", what, h.dump(item))
		default:
		}
		h.wake()
	}
}

// dump renders the item and the checkouts its workers hold, so a failing
// assertion shows what the journal and Git actually contain.
func (h *harness) dump(item bridge.FactoryItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "item %s state=%q stage=%q baseSHA=%q branch=%q workers=%d\n",
		item.ID, item.State, item.Stage, item.BaseSHA, item.Branch, len(item.Workers))
	for _, e := range item.Effects {
		outcome := ""
		if e.Outcome != nil {
			outcome = " error=" + e.Outcome.Error
		}
		fmt.Fprintf(&b, "  effect %s %s %s round=%d%s\n", e.ID, e.Kind, e.Status, e.Round, outcome)
		if e.Delegated != nil {
			fmt.Fprintf(&b, "    delegated state=%s children=%v continuation=%q worker=%s session=%s\n",
				e.Delegated.State, e.Delegated.ChildIDs, e.Delegated.ContinuationID, e.Delegated.WorkerID, e.Delegated.SessionID)
		}
	}
	for _, w := range item.Workers {
		fmt.Fprintf(&b, "  worker %s role=%s session=%s parked=%t\n", w.AgentID, w.Role, w.SessionID, w.Parked)
		agent, ok := h.agent(w.AgentID)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "    project=%s\n", agent.Project)
		fmt.Fprintf(&b, "    head=%s status=%q\n", oneLine(gitString(agent.Project, "rev-parse", "HEAD")),
			oneLine(gitString(agent.Project, "status", "--porcelain", "--untracked-files=all")))
		if stack := h.tryStack(w.SessionID, 0); stack != "" {
			fmt.Fprintf(&b, "    transcript tail: %s\n", tail(stack, 1600))
		}
	}
	if history := item.History; len(history) > 0 {
		for i := len(history) - 1; i >= 0 && i >= len(history)-8; i-- {
			fmt.Fprintf(&b, "  history %s effect=%s %s\n", history[i].Kind, history[i].EffectID, preview(history[i].Message))
		}
	}
	return b.String()
}

// gitString runs a read-only git command in dir and returns its combined
// output, never failing the test: it exists for diagnostics.
func gitString(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// oneLine flattens diagnostics onto the line they belong to.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// tail is the end of a long diagnostic value.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// effect finds one effect of the item by id.
func effect(item bridge.FactoryItem, id string) (bridge.FactoryEffect, bool) {
	for _, e := range item.Effects {
		if e.ID == id {
			return e, true
		}
	}
	return bridge.FactoryEffect{}, false
}

// historyCount counts the journal events of one kind recorded against an
// effect.
func historyCount(item bridge.FactoryItem, kind, effectID string) int {
	n := 0
	for _, event := range item.History {
		if event.Kind == kind && event.EffectID == effectID {
			n++
		}
	}
	return n
}

// createItem drafts and approves one work item through the Studio routes, so
// the approval — and the subagent snapshot it freezes — is the one a real
// client would produce.
func (h *harness) createItem(project string) bridge.FactoryItem {
	h.t.Helper()
	enabled := true
	concurrency := 1
	create := bridge.FactoryCreate{
		RequestID: "e2e-create",
		Title:     "fixture work item",
		Plan:      "the approved plan\n",
		Spec:      "the approved spec\n",
		Config: bridge.FactoryConfig{
			RepoID:       fixtureRepoID,
			ProjectRoot:  project,
			TargetBranch: fixtureBranch,
			Native:       true,
			// Both roles run the profile the fixture config defines, so the
			// workers' route is the fixture provider and nothing else.
			ImplementerRouting: json.RawMessage(`{"profile":"local"}`),
			ReviewerRouting:    json.RawMessage(`{"profile":"local"}`),
			// The implementer worker is started in Auto: the factory's own
			// starting tuple for it, so a workspace-write tool call is not
			// left waiting for a client that does not exist.
			ImplementerAuto: true,
			// No required checks are selected and none are claimed, so a
			// round's CI evidence is whatever the fixture forge reports.
			NoRequiredCIAcknowledged: true,
			ImplementerSubagents:     &bridge.FactorySubagentOverrides{Enabled: &enabled, Concurrency: &concurrency},
			ReviewerSubagents:        &bridge.FactorySubagentOverrides{Enabled: &enabled, Concurrency: &concurrency},
		},
	}
	item := postJSON[bridge.FactoryItem](h.t, h.server.URL+"/api/factory/items", create, "item")
	h.itemID = item.ID
	h.forge.setBranch(item.Branch)
	return h.approve(item)
}

// approve approves the draft through the same route a Studio surface uses, so
// the approval revision carries the resolved subagent snapshot the freeze
// computed from the runtime's own report.
func (h *harness) approve(item bridge.FactoryItem) bridge.FactoryItem {
	h.t.Helper()
	cmd := bridge.FactoryCommand{
		RequestID:       "e2e-approve",
		Kind:            "approve",
		ExpectedVersion: item.Version,
	}
	approved := postJSON[bridge.FactoryItem](h.t, h.server.URL+"/api/factory/items/"+item.ID+"/commands", cmd, "item")
	if approved.State != "queued" {
		h.t.Fatalf("approved item is in state %q, want queued", approved.State)
	}
	if len(approved.Approvals) != 1 {
		h.t.Fatalf("approved item has %d approvals, want 1", len(approved.Approvals))
	}
	approval := approved.Approvals[0]
	if got := approval.ImplementerSubagents; !got.Enabled || got.Concurrency != 1 {
		h.t.Fatalf("approval froze implementer subagents %+v, want enabled with a cap of 1", got)
	}
	if got := approval.ReviewerSubagents; !got.Enabled || got.Concurrency != 1 {
		h.t.Fatalf("approval froze reviewer subagents %+v, want enabled with a cap of 1", got)
	}
	return approved
}

// agent is one saved agent record.
func (h *harness) agent(id string) (bridge.Agent, bool) { return h.ws.Agent(id) }

// stack reads a session's transcript tree; a non-zero subagentID selects that
// child's own transcript.
func (h *harness) stack(sessionID string, subagentID int64) string {
	h.t.Helper()
	reg, err := h.fleet.RegistryForSession(sessionID)
	if err != nil {
		h.t.Fatalf("registry for session %q: %v", sessionID, err)
	}
	raw, err := reg.Stack(h.ctx, sessionID, subagentID)
	if err != nil {
		h.t.Fatalf("stack of session %q (child %d): %v", sessionID, subagentID, err)
	}
	return string(raw)
}

// tryStack is stack for diagnostics: a session the fleet has already closed
// yields its reason instead of failing the test.
func (h *harness) tryStack(sessionID string, subagentID int64) string {
	reg, err := h.fleet.RegistryForSession(sessionID)
	if err != nil {
		return "(session unavailable: " + err.Error() + ")"
	}
	raw, err := reg.Stack(h.ctx, sessionID, subagentID)
	if err != nil {
		return "(stack unavailable: " + err.Error() + ")"
	}
	return string(raw)
}

// factoryEffectOfKind finds the newest effect of one kind.
func factoryEffectOfKind(item bridge.FactoryItem, kind string) (bridge.FactoryEffect, bool) {
	for i := len(item.Effects) - 1; i >= 0; i-- {
		if item.Effects[i].Kind == kind {
			return item.Effects[i], true
		}
	}
	return bridge.FactoryEffect{}, false
}

// factoryWorkerOfRole finds the newest worker of one role.
func factoryWorkerOfRole(item bridge.FactoryItem, role string) (bridge.FactoryWorker, bool) {
	for i := len(item.Workers) - 1; i >= 0; i-- {
		if item.Workers[i].Role == role {
			return item.Workers[i], true
		}
	}
	return bridge.FactoryWorker{}, false
}

// dumpRequests renders the fixture's request sequence, so a failing script
// assertion shows what the runtime actually asked for.
func (h *harness) dumpRequests() string {
	var b strings.Builder
	for i, r := range h.prov.Requests() {
		system := r.Req.systemText()
		last := ""
		if n := len(r.Req.Messages); n > 0 {
			m := r.Req.Messages[n-1]
			last = m.Role + ":" + preview(m.text())
		}
		names := make([]string, 0, len(r.Req.Tools))
		for _, tool := range r.Req.Tools {
			names = append(names, tool.Function.Name)
		}
		fmt.Fprintf(&b, "  request %d kind=%s model=%s messages=%d toolList=%t last=%s\n    tools: %s\n",
			i, r.Kind, r.Req.Model, len(r.Req.Messages),
			strings.Contains(system, "Available tools:"), last, strings.Join(names, ", "))
	}
	if refusals := h.prov.Refusals(); len(refusals) > 0 {
		fmt.Fprintf(&b, "  refusals: %v\n", refusals)
	}
	return b.String()
}

// postJSON sends one body and decodes the named field of the reply.
func postJSON[T any](t *testing.T, url string, body any, field string) T {
	t.Helper()
	var out T
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request for %s: %v", url, err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("post %s: %s: %s", url, resp.Status, raw)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode reply from %s: %v: %s", url, err, raw)
	}
	if err := json.Unmarshal(envelope[field], &out); err != nil {
		t.Fatalf("decode %q of %s: %v: %s", field, url, err, raw)
	}
	return out
}

// TestFactorySubagentRealSession is the end-to-end proof: an approved factory
// item runs a real implementer worker and a real reviewer worker, each a real
// Marshal ACP session with the production agent runtime, against a
// deterministic offline provider and a disposable Git repository.
//
// It proves that a delegated child is observed and bounded by the approved
// snapshot, that a direct user steer reaches that child exactly once and
// notifies the parent, that the turn waits while the child runs and the item
// does not advance on its early report, that exactly one continuation produces
// a fresh report bound to it, that the reviewer runs independently, and that no
// unrelated worker's session is cancelled.
func TestFactorySubagentRealSession(t *testing.T) {
	if testing.Short() {
		t.Skip("real Marshal ACP children, real Git and a scripted provider")
	}
	h := newHarness(t)
	// A failing assertion must not leave the child's scripted step blocked:
	// the gate is closed on the way out, so cleanup never waits on it.
	t.Cleanup(h.prov.ReleaseChild)
	project := h.project

	// An unrelated agent on the same project: no factory item owns it, and
	// nothing in the factory's own stop path may reach it.
	unrelated, err := h.fleet.Spawn(h.ctx, project, bridge.SpawnOptions{Name: "unrelated worker", Mode: "act"})
	if err != nil {
		t.Fatalf("spawn unrelated agent: %v", err)
	}
	h.until("the unrelated agent's session", func() bool {
		a, ok := h.agent(unrelated)
		return ok && a.SessionID != ""
	})
	unrelatedAgent, _ := h.agent(unrelated)

	item := h.createItem(project)

	// --- 1. The implementer turn invokes agent.run and its child is observed.
	var waiting bridge.FactoryItem
	h.until("the implementer turn to wait for its child", func() bool {
		item = h.item()
		for _, e := range item.Effects {
			if e.Kind == "implement" && e.Delegated != nil && e.Delegated.State == "waiting" {
				waiting = item
				return true
			}
		}
		return false
	})
	implementEffect, ok := factoryEffectOfKind(waiting, "implement")
	if !ok {
		t.Fatal("the waiting item has no implement effect")
	}
	delegated := implementEffect.Delegated
	worker := *implementEffect.Worker
	if delegated.WorkerID != worker.AgentID || delegated.SessionID != worker.SessionID {
		t.Fatalf("delegated turn names worker %q/%q, want %q/%q",
			delegated.WorkerID, delegated.SessionID, worker.AgentID, worker.SessionID)
	}
	if len(delegated.ChildIDs) != 1 {
		t.Fatalf("delegated turn observed %d children, want exactly 1 (the approved cap)", len(delegated.ChildIDs))
	}
	childID := delegated.ChildIDs[0]
	if delegated.ParentTurnID == "" {
		t.Fatal("the delegated turn names no parent turn")
	}
	if delegated.ContinuationID != "" {
		t.Fatalf("a waiting turn already carries continuation %q", delegated.ContinuationID)
	}

	// The child is a real agent: it answered its own model request under the
	// delegated-subtask prompt, and the runtime never offered it agent.run —
	// a child cannot delegate further, so it cannot exceed the worker's
	// authority.
	childRequests := h.prov.requestsOfKind(kindChild)
	if len(childRequests) == 0 {
		t.Fatal("the child never ran a turn of its own")
	}
	childSystem := childRequests[0].Req.systemText()
	if !strings.Contains(childSystem, "ad-hoc subtask delegated") {
		t.Fatal("the child's request does not carry the delegated-subtask prompt")
	}
	if childRequests[0].Req.toolNames()["agent_run"] {
		t.Fatal("the delegated child was offered agent.run: a child must not exceed the worker's authority")
	}
	// The approved snapshot — not the runtime default — is what the worker's
	// own session states: delegation is on, and the cap is the approved one.
	implementReq := h.prov.requestsOfKind(kindImplement)[0].Req
	if !implementReq.toolNames()["agent_run"] {
		t.Fatalf("the worker was not offered agent.run, so no delegation could have happened:\n%s", h.dumpRequests())
	}
	if got := implementReq.toolDescription("agent_run"); !strings.Contains(got, "Up to 1 run at once") {
		t.Fatalf("the worker's agent.run carries no approved cap of one child: %q\n%s",
			preview(got), h.dumpRequests())
	}
	// The cap is enforced, not merely advertised: the second delegation in
	// the turn was refused at admission by the approved bound.
	workerStack := h.stack(worker.SessionID, 0)
	if !strings.Contains(workerStack, "concurrency limit") {
		t.Fatalf("the second delegation was not refused by the approved cap:\n%s", workerStack)
	}

	// --- 3. The item does not advance while the child runs.
	if waiting.State != "implementing" {
		t.Fatalf("item state while a child runs = %q, want implementing", waiting.State)
	}
	if implementEffect.Status != "dispatched" {
		t.Fatalf("the delegated effect is %q, want dispatched", implementEffect.Status)
	}
	if implementEffect.Outcome != nil && implementEffect.Outcome.Report != nil {
		t.Fatalf("the early report was accepted as the effect's result: %+v", implementEffect.Outcome.Report)
	}
	if len(waiting.Rounds) != 1 || waiting.Rounds[0].Implementation != nil {
		t.Fatal("the round carries an implementation report before the turn is finished")
	}

	// --- 2. A direct steer reaches the child once and notifies the parent.
	steerURL := fmt.Sprintf("%s/api/sessions/%s/subagents/%d/steer", h.server.URL, worker.SessionID, childID)
	first := steer(t, steerURL, steerRequestID, steerInstruction)
	if first.Status != "accepted" {
		t.Fatalf("steer status = %q, want accepted (queue acceptance, not consumption)", first.Status)
	}
	if first.RequestID != steerRequestID {
		t.Fatalf("steer answered for request %q, want %q", first.RequestID, steerRequestID)
	}
	// Acceptance is not consumption: the child has not read the instruction
	// yet, so no request it sent carries it.
	if got := h.prov.occurrences(steerInstruction); got != 0 {
		t.Fatalf("the child had already consumed the instruction %d time(s) at acceptance", got)
	}
	// A replay of the same request ID is answered from the receipt and
	// delivers nothing a second time.
	replay := steer(t, steerURL, steerRequestID, steerInstruction)
	if replay.Status != first.Status {
		t.Fatalf("replayed steer status = %q, want %q", replay.Status, first.Status)
	}
	// The parent's transcript carries the notice naming the child and the
	// instruction, exactly once after the replay.
	if got := strings.Count(h.stack(worker.SessionID, 0), steerInstruction); got != 1 {
		t.Fatalf("the parent transcript carries the instruction %d time(s), want exactly 1:\n%s",
			got, h.stack(worker.SessionID, 0))
	}

	// The child reads it on its next step: exactly one of its requests
	// carries the instruction, and only one.
	h.prov.ReleaseChild()
	h.until("the child to read the steering instruction", func() bool {
		return h.prov.occurrences(steerInstruction) > 0
	})
	if got := h.prov.occurrences(steerInstruction); got != 1 {
		t.Fatalf("the child was told the instruction %d times, want exactly 1", got)
	}

	// --- 4. Exactly one continuation, bound to a fresh report.
	h.until("the continuation to be dispatched", func() bool {
		item = h.item()
		e, ok := effect(item, implementEffect.ID)
		return ok && e.Delegated != nil && e.Delegated.State == "dispatched"
	})
	dispatched, _ := effect(item, implementEffect.ID)
	continuationID := dispatched.Delegated.ContinuationID
	if continuationID == "" {
		t.Fatal("the dispatched continuation carries no continuation ID")
	}
	if n := historyCount(item, "continuation_prepared", implementEffect.ID); n != 1 {
		t.Fatalf("journal records %d prepared continuations, want exactly 1", n)
	}
	if n := historyCount(item, "continuation_dispatched", implementEffect.ID); n != 1 {
		t.Fatalf("journal records %d dispatched continuations, want exactly 1", n)
	}
	// The journal is written before the prompt is sent, so the turn itself is
	// what proves the continuation ran: wait for the provider to serve it. A
	// turn may take more than one model request (a step that answers without
	// tool calls is asked to verify), so the journal — one prepared and one
	// dispatched continuation — is what proves there is only one, and every
	// request of that turn must name the same iteration.
	h.until("the continuation turn to run", func() bool {
		return len(h.prov.requestsOfKind(kindContinuation)) > 0
	})
	continuations := h.prov.requestsOfKind(kindContinuation)
	for _, turn := range continuations {
		prompt := turn.Req.factoryPrompt()
		if match := continuationIDPattern.FindStringSubmatch(prompt); match == nil || match[1] != continuationID {
			t.Fatalf("a continuation turn does not name %q:\n%s", continuationID, prompt)
		}
	}

	// The fresh report — not the early message — is the round's result.
	h.until("the implement effect to complete", func() bool {
		item = h.item()
		e, ok := effect(item, implementEffect.ID)
		return ok && e.Status == "completed"
	})
	item = h.item()
	completed, _ := effect(item, implementEffect.ID)
	if completed.Outcome == nil || completed.Outcome.Report == nil {
		t.Fatalf("the completed implement effect carries no report: %+v", completed.Outcome)
	}
	report := completed.Outcome.Report
	if report.OperationID != implementEffect.ID {
		t.Fatalf("the accepted report names operation %q, want %q", report.OperationID, implementEffect.ID)
	}
	if !strings.Contains(report.Summary, "delegated analysis incorporated") {
		t.Fatalf("the accepted report is not the continuation's fresh one: %+v", report)
	}
	if strings.Contains(report.Summary, "still running") || strings.Contains(report.Summary, "Delegated the audit") {
		t.Fatalf("the early report was accepted as the continuation's result: %+v", report)
	}
	if item.Rounds[0].Implementation == nil || item.Rounds[0].Implementation.Summary != report.Summary {
		t.Fatalf("round evidence does not carry the continuation's report: %+v", item.Rounds[0].Implementation)
	}

	// --- 5. The reviewer runs independently.
	h.until("the reviewer worker to spawn", func() bool {
		item = h.item()
		for _, w := range item.Workers {
			if w.Role == "reviewer" {
				return true
			}
		}
		return false
	})
	item = h.item()
	reviewer, ok := factoryWorkerOfRole(item, "reviewer")
	if !ok {
		t.Fatal("no reviewer worker was recorded")
	}
	if reviewer.AgentID == worker.AgentID || reviewer.SessionID == worker.SessionID {
		t.Fatalf("the reviewer shares the implementer's agent/session (%q/%q)", reviewer.AgentID, reviewer.SessionID)
	}
	h.until("the independent review to finish", func() bool {
		item = h.item()
		return len(item.Rounds) > 0 && item.Rounds[0].Review != nil
	})
	item = h.item()
	review := item.Rounds[0].Review
	if review == nil || review.Outcome != "complete" || !review.InputsAccessible || review.Findings == nil || len(review.Findings) != 0 {
		t.Fatalf("the review evidence is not a clean independent review: %+v", review)
	}
	// The reviewer is a top-level worker, not a child of the implementer: its
	// own model requests carry the reviewer prompt and the full tool list,
	// and its session is its own. That is what makes the review independent.
	reviews := h.prov.requestsOfKind(kindReview)
	if len(reviews) == 0 {
		t.Fatal("the reviewer never ran a model turn")
	}
	if strings.Contains(reviews[0].Req.systemText(), "ad-hoc subtask delegated") {
		t.Fatal("the reviewer ran as a delegated child, not as an independent worker")
	}
	if !reviews[0].Req.toolNames()["agent_run"] {
		t.Fatal("the reviewer's request is not a top-level worker's")
	}
	if got := len(item.Effects); got == 0 {
		t.Fatal("the item carries no effects")
	}
	// The reviewer is the factory's own worker: it was spawned by the
	// factory's reviewer spawn effect and the review effect runs against it.
	// A child the implementer's turn created would instead appear in that
	// turn's child list, which still holds exactly the one child the approved
	// cap allowed.
	var reviewerSpawn *bridge.FactoryEffect
	for i := len(item.Effects) - 1; i >= 0; i-- {
		if item.Effects[i].Kind == "spawn" && item.Effects[i].WorkerRole == "reviewer" {
			reviewerSpawn = &item.Effects[i]
			break
		}
	}
	if reviewerSpawn == nil {
		t.Fatal("the factory recorded no reviewer spawn effect")
	}
	if reviewer.SpawnEffectID != reviewerSpawn.ID {
		t.Fatalf("the reviewer was not spawned by the factory's reviewer spawn effect (%q != %q)",
			reviewer.SpawnEffectID, reviewerSpawn.ID)
	}
	reviewEffect, ok := factoryEffectOfKind(item, "review")
	if !ok {
		t.Fatal("the item carries no review effect")
	}
	if reviewEffect.Worker == nil || reviewEffect.Worker.AgentID != reviewer.AgentID {
		t.Fatalf("the review effect does not run against the reviewer worker: %+v", reviewEffect.Worker)
	}
	if implementerAgent, ok := h.agent(worker.AgentID); !ok || implementerAgent.FactoryRole != "implementer" {
		t.Fatal("the implementer worker is no longer registered as this item's implementer")
	}
	delegatedNow, ok := effect(item, implementEffect.ID)
	if !ok || delegatedNow.Delegated == nil || len(delegatedNow.Delegated.ChildIDs) != 1 {
		t.Fatalf("the implementer's delegated turn no longer names exactly one child: %+v", delegatedNow.Delegated)
	}
	if delegatedNow.Delegated.ChildIDs[0] != childID {
		t.Fatalf("the delegated child changed from %d to %d", childID, delegatedNow.Delegated.ChildIDs[0])
	}

	// --- 6. A stop reaches the item's own work and nothing else.
	item = h.item()
	stopping := postJSON[bridge.FactoryItem](t, h.server.URL+"/api/factory/items/"+item.ID+"/commands", bridge.FactoryCommand{
		RequestID:       "e2e-stop",
		Kind:            "stop",
		ExpectedVersion: item.Version,
		Reason:          "the flow is proven; the fixture ends here",
	}, "item")
	if stopping.State != "stopping" && stopping.State != "stopped" {
		t.Fatalf("the stop left the item in state %q", stopping.State)
	}
	// The stop is durable before anything is cancelled: the journal says the
	// item owns its own workers, and no other session is named.
	h.until("the stop to be journaled", func() bool {
		state := h.item().State
		return state == "stopping" || state == "stopped"
	})
	stoppedItem := h.item()
	if len(stoppedItem.Workers) != 2 {
		t.Fatalf("the stopped item names %d workers, want its implementer and reviewer", len(stoppedItem.Workers))
	}
	for _, w := range stoppedItem.Workers {
		if w.AgentID == unrelated {
			t.Fatal("the factory's stop names a session it does not own")
		}
	}
	// The unrelated agent's session is untouched: it is still registered, its
	// session is unchanged, and its runtime still resolves — it was never a
	// stop target, so nothing cancelled or restarted it.
	still, ok := h.agent(unrelated)
	if !ok {
		t.Fatal("an unrelated agent was removed by the factory's stop")
	}
	if still.SessionID != unrelatedAgent.SessionID {
		t.Fatalf("the unrelated agent's session changed from %q to %q", unrelatedAgent.SessionID, still.SessionID)
	}
	if _, err := h.fleet.RegistryForSession(still.SessionID); err != nil {
		t.Fatalf("the unrelated agent's session no longer resolves after the factory's stop: %v", err)
	}
	// Nothing was ever prompted on that session: the fixture provider served
	// only the factory's own turns.
	if h.prov.refusalsCount() != 0 {
		t.Fatalf("the fixture provider refused a request: %v", h.prov.Refusals())
	}
	if served := h.prov.Served(); served[kindImplement] != 1 || served[kindContinuation] < 1 || served[kindReview] != 1 {
		t.Fatalf("provider served %v, want one implement turn, the continuation and one review", served)
	}
}

// steer sends one session/subagent_steer through the bridge's route and decodes
// its acceptance.
func steer(t *testing.T, url, requestID, message string) struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
} {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"requestId": requestID, "message": message})
	if err != nil {
		t.Fatalf("marshal steer: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("steer %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("steer %s: %s: %s", url, resp.Status, raw)
	}
	var out struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode steer reply: %v: %s", err, raw)
	}
	return out
}
