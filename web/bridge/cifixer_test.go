package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func deletionDiff(path string) string {
	return "diff --git a/" + path + " b/" + path + "\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/" + path +
		"\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-line one\n-line two\n"
}

func additionDiff(lines ...string) string {
	return "diff --git a/x b/x\nindex 1111111..2222222 100644\n--- a/x\n+++ b/x\n@@ -1,1 +1,2 @@\n context\n+" +
		strings.Join(lines, "\n+") + "\n"
}

func TestForbiddenChangeDeletedTestFiles(t *testing.T) {
	for _, p := range []string{
		"pkg/a_test.go", "tests/helper.txt", "test/fixtures/data.json", "src/tests/deep/x.rs",
		"test_models.py", "pkg/test_models.py", "models_test.py", "web/a.test.ts", "web/a.spec.js", "web/a.test.tsx",
	} {
		got, bad := forbiddenChange([]diffEntry{{Path: p, Removed: 2}}, map[string]string{p: deletionDiff(p)})
		if !bad || !strings.Contains(got, p) || !strings.Contains(got, "deleted") {
			t.Errorf("deleting %s: got %q, %v", p, got, bad)
		}
	}
}

func TestForbiddenChangeSkipMarkers(t *testing.T) {
	for marker, line := range map[string]string{
		"t.Skip(":           "\tt.Skip(\"flaky\")",
		"@pytest.mark.skip": "@pytest.mark.skip(reason='x')",
		"@unittest.skip":    "    @unittest.skip('later')",
		"it.skip(":          "  it.skip('works', () => {})",
		"describe.skip(":    "describe.skip('suite', () => {})",
		"xit(":              "xit('works', () => {})",
		"test.skip(":        "test.skip('works', async () => {})",
		".only(":            "it.only('works', () => {})",
		"#[ignore]":         "#[ignore]",
	} {
		got, bad := forbiddenChange([]diffEntry{{Path: "a_test.go", Added: 1}}, map[string]string{"a_test.go": additionDiff(line)})
		if !bad || !strings.HasPrefix(got, marker) || !strings.Contains(got, "a_test.go") {
			t.Errorf("%q: got %q, %v", line, got, bad)
		}
	}
}

func TestForbiddenChangeCIConfig(t *testing.T) {
	for _, c := range []struct{ path, line, want string }{
		{".github/workflows/ci.yml", "    if: false", "if: false"},
		{".github/workflows/ci.yml", "    continue-on-error: true", "continue-on-error: true"},
		{".gitea/workflows/build.yaml", "  if:  false", "if: false"},
		{".gitea/workflows/build.yaml", "  continue-on-error:true", "continue-on-error: true"},
	} {
		got, bad := forbiddenChange([]diffEntry{{Path: c.path}}, map[string]string{c.path: additionDiff(c.line)})
		if !bad || !strings.HasPrefix(got, c.want) {
			t.Errorf("%s %q: got %q, %v", c.path, c.line, got, bad)
		}
	}
	got, bad := forbiddenChange([]diffEntry{{Path: ".github/workflows/ci.yml"}}, map[string]string{".github/workflows/ci.yml": deletionDiff(".github/workflows/ci.yml")})
	if !bad || !strings.Contains(got, "workflow") {
		t.Errorf("deleting a workflow: got %q, %v", got, bad)
	}
}

func TestForbiddenChangeAllowsLegitimateEdits(t *testing.T) {
	for name, c := range map[string]struct {
		path string
		diff string
	}{
		"a new test":                 {"new_test.go", additionDiff("func TestNew(t *testing.T) {", "\tif got != want { t.Fatal() }", "}")},
		"a test edited, not skipped": {"a_test.go", additionDiff("\twant := 2")},
		"exit( is not xit(":          {"main.go", additionDiff("\tos.Exit(1)", "\texit(1)")},
		"a non-test file deleted":    {"pkg/util.go", deletionDiff("pkg/util.go")},
		"if: false outside CI":       {"docs/notes.md", additionDiff("if: false")},
		"continue-on-error false":    {".github/workflows/ci.yml", additionDiff("    continue-on-error: false")},
		"a workflow edited":          {".github/workflows/ci.yml", additionDiff("    run: go test ./...")},
		"a removed skip":             {"a_test.go", "diff --git a/a_test.go b/a_test.go\n--- a/a_test.go\n+++ b/a_test.go\n@@ -1,2 +1,1 @@\n-\tt.Skip(\"x\")\n context\n"},
		"a +++ header line":          {"a.go", "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+// fine\n"},
	} {
		if got, bad := forbiddenChange([]diffEntry{{Path: c.path}}, map[string]string{c.path: c.diff}); bad {
			t.Errorf("%s: flagged as %q", name, got)
		}
	}
}

