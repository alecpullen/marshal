package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func reviewBotOn() *Automations {
	return &Automations{ReviewBot: &ReviewBotSettings{Enabled: true, RepoID: "r1"}}
}

func findingsResult(findings ...ReviewFinding) RecipeResult {
	return RecipeResult{AgentID: "agent-1", Output: OutputReviewFindings,
		Parsed: ReviewFindings{Findings: findings, Summary: "Mostly fine."}}
}

func TestReviewBotSpawnsTheRecipeForAMatchingEvent(t *testing.T) {
	a := reviewBotOn()
	a.ReviewBot.Routing = json.RawMessage(`{"profile":"cheap"}`)
	e := newAutoEnv(t, a)
	e.forge.prs[7] = prJSON(7, "Add {{x}} thing", "sha1", "feat", "bob", false)
	spy := e.spyRecipes()

	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7, headSHA: "sha1"})

	if spy.count() != 1 {
		t.Fatalf("recipe runs = %d, want 1", spy.count())
	}
	name, req := spy.last()
	if name != "review-pr" || req.Ref != "sha1" || req.RepoID != "r1" || req.Origin != OriginReviewBot || req.Project != e.root {
		t.Fatalf("name = %q, req = %+v", name, req)
	}
	if req.Inputs["pr"] != "7" || strings.Contains(req.Inputs["title"], "{{") {
		t.Errorf("inputs = %v (a title with a placeholder must be defused)", req.Inputs)
	}
	if string(req.Routing) != `{"profile":"cheap"}` {
		t.Errorf("routing = %s", req.Routing)
	}
	// The input must survive the real renderer.
	rec, _ := e.f.recipes.Get("review-pr")
	if _, err := render(rec.Prompt, req.Inputs, rec.Inputs); err != nil {
		t.Errorf("render: %v", err)
	}
}

func TestReviewBotFiltersExcludeDraftsLabelsAndAuthors(t *testing.T) {
	a := reviewBotOn()
	a.ReviewBot.SkipDrafts = true
	a.ReviewBot.Labels = []string{"review"}
	a.ReviewBot.Authors = []string{"Bob"}
	e := newAutoEnv(t, a)
	spy := e.spyRecipes()
	e.forge.prs[1] = prJSON(1, "draft", "s1", "a", "bob", true, "review")
	e.forge.prs[2] = prJSON(2, "no label", "s2", "b", "bob", false, "other")
	e.forge.prs[3] = prJSON(3, "wrong author", "s3", "c", "amy", false, "review")
	e.forge.prs[4] = prJSON(4, "ok", "s4", "d", "bob", false, "review")
	for n := 1; n <= 4; n++ {
		e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: n})
	}
	if spy.count() != 1 {
		t.Fatalf("recipe runs = %d, want only PR 4", spy.count())
	}
	if _, req := spy.last(); req.Inputs["pr"] != "4" {
		t.Fatalf("reviewed PR %s", req.Inputs["pr"])
	}
}

func TestReviewBotDoesNothingWhenOffOrWhenTheEventIsStale(t *testing.T) {
	e := newAutoEnv(t, nil)
	spy := e.spyRecipes()
	e.forge.prs[7] = prJSON(7, "t", "new", "feat", "bob", false)
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7})
	if spy.count() != 0 {
		t.Fatal("the bot ran while off")
	}
	e.setAutomations(t, reviewBotOn())
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7, headSHA: "old"})
	if spy.count() != 0 {
		t.Fatal("the bot reviewed a commit the PR has already moved past")
	}
}

func TestReviewBotDedupesOnRepoNumberAndSHA(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	e.forge.prs[7] = prJSON(7, "t", "sha1", "feat", "bob", false)
	spy := e.spyRecipes()
	ev := prEvent{repoID: "r1", number: 7, headSHA: "sha1"}

	e.f.onPREvent(t.Context(), ev)
	e.f.onPREvent(t.Context(), ev) // still running
	if spy.count() != 1 {
		t.Fatalf("a burst of events started %d runs", spy.count())
	}
	_, req := spy.last()
	req.OnDone(findingsResult())
	e.f.onPREvent(t.Context(), ev) // draft stored
	if spy.count() != 1 {
		t.Fatalf("a stored draft did not stop a repeat: %d runs", spy.count())
	}
	// A discarded draft is not reviewed again by an automatic event.
	d := e.f.ReviewDrafts("", "")[0]
	if _, err := e.f.DiscardReviewDraft(d.ID); err != nil {
		t.Fatal(err)
	}
	e.f.onPREvent(t.Context(), ev)
	if spy.count() != 1 {
		t.Fatal("an automatic event reviewed a discarded draft's commit again")
	}
	// A new commit is a new review.
	e.forge.prs[7] = prJSON(7, "t", "sha2", "feat", "bob", false)
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7, headSHA: "sha2"})
	if spy.count() != 2 {
		t.Fatalf("a new commit was not reviewed: %d runs", spy.count())
	}
}

