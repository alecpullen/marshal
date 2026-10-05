package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recipeFleet is an agentFleet whose project agents are preconfigured by
// setup before they start, so a test can answer session/prompt or
// session/stack from the first request.
func recipeFleet(t *testing.T, setup func(*fakeAgent)) (*Fleet, func(string) *fakeAgent) {
	t.Helper()
	f, _, agentOf := agentFleet(t)
	inner := f.newRuntime
	f.newRuntime = func(a Agent) (*Child, error) {
		c, err := inner(a)
		if setup != nil {
			setup(agentOf(a.ID))
		}
		return c, err
	}
	// A scheduled run records its result after the test body ends; wait for
	// that so it does not write into a removed temp dir.
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			f.sched.mu.Lock()
			n := len(f.sched.inflight)
			f.sched.mu.Unlock()
			if n == 0 {
				time.Sleep(20 * time.Millisecond)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return f, agentOf
}

func stackWithFinal(content string) map[string]any {
	return map[string]any{
		"roots": []string{"t1"},
		"nodes": []any{
			map[string]any{"id": "t1", "kind": "turn"},
			map[string]any{"id": "m1", "kind": "message", "message": map[string]any{"role": "user", "content": "go"}},
			map[string]any{"id": "f1", "kind": "final", "message": map[string]any{"role": "assistant", "content": content, "final": true}},
		},
	}
}

const findingsBlock = "Looks mostly fine.\n\n```json\n" +
	`{"findings":[{"severity":"nit","path":"a.go","line":3,"title":"naming","body":"rename"}],"summary":"ok"}` +
	"\n```\n"

func waitResult(t *testing.T, ch <-chan RecipeResult) RecipeResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the recipe to finish")
		return RecipeResult{}
	}
}