func TestInferCommand(t *testing.T) {
	for log, want := range map[string]string{
		"build\n$ go test ./...\nFAIL\n":   "go test ./...",
		"+ npm run lint\nerror\n":          "npm run lint",
		"$ first\nstuff\n+ second\nmore\n": "second",
		"no command here\n":                "",
		"":                                 "",
	} {
		if got := inferCommand(log); got != want {
			t.Errorf("inferCommand(%q) = %q, want %q", log, got, want)
		}
	}
}

// ---- onCheckEvent ----

func ciFixerOn() *Automations {
	return &Automations{CIFixer: &CIFixerSettings{Enabled: true, RepoID: "r1", Branches: []string{"main", "release/*"}}}
}

const twoFailures = `[{"id":2,"name":"test","conclusion":"failure","html_url":"h"},
 {"id":3,"name":"lint","conclusion":"failure"},{"id":4,"name":"build","conclusion":"success"}]`

func TestCIFixerRunsFixCIForTheFirstFailedCheck(t *testing.T) {
	a := ciFixerOn()
	a.CIFixer.MaxMinutes, a.CIFixer.MaxUSD = 20, 3.5
	e := newAutoEnv(t, a)
	e.forge.checkRuns["c1"] = twoFailures
	spy := e.spyRecipes()

	e.f.onCheckEvent(t.Context(), checkEvent{repoID: "r1", ref: "main", sha: "c1"})

	if spy.count() != 1 {
		t.Fatalf("recipe runs = %d, want 1", spy.count())
	}
	name, req := spy.last()
	if name != "fix-ci" || req.Ref != "c1" || req.RepoID != "r1" || req.Origin != OriginCI || req.Project != e.root {
		t.Fatalf("name = %q, req = %+v", name, req)
	}
	if req.Inputs["check"] != "test" || req.Inputs["command"] != "go test ./..." || !strings.Contains(req.Inputs["log"], "FAIL") {
		t.Errorf("inputs = %v", req.Inputs)
	}
	if req.Limits == nil || req.Limits.MaxMinutes != 20 || req.Limits.MaxUSD != 3.5 {
		t.Errorf("limits = %+v", req.Limits)
	}
}

func TestCIFixerIgnoresUnwatchedBranchesAndWhenOff(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.forge.checkRuns["c1"] = twoFailures
	spy := e.spyRecipes()
	e.f.onCheckEvent(t.Context(), checkEvent{repoID: "r1", ref: "main", sha: "c1"})
	e.setAutomations(t, ciFixerOn())
	e.f.onCheckEvent(t.Context(), checkEvent{repoID: "r1", ref: "feature/x", sha: "c1"})
	if spy.count() != 0 {
		t.Fatalf("recipe runs = %d, want none", spy.count())
	}
	e.f.onCheckEvent(t.Context(), checkEvent{repoID: "r1", ref: "release/1.2", sha: "c1"})
	if spy.count() != 1 {
		t.Fatalf("a glob-matched branch was not watched: %d runs", spy.count())
	}
}

func TestCIFixerDedupesOnRepoSHAAndCheck(t *testing.T) {
	e := newAutoEnv(t, ciFixerOn())
	e.forge.checkRuns["c1"] = twoFailures
	spy := e.spyRecipes()
	ev := checkEvent{repoID: "r1", ref: "main", sha: "c1"}

	e.f.onCheckEvent(t.Context(), ev)
	e.f.onCheckEvent(t.Context(), ev) // "test" is still running, so "lint" is next
	if spy.count() != 2 {
		t.Fatalf("runs = %d, want one per distinct check", spy.count())
	}
	if _, req := spy.last(); req.Inputs["check"] != "lint" {
		t.Fatalf("second run was for %q, want lint", req.Inputs["check"])
	}
	e.f.onCheckEvent(t.Context(), ev) // both running
	if spy.count() != 2 {
		t.Fatalf("a third event started run %d", spy.count())
	}
	// Once a result is stored, the same check on the same commit stays done.
	spy.mu.Lock()
	first := spy.reqs[0]
	spy.mu.Unlock()
	first.OnDone(RecipeResult{AgentID: "a1", Err: "boom"})
	e.f.onCheckEvent(t.Context(), ev)
	if spy.count() != 2 {
		t.Fatalf("a recorded check was fixed again: %d runs", spy.count())
	}
	// A new commit is new work.
	e.forge.checkRuns["c2"] = twoFailures
	e.f.onCheckEvent(t.Context(), checkEvent{repoID: "r1", ref: "main", sha: "c2"})
	if spy.count() != 3 {
		t.Fatalf("a new commit's failure was skipped: %d runs", spy.count())
	}
}

