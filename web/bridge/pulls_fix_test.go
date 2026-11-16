package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// fixTestPull records one failing pull request on repo r1 and returns its id.
func fixTestPull(t *testing.T, f *Fleet, head, check string) string {
	t.Helper()
	if err := f.recordPull(contract.PullRecord{
		RepoID: "r1", ProjectRoot: "/p", Number: 7, HeadRef: "marshal/a1", BaseRef: "main",
		HeadSHA: head, Origin: contract.PullOriginAgent, ObservedAt: 1,
		Checks: []contract.PullCheck{{Name: check, State: contract.CheckFailed}},
	}); err != nil {
		t.Fatal(err)
	}
	return pullID("r1", 7)
}

// runFixInline makes a dispatch run its fix synchronously.
func runFixInline(f *Fleet) {
	f.auto.pullFix = func(ctx context.Context, rec contract.PullRecord, fix contract.PullFix, req contract.PullFixRequest) error {
		return f.runPullFix(ctx, rec, fix, req)
	}
}

// pullAsk is the Inbox ask about one pull request.
func pullAsk(t *testing.T, f *Fleet, id string) contract.PullAsk {
	t.Helper()
	for _, item := range f.Inbox().Queue {
		if item.Kind == contract.InboxItemPull && item.Pull != nil && item.Pull.Pull == id {
			return *item.Pull
		}
	}
	t.Fatalf("no inbox ask for %s; queue = %+v", id, f.Inbox().Queue)
	return contract.PullAsk{}
}

