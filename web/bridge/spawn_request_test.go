package bridge

// These tests pin the exactly-once first send (agent-first Studio, task 3): one
// caller-generated request ID resolves to one agent, whatever the retry looked
// like — a second press, an answer that never arrived, a restart between the
// claim and the spawn — and the same ID with different content is refused
// rather than silently starting a second agent.

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// spawnRequestEnv is a bridge over a registered project whose agents are fakes,
// so a test can see which prompts an agent actually received.
func spawnRequestEnv(t *testing.T) (*Fleet, *Server, string, func(string) *fakeAgent) {
	t.Helper()
	f, _, agentFor := agentFleet(t)
	root := t.TempDir()
	if err := f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	return f, NewServer(f), root, agentFor
}

// postSpawn is one POST /api/agents, decoded.
func postSpawn(t *testing.T, s *Server, body map[string]any) (int, string) {
	t.Helper()
	rec := doReq(t, s, http.MethodPost, "/api/agents", body, nil)
	var out struct {
		AgentID string `json:"agentId"`
	}
	if rec.Code == http.StatusCreated {
		decodeBody(t, rec, &out)
	}
	return rec.Code, out.AgentID
}

// agentsOf returns the project's agents, so a test can say how many a request
// started.
func agentsOf(f *Fleet, root string) []Agent {
	var out []Agent
	for _, a := range f.ws.Agents() {
		if a.Project == root {
			out = append(out, a)
		}
	}
	return out
}