func TestCIFixerPollFallbackChecksTheTipOfEachWatchedBranch(t *testing.T) {
	e := newAutoEnv(t, ciFixerOn())
	e.forge.checkRuns["tip1"] = twoFailures
	spy := e.spyRecipes()
	var mu sync.Mutex
	var asked []string
	e.f.auto.branchHead = func(_ context.Context, _ Repo, branch string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, branch)
		return "tip1", nil
	}
	repo, _ := e.f.ws.Repo("r1")
	if err := e.f.pollRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if spy.count() != 1 {
		t.Fatalf("poll runs = %d, want 1", spy.count())
	}
	if _, req := spy.last(); req.Ref != "tip1" {
		t.Errorf("ref = %q", req.Ref)
	}
	if len(asked) != 1 || asked[0] != "main" {
		t.Errorf("branches resolved = %v (a glob cannot be polled)", asked)
	}
}

// ---- finishCI ----

type ciHarness struct {
	e         *autoEnv
	discarded []string
	exits     []ExitOptions
	exitRes   ExitResult
	exitErr   error
	diff      string // the diff the agent "made"
	files     []diffEntry
}

func newCIHarness(t *testing.T, a *Automations) *ciHarness {
	t.Helper()
	h := &ciHarness{e: newAutoEnv(t, a)}
	h.exitRes = ExitResult{Destination: "push", Branch: "marshal/x", PRUrl: "https://github.com/you/r/pull/9"}
	if err := h.e.f.ws.PutAgent(Agent{ID: "agent-1", Name: "Fix a failing check", OwnerID: DefaultOwnerID, SourceKind: "git", SourceRef: "r1"}); err != nil {
		t.Fatal(err)
	}
	h.e.f.auto.diff = func(_ context.Context, _ string, path string) (json.RawMessage, error) {
		if path == "" {
			return json.Marshal(map[string]any{"files": h.files})
		}
		return json.Marshal(map[string]any{"diff": h.diff})
	}
	h.e.f.auto.discard = func(_ context.Context, id string) error { h.discarded = append(h.discarded, id); return nil }
	h.e.f.auto.exit = func(_ context.Context, _ string, opts ExitOptions) (ExitResult, error) {
		h.exits = append(h.exits, opts)
		return h.exitRes, h.exitErr
	}
	return h
}

func (h *ciHarness) finish(t *testing.T, branch string, res RecipeResult) CIHistoryEntry {
	t.Helper()
	repo, _ := h.e.f.ws.Repo("r1")
	_, ci, _ := h.e.f.ciFixerSettings("r1")
	h.e.f.finishCI(repo, ci, checkEvent{repoID: "r1", ref: branch, sha: "abcdef0123456"}, CheckInfo{Name: "test"}, res)
	hist := h.e.f.CIHistory("")
	if len(hist) != 1 {
		t.Fatalf("history = %+v", hist)
	}
	return hist[0]
}

func ciRes(r CIResult) RecipeResult {
	return RecipeResult{AgentID: "agent-1", Output: OutputCIResult, Parsed: r}
}

func TestCIFixerNotReproducedIsDiscarded(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	got := h.finish(t, "main", ciRes(CIResult{Reproduced: false, Notes: "passes locally"}))
	if got.Status != ciNotReproduced || got.Reason != "passes locally" {
		t.Fatalf("entry = %+v", got)
	}
	if len(h.discarded) != 1 || len(h.exits) != 0 {
		t.Fatalf("discarded = %v, exits = %d", h.discarded, len(h.exits))
	}
}

func TestCIFixerNoStructuredResultIsDiscarded(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	got := h.finish(t, "main", RecipeResult{AgentID: "agent-1", Err: errNoStructuredResult})
	if got.Status != ciGaveUp || got.Reason != errNoStructuredResult || len(h.discarded) != 1 {
		t.Fatalf("entry = %+v, discarded = %v", got, h.discarded)
	}
}