// grantFactoryRounds gives an item an approval's round allowance.
func grantFactoryRounds(t *testing.T, s *FactoryStore, item FactoryItem, rounds int) FactoryItem {
	t.Helper()
	now := time.Now().UTC()
	updated, err := s.Update(item.ID, "grant-rounds", item.Version, json.RawMessage(`{"kind":"grant-rounds"}`),
		func(i *FactoryItem) error {
			i.Grants = []FactoryGrant{{ID: "approve", Reason: "initial approval", AdditionalRounds: rounds, At: now}}
			return nil
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func TestFixRefusesACheckThatIsNotFailing(t *testing.T) {
	pf := newPullForge(t)
	f := pullTestFleet(t, pf)
	id := fixTestPull(t, f, "abc", "lint")

	_, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Check: "build"}, DefaultOwnerID)
	if !errors.Is(err, ErrPullFixNotFailing) {
		t.Fatalf("err = %v, want ErrPullFixNotFailing", err)
	}
	if rec, _ := f.pull(id); rec.Fix != nil {
		t.Fatalf("a refused request stored a fix: %+v", rec.Fix)
	}
	if pf.called() {
		t.Fatalf("a refused request reached the forge: %v", pf.requests)
	}
}

func TestSecondFixIsRefusedWithTheRunningID(t *testing.T) {
	pf := newPullForge(t)
	f := pullTestFleet(t, pf)
	id := fixTestPull(t, f, "abc", "lint")
	running := contract.PullFix{ID: "fix-1", Check: "lint", HeadSHA: "abc",
		Mode: contract.PullFixModeFix, StartedAt: 1}
	if _, err := f.updatePull(id, func(r *contract.PullRecord) error { r.Fix = &running; return nil }); err != nil {
		t.Fatal(err)
	}

	_, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Check: "lint"}, DefaultOwnerID)
	var refusal pullFixRunning
	if !errors.As(err, &refusal) || refusal.ID != "fix-1" {
		t.Fatalf("err = %v, want the running fix's id", err)
	}

	// The HTTP refusal is a 409 naming that fix.
	srv := &Server{fleet: f}
	req := httptest.NewRequest("POST", "/api/pulls/"+id+"/fix", strings.NewReader(`{"check":"lint"}`))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.fixPull(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var body struct {
		FixID string `json:"fixId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.FixID != "fix-1" {
		t.Fatalf("body = %s", w.Body)
	}
}

// factoryFixFleet is a fleet whose index holds one factory item's failing pull
// request, with the item journalled in store.
func factoryFixFleet(t *testing.T, f *Fleet, s *FactoryStore, item FactoryItem) string {
	t.Helper()
	if err := f.recordPull(contract.PullRecord{
		RepoID: "r1", ProjectRoot: "/p", Number: 9, HeadRef: "factory/x", BaseRef: "main",
		HeadSHA: "abc123", Origin: contract.PullOriginFactory, FactoryItem: item.ID,
		Checks: []contract.PullCheck{{Name: "test", State: contract.CheckFailed}}, ObservedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	f.SetFactoryController(NewFactoryController(s, &loopEffects{store: s}, f.now))
	runFixInline(f)
	return pullID("r1", 9)
}

func TestFactoryPullFixUsesTheRepairLoop(t *testing.T) {
	f := testFleet(t)
	s, item := factoryPullItem(t, 9, "abc123", nil)
	grantFactoryRounds(t, s, item, 3)
	id := factoryFixFleet(t, f, s, item)

	if _, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Check: "test"}, DefaultOwnerID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	after, ok := f.factory.Item(item.ID)
	if !ok {
		t.Fatal("the item vanished")
	}
	if after.State != "repairing" {
		t.Fatalf("item state = %q, want repairing: the repair is the factory's own round", after.State)
	}
	if len(after.Grants) != 1 {
		t.Fatalf("grants = %+v; a repair within the allowance grants nothing", after.Grants)
	}
	if cur, _ := f.pull(id); cur.Fix == nil {
		t.Fatal("the fix was cleared before the factory's round reported")
	}
}

func TestExhaustedRoundsGrantOne(t *testing.T) {
	f := testFleetWithAudit(t)
	// One round and no allowance: the item's rounds are already spent.
	s, item := factoryPullItem(t, 9, "abc123", nil)
	id := factoryFixFleet(t, f, s, item)

	if _, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Check: "test"}, DefaultOwnerID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	after, _ := f.factory.Item(item.ID)
	if len(after.Grants) != 1 {
		t.Fatalf("grants = %+v, want exactly the round the user's request bought", after.Grants)
	}
	grant := after.Grants[len(after.Grants)-1]
	if grant.AdditionalRounds != 1 {
		t.Fatalf("granted rounds = %d, want exactly 1", grant.AdditionalRounds)
	}
	if grant.Reason != "requested by the user" {
		t.Fatalf("grant reason = %q", grant.Reason)
	}
	if findEvent(auditTail(t, f), AuditPullFixGranted) == nil {
		t.Fatal("the granted round left no audit record")
	}
}

func TestNeedsInputClearsTheFactoryFix(t *testing.T) {
	f := testFleet(t)
	s, item := factoryPullItem(t, 9, "abc123", []FactoryCheck{{
		Key: FactoryCheckKey{Kind: "check", Name: "test"}, SHA: "abc123",
		Status: "completed", Conclusion: "failure",
	}})
	grantFactoryRounds(t, s, item, 3)
	id := factoryFixFleet(t, f, s, item)

	if _, err := f.dispatchPullFix(context.Background(), id,
		contract.PullFixRequest{Check: "test"}, DefaultOwnerID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// The repair round ends needing a person: no round is running on the item
	// any more, so nothing is carrying the fix.
	cur, err := s.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(cur.ID, "needs-input", cur.Version, json.RawMessage(`{"kind":"needs-input"}`),
		func(i *FactoryItem) error {
			i.State = "needs_input"
			i.Decision = &FactoryDecision{ID: "decision-1", Kind: "ask", Reason: "which way?", CreatedAt: time.Now().UTC()}
			return nil
		}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	f.observePulls(context.Background())

	rec, ok := f.pull(id)
	if !ok {
		t.Fatal("the record was removed")
	}
	if rec.Fix != nil {
		t.Fatalf("a fix stayed on an item that settled at needs_input: %+v", rec.Fix)
	}
	if rec.State != contract.PullFailing {
		t.Fatalf("state = %q, want failing: the item's own checks are the evidence", rec.State)
	}
}

func TestAgentPullFixRunsTheFixer(t *testing.T) {
	pf := newPullForge(t)
	pf.setChecks("abc123", map[string]any{
		"id": 412, "name": "lint", "status": "completed", "conclusion": "failure",
		"html_url":    "https://github.com/you/r/runs/412",
		"details_url": "https://github.com/you/r/actions/runs/412/job/9002",
	})
	f := pullTestFleet(t, pf)
	id := fixTestPull(t, f, "abc123", "lint")

	var ran RecipeRunRequest
	var exited *ExitOptions
	f.auto.runRecipe = func(ctx context.Context, name string, req RecipeRunRequest) (string, error) {
		if name != "fix-ci" {
			t.Errorf("recipe = %q, want fix-ci", name)
		}
		ran = req
		req.OnDone(RecipeResult{AgentID: "fixer-1", Parsed: CIResult{Reproduced: true, Fixed: true}})
		return "fixer-1", nil
	}
	f.auto.diff = func(context.Context, string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[]}`), nil
	}
	f.auto.exit = func(ctx context.Context, agentID string, opts ExitOptions) (ExitResult, error) {
		if agentID != "fixer-1" {
			t.Errorf("exited %q, want the worker", agentID)
		}
		exited = &opts
		return ExitResult{Destination: "push", Branch: opts.Branch, PRUrl: "https://github.com/you/r/pull/7"}, nil
	}
	runFixInline(f)

	dispatched, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Note: "try the cache"}, DefaultOwnerID)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if dispatched.Fix == nil || dispatched.State != contract.PullBeingFixed {
		t.Fatalf("dispatch record = %+v", dispatched)
	}
	// The recipe ran against the pull request's own head.
	if ran.RepoID != "r1" || ran.Ref != "abc123" || ran.Inputs["check"] != "lint" {
		t.Fatalf("recipe request = %+v", ran)
	}
	if !strings.Contains(ran.Inputs["log"], "try the cache") {
		t.Fatalf("the note did not reach the recipe's prompt: %q", ran.Inputs["log"])
	}
	// It pushed to the pull request's head branch, and opened nothing.
	if exited == nil {
		t.Fatal("a fixed check was never pushed")
	}
	if exited.Branch != "marshal/a1" || !exited.NoPR {
		t.Fatalf("exit opts = %+v, want the pull request's branch and no new pull request", *exited)
	}
	if cur, _ := f.pull(id); cur.Fix != nil {
		t.Fatalf("a pushed fix stayed on the record: %+v", cur.Fix)
	}
}