func TestRenderErrors(t *testing.T) {
	in := []RecipeInput{{Name: "a", Required: true}, {Name: "b"}}
	if got, err := render("x {{a}} y {{ b }}", map[string]string{"a": "1"}, in); err != nil || got != "x 1 y " {
		t.Fatalf("render = %q, %v", got, err)
	}
	for name, inputs := range map[string]map[string]string{
		"missing required": {},
		"blank required":   {"a": "  "},
		"placeholder":      {"a": "{{b}}"},
		"unknown input":    {"a": "1", "zzz": "2"},
	} {
		if _, err := render("{{a}}", inputs, in); !errors.Is(err, ErrInvalidRecipe) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRecipeRunPromptSpawnsWithModeAndSendsPrompt(t *testing.T) {
	f, agentOf := recipeFleet(t, nil)
	root := t.TempDir()
	done := make(chan RecipeResult, 1)
	id, err := f.RunRecipe(t.Context(), "summarize-changes", RecipeRunRequest{
		Project: root, Inputs: map[string]string{"since": "v1.2"}, OnDone: func(r RecipeResult) { done <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitResult(t, done)
	if res.Err != "" || res.AgentID != id || res.Parsed != nil {
		t.Fatalf("result = %+v", res)
	}
	fa := agentOf(id)
	if got := fa.calls("session/set_mode"); len(got) != 1 || !strings.Contains(got[0], `"mode":"plan"`) {
		t.Errorf("set_mode = %v", got)
	}
	if got := fa.calls("session/prompt"); len(got) != 1 || !strings.Contains(got[0], "since v1.2") {
		t.Errorf("prompt = %v", got)
	}
	a, _ := f.ws.Agent(id)
	if a.Recipe != "summarize-changes" || a.Origin != OriginUI {
		t.Errorf("agent = %+v", a)
	}
}

func TestRecipeRunRefusesBadInputsBeforeSpawning(t *testing.T) {
	f, _ := recipeFleet(t, nil)
	if _, err := f.RunRecipe(t.Context(), "summarize-changes", RecipeRunRequest{Project: t.TempDir()}); !errors.Is(err, ErrInvalidRecipe) {
		t.Errorf("missing input = %v", err)
	}
	if _, err := f.RunRecipe(t.Context(), "nope", RecipeRunRequest{Project: t.TempDir()}); !errors.Is(err, ErrUnknownRecipe) {
		t.Errorf("unknown recipe = %v", err)
	}
	if n := len(f.ws.Agents()); n != 0 {
		t.Errorf("%d agents spawned for refused runs", n)
	}
}

func TestRecipeRunOriginRoutingAndLimitsOverrides(t *testing.T) {
	f, agentOf := recipeFleet(t, nil)
	done := make(chan RecipeResult, 1)
	id, err := f.RunRecipe(t.Context(), "summarize-changes", RecipeRunRequest{
		Project: t.TempDir(), Inputs: map[string]string{"since": "x"}, Origin: OriginSchedule,
		Routing: json.RawMessage(`{"profile":"cheap"}`), OnDone: func(r RecipeResult) { done <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitResult(t, done)
	if a, _ := f.ws.Agent(id); a.Origin != OriginSchedule {
		t.Errorf("origin = %q", a.Origin)
	}
	if got := agentOf(id).calls("session/new"); len(got) != 1 || !strings.Contains(got[0], `"routing":{"profile":"cheap"}`) {
		t.Errorf("session/new = %v", got)
	}
}

func TestRecipeRunSDDAndSwarmGoThroughStartRun(t *testing.T) {
	f, agentOf := recipeFleet(t, nil)
	for name, tc := range map[string]struct {
		kind, method string
	}{
		"sdd-recipe":   {RunSDD, "session/sdd_start"},
		"swarm-recipe": {RunSwarm, "session/swarm_start"},
	} {
		if err := f.recipes.Put(Recipe{Name: name, Kind: tc.kind, Prompt: "Do {{what}}.", Inputs: []RecipeInput{{Name: "what", Required: true}}}); err != nil {
			t.Fatal(err)
		}
		done := make(chan RecipeResult, 1)
		id, err := f.RunRecipe(t.Context(), name, RecipeRunRequest{
			Project: t.TempDir(), Inputs: map[string]string{"what": "the thing"}, OnDone: func(r RecipeResult) { done <- r },
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res := waitResult(t, done); res.Err != "" {
			t.Errorf("%s result = %+v", name, res)
		}
		got := agentOf(id).calls(tc.method)
		if len(got) != 1 {
			t.Fatalf("%s: %s calls = %v", name, tc.method, got)
		}
		if tc.kind == RunSwarm && !strings.Contains(got[0], `"goal":"Do the thing."`) {
			t.Errorf("swarm params = %s", got[0])
		}
		if tc.kind == RunSDD {
			a, _ := f.ws.Agent(id)
			plan, err := os.ReadFile(findPlan(t, a.Project))
			if err != nil || string(plan) != "Do the thing." {
				t.Errorf("plan = %q, %v", plan, err)
			}
		}
	}
}

func findPlan(t *testing.T, project string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(project, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".md") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("no plan file written")
	}
	return found
}

func TestRecipeRunReportsAFailedRun(t *testing.T) {
	f, _ := recipeFleet(t, func(fa *fakeAgent) {
		fa.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
			if m == "session/swarm_start" {
				return nil, &rpcError{Code: -32000, Message: "swarm blew up"}, true
			}
			return nil, nil, false
		}
	})
	_ = f.recipes.Put(Recipe{Name: "swarm-recipe", Kind: RunSwarm, Prompt: "Go."})
	done := make(chan RecipeResult, 1)
	_, err := f.RunRecipe(t.Context(), "swarm-recipe", RecipeRunRequest{Project: t.TempDir(), OnDone: func(r RecipeResult) { done <- r }})
	// An early failure is returned to the caller, and OnDone is not also called.
	if err == nil || !strings.Contains(err.Error(), "swarm blew up") {
		t.Fatalf("err = %v", err)
	}
	select {
	case r := <-done:
		t.Fatalf("OnDone called for a run that was refused: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	if n := len(f.ws.Agents()); n != 0 {
		t.Errorf("%d agents left behind", n)
	}
}

// blockPrompt makes session/prompt wait until release is closed.
func blockPrompt(release <-chan struct{}) func(*fakeAgent) {
	return func(fa *fakeAgent) {
		fa.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
			if m == "session/prompt" {
				<-release
				return map[string]any{"stopReason": "end_turn"}, nil, true
			}
			return nil, nil, false
		}
	}
}

func TestRecipeRunMaxUSDSetsAPausingCapForTheRun(t *testing.T) {
	release := make(chan struct{})
	f, _ := recipeFleet(t, blockPrompt(release))
	if err := f.recipes.Put(Recipe{Name: "capped", Kind: RecipePrompt, Prompt: "Go.", Limits: &RecipeLimits{MaxUSD: 2}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan RecipeResult, 1)
	id, err := f.RunRecipe(t.Context(), "capped", RecipeRunRequest{Project: t.TempDir(), OnDone: func(r RecipeResult) { done <- r }})
	if err != nil {
		t.Fatal(err)
	}
	f.budgets.mu.Lock()
	capUSD, action := f.budgets.capFor(id)
	f.budgets.mu.Unlock()
	if capUSD != 2 || action != budgetPause {
		t.Fatalf("cap = %v %q, want 2 pause", capUSD, action)
	}
	// Spend past the cap, with no configured budgets loaded: the run's own
	// cap still pauses it.
	f.checkBudgets(UsageRow{AgentID: id, CostMicroUSD: 3_000_000, StartedAt: f.now().UnixMilli()})
	if err := f.budgetGate(id); !errors.As(err, new(ErrBudget)) {
		t.Errorf("agent not paused past its cap: %v", err)
	}
	close(release)
	waitResult(t, done)
	f.budgets.mu.Lock()
	_, still := f.budgets.agentCaps[id]
	f.budgets.mu.Unlock()
	if still {
		t.Error("cap outlived the run")
	}
}

func TestRecipeRunMaxMinutesArmsADeadlineThatPauses(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f, _ := recipeFleet(t, blockPrompt(release))
	var mu sync.Mutex
	var armed time.Duration
	var fire func()
	f.afterFunc = func(d time.Duration, fn func()) func() {
		mu.Lock()
		defer mu.Unlock()
		armed, fire = d, fn
		return func() {}
	}
	if err := f.recipes.Put(Recipe{Name: "timed", Kind: RecipePrompt, Prompt: "Go.", Limits: &RecipeLimits{MaxMinutes: 30}}); err != nil {
		t.Fatal(err)
	}
	id, err := f.RunRecipe(t.Context(), "timed", RecipeRunRequest{Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	d, fn := armed, fire
	mu.Unlock()
	if d != 30*time.Minute || fn == nil {
		t.Fatalf("deadline = %v", d)
	}
	fn()
	if _, err := f.runtimeForAgent(id); err == nil {
		t.Error("agent still running after its deadline")
	}
	if !hasEvent(auditTail(t, f), AuditAgentTimeout, id) {
		t.Error("timeout not audited")
	}
}

func TestRecipeCompletionParsesFindings(t *testing.T) {
	f, _ := recipeFleet(t, func(fa *fakeAgent) { fa.results["session/stack"] = stackWithFinal(findingsBlock) })
	done := make(chan RecipeResult, 1)
	_, err := f.RunRecipe(t.Context(), "review-pr", RecipeRunRequest{
		Project: t.TempDir(), Inputs: map[string]string{"pr": "7", "title": "T"}, OnDone: func(r RecipeResult) { done <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitResult(t, done)
	rf, ok := res.Parsed.(ReviewFindings)
	if res.Err != "" || !ok || len(rf.Findings) != 1 || rf.Findings[0].Severity != "nit" || rf.Summary != "ok" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRecipeCompletionParsesCIResult(t *testing.T) {
	msg := "Fixed it.\n```json\n" + `{"reproduced":false,"command":"go test","cause":"","fixed":false,"notes":"n"}` + "\n```"
	f, _ := recipeFleet(t, func(fa *fakeAgent) { fa.results["session/stack"] = stackWithFinal(msg) })
	done := make(chan RecipeResult, 1)
	_, err := f.RunRecipe(t.Context(), "fix-ci", RecipeRunRequest{
		Project: t.TempDir(), Inputs: map[string]string{"check": "unit"}, OnDone: func(r RecipeResult) { done <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitResult(t, done)
	cr, ok := res.Parsed.(CIResult)
	if res.Err != "" || !ok || cr.Reproduced || cr.Command != "go test" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRecipeCompletionWithoutABlockGivesUp(t *testing.T) {
	for name, content := range map[string]string{
		"no block":       "All good, no json here.",
		"invalid json":   "```json\n{nope\n```",
		"bad severity":   "```json\n{\"findings\":[{\"severity\":\"huge\",\"title\":\"x\"}],\"summary\":\"\"}\n```",
		"not a findings": "```json\n{\"hello\":1}\n```",
		"unterminated":   "```json\n{\"findings\":[],\"summary\":\"\"}",
	} {
		f, _ := recipeFleet(t, func(fa *fakeAgent) { fa.results["session/stack"] = stackWithFinal(content) })
		done := make(chan RecipeResult, 1)
		_, err := f.RunRecipe(t.Context(), "review-pr", RecipeRunRequest{
			Project: t.TempDir(), Inputs: map[string]string{"pr": "7"}, OnDone: func(r RecipeResult) { done <- r },
		})
		if err != nil {
			t.Fatal(err)
		}
		if res := waitResult(t, done); res.Err != errNoStructuredResult || res.Parsed != nil {
			t.Errorf("%s: result = %+v", name, res)
		}
	}
}

func TestLastJSONBlock(t *testing.T) {
	got, ok := lastJSONBlock("a\n```json\n{\"a\":1}\n```\nb\n```json\n{\"b\":2}\n```\n")
	if !ok || got != `{"b":2}` {
		t.Errorf("last of two = %q, %v", got, ok)
	}
	if got, ok := lastJSONBlock("```json\n{\"a\":1}\n```\ntrailing ```json oops"); !ok || got != `{"a":1}` {
		t.Errorf("unterminated tail = %q, %v", got, ok)
	}
	if _, ok := lastJSONBlock("no fences"); ok {
		t.Error("found a block in plain text")
	}
	if _, ok := lastJSONBlock("```js\n{}\n```"); ok {
		t.Error("a non-json fence counted")
	}
}

func TestRecipeRunRoute(t *testing.T) {
	f, agentOf := recipeFleet(t, nil)
	s := NewServer(f, "")
	root := t.TempDir()

	rec := doReq(t, s, http.MethodPost, "/api/recipes/summarize-changes/run",
		map[string]any{"project": root, "inputs": map[string]string{"since": "v1"}}, nil)
	var out struct {
		AgentID string `json:"agentId"`
	}
	decodeBody(t, rec, &out)
	if rec.Code != http.StatusAccepted || out.AgentID == "" {
		t.Fatalf("run = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 5*time.Second, "prompt", func() bool { return len(agentOf(out.AgentID).calls("session/prompt")) == 1 })

	for name, tc := range map[string]struct {
		path string
		body map[string]any
		want int
	}{
		"missing input":    {"/api/recipes/summarize-changes/run", map[string]any{"project": root}, http.StatusBadRequest},
		"unknown recipe":   {"/api/recipes/nope/run", map[string]any{"project": root}, http.StatusNotFound},
		"no project":       {"/api/recipes/summarize-changes/run", map[string]any{"inputs": map[string]string{"since": "x"}}, http.StatusBadRequest},
		"relative project": {"/api/recipes/summarize-changes/run", map[string]any{"project": "rel", "inputs": map[string]string{"since": "x"}}, http.StatusBadRequest},
	} {
		if rec := doReq(t, s, http.MethodPost, tc.path, tc.body, nil); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

func TestRecipeRunHonoursTheDailyBlock(t *testing.T) {
	f, _ := recipeFleet(t, nil)
	f.budgets.mu.Lock()
	f.budgets.loaded = true
	f.budgets.blockedDay = dayOf(f.now())
	f.budgets.mu.Unlock()
	s := NewServer(f, "")
	rec := doReq(t, s, http.MethodPost, "/api/recipes/summarize-changes/run",
		map[string]any{"project": t.TempDir(), "inputs": map[string]string{"since": "v1"}}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("run on a blocked day = %d", rec.Code)
	}
}

func TestWorkspaceLoadsV10AndSavesV11(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	v10 := `{"version":10,"projects":["/p"],"agents":[{"id":"a1","project":"/p","createdAt":"2026-01-01T00:00:00Z","ownerId":"local","origin":"ui","profile":{}}]}`
	if err := os.WriteFile(path, []byte(v10), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	if backup, err := ws.Load(); err != nil || backup != "" {
		t.Fatalf("Load = %q, %v", backup, err)
	}
	if _, ok := ws.Agent("a1"); !ok {
		t.Fatal("v10 agent lost")
	}
	if len(ws.Schedules()) != 0 || len(ws.StatusLinks()) != 0 || len(ws.Notifications().Webhooks) != 0 {
		t.Fatal("v10 file has v11 sections")
	}
	a, _ := ws.Agent("a1")
	a.Recipe = "review-pr"
	if err := ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	if err := ws.PutSchedule(Schedule{ID: "s1", Name: "n", Recipe: "r", Cron: "@daily"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var disk struct {
		Version   int        `json:"version"`
		Schedules []Schedule `json:"schedules"`
		Agents    []Agent    `json:"agents"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Version != 11 || len(disk.Schedules) != 1 || disk.Agents[0].Recipe != "review-pr" {
		t.Fatalf("saved = %s", data)
	}
	// And it reloads.
	ws2 := NewWorkspace(path)
	if backup, err := ws2.Load(); err != nil || backup != "" {
		t.Fatalf("reload = %q, %v", backup, err)
	}
	if got, ok := ws2.Schedule("s1"); !ok || got.Recipe != "r" {
		t.Fatalf("schedule lost on reload: %+v", got)
	}
}