func TestCIFixerForbiddenChangeIsDiscardedWithTheReason(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	h.files = []diffEntry{{Path: "a_test.go", Added: 1}}
	h.diff = additionDiff("\tt.Skip(\"later\")")
	got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true}))
	if got.Status != ciGaveUp || got.Reason != "forbidden change (t.Skip( in a_test.go)" {
		t.Fatalf("entry = %+v", got)
	}
	if len(h.discarded) != 1 || len(h.exits) != 0 {
		t.Fatalf("discarded = %v, exits = %d", h.discarded, len(h.exits))
	}
}

func TestCIFixerUnreadableDiffShipsNothing(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	h.e.f.auto.diff = func(context.Context, string, string) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	}
	got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true}))
	if got.Status != ciGaveUp || !strings.Contains(got.Reason, "could not read the agent's diff") || len(h.exits) != 0 {
		t.Fatalf("entry = %+v, exits = %d", got, len(h.exits))
	}
}

func TestCIFixerNotFixedGivesUpWithTheAgentsNotes(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: false, Notes: "needs a schema change"}))
	if got.Status != ciGaveUp || !strings.Contains(got.Reason, "needs a schema change") || len(h.exits) != 0 {
		t.Fatalf("entry = %+v", got)
	}
}

func TestCIFixerGateFailureGivesUpAndNeverOverrides(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	h.exitRes = ExitResult{Destination: "push", Blocked: true}
	got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true}))
	if got.Status != ciGaveUp || got.Reason != "gate failed" {
		t.Fatalf("entry = %+v", got)
	}
	if len(h.exits) != 1 || h.exits[0].Override != nil {
		t.Fatalf("exit opts = %+v: automation must never override the gate", h.exits)
	}
}

func TestCIFixerSuccessOpensAPRAndRecordsCost(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	h.e.f.audit = NewAuditLog(t.TempDir())
	h.files = []diffEntry{{Path: "pkg/a.go", Added: 3}}
	h.diff = additionDiff("\treturn nil")
	now := time.Now()
	if _, err := h.e.f.usage.Append([]UsageRow{
		{ID: 1, AgentID: "agent-1", StartedAt: now.UnixMilli(), CostMicroUSD: 1_250_000},
		{ID: 2, AgentID: "other", StartedAt: now.UnixMilli(), CostMicroUSD: 9_000_000},
	}); err != nil {
		t.Fatal(err)
	}

	got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true}))
	if got.Status != ciFixed || got.PRURL != "https://github.com/you/r/pull/9" || got.CostUSD != 1.25 ||
		got.AgentID != "agent-1" || got.Check != "test" || got.SHA != "abcdef0123456" || got.Branch != "main" {
		t.Fatalf("entry = %+v", got)
	}
	if len(h.exits) != 1 || h.exits[0].NoPR || h.exits[0].Override != nil ||
		h.exits[0].Branch != "marshal/fix-ci-abcdef0-test" || !strings.Contains(h.exits[0].CommitMessage, "test") {
		t.Fatalf("exit opts = %+v", h.exits)
	}
	a, _ := h.e.f.ws.Agent("agent-1")
	if a.TargetBranch != "main" || a.Name != "Fix CI: test @ abcdef0" {
		t.Errorf("agent = %+v: the PR must target the failing branch with a readable name", a)
	}
	if len(h.discarded) != 0 {
		t.Errorf("a shipped fix was discarded")
	}
	deltas := automationDeltas(h.e.f)
	if len(deltas) != 1 || deltas[0].Type != "ci_result" || deltas[0].Status != ciFixed || deltas[0].ID != got.ID {
		t.Fatalf("deltas = %+v", deltas)
	}
	if ev := findEvent(auditTail(t, h.e.f), AuditCIFixerResult); ev == nil || ev.Detail != ciFixed {
		t.Fatalf("audit = %+v", ev)
	}
}