func TestFixIsCancelledWhenTheHeadMoved(t *testing.T) {
	pf := newPullForge(t)
	f := pullTestFleet(t, pf)
	id := fixTestPull(t, f, "newhead", "lint")
	fix := contract.PullFix{ID: "fix-1", Check: "lint", HeadSHA: "oldhead", Mode: contract.PullFixModeFix}
	if _, err := f.updatePull(id, func(r *contract.PullRecord) error { r.Fix = &fix; return nil }); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.pull(id)

	runFixInline(f)
	if err := f.runPullFix(context.Background(), rec, fix, contract.PullFixRequest{}); err != nil {
		t.Fatalf("runPullFix: %v", err)
	}
	if cur, _ := f.pull(id); cur.Fix != nil {
		t.Fatalf("the cancelled fix stayed on the record: %+v", cur.Fix)
	}
	if ask := pullAsk(t, f, id); ask.Outcome != pullFixHeadMoved {
		t.Fatalf("ask = %+v, want a head_moved ask", ask)
	}
	// Nothing ran against the new head: the forge was never asked.
	if pf.called() {
		t.Fatalf("a cancelled fix reached the forge: %v", pf.requests)
	}
}

func TestAFixDeletingATestIsRefused(t *testing.T) {
	pf := newPullForge(t)
	pf.setChecks("abc123", map[string]any{
		"id": 412, "name": "lint", "status": "completed", "conclusion": "failure",
		"html_url":    "https://github.com/you/r/runs/412",
		"details_url": "https://github.com/you/r/actions/runs/412/job/9002",
	})
	f := pullTestFleet(t, pf)
	id := fixTestPull(t, f, "abc123", "lint")

	discarded := ""
	f.auto.discard = func(ctx context.Context, agentID string) error {
		discarded = agentID
		return nil
	}
	f.auto.runRecipe = func(ctx context.Context, name string, req RecipeRunRequest) (string, error) {
		req.OnDone(RecipeResult{AgentID: "fixer-1", Parsed: CIResult{Reproduced: true, Fixed: true}})
		return "fixer-1", nil
	}
	f.auto.diff = func(ctx context.Context, agentID, p string) (json.RawMessage, error) {
		if p == "" {
			return json.RawMessage(`{"files":[{"path":"foo_test.go","added":0,"removed":5}]}`), nil
		}
		return json.RawMessage(`{"diff":"diff --git a/foo_test.go b/foo_test.go\ndeleted file mode 100644\n--- a/foo_test.go\n+++ /dev/null\n@@ -1,5 +0,0 @@\n-x\n"}`), nil
	}
	shipped := false
	f.auto.exit = func(context.Context, string, ExitOptions) (ExitResult, error) {
		shipped = true
		return ExitResult{Destination: "push"}, nil
	}
	runFixInline(f)

	if _, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Check: "lint"}, DefaultOwnerID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if shipped {
		t.Fatal("a fix that deletes a test was pushed")
	}
	if discarded != "fixer-1" {
		t.Fatalf("the worker was not discarded: %q", discarded)
	}
	ask := pullAsk(t, f, id)
	if ask.Outcome != pullFixForbiddenChange {
		t.Fatalf("ask = %+v, want a forbidden_change ask", ask)
	}
	if !strings.Contains(ask.Finding, "foo_test.go") {
		t.Fatalf("the ask does not say what was refused: %q", ask.Finding)
	}
}