func TestReviewBotFindingsStoreADraftAndEmit(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	e.forge.prs[7] = prJSON(7, "Add thing", "sha1", "feat", "bob", false)
	spy := e.spyRecipes()
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7})
	_, req := spy.last()
	req.OnDone(findingsResult(
		ReviewFinding{Severity: "should-fix", Path: "a.go", Line: 3, Title: "naming"},
		ReviewFinding{Severity: "nit", Title: "general"}))

	drafts := e.f.ReviewDrafts("", "")
	if len(drafts) != 1 {
		t.Fatalf("drafts = %d", len(drafts))
	}
	d := drafts[0]
	if d.Status != draftStatusDraft || d.Number != 7 || d.HeadSHA != "sha1" || d.HeadRef != "feat" ||
		d.AgentID != "agent-1" || d.Summary != "Mostly fine." || d.Project != e.root ||
		d.PRURL != "https://github.com/you/r/pull/7" {
		t.Fatalf("draft = %+v", d)
	}
	if len(d.Findings) != 2 || d.Findings[0].ID == "" || d.Findings[0].ID == d.Findings[1].ID {
		t.Fatalf("findings = %+v", d.Findings)
	}
	deltas := automationDeltas(e.f)
	if len(deltas) != 1 || deltas[0].Type != "review_draft" || deltas[0].ID != d.ID || deltas[0].Title == "" {
		t.Fatalf("deltas = %+v", deltas)
	}
	if len(e.forge.postedReviews()) != 0 {
		t.Fatal("a draft was posted without autoPost")
	}
}

func TestReviewBotFailedResultStoresAFailedDraft(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	e.forge.prs[7] = prJSON(7, "t", "sha1", "feat", "bob", false)
	spy := e.spyRecipes()
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7})
	_, req := spy.last()
	req.OnDone(RecipeResult{AgentID: "agent-1", Err: errNoStructuredResult})
	d := e.f.ReviewDrafts("", "")[0]
	if d.Status != draftStatusFailed || d.Error != errNoStructuredResult {
		t.Fatalf("draft = %+v", d)
	}
	if got := e.f.ReviewDrafts("", draftStatusDraft); len(got) != 0 {
		t.Fatalf("the status filter returned %d drafts", len(got))
	}
}

func TestReviewBotAutoPostRespectsHoldSeverities(t *testing.T) {
	a := reviewBotOn()
	a.ReviewBot.AutoPost = true
	a.ReviewBot.HoldSeverities = []string{"blocking"}
	e := newAutoEnv(t, a)
	e.forge.prs[7] = prJSON(7, "t", "sha1", "feat", "bob", false)
	e.forge.prs[8] = prJSON(8, "t", "sha8", "feat2", "bob", false)
	spy := e.spyRecipes()

	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7})
	_, req := spy.last()
	req.OnDone(findingsResult(ReviewFinding{Severity: "nit", Title: "n"}))
	if len(e.forge.postedReviews()) != 1 {
		t.Fatal("a review with no held severity was not auto-posted")
	}
	if d := e.f.ReviewDrafts("", "")[0]; d.Status != draftStatusPosted || d.ReviewURL == "" {
		t.Fatalf("draft = %+v", d)
	}

	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 8})
	_, req = spy.last()
	req.OnDone(findingsResult(ReviewFinding{Severity: "blocking", Title: "b"}))
	if len(e.forge.postedReviews()) != 1 {
		t.Fatal("a review with a blocking finding was auto-posted")
	}
}

