package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Review draft statuses.
const (
	draftStatusDraft     = "draft"
	draftStatusPosted    = "posted"
	draftStatusDiscarded = "discarded"
	draftStatusFailed    = "failed"
)

var (
	// errReviewBotOff is returned when no project enables the review bot
	// for a repo. HTTP maps it to 409.
	errReviewBotOff = errors.New("bridge: the review bot is off for this repo")
	// errDraftSettled is returned when an edit, post or discard targets a
	// draft that is no longer a draft. HTTP maps it to 409.
	errDraftSettled = errors.New("bridge: the review draft has already been posted, discarded or failed")
	// errNoAuthorAgent is returned when no Marshal agent owns the PR.
	errNoAuthorAgent = errors.New("bridge: no Marshal agent owns this PR")
	errInvalidDraft  = errors.New("bridge: invalid review draft")
)

// DraftFinding is a ReviewFinding with a stable id, so the UI can pick
// findings to send to the author.
type DraftFinding struct {
	ID string `json:"id"`
	ReviewFinding
}

// ReviewDraft is a stored review awaiting a human.
type ReviewDraft struct {
	ID        string         `json:"id"`
	RepoID    string         `json:"repoId"`
	Number    int            `json:"number"`
	Title     string         `json:"title,omitempty"`
	PRURL     string         `json:"prUrl,omitempty"`
	HeadRef   string         `json:"headRef,omitempty"`
	HeadSHA   string         `json:"headSha"`
	Project   string         `json:"project,omitempty"`
	AgentID   string         `json:"agentId,omitempty"`
	Findings  []DraftFinding `json:"findings"`
	Summary   string         `json:"summary"`
	Status    string         `json:"status"`
	Error     string         `json:"error,omitempty"`
	ReviewURL string         `json:"reviewUrl,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// assignFindingIDs gives every finding a unique id, keeping those it has.
func assignFindingIDs(in []DraftFinding) []DraftFinding {
	used := map[string]bool{}
	for _, d := range in {
		if d.ID != "" {
			used[d.ID] = true
		}
	}
	out := make([]DraftFinding, len(in))
	next := 1
	for i, d := range in {
		if d.ID == "" || (used[d.ID] && containsDup(in[:i], d.ID)) {
			for {
				id := "f" + strconv.Itoa(next)
				next++
				if !used[id] {
					d.ID = id
					used[id] = true
					break
				}
			}
		}
		out[i] = d
	}
	return out
}

func containsDup(prev []DraftFinding, id string) bool {
	for _, p := range prev {
		if p.ID == id {
			return true
		}
	}
	return false
}

// sanitizeInput keeps a value usable as a recipe input: the renderer
// refuses "{{", and a PR title is the author's text.
func sanitizeInput(s string) string { return strings.ReplaceAll(s, "{{", "{ {") }

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func hasAny(list []string, wanted []string) bool {
	for _, w := range wanted {
		if containsString(list, w) {
			return true
		}
	}
	return false
}

// onPREvent starts a review for a PR event, if the project's review bot is
// on and the PR passes its filters. Failures are logged: an event has no
// caller to tell.
func (f *Fleet) onPREvent(ctx context.Context, ev prEvent) {
	if err := f.reviewPR(ctx, ev); err != nil && !errors.Is(err, errReviewBotOff) {
		slog.Default().Warn("webbridge: review bot", "repo", ev.repoID, "pr", ev.number, "err", err)
	}
}

// reviewPR is onPREvent's body. It returns nil for an event the filters or
// de-duplication drop, and errReviewBotOff when no project wants one.
func (f *Fleet) reviewPR(ctx context.Context, ev prEvent) error {
	root, rb, ok := f.reviewBotSettings(ev.repoID)
	if !ok {
		return errReviewBotOff
	}
	repo, ok := f.ws.Repo(ev.repoID)
	if !ok {
		return ErrUnknownRepo
	}
	forge, cred, err := f.forgeFor(ctx, repo)
	if err != nil {
		return err
	}
	pr, err := forge.GetPR(ctx, repo, ev.number, cred)
	if err != nil {
		f.noteRateLimit(repo.ID, err)
		return err
	}
	// A newer push has its own event; reviewing this one would be stale.
	if ev.headSHA != "" && pr.HeadSHA != "" && ev.headSHA != pr.HeadSHA {
		return nil
	}
	if !ev.manual {
		if rb.SkipDrafts && pr.Draft {
			return nil
		}
		if len(rb.Labels) > 0 && !hasAny(pr.Labels, rb.Labels) {
			return nil
		}
		if len(rb.Authors) > 0 && !containsFold(rb.Authors, pr.Author) {
			return nil
		}
	}
	if pr.HeadSHA == "" {
		return fmt.Errorf("PR %d has no head commit", pr.Number)
	}

	key := fmt.Sprintf("review:%s:%d:%s", repo.ID, pr.Number, pr.HeadSHA)
	if !f.autoClaim(key) {
		return nil
	}
	released := false
	release := func() {
		if !released {
			released = true
			f.autoRelease(key)
		}
	}
	defer func() { release() }()
	// An automatic event never repeats a review, a discarded one included;
	// an operator asking again may redo a discarded or failed one.
	for _, d := range f.reviewDrafts().all() {
		if d.RepoID != repo.ID || d.Number != pr.Number || d.HeadSHA != pr.HeadSHA {
			continue
		}
		if !ev.manual || d.Status == draftStatusDraft || d.Status == draftStatusPosted {
			return nil
		}
	}

	_, err = f.autoRunRecipe(ctx, "review-pr", RecipeRunRequest{
		Project: root, RepoID: repo.ID, Ref: pr.HeadSHA,
		Inputs:  map[string]string{"pr": strconv.Itoa(pr.Number), "title": sanitizeInput(pr.Title)},
		Origin:  OriginReviewBot,
		Routing: rb.Routing,
		OnDone: func(res RecipeResult) {
			defer f.autoRelease(key)
			f.finishReview(repo, root, rb, pr, res)
		},
	})
	if err != nil {
		return err
	}
	// The run is under way; OnDone releases the claim.
	released = true
	return nil
}

// finishReview stores the draft a review run produced and, when allowed,
// posts it.
func (f *Fleet) finishReview(repo Repo, root string, rb *ReviewBotSettings, pr PRInfo, res RecipeResult) {
	d := ReviewDraft{
		ID: newAutoID(), RepoID: repo.ID, Number: pr.Number, Title: pr.Title, PRURL: pr.URL,
		HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA, Project: root, AgentID: res.AgentID,
		Findings: []DraftFinding{}, Status: draftStatusDraft, CreatedAt: f.now(),
	}
	rf, parsed := res.Parsed.(ReviewFindings)
	switch {
	case res.Err != "":
		d.Status, d.Error = draftStatusFailed, res.Err
	case !parsed:
		d.Status, d.Error = draftStatusFailed, errNoStructuredResult
	default:
		d.Summary = rf.Summary
		for _, fd := range rf.Findings {
			d.Findings = append(d.Findings, DraftFinding{ReviewFinding: fd})
		}
		d.Findings = assignFindingIDs(d.Findings)
	}
	if err := f.reviewDrafts().put(d.ID, d); err != nil {
		slog.Default().Warn("webbridge: store review draft", "repo", repo.ID, "pr", pr.Number, "err", err)
		return
	}
	title := fmt.Sprintf("Review ready: PR #%d", pr.Number)
	text := fmt.Sprintf("The review of PR #%d (%s) has %d finding(s) waiting for you.", pr.Number, pr.Title, len(d.Findings))
	if d.Status == draftStatusFailed {
		title = fmt.Sprintf("Review failed: PR #%d", pr.Number)
		text = fmt.Sprintf("The review of PR #%d (%s) did not produce findings: %s", pr.Number, pr.Title, d.Error)
	}
	f.emitAutomation("review_draft", d.ID, d.AgentID, d.Status, title, text)

	if d.Status == draftStatusDraft && rb.AutoPost && !holdsBack(d.Findings, rb.HoldSeverities) {
		ctx, cancel := f.autoContext()
		defer cancel()
		if _, err := f.PostReviewDraft(ctx, d.ID); err != nil {
			slog.Default().Warn("webbridge: auto-post review", "draft", d.ID, "err", err)
		}
	}
}

func holdsBack(findings []DraftFinding, hold []string) bool {
	for _, fd := range findings {
		if containsString(hold, fd.Severity) {
			return true
		}
	}
	return false
}

// pollReviews is the polling fallback: the PRs updated since the last poll
// go through onPREvent as if a webhook had said so.
func (f *Fleet) pollReviews(ctx context.Context, r Repo, forge Forge, cred Credential) error {
	// The first poll only sets the baseline: switching the bot on must not
	// review every PR that is already open.
	if r.LastPolled.IsZero() {
		return nil
	}
	prs, err := forge.ListPRs(ctx, r, PRQuery{State: "open", Since: r.LastPolled}, cred)
	if err != nil {
		f.noteRateLimit(r.ID, err)
		return err
	}
	for _, pr := range prs {
		if err := f.reviewPR(ctx, prEvent{repoID: r.ID, number: pr.Number, headSHA: pr.HeadSHA}); err != nil && !errors.Is(err, errReviewBotOff) {
			slog.Default().Warn("webbridge: review bot poll", "repo", r.ID, "pr", pr.Number, "err", err)
		}
	}
	return nil
}

// ReviewDrafts lists drafts, newest first, optionally for one project and
// status.
func (f *Fleet) ReviewDrafts(project, status string) []ReviewDraft {
	out := []ReviewDraft{}
	for _, d := range f.reviewDrafts().all() {
		if (project == "" || d.Project == project) && (status == "" || d.Status == status) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// EditReviewDraft replaces a draft's findings and summary.
func (f *Fleet) EditReviewDraft(id string, findings []DraftFinding, summary string) (ReviewDraft, error) {
	for _, fd := range findings {
		if !validSeverities[fd.Severity] {
			return ReviewDraft{}, fmt.Errorf("%w: severity %q is not blocking, should-fix or nit", errInvalidDraft, fd.Severity)
		}
		if strings.TrimSpace(fd.Title) == "" {
			return ReviewDraft{}, fmt.Errorf("%w: every finding needs a title", errInvalidDraft)
		}
		if fd.Line < 0 {
			return ReviewDraft{}, fmt.Errorf("%w: line cannot be negative", errInvalidDraft)
		}
	}
	return f.reviewDrafts().update(id, func(d *ReviewDraft) error {
		if d.Status != draftStatusDraft {
			return errDraftSettled
		}
		d.Findings = assignFindingIDs(append([]DraftFinding{}, findings...))
		d.Summary = summary
		return nil
	})
}

// DiscardReviewDraft marks a draft discarded.
func (f *Fleet) DiscardReviewDraft(id string) (ReviewDraft, error) {
	d, err := f.reviewDrafts().update(id, func(d *ReviewDraft) error {
		if d.Status != draftStatusDraft && d.Status != draftStatusFailed {
			return errDraftSettled
		}
		d.Status = draftStatusDiscarded
		return nil
	})
	if err == nil {
		f.auditf(AuditEvent{Event: AuditReviewDiscarded, OwnerID: DefaultOwnerID, RepoID: d.RepoID, Detail: "pr " + strconv.Itoa(d.Number)})
	}
	return d, err
}

// buildReview turns a draft into the forge's review: findings with a path
// and line become inline comments, the rest fold into the body by severity.
func buildReview(d ReviewDraft) Review {
	var body strings.Builder
	body.WriteString(strings.TrimSpace(d.Summary))
	var loose []DraftFinding
	var inline []ReviewLine
	for _, fd := range d.Findings {
		if fd.Path != "" && fd.Line > 0 {
			text := fmt.Sprintf("**%s**: %s", fd.Severity, fd.Title)
			if fd.Body != "" {
				text += "\n\n" + fd.Body
			}
			inline = append(inline, ReviewLine{Path: fd.Path, Line: fd.Line, Body: text})
			continue
		}
		loose = append(loose, fd)
	}
	for _, sev := range []string{"blocking", "should-fix", "nit"} {
		var items []string
		for _, fd := range loose {
			if fd.Severity != sev {
				continue
			}
			item := "- " + fd.Title
			if fd.Body != "" {
				item += ": " + fd.Body
			}
			items = append(items, item)
		}
		if len(items) == 0 {
			continue
		}
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString("**" + sev + "**\n" + strings.Join(items, "\n"))
	}
	return Review{Body: body.String(), Comments: inline}
}

// PostReviewDraft posts a draft to the forge and records its URL.
func (f *Fleet) PostReviewDraft(ctx context.Context, id string) (ReviewDraft, error) {
	d, err := f.reviewDrafts().get(id)
	if err != nil {
		return d, err
	}
	if d.Status != draftStatusDraft {
		return d, errDraftSettled
	}
	// One post at a time per draft, so a double click cannot post twice.
	key := "post:" + id
	if !f.autoClaim(key) {
		return d, errDraftSettled
	}
	defer f.autoRelease(key)

	repo, ok := f.ws.Repo(d.RepoID)
	if !ok {
		return d, ErrUnknownRepo
	}
	forge, cred, err := f.forgeFor(ctx, repo)
	if err != nil {
		return d, err
	}
	url, err := forge.PostReview(ctx, repo, d.Number, buildReview(d), cred)
	if err != nil {
		return d, err
	}
	d, err = f.reviewDrafts().update(id, func(d *ReviewDraft) error {
		d.Status, d.ReviewURL = draftStatusPosted, url
		return nil
	})
	if err != nil {
		return d, fmt.Errorf("review posted but recording it failed: %w", err)
	}
	f.auditf(AuditEvent{Event: AuditReviewPosted, OwnerID: DefaultOwnerID, RepoID: d.RepoID, Detail: "pr " + strconv.Itoa(d.Number)})
	return d, nil
}

// SendReviewToAuthor delivers the chosen findings, as review comments, to
// the Marshal agent that owns the PR. It returns how many were sent and
// how many lacked a path or line and so could not be.
func (f *Fleet) SendReviewToAuthor(ctx context.Context, id string, findingIDs []string) (sent, skipped int, err error) {
	d, err := f.reviewDrafts().get(id)
	if err != nil {
		return 0, 0, err
	}
	var author Agent
	found := false
	for _, a := range f.ws.Agents() {
		if d.HeadRef != "" && a.Branch == d.HeadRef && d.PRURL != "" && a.PRUrl == d.PRURL {
			author, found = a, true
			break
		}
	}
	if !found {
		return 0, 0, errNoAuthorAgent
	}
	want := map[string]bool{}
	for _, fid := range findingIDs {
		want[fid] = true
	}
	for _, fd := range d.Findings {
		if !want[fd.ID] {
			continue
		}
		if fd.Path == "" || fd.Line <= 0 {
			skipped++
			continue
		}
		body := "From review bot: **" + fd.Severity + "**: " + fd.Title
		if fd.Body != "" {
			body += "\n\n" + fd.Body
		}
		if _, err := f.AddReviewComment(ctx, author.ID, ReviewComment{Path: fd.Path, Line: fd.Line, Side: "new", Body: body}); err != nil {
			return sent, skipped, err
		}
		sent++
	}
	f.auditf(AuditEvent{Event: AuditReviewSentToAuthor, OwnerID: DefaultOwnerID, AgentID: author.ID, RepoID: d.RepoID, Detail: "pr " + strconv.Itoa(d.Number)})
	return sent, skipped, nil
}

// RunReviewBot runs the bot on a PR now, at the PR's current head. It
// returns errReviewBotOff when no project wants reviews for the repo.
func (f *Fleet) RunReviewBot(ctx context.Context, repoID string, number int) error {
	if _, _, ok := f.reviewBotSettings(repoID); !ok {
		return errReviewBotOff
	}
	repo, ok := f.ws.Repo(repoID)
	if !ok {
		return ErrUnknownRepo
	}
	forge, cred, err := f.forgeFor(ctx, repo)
	if err != nil {
		return err
	}
	pr, err := forge.GetPR(ctx, repo, number, cred)
	if err != nil {
		return err
	}
	ev := prEvent{repoID: repoID, number: number, headSHA: pr.HeadSHA, manual: true}
	go func() {
		bg, cancel := f.autoContext()
		defer cancel()
		if f.auto.onPR != nil {
			f.auto.onPR(bg, ev)
			return
		}
		f.onPREvent(bg, ev)
	}()
	return nil
}

// ---- HTTP ----

func (s *Server) automationRoutes() {
	s.mux.HandleFunc("GET /api/automations/review/drafts", s.listReviewDrafts)
	s.mux.HandleFunc("GET /api/automations/review/drafts/{id}", s.getReviewDraft)
	s.mux.HandleFunc("PUT /api/automations/review/drafts/{id}", s.editReviewDraft)
	s.mux.HandleFunc("POST /api/automations/review/drafts/{id}/post", s.postReviewDraft)
	s.mux.HandleFunc("POST /api/automations/review/drafts/{id}/discard", s.discardReviewDraft)
	s.mux.HandleFunc("POST /api/automations/review/drafts/{id}/send-to-author", s.sendReviewToAuthor)
	s.mux.HandleFunc("POST /api/automations/review/run", s.runReviewBot)
	s.mux.HandleFunc("GET /api/automations/ci/history", s.listCIHistory)
	s.mux.HandleFunc("GET /api/automations/ci/history/{id}", s.getCIHistory)
	s.mux.HandleFunc("POST /api/repos/{id}/webhook-secret", s.issueWebhookSecret)
}

// writeAutomationErr maps the automations' errors onto status codes and
// leaves the rest to writeErr.
func writeAutomationErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnknownAutomation), errors.Is(err, errNoAuthorAgent), errors.Is(err, ErrUnknownRepo):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errDraftSettled), errors.Is(err, errReviewBotOff):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, errInvalidDraft):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, errNoForge):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeErr(w, err)
	}
}

func (s *Server) listReviewDrafts(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{"drafts": s.fleet.ReviewDrafts(q.Get("project"), q.Get("status"))})
}

func (s *Server) getReviewDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	d, err := s.fleet.reviewDrafts().get(r.PathValue("id"))
	if err != nil {
		writeAutomationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) editReviewDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body struct {
		Findings []DraftFinding `json:"findings"`
		Summary  string         `json:"summary"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	d, err := s.fleet.EditReviewDraft(r.PathValue("id"), body.Findings, body.Summary)
	if err != nil {
		writeAutomationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) postReviewDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	d, err := s.fleet.PostReviewDraft(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAutomationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) discardReviewDraft(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	d, err := s.fleet.DiscardReviewDraft(r.PathValue("id"))
	if err != nil {
		writeAutomationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) sendReviewToAuthor(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body struct {
		FindingIDs []string `json:"findingIds"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	sent, skipped, err := s.fleet.SendReviewToAuthor(r.Context(), r.PathValue("id"), body.FindingIDs)
	if err != nil {
		writeAutomationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent, "skipped": skipped})
}

func (s *Server) runReviewBot(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body struct {
		RepoID string `json:"repoId"`
		Number int    `json:"number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RepoID == "" || body.Number <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repoId and number are required"})
		return
	}
	if err := s.fleet.RunReviewBot(r.Context(), body.RepoID, body.Number); err != nil {
		writeAutomationErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