func TestCIFixerPushPushesToAMatchingBranchOnly(t *testing.T) {
	a := ciFixerOn()
	a.CIFixer.Push, a.CIFixer.PushBranches = true, []string{"main"}
	h := newCIHarness(t, a)
	h.exitRes = ExitResult{Destination: "push", Branch: "main"}

	got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true}))
	if got.Status != ciFixed || got.PRURL != "" || !strings.Contains(got.Reason, "pushed to main") {
		t.Fatalf("entry = %+v", got)
	}
	if len(h.exits) != 1 || !h.exits[0].NoPR || h.exits[0].Branch != "main" {
		t.Fatalf("exit opts = %+v", h.exits)
	}

	// A branch outside pushBranches gets a pull request instead.
	h2 := newCIHarness(t, func() *Automations {
		b := ciFixerOn()
		b.CIFixer.Push, b.CIFixer.PushBranches = true, []string{"main"}
		return b
	}())
	h2.finish(t, "release/1", ciRes(CIResult{Reproduced: true, Fixed: true}))
	if len(h2.exits) != 1 || h2.exits[0].NoPR || !strings.HasPrefix(h2.exits[0].Branch, "marshal/fix-ci-") {
		t.Fatalf("exit opts = %+v", h2.exits)
	}
}

func TestCIFixerShipRefusalsAreRecorded(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	h.exitErr = context.Canceled
	if got := h.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true})); got.Status != ciGaveUp || !strings.HasPrefix(got.Reason, "ship failed") {
		t.Fatalf("entry = %+v", got)
	}
	h2 := newCIHarness(t, ciFixerOn())
	h2.exitRes = ExitResult{Destination: "patch"}
	if got := h2.finish(t, "main", ciRes(CIResult{Reproduced: true, Fixed: true})); got.Status != ciGaveUp || !strings.Contains(got.Reason, "patch") {
		t.Fatalf("entry = %+v", got)
	}
}

// TestCIFixerReadsTheDiffThroughTheAgent runs the guard against a real
// (scripted) agent child rather than the seam.
func TestCIFixerReadsTheDiffThroughTheAgent(t *testing.T) {
	tr := &scriptedTransport{gate: gateResult{OK: true}, results: map[string]any{
		"session/diff": map[string]any{
			"files": []map[string]any{{"path": "pkg/a_test.go", "added": 0, "removed": 2}},
			"diff":  deletionDiff("pkg/a_test.go"),
		},
	}}
	f := testFleetScripted(t, tr)
	id := spawnGitAgent(t, f)
	e := newAutoEnvOn(t, f, ciFixerOn())
	repo, _ := f.ws.Repo("r1")
	_, ci, _ := f.ciFixerSettings("r1")
	f.finishCI(repo, ci, checkEvent{repoID: "r1", ref: "main", sha: "abcdef0123"}, CheckInfo{Name: "test"},
		RecipeResult{AgentID: id, Parsed: CIResult{Reproduced: true, Fixed: true}})
	hist := f.CIHistory("")
	if len(hist) != 1 || hist[0].Status != ciGaveUp || !strings.Contains(hist[0].Reason, "pkg/a_test.go") {
		t.Fatalf("history = %+v", hist)
	}
	if count(tr.methods(), "session/discard") != 1 || count(tr.methods(), "session/commit") != 0 {
		t.Fatalf("methods = %v: a tampering run must be discarded, not committed", tr.methods())
	}
	_ = e
}

func TestCIHistoryRoutes(t *testing.T) {
	h := newCIHarness(t, ciFixerOn())
	h.finish(t, "main", ciRes(CIResult{Reproduced: false}))
	id := h.e.f.CIHistory("")[0].ID

	var list struct{ History []CIHistoryEntry }
	decodeBody(t, doReq(t, h.e.srv, http.MethodGet, "/api/automations/ci/history?project="+h.e.root, nil, nil), &list)
	if len(list.History) != 1 || list.History[0].ID != id {
		t.Fatalf("history = %+v", list.History)
	}
	decodeBody(t, doReq(t, h.e.srv, http.MethodGet, "/api/automations/ci/history?project=/elsewhere", nil, nil), &list)
	if len(list.History) != 0 {
		t.Fatalf("another project's history = %+v", list.History)
	}
	var one CIHistoryEntry
	decodeBody(t, doReq(t, h.e.srv, http.MethodGet, "/api/automations/ci/history/"+id, nil, nil), &one)
	if one.ID != id || one.Status != ciNotReproduced {
		t.Fatalf("entry = %+v", one)
	}
	if rec := doReq(t, h.e.srv, http.MethodGet, "/api/automations/ci/history/ffffffffffffffff", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id code = %d, want 404", rec.Code)
	}
}

// ---- Exit options ----