func TestReviewBotPollFallbackDispatchesWhenThereIsNoWebhookSecret(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	e.forge.prs[7] = prJSON(7, "t", "sha1", "feat", "bob", false)
	spy := e.spyRecipes()
	repo, _ := e.f.ws.Repo("r1")

	// The first poll only sets the baseline.
	if err := e.f.pollRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if spy.count() != 0 {
		t.Fatal("the first poll reviewed every open PR")
	}
	repo.LastPolled = time.Now().Add(-time.Hour)
	if err := e.f.pollRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if spy.count() != 1 {
		t.Fatalf("poll runs = %d, want 1", spy.count())
	}
	if !e.f.pollsAutomations(repo) {
		t.Fatal("pollOnce would skip a repo with the bot on and no secret")
	}
	// With a webhook secret the poller leaves the repo to its webhooks.
	if err := e.f.secrets.Put(t.Context(), DefaultOwnerID, "hooks/r1", []byte("s")); err != nil {
		t.Fatal(err)
	}
	if e.f.pollsAutomations(repo) {
		t.Fatal("the poller still covers a repo that has a webhook secret")
	}
}

// ---- draft actions ----

func storeDraft(t *testing.T, e *autoEnv, d ReviewDraft) ReviewDraft {
	t.Helper()
	if d.ID == "" {
		d.ID = newAutoID()
	}
	if d.Status == "" {
		d.Status = draftStatusDraft
	}
	if d.RepoID == "" {
		d.RepoID, d.Number, d.HeadSHA = "r1", 7, "sha1"
	}
	d.Project = e.root
	if err := e.f.reviewDrafts().put(d.ID, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestReviewDraftPostMapsFindingsToInlineAndBodySections(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	d := storeDraft(t, e, ReviewDraft{Summary: "Looks ok.", Findings: assignFindingIDs([]DraftFinding{
		{ReviewFinding: ReviewFinding{Severity: "should-fix", Path: "a.go", Line: 3, Title: "naming", Body: "rename x"}},
		{ReviewFinding: ReviewFinding{Severity: "blocking", Title: "no tests", Body: "add some"}},
		{ReviewFinding: ReviewFinding{Severity: "nit", Title: "typo"}},
		{ReviewFinding: ReviewFinding{Severity: "nit", Path: "b.go", Title: "no line"}},
	})})
	e.f.audit = NewAuditLog(t.TempDir())

	rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d.ID+"/post", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	var got ReviewDraft
	decodeBody(t, rec, &got)
	if got.Status != draftStatusPosted || got.ReviewURL != e.forge.reviewURL {
		t.Fatalf("draft = %+v", got)
	}
	posted := e.forge.postedReviews()
	if len(posted) != 1 {
		t.Fatalf("reviews = %d", len(posted))
	}
	var body struct {
		Body     string
		Event    string
		Comments []struct {
			Path, Body string
			Line       int
		}
	}
	if err := json.Unmarshal([]byte(posted[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Event != "COMMENT" || len(body.Comments) != 1 || body.Comments[0].Path != "a.go" || body.Comments[0].Line != 3 ||
		body.Comments[0].Body != "**should-fix**: naming\n\nrename x" {
		t.Fatalf("review = %+v", body)
	}
	want := "Looks ok.\n\n**blocking**\n- no tests: add some\n\n**nit**\n- typo\n- no line"
	if body.Body != want {
		t.Fatalf("body = %q, want %q", body.Body, want)
	}
	if !hasEvent(auditTail(t, e.f), AuditReviewPosted, "") {
		t.Error("no review_posted audit record")
	}
	// A second post is refused.
	if rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d.ID+"/post", nil, nil); rec.Code != http.StatusConflict {
		t.Errorf("repost code = %d, want 409", rec.Code)
	}
}

func TestReviewDraftEditAndRefuseOnceSettled(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	d := storeDraft(t, e, ReviewDraft{Summary: "old", Findings: assignFindingIDs([]DraftFinding{
		{ReviewFinding: ReviewFinding{Severity: "nit", Title: "a"}}})})
	url := "/api/automations/review/drafts/" + d.ID

	rec := doReq(t, e.srv, http.MethodPut, url, map[string]any{
		"summary":  "new",
		"findings": []map[string]any{{"id": "f1", "severity": "nit", "title": "a"}, {"severity": "blocking", "title": "b", "path": "x.go", "line": 2}},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit code = %d: %s", rec.Code, rec.Body)
	}
	var got ReviewDraft
	decodeBody(t, rec, &got)
	if got.Summary != "new" || len(got.Findings) != 2 || got.Findings[0].ID != "f1" || got.Findings[1].ID == "" || got.Findings[1].ID == "f1" {
		t.Fatalf("draft = %+v", got)
	}
	if rec := doReq(t, e.srv, http.MethodPut, url, map[string]any{"findings": []map[string]any{{"severity": "huge", "title": "x"}}}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad severity code = %d, want 400", rec.Code)
	}
	if rec := doReq(t, e.srv, http.MethodPost, url+"/post", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("post code = %d", rec.Code)
	}
	if rec := doReq(t, e.srv, http.MethodPut, url, map[string]any{"summary": "late", "findings": []any{}}, nil); rec.Code != http.StatusConflict {
		t.Errorf("editing a posted draft code = %d, want 409", rec.Code)
	}
	if rec := doReq(t, e.srv, http.MethodGet, "/api/automations/review/drafts/ffffffffffffffff", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown draft code = %d, want 404", rec.Code)
	}
	if rec := doReq(t, e.srv, http.MethodGet, "/api/automations/review/drafts/..%2Fx", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a path-like id code = %d, want 404", rec.Code)
	}
}

func TestReviewDraftDiscardAndList(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	e.f.audit = NewAuditLog(t.TempDir())
	d1 := storeDraft(t, e, ReviewDraft{Number: 1, RepoID: "r1", HeadSHA: "a", CreatedAt: time.Now().Add(-time.Hour)})
	d2 := storeDraft(t, e, ReviewDraft{Number: 2, RepoID: "r1", HeadSHA: "b", CreatedAt: time.Now()})

	rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d1.ID+"/discard", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard code = %d", rec.Code)
	}
	if rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d1.ID+"/discard", nil, nil); rec.Code != http.StatusConflict {
		t.Errorf("second discard code = %d, want 409", rec.Code)
	}
	var list struct{ Drafts []ReviewDraft }
	decodeBody(t, doReq(t, e.srv, http.MethodGet, "/api/automations/review/drafts?status=draft", nil, nil), &list)
	if len(list.Drafts) != 1 || list.Drafts[0].ID != d2.ID {
		t.Fatalf("draft list = %+v", list.Drafts)
	}
	decodeBody(t, doReq(t, e.srv, http.MethodGet, "/api/automations/review/drafts?project="+e.root, nil, nil), &list)
	if len(list.Drafts) != 2 || list.Drafts[0].ID != d2.ID {
		t.Fatalf("project list = %+v, want newest first", list.Drafts)
	}
	decodeBody(t, doReq(t, e.srv, http.MethodGet, "/api/automations/review/drafts?project=/elsewhere", nil, nil), &list)
	if len(list.Drafts) != 0 {
		t.Fatalf("another project's list = %+v", list.Drafts)
	}
	if !hasEvent(auditTail(t, e.f), AuditReviewDiscarded, "") {
		t.Error("no review_discarded audit record")
	}
}

func TestReviewDraftSendToAuthorCreatesReviewCommentsOnTheOwningAgent(t *testing.T) {
	tr := &scriptedTransport{gate: gateResult{OK: true}}
	f := testFleetScripted(t, tr)
	f.audit = NewAuditLog(t.TempDir())
	agentID := spawnGitAgent(t, f)
	e := newAutoEnvOn(t, f, reviewBotOn())
	a, _ := f.ws.Agent(agentID)
	a.Branch, a.PRUrl = "marshal/feat", "https://github.com/you/r/pull/7"
	if err := f.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	d := storeDraft(t, e, ReviewDraft{HeadRef: "marshal/feat", PRURL: "https://github.com/you/r/pull/7",
		Findings: []DraftFinding{
			{ID: "f1", ReviewFinding: ReviewFinding{Severity: "should-fix", Path: "a.go", Line: 3, Title: "naming", Body: "rename x"}},
			{ID: "f2", ReviewFinding: ReviewFinding{Severity: "nit", Path: "b.go", Line: 9, Title: "typo"}},
			{ID: "f3", ReviewFinding: ReviewFinding{Severity: "nit", Title: "general"}},
		}})

	rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d.ID+"/send-to-author",
		map[string]any{"findingIds": []string{"f1", "f3"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	var res map[string]int
	decodeBody(t, rec, &res)
	if res["sent"] != 1 || res["skipped"] != 1 {
		t.Fatalf("result = %v (f3 has no line, f2 was not chosen)", res)
	}
	comments := f.ws.ReviewComments(agentID)
	if len(comments) != 1 || comments[0].Path != "a.go" || comments[0].Line != 3 || comments[0].Side != "new" ||
		comments[0].Quote != "" || !strings.HasPrefix(comments[0].Body, "From review bot:") {
		t.Fatalf("comments = %+v", comments)
	}
	if !hasEvent(auditTail(t, f), AuditReviewSentToAuthor, agentID) {
		t.Error("no review_sent_to_author audit record")
	}
}

func TestReviewDraftSendToAuthorWithNoMatchingAgentIs404(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	d := storeDraft(t, e, ReviewDraft{HeadRef: "someone-elses", PRURL: "https://github.com/you/r/pull/7",
		Findings: []DraftFinding{{ID: "f1", ReviewFinding: ReviewFinding{Severity: "nit", Path: "a.go", Line: 1, Title: "t"}}}})
	rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d.ID+"/send-to-author",
		map[string]any{"findingIds": []string{"f1"}}, nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no Marshal agent owns this PR") {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestReviewBotRunRouteDispatchesAtTheCurrentHeadAndIs409WhenOff(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.forge.prs[7] = prJSON(7, "t", "headnow", "feat", "bob", true)
	got := make(chan prEvent, 1)
	e.f.auto.onPR = func(_ context.Context, ev prEvent) { got <- ev }
	body := map[string]any{"repoId": "r1", "number": 7}

	if rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/run", body, nil); rec.Code != http.StatusConflict {
		t.Fatalf("off: code = %d, want 409", rec.Code)
	}
	e.setAutomations(t, reviewBotOn())
	if rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/run", body, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("on: code = %d, want 202", rec.Code)
	}
	select {
	case ev := <-got:
		if ev.repoID != "r1" || ev.number != 7 || ev.headSHA != "headnow" || !ev.manual {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the run route dispatched nothing")
	}
	if rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/run", map[string]any{"repoId": "r1"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing number: code = %d, want 400", rec.Code)
	}
}

func TestReviewBotManualRunSkipsFiltersAndMayRedoADiscardedDraft(t *testing.T) {
	a := reviewBotOn()
	a.ReviewBot.SkipDrafts = true
	e := newAutoEnv(t, a)
	e.forge.prs[7] = prJSON(7, "t", "sha1", "feat", "bob", true)
	spy := e.spyRecipes()
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7, manual: true})
	if spy.count() != 1 {
		t.Fatal("a manual run was filtered as a draft PR")
	}
	_, req := spy.last()
	req.OnDone(findingsResult())
	d := e.f.ReviewDrafts("", "")[0]
	if _, err := e.f.DiscardReviewDraft(d.ID); err != nil {
		t.Fatal(err)
	}
	e.f.onPREvent(t.Context(), prEvent{repoID: "r1", number: 7, manual: true})
	if spy.count() != 2 {
		t.Fatal("an operator could not re-run a discarded review")
	}
}