// waitForPrompt waits for the first prompt to reach the agent, which the HTTP
// handler dispatches on its own goroutine after answering.
func waitForPrompt(t *testing.T, a *fakeAgent, want int) []string {
	t.Helper()
	if a == nil {
		t.Fatal("the spawn left no fake agent to inspect")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		calls := a.calls("session/prompt")
		if len(calls) >= want || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTwoSubmissionsOfOneRequestStartOneAgent: a second press while the first
// request is unresolved carries the same ID, so it binds the agent that
// request started and never sends the prompt again.
func TestTwoSubmissionsOfOneRequestStartOneAgent(t *testing.T) {
	f, s, root, agentFor := spawnRequestEnv(t)
	body := map[string]any{"project": root, "requestId": "req-double", "prompt": "Fix the login redirect"}

	code, first := postSpawn(t, s, body)
	if code != http.StatusCreated || first == "" {
		t.Fatalf("first send = %d %q", code, first)
	}
	code, second := postSpawn(t, s, body)
	if code != http.StatusCreated {
		t.Fatalf("second send = %d", code)
	}
	if second != first {
		t.Fatalf("second send started %q, want the first request's %q", second, first)
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1: %+v", len(got), got)
	}
	if calls := waitForPrompt(t, agentFor(first), 1); len(calls) != 1 {
		t.Fatalf("session/prompt = %v, want exactly the first send's", calls)
	}
}

// TestOneRequestDeliversItsPromptOnceAcrossALostAnswer: the answer never
// reaches the caller (the request still ran), and the retry resolves the agent
// without repeating the first turn — the failure case the plan names.
func TestOneRequestDeliversItsPromptOnceAcrossALostAnswer(t *testing.T) {
	f, s, root, agentFor := spawnRequestEnv(t)
	body := map[string]any{"project": root, "requestId": "req-lost", "prompt": "Find the flaky test"}

	code, first := postSpawn(t, s, body)
	if code != http.StatusCreated || first == "" {
		t.Fatalf("first send = %d %q", code, first)
	}
	// The caller saw nothing: it retries the same content, which is what the
	// Studio does when its answer is lost.
	if _, retry := postSpawn(t, s, body); retry != first {
		t.Fatalf("retry resolved %q, want %q", retry, first)
	}
	if calls := waitForPrompt(t, agentFor(first), 1); len(calls) != 1 {
		t.Fatalf("session/prompt = %v, want the message delivered once", calls)
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
}

// TestTheSameIDWithDifferentContentIsRefused: an edited draft under the same ID
// is a conflict, not a retry, and starts nothing.
func TestTheSameIDWithDifferentContentIsRefused(t *testing.T) {
	f, s, root, _ := spawnRequestEnv(t)
	if code, _ := postSpawn(t, s, map[string]any{"project": root, "requestId": "req-edit", "prompt": "Fix the login redirect"}); code != http.StatusCreated {
		t.Fatalf("first send = %d", code)
	}

	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]any{
		"project": root, "requestId": "req-edit", "prompt": "Fix the signup redirect",
	}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("same id, different content = %d, want 409: %s", rec.Code, rec.Body)
	}
	var refusal struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeBody(t, rec, &refusal)
	if refusal.Code != "spawn_request_conflict" {
		t.Fatalf("code = %q, want spawn_request_conflict so a client can tell a definite rejection", refusal.Code)
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1: the refusal started nothing", len(got))
	}
}

// TestADefiniteRejectionLetsTheDraftBeRetried: a refusal the bridge answers
// exactly — here a workspace it cannot build — starts nothing and releases its
// claim, so the edited draft is not refused as a conflict with a request that
// never happened.
func TestADefiniteRejectionLetsTheDraftBeRetried(t *testing.T) {
	f, s, root, _ := spawnRequestEnv(t)
	// A workspace that does not exist is refused after the claim, by the spawn.
	code, _ := postSpawn(t, s, map[string]any{
		"project": root, "requestId": "req-retry", "prompt": "Anything", "workspace": "nope@1",
	})
	if code < 400 || code >= 500 {
		t.Fatalf("refused send = %d, want a definite 4xx", code)
	}
	// The request ID is free again, so an edited draft may use it.
	code, retried := postSpawn(t, s, map[string]any{"project": root, "requestId": "req-retry", "prompt": "Something else entirely"})
	if code != http.StatusCreated || retried == "" {
		t.Fatalf("edited retry = %d %q, want a fresh agent", code, retried)
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
}

// TestALegacySpawnStillStartsEveryRequest: a caller that names no request ID —
// every caller before task 3 — is not idempotent, and each request spawns.
func TestALegacySpawnStillStartsEveryRequest(t *testing.T) {
	f, s, root, _ := spawnRequestEnv(t)
	firstCode, first := postSpawn(t, s, map[string]any{"project": root})
	secondCode, second := postSpawn(t, s, map[string]any{"project": root})
	if firstCode != http.StatusCreated || secondCode != http.StatusCreated {
		t.Fatalf("legacy sends = %d, %d", firstCode, secondCode)
	}
	if first == second {
		t.Fatalf("both requests resolved to %q; a request with no ID is not deduplicated", first)
	}
	if got := agentsOf(f, root); len(got) != 2 {
		t.Fatalf("agents = %d, want 2", len(got))
	}
}

// TestConcurrentFirstSendsSerializeTheirClaim pins the serialization between
// one request's claim and the spawn it guards: a racing caller of the same
// request has claimed nothing durably while the first caller still holds the
// claim's lock, so the claimed identity can never be spawned twice, however
// close the two calls are.
func TestConcurrentFirstSendsSerializeTheirClaim(t *testing.T) {
	f, _, _ := agentFleet(t)
	root := t.TempDir()
	if err := f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	scope := spawnRequestScope(OriginUI, root)
	release := f.lockSpawnClaim(scope, "req-serialize")
	started := make(chan struct{})
	done := make(chan struct{})
	var spawnErr error
	go func() {
		close(started)
		_, _, spawnErr = f.SpawnClaimed(context.Background(), root, SpawnOptions{Prompt: "one attempt"}, "req-serialize")
		close(done)
	}()
	<-started
	// The racing caller has minted nothing durably: the claim is written inside
	// the same serialized step as the spawn it guards, so it cannot exist while
	// the first caller holds the lock.
	if _, held := f.ws.SpawnClaim(scope, "req-serialize"); held {
		t.Fatal("a racing caller claimed the request while another held the claim's lock")
	}
	release()
	<-done
	if spawnErr != nil {
		t.Fatalf("the racing caller's resolution failed: %v", spawnErr)
	}
	// And once it ran, the claim and its one agent both exist: the wait let the
	// second caller resolve the agent the first started instead of starting
	// another.
	claim, held := f.ws.SpawnClaim(scope, "req-serialize")
	if !held || claim.AgentID == "" {
		t.Fatalf("the racing caller left no claim: %+v", claim)
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
}

// TestConcurrentIdenticalRequestsYieldOneAgent: submissions that arrive
// together are one request, so they resolve to one agent.
func TestConcurrentIdenticalRequestsYieldOneAgent(t *testing.T) {
	f, s, root, _ := spawnRequestEnv(t)
	const callers = 4
	var wg sync.WaitGroup
	ids := make([]string, callers)
	codes := make([]int, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]any{
				"project": root, "requestId": "req-race", "prompt": "Race the fixtures",
			}, nil)
			var out struct {
				AgentID string `json:"agentId"`
			}
			if rec.Code == http.StatusCreated {
				decodeBody(t, rec, &out)
			}
			ids[i], codes[i] = out.AgentID, rec.Code
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if codes[i] != http.StatusCreated || ids[i] != ids[0] {
			t.Fatalf("caller %d = %d %q, want 201 %q", i, codes[i], ids[i], ids[0])
		}
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
}

// TestTheClaimSurvivesARestart: the claim is on disk before the spawn, so a
// daemon that restarts between the claim and the answer resolves the same
// request to the same agent instead of starting a second one.
func TestTheClaimSurvivesARestart(t *testing.T) {
	f, s, root, _ := spawnRequestEnv(t)
	body := map[string]any{"project": root, "requestId": "req-restart", "prompt": "Survive a restart"}
	code, first := postSpawn(t, s, body)
	if code != http.StatusCreated || first == "" {
		t.Fatalf("first send = %d %q", code, first)
	}

	// A restart rereads the workspace file the bridge kept its claims in.
	reloaded := NewWorkspace(f.ws.path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	scope := spawnRequestScope(OriginUI, root)
	claim, ok := reloaded.SpawnClaim(scope, "req-restart")
	if !ok {
		t.Fatal("the claim did not survive the reload; a retry would start a second agent")
	}
	if claim.AgentID != first {
		t.Fatalf("claim names %q, want %q", claim.AgentID, first)
	}
	if !claim.Prompted {
		t.Fatal("the claim does not record the delivered prompt; a replay would send it twice")
	}
	// The agent record carries the request too, so a claim can be reconciled
	// from either side.
	a, ok := reloaded.Agent(first)
	if !ok {
		t.Fatal("the agent did not survive the reload")
	}
	if a.SpawnRequestID != "req-restart" {
		t.Fatalf("agent request id = %q", a.SpawnRequestID)
	}
}

// TestAResumedSpawnReusesTheClaimedIdentity: a claim whose spawn never recorded
// an agent resumes under the identity the claim fixed, so a daemon that died
// mid-spawn still yields one agent.
func TestAResumedSpawnReusesTheClaimedIdentity(t *testing.T) {
	f, s, root, _ := spawnRequestEnv(t)
	scope := spawnRequestScope(OriginUI, root)
	// A claim left behind by a daemon that died between the claim and the
	// spawn: it names an agent that does not exist.
	const requestID = "req-resume"
	const claimedID = "abc123abc123abcd"
	if _, _, err := f.ws.ClaimSpawnRequest(scope, requestID, spawnRequestDigest(root, SpawnOptions{Prompt: "Resume this"}), claimedID); err != nil {
		t.Fatal(err)
	}

	code, resumed := postSpawn(t, s, map[string]any{"project": root, "requestId": requestID, "prompt": "Resume this"})
	if code != http.StatusCreated {
		t.Fatalf("resumed send = %d", code)
	}
	if resumed != claimedID {
		t.Fatalf("resumed send started %q, want the claimed %q", resumed, claimedID)
	}
	if got := agentsOf(f, root); len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
}

// TestSpawnRequestsAreScopedToTheirProject: the same request ID in two projects
// is two requests, because a request ID is only unique within its caller and
// project.
func TestSpawnRequestsAreScopedToTheirProject(t *testing.T) {
	f, s, first, _ := spawnRequestEnv(t)
	second := t.TempDir()
	if err := f.ws.AddProject(second); err != nil {
		t.Fatal(err)
	}
	codeA, a := postSpawn(t, s, map[string]any{"project": first, "requestId": "req-scope", "prompt": "Fix one"})
	codeB, b := postSpawn(t, s, map[string]any{"project": second, "requestId": "req-scope", "prompt": "Fix one"})
	if codeA != http.StatusCreated || codeB != http.StatusCreated {
		t.Fatalf("spawns = %d, %d", codeA, codeB)
	}
	if a == "" || b == "" {
		t.Fatal("a scoped spawn started nothing")
	}
	if a == b {
		t.Fatal("the same request id in two projects resolved to one agent")
	}
}

// TestAnAgentStartedWithNoPromptIsNotPromptedLater: an agent spawned with
// nothing to do carries no first turn, so a replay has nothing to deliver.
func TestAnAgentStartedWithNoPromptIsNotPromptedLater(t *testing.T) {
	_, s, root, agentFor := spawnRequestEnv(t)
	body := map[string]any{"project": root, "requestId": "req-blank"}
	code, first := postSpawn(t, s, body)
	if code != http.StatusCreated || first == "" {
		t.Fatalf("blank spawn = %d %q", code, first)
	}
	if _, retry := postSpawn(t, s, body); retry != first {
		t.Fatalf("retry = %q, want %q", retry, first)
	}
	if calls := agentFor(first).calls("session/prompt"); len(calls) != 0 {
		t.Fatalf("session/prompt = %v, want none for a request that asked nothing", calls)
	}
}

// TestAnOversizedRequestIDIsRefused: the key is stored and compared, so one
// request cannot grow the store without bound.
func TestAnOversizedRequestIDIsRefused(t *testing.T) {
	_, s, root, _ := spawnRequestEnv(t)
	big := make([]byte, 129)
	for i := range big {
		big[i] = 'a'
	}
	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]any{
		"project": root, "requestId": string(big),
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized request id = %d, want 400", rec.Code)
	}
}

// TestSpawnClaimedResumesAfterTheAgentRecordIsLost exercises the fleet path
// directly: a claim whose agent record is gone (a daemon that died before
// PutAgent) is resumed under the claim's identity, with the first prompt still
// to deliver.
func TestSpawnClaimedResumesAfterTheAgentRecordIsLost(t *testing.T) {
	f, _, agentFor := agentFleet(t)
	root := t.TempDir()
	if err := f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	opts := SpawnOptions{Prompt: "Resume me"}
	scope := spawnRequestScope(OriginUI, root)
	const claimedID = "0123456789abcdef"
	if _, _, err := f.ws.ClaimSpawnRequest(scope, "req-fleet", spawnRequestDigest(root, opts), claimedID); err != nil {
		t.Fatal(err)
	}

	id, deliver, err := f.SpawnClaimed(context.Background(), root, opts, "req-fleet")
	if err != nil {
		t.Fatalf("SpawnClaimed: %v", err)
	}
	if id != claimedID {
		t.Fatalf("resumed agent = %q, want the claimed %q", id, claimedID)
	}
	if !deliver {
		t.Fatal("a resumed request must still deliver its first prompt")
	}
	if calls := agentFor(claimedID).calls("session/prompt"); len(calls) != 0 {
		// The fleet does not dispatch the turn itself; the handler does. The
		// claim's record of the delivery is what this asserts.
		t.Fatalf("the fleet dispatched a turn: %v", calls)
	}
	// A second resolution replays: the prompt is recorded as sent, so there is
	// nothing left to deliver.
	claim, ok := f.ws.SpawnClaim(scope, "req-fleet")
	if !ok || !claim.Prompted {
		t.Fatalf("claim = %+v, want it recorded prompted", claim)
	}
	if _, again, err := f.SpawnClaimed(context.Background(), root, opts, "req-fleet"); err != nil || again {
		t.Fatalf("replay = deliver %v, err %v; want no second delivery", again, err)
	}
}

// TestAPlanFirstSpawnHasNoEditingAuthority: a request to plan first starts the
// session in Ask, where it holds no editing authority at all — so no tool
// mutation can precede the person's decision, and the bridge sends no implicit
// mode patch that would grant one.
func TestAPlanFirstSpawnHasNoEditingAuthority(t *testing.T) {
	_, s, root, agentFor := spawnRequestEnv(t)
	code, id := postSpawn(t, s, map[string]any{
		"project":   root,
		"requestId": "req-plan",
		"mode":      "ask",
		"prompt":    "Plan how to fix the login redirect first",
	})
	if code != http.StatusCreated || id == "" {
		t.Fatalf("plan-first spawn = %d %q", code, id)
	}
	calls := agentFor(id).calls("session/new")
	if len(calls) != 1 {
		t.Fatalf("session/new = %v, want one", calls)
	}
	if !strings.Contains(calls[0], `"policy":{"mode":"ask"}`) {
		t.Fatalf("session/new = %v, want the ask policy: the session must discuss before it edits", calls[0])
	}
	// The policy travels with the spawn; nothing follows it with a patch that
	// silently widens what the session may do.
	if got := agentFor(id).calls("session/set_mode"); len(got) != 0 {
		t.Fatalf("session/set_mode = %v, want none", got)
	}
}

// TestSpawnRequestScopeNamesCallerAndProject pins the scope a claim is keyed
// by: the caller's origin and the cleaned project root.
func TestSpawnRequestScopeNamesCallerAndProject(t *testing.T) {
	got := spawnRequestScope(OriginMCP, "/home/u/marshal/../marshal")
	want := OriginMCP + "\x00" + "/home/u/marshal"
	if got != want {
		t.Fatalf("scope = %q, want %q", got, want)
	}
	if spawnRequestScope("", "/p") != OriginUI+"\x00/p" {
		t.Fatal("an empty origin must read as the UI's")
	}
	if filepath.Clean(got) != got && filepath.Base(got) == "marshal" {
		t.Fatal("the scope must keep the cleaned project root")
	}
}