func TestStopClearsTheFix(t *testing.T) {
	pf := newPullForge(t)
	f := pullTestFleet(t, pf)
	id := fixTestPull(t, f, "abc", "lint")
	// The worker start is held back, so the fix is still on the record.
	f.auto.pullFix = func(context.Context, contract.PullRecord, contract.PullFix, contract.PullFixRequest) error {
		return nil
	}
	if _, err := f.dispatchPullFix(context.Background(), id, contract.PullFixRequest{Check: "lint"}, DefaultOwnerID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if rec, _ := f.pull(id); rec.Fix == nil {
		t.Fatal("the dispatch stored no fix")
	}

	out, err := f.stopPullFix(context.Background(), id, DefaultOwnerID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if out.Fix != nil {
		t.Fatalf("the stopped fix stayed: %+v", out.Fix)
	}
	if ask := pullAsk(t, f, id); ask.Outcome != pullFixStopped {
		t.Fatalf("ask = %+v, want a stopped ask", ask)
	}
}

// startDeferredFix dispatches a fix whose recipe run is under way and not
// finished: the run exists, and its completion has not fired. It returns the
// completion, so a test can settle the run later.
func startDeferredFix(t *testing.T, f *Fleet, id string) func(RecipeResult) {
	t.Helper()
	var done func(RecipeResult)
	f.auto.runRecipe = func(ctx context.Context, name string, req RecipeRunRequest) (string, error) {
		done = req.OnDone
		return "fixer-1", nil
	}
	runFixInline(f)
	if _, err := f.dispatchPullFix(context.Background(), id,
		contract.PullFixRequest{Check: "lint"}, DefaultOwnerID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if done == nil {
		t.Fatal("the fix run never started")
	}
	return done
}

// failingLintForge is a forge reporting one failing lint check on abc123.
func failingLintForge(t *testing.T) *pullForge {
	t.Helper()
	pf := newPullForge(t)
	pf.setChecks("abc123", map[string]any{
		"id": 412, "name": "lint", "status": "completed", "conclusion": "failure",
		"html_url":    "https://github.com/you/r/runs/412",
		"details_url": "https://github.com/you/r/actions/runs/412/job/9002",
	})
	return pf
}

func TestStopReachesTheLiveWorker(t *testing.T) {
	f := pullTestFleet(t, failingLintForge(t))
	id := fixTestPull(t, f, "abc123", "lint")
	discarded := ""
	f.auto.discard = func(ctx context.Context, agentID string) error {
		discarded = agentID
		return nil
	}
	startDeferredFix(t, f, id)

	rec, _ := f.pull(id)
	if rec.Fix == nil || rec.Fix.AgentID != "fixer-1" {
		t.Fatalf("the running fix does not name its worker: %+v", rec.Fix)
	}
	if _, err := f.stopPullFix(context.Background(), id, DefaultOwnerID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if discarded != "fixer-1" {
		t.Fatalf("stop discarded %q, want the live worker fixer-1", discarded)
	}
}

func TestAStoppedFixPushesNothing(t *testing.T) {
	f := pullTestFleet(t, failingLintForge(t))
	id := fixTestPull(t, f, "abc123", "lint")
	f.auto.discard = func(context.Context, string) error { return nil }
	shipped := false
	f.auto.exit = func(context.Context, string, ExitOptions) (ExitResult, error) {
		shipped = true
		return ExitResult{Destination: "push"}, nil
	}
	f.auto.diff = func(context.Context, string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[]}`), nil
	}
	done := startDeferredFix(t, f, id)

	if _, err := f.stopPullFix(context.Background(), id, DefaultOwnerID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The stopped worker's turn reports anyway; nothing it produced may land.
	done(RecipeResult{AgentID: "fixer-1", Parsed: CIResult{Reproduced: true, Fixed: true}})

	if shipped {
		t.Fatal("a fix stopped while it ran pushed its work")
	}
	if cur, _ := f.pull(id); cur.Fix != nil {
		t.Fatalf("the stopped fix came back: %+v", cur.Fix)
	}
	if ask := pullAsk(t, f, id); ask.Outcome != pullFixStopped {
		t.Fatalf("ask = %+v, want the stop's own ask", ask)
	}
}

func TestFixOutcomeBecomesAnAsk(t *testing.T) {
	cases := []struct {
		name string
		mode string
		res  RecipeResult
		want string
	}{
		{"not reproduced", contract.PullFixModeFix,
			RecipeResult{AgentID: "a1", Parsed: CIResult{Reproduced: false, Notes: "it passed here"}},
			pullFixNotReproduced},
		{"gave up", contract.PullFixModeFix,
			RecipeResult{AgentID: "a1", Err: "the worker crashed"}, pullFixGaveUp},
		{"investigated", contract.PullFixModeInvestigate,
			RecipeResult{AgentID: "a1", Parsed: CIResult{Reproduced: true, Cause: "a nil map"}},
			pullFixInvestigated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pf := newPullForge(t)
			f := pullTestFleet(t, pf)
			id := fixTestPull(t, f, "abc", "lint")
			fix := contract.PullFix{ID: "fix-1", Check: "lint", HeadSHA: "abc", Mode: tc.mode}
			if _, err := f.updatePull(id, func(r *contract.PullRecord) error { r.Fix = &fix; return nil }); err != nil {
				t.Fatal(err)
			}
			discarded := false
			f.auto.discard = func(context.Context, string) error { discarded = true; return nil }
			rec, _ := f.pull(id)

			f.finishPullFixRun(rec, fix, tc.res)

			if cur, _ := f.pull(id); cur.Fix != nil {
				t.Fatalf("the fix stayed on the record: %+v", cur.Fix)
			}
			ask := pullAsk(t, f, id)
			if ask.Outcome != tc.want {
				t.Fatalf("ask outcome = %q, want %q", ask.Outcome, tc.want)
			}
			if ask.Pull != id || ask.Check != "lint" || ask.Action == "" {
				t.Fatalf("ask = %+v", ask)
			}
			if !discarded {
				t.Fatal("the worker was not discarded")
			}
		})
	}
}

func TestFixIsAudited(t *testing.T) {
	pf := newPullForge(t)
	f := pullTestFleetOn(t, testFleetWithAudit(t), pf)
	id := fixTestPull(t, f, "abc", "lint")
	f.auto.pullFix = func(context.Context, contract.PullRecord, contract.PullFix, contract.PullFixRequest) error {
		return nil
	}

	if _, err := f.dispatchPullFix(context.Background(), id,
		contract.PullFixRequest{Check: "lint", Note: "the cache looks stale"}, "operator-7"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	dispatched := findEvent(auditTail(t, f), AuditPullFixDispatched)
	if dispatched == nil {
		t.Fatal("the dispatch left no audit record")
	}
	if dispatched.OwnerID != "operator-7" {
		t.Fatalf("audit owner = %q, want the caller", dispatched.OwnerID)
	}

	if _, err := f.stopPullFix(context.Background(), id, "operator-7"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if findEvent(auditTail(t, f), AuditPullFixStopped) == nil {
		t.Fatal("the stop left no audit record")
	}
}

// autoFixFleet is a fleet whose project /p holds automations, with one failing
// pull request the forge reports.
func autoFixFleet(t *testing.T, automations *Automations) (*Fleet, *pullForge, *int) {
	t.Helper()
	pf := newPullForge(t)
	pf.setOpen(7, "abc123", false)
	pf.setChecks("abc123", map[string]any{
		"id": 412, "name": "lint", "status": "completed", "conclusion": "failure",
		"html_url": "https://github.com/you/r/runs/412",
	})
	f := pullTestFleet(t, pf)
	if err := f.ws.PutProjectSettings("/p", ProjectSettings{Automations: automations}); err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	f.auto.pullFix = func(context.Context, contract.PullRecord, contract.PullFix, contract.PullFixRequest) error {
		dispatches++
		return nil
	}
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", ProjectRoot: "/p", Number: 7,
		HeadRef: "marshal/a1", BaseRef: "main", HeadSHA: "abc123",
		Origin: contract.PullOriginAgent, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return f, pf, &dispatches
}

func TestAutoFixDispatchesOncePerHead(t *testing.T) {
	f, pf, dispatches := autoFixFleet(t, &Automations{AutoFix: true})
	f.observePulls(context.Background())
	if *dispatches != 1 {
		t.Fatalf("dispatches = %d, want 1", *dispatches)
	}
	rec, _ := f.pull("r1#7")
	if rec.Fix == nil || rec.Fix.Check != "lint" {
		t.Fatalf("the automatic fix was not recorded: %+v", rec.Fix)
	}

	// The fix finishes without moving the head, and the same check still
	// fails: an automatic pass does not fix it again.
	if _, err := f.updatePull("r1#7", func(r *contract.PullRecord) error {
		r.Fix = nil
		r.ObservedAt = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.observePulls(context.Background())
	if *dispatches != 1 {
		t.Fatalf("a second pass over the same head dispatched %d fixes", *dispatches)
	}

	// A new head is a new failure, and it is fixed on its own.
	pf.setOpen(7, "def456", false)
	pf.setChecks("def456", map[string]any{
		"id": 413, "name": "lint", "status": "completed", "conclusion": "failure",
		"html_url": "https://github.com/you/r/runs/413",
	})
	if _, err := f.updatePull("r1#7", func(r *contract.PullRecord) error { r.ObservedAt = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	f.observePulls(context.Background())
	if *dispatches != 2 {
		t.Fatalf("dispatches on a new head = %d, want 2", *dispatches)
	}
}

func TestAutoFixIsOffByDefault(t *testing.T) {
	cases := map[string]*Automations{
		"no automation settings": nil,
		"another automation":     {CIFixer: &CIFixerSettings{Enabled: true, RepoID: "r1", Branches: []string{"main"}}},
	}
	for name, automations := range cases {
		t.Run(name, func(t *testing.T) {
			f, _, dispatches := autoFixFleet(t, automations)
			f.observePulls(context.Background())
			if *dispatches != 0 {
				t.Fatalf("dispatched %d fixes without the setting", *dispatches)
			}
			if rec, _ := f.pull("r1#7"); rec.Fix != nil {
				t.Fatalf("a fix was recorded without the setting: %+v", rec.Fix)
			}
		})
	}
}

func TestAutoFixLeavesFactoryPullsAlone(t *testing.T) {
	f := testFleet(t)
	if err := f.ws.PutProjectSettings("/p", ProjectSettings{
		Automations: &Automations{AutoFix: true},
	}); err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	f.auto.pullFix = func(context.Context, contract.PullRecord, contract.PullFix, contract.PullFixRequest) error {
		dispatches++
		return nil
	}
	s, item := factoryPullItem(t, 9, "abc123", []FactoryCheck{{
		Key: FactoryCheckKey{Kind: "check", Name: "test"}, SHA: "abc123",
		Status: "completed", Conclusion: "failure",
	}})
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", ProjectRoot: "/p", Number: 9,
		HeadRef: "factory/x", BaseRef: "main", HeadSHA: "abc123",
		Origin: contract.PullOriginFactory, FactoryItem: item.ID, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}
	f.SetFactoryController(NewFactoryController(s, &loopEffects{store: s}, f.now))

	f.observePulls(context.Background())

	if dispatches != 0 {
		t.Fatalf("a factory item's pull request was fixed automatically %d times", dispatches)
	}
	rec, _ := f.pull("r1#9")
	if rec.Fix != nil {
		t.Fatalf("a fix was dispatched for a factory item: %+v", rec.Fix)
	}
	// The factory's own repair is what answers its failures, and its evidence
	// is still the record's.
	if rec.State != contract.PullFailing {
		t.Fatalf("state = %q, want failing", rec.State)
	}
}