func TestReviewDraftPostFallsBackToABodyOnlyReviewOn422(t *testing.T) {
	e := newAutoEnv(t, reviewBotOn())
	e.forge.rejectInline = true
	d := storeDraft(t, e, ReviewDraft{Summary: "Looks ok.", Findings: assignFindingIDs([]DraftFinding{
		{ReviewFinding: ReviewFinding{Severity: "should-fix", Path: "gone.go", Line: 99, Title: "naming", Body: "rename x"}},
		{ReviewFinding: ReviewFinding{Severity: "nit", Title: "typo"}},
	})})
	rec := doReq(t, e.srv, http.MethodPost, "/api/automations/review/drafts/"+d.ID+"/post", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	posted := e.forge.postedReviews()
	if len(posted) != 1 {
		t.Fatalf("reviews = %d", len(posted))
	}
	var body struct {
		Body     string
		Comments []any
	}
	if err := json.Unmarshal([]byte(posted[0]), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Comments) != 0 || !strings.Contains(body.Body, "`gone.go:99` naming: rename x") || !strings.Contains(body.Body, "- typo") {
		t.Fatalf("fallback review = %+v", body)
	}
	if got := e.f.ReviewDrafts("", draftStatusPosted); len(got) != 1 {
		t.Fatal("the draft was not marked posted after the fallback")
	}
}