func TestExitBranchAndNoPRPushToTheNamedBranchWithoutAPullRequest(t *testing.T) {
	var prHits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		prHits++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/you/r/pull/7"}`))
	}))
	defer srv.Close()
	f := testFleetWithGate(t, gateResult{OK: true})
	id := spawnGitAgentWithPAT(t, f, srv.URL)
	var pushed []string
	orig := f.git.exec
	f.git.exec = func(dir string, env []string, args ...string) ([]byte, error) {
		if len(args) > 4 && args[4] == "push" {
			pushed = append(pushed, strings.Join(args[5:], " "))
		}
		return orig(dir, env, args...)
	}

	res, err := f.Exit(t.Context(), id, ExitOptions{CommitMessage: "fix", Branch: "main", NoPR: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "main" || res.PRUrl != "" || prHits != 0 {
		t.Fatalf("res = %+v, PR API hits = %d", res, prHits)
	}
	if len(pushed) != 1 || !strings.Contains(pushed[0], "HEAD:refs/heads/main") {
		t.Fatalf("pushes = %v", pushed)
	}
}

func TestExitOptionsBranchAndNoPRNeverComeOffTheWire(t *testing.T) {
	var o ExitOptions
	if err := json.Unmarshal([]byte(`{"commitMessage":"m","Branch":"main","branch":"main","noPR":true,"NoPR":true}`), &o); err != nil {
		t.Fatal(err)
	}
	if o.Branch != "" || o.NoPR {
		t.Fatalf("options = %+v: an API client must not choose the pushed branch", o)
	}
}

func TestProjectSettingsValidateAutomations(t *testing.T) {
	e := newAutoEnv(t, nil)
	if err := e.f.ws.PutRepo(Repo{ID: "bare", URL: "https://example.com/x.git", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	good := ProjectSettings{Automations: &Automations{
		ReviewBot: &ReviewBotSettings{Enabled: true, RepoID: "r1", HoldSeverities: []string{"blocking"}, Routing: json.RawMessage(`{"profile":"x"}`)},
		CIFixer:   &CIFixerSettings{Enabled: true, RepoID: "r1", Branches: []string{"main"}, MaxMinutes: 10, MaxUSD: 2, Push: true, PushBranches: []string{"main"}},
	}}
	put := func(ps ProjectSettings) int {
		return doReq(t, e.srv, http.MethodPut, "/api/projects/settings?root="+e.root, ps, nil).Code
	}
	if code := put(good); code != http.StatusOK {
		t.Fatalf("good settings = %d", code)
	}
	var got ProjectSettings
	decodeBody(t, doReq(t, e.srv, http.MethodGet, "/api/projects/settings?root="+e.root, nil, nil), &got)
	if got.Automations == nil || got.Automations.CIFixer == nil || got.Automations.CIFixer.MaxUSD != 2 || !got.Automations.CIFixer.Push {
		t.Fatalf("round trip = %+v", got.Automations)
	}
	bad := map[string]*Automations{
		"unknown repo":       {ReviewBot: &ReviewBotSettings{Enabled: true, RepoID: "nope"}},
		"repo with no forge": {ReviewBot: &ReviewBotSettings{Enabled: true, RepoID: "bare"}},
		"bad severity":       {ReviewBot: &ReviewBotSettings{Enabled: true, RepoID: "r1", HoldSeverities: []string{"huge"}}},
		"bad routing":        {ReviewBot: &ReviewBotSettings{Enabled: true, RepoID: "r1", Routing: json.RawMessage(`[1]`)}},
		"no branches":        {CIFixer: &CIFixerSettings{Enabled: true, RepoID: "r1"}},
		"negative limit":     {CIFixer: &CIFixerSettings{RepoID: "r1", MaxUSD: -1}},
		"bad pattern":        {CIFixer: &CIFixerSettings{Enabled: true, RepoID: "r1", Branches: []string{"[x"}}},
	}
	for name, a := range bad {
		if code := put(ProjectSettings{Automations: a}); code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", name, code)
		}
	}
}

func TestWebhookSecretRouteRefusesToOverwriteANotificationSecret(t *testing.T) {
	e := newAutoEnv(t, nil)
	cfg := e.f.ws.Notifications()
	cfg.Webhooks = []Webhook{{ID: "r1", URL: "https://example.com/h", SecretRef: "vault:hooks/r1", Events: []string{NotifyAutomation}}}
	if err := e.f.ws.SetNotifications(cfg); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, e.srv, http.MethodPost, "/api/repos/r1/webhook-secret", nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec.Code)
	}
}
