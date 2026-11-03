package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/sddreview"
	"marshal/internal/sddplans"
)

// fakeAuthorPlan is the runtime's authoring entry point: it resolves the draft
// path the session would use, writes a valid executable candidate there and
// reports the inspection the review panel shows.
func fakeAuthorPlan(state *session.State) func(context.Context, acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
	return func(_ context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
		path, err := sddplans.DraftPath(state.WorkingDir, state.Config.SDD.PlansDir, params.Goal, time.Now())
		if err != nil {
			return acpwire.AuthoredPlan{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return acpwire.AuthoredPlan{}, err
		}
		if err := os.WriteFile(path, []byte("# Plan\n\n## Task 1: Add file\n\n"+
			"```marshal.file path=\"created.txt\"\nhello\n```\n"), 0o644); err != nil {
			return acpwire.AuthoredPlan{}, err
		}
		return acpwire.AuthoredPlan{
			PlanPath: path,
			Inspection: &acpwire.PlanInspection{
				Strategy:  acpwire.StrategyAdaptive,
				HasBlocks: true,
				DetOps:    1,
				Tasks:     []acpwire.PlanTask{{TaskN: 1, Title: "Add file", DetOps: 1, EstCalls: 1}},
			},
		}, nil
	}
}

// authoringModel builds a sized model whose session authors a plan on the run
// path, and returns it with the recorder that saw the call.
func authoringModel(t *testing.T) (Model, *turnRecorder) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:  state,
		Runner: runnerOf(&testAgentRunner{}),
		PlanPreflight: preflightOf(acpwire.PlanPreflight{
			Inspection: acpwire.PlanInspection{
				Strategy:  acpwire.StrategyAdaptive,
				HasBlocks: true,
				DetOps:    1,
				Tasks:     []acpwire.PlanTask{{TaskN: 1, Title: "Add file", DetOps: 1, EstCalls: 1}},
			},
		}),
		AuthorPlan: fakeAuthorPlan(state),
	}, rec, WithCommandRegistry(setupCmdReg(t)))
	m.resize(100, 40)
	return m, rec
}

// newAuthoringModel is authoringModel's model alone, for the callers that only
// need the model.
func newAuthoringModel(t *testing.T) Model {
	t.Helper()
	m, _ := authoringModel(t)
	return m
}

// runAuthoringCmd executes the batch returned by /sdd new and feeds the
// planAuthorFinishedMsg back into the model, returning the updated model.
func runAuthoringCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		if res := sub(); res != nil {
			if _, ok := res.(planAuthorFinishedMsg); ok {
				updated, _ := m.Update(res)
				return asModel(t, updated)
			}
		}
	}
	t.Fatal("no planAuthorFinishedMsg produced by authoring command")
	return m
}

func TestSDDNewStartsAuthoringNotRun(t *testing.T) {
	m := newAuthoringModel(t)
	updated, cmd := m.dispatchCommand("/sdd new add marker file")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd for /sdd new")
	}
	if !m.busy {
		t.Fatal("model should be busy during authoring")
	}
	m = runAuthoringCmd(t, m, cmd)
	if m.busy {
		t.Fatal("model should not be busy after authoring completes")
	}
	if _, ok := m.dock.Panel().(*sddreview.Panel); !ok {
		t.Fatalf("expected *sddreview.Panel, got %T", m.dock.Panel())
	}
}

func TestSDDNewAcceptClosesReviewAndKeepsArtifact(t *testing.T) {
	m := newAuthoringModel(t)
	updated, cmd := m.dispatchCommand("/sdd new add marker file")
	m = asModel(t, updated)
	m = runAuthoringCmd(t, m, cmd)
	panel := m.dock.Panel().(*sddreview.Panel)
	path := panel.CandidatePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("candidate artifact should exist after authoring: %v", err)
	}
	// Accept closes review and keeps the artifact.
	updated, _ = m.Update(sddreview.AcceptMsg{Panel: panel})
	m = asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("dock should be closed after accept")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("accepted candidate must remain on disk: %v", err)
	}
}

// authoredReview runs /sdd new to completion and returns the open review panel
// with its candidate path.
func authoredReview(t *testing.T, m Model) (Model, *sddreview.Panel, string) {
	t.Helper()
	updated, cmd := m.dispatchCommand("/sdd new add marker file")
	m = asModel(t, updated)
	m = runAuthoringCmd(t, m, cmd)
	panel, ok := m.dock.Panel().(*sddreview.Panel)
	if !ok {
		t.Fatalf("expected *sddreview.Panel, got %T", m.dock.Panel())
	}
	return m, panel, panel.CandidatePath()
}

// TestCandidatePhraseNamesTheCandidateSafely pins the transcript wording: the
// path is escaped, and a review that carried no path does not trail off into a
// dangling "at .".
func TestCandidatePhraseNamesTheCandidateSafely(t *testing.T) {
	generic := candidatePhrase("")
	if strings.Contains(generic, " at ") || strings.HasSuffix(generic, " .") {
		t.Errorf("empty path phrase = %q, want a phrase that names no path", generic)
	}
	if got := candidatePhrase("/repo/plan.md"); got != "the generated plan candidate at /repo/plan.md" {
		t.Errorf("phrase = %q, want the path named", got)
	}
	if got := candidatePhrase("/repo/\x1b[31mevil.md"); strings.Contains(got, "\x1b") {
		t.Errorf("phrase = %q, want control characters escaped", got)
	}
}

func TestSDDNewKeepClosesReviewAndKeepsCandidate(t *testing.T) {
	m := newAuthoringModel(t)
	m, panel, path := authoredReview(t, m)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("candidate artifact should exist after authoring: %v", err)
	}

	updated, _ := m.Update(sddreview.KeepMsg{Panel: panel})
	m = asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("dock should be closed after keep")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("kept candidate must remain on disk: %v", err)
	}

	// The transcript has to say what was kept and what to do next.
	var kept string
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, path) {
			kept = msg.Content
		}
	}
	if kept == "" {
		t.Fatalf("no transcript message named the retained candidate %q", path)
	}
	if !strings.Contains(kept, "/sdd") {
		t.Errorf("kept message must name the next step: %q", kept)
	}
}

func TestSDDNewDiscardRemovesOnlyCandidate(t *testing.T) {
	m := newAuthoringModel(t)
	m, panel, path := authoredReview(t, m)
	sentinel := filepath.Join(filepath.Dir(path), "keep-me.md")
	if err := os.WriteFile(sentinel, []byte("# sibling\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// The panel only acts on a key pressed in a frame that showed the
	// candidate and the confirmation state.
	m.viewString()
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("first d must only arm, got %T", cmd())
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("second consecutive d must confirm the discard")
	}
	discarded, ok := cmd().(sddreview.DiscardMsg)
	if !ok {
		t.Fatalf("msg = %T, want sddreview.DiscardMsg", cmd())
	}
	if discarded.Panel != panel {
		t.Fatalf("DiscardMsg.Panel = %p, want the docked panel %p", discarded.Panel, panel)
	}

	updated, _ := m.Update(discarded)
	m = asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("dock should be closed after a successful discard")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("discarded candidate should be removed, got %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("discard must not touch a sibling plan: %v", err)
	}
}

// TestDiscardFailureKeepsPanelAndCandidate proves a failed removal is reported
// rather than swallowed: the panel stays open, the path is untouched, and the
// frame says what went wrong so the user can retry or keep.
func TestDiscardFailureKeepsPanelAndCandidate(t *testing.T) {
	m := newAuthoringModel(t)
	m, panel, path := authoredReview(t, m)
	sentinel := filepath.Join(filepath.Dir(path), "keep-me.md")
	if err := os.WriteFile(sentinel, []byte("# sibling\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	// RemoveCandidate rejects a path that is not a regular file, so the
	// removal fails while something still occupies the candidate path.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove authored candidate: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("occupy the candidate path: %v", err)
	}
	snapshot := len(m.state.Messages())
	m.viewString()

	m.Update(tea.KeyPressMsg{Code: 'd'})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("second consecutive d must confirm the discard")
	}
	updated, _ := m.Update(cmd())
	m = asModel(t, updated)

	if !m.dock.IsOpen() {
		t.Fatal("a failed discard must keep the panel open")
	}
	if m.dock.Panel() != panel {
		t.Fatalf("dock panel = %T, want the same review", m.dock.Panel())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed discard must leave the path in place: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("failed discard must not touch a sibling plan: %v", err)
	}
	view := stripANSI(panel.View(100, 20))
	if !strings.Contains(view, "discard failed") {
		t.Fatalf("the panel must report the failure it hit:\n%s", view)
	}
	for _, msg := range m.state.Messages()[snapshot:] {
		if strings.Contains(msg.Content, "Discarded") {
			t.Fatalf("a failed discard must not claim removal: %q", msg.Content)
		}
	}
}

// TestStaleReviewMessagesCannotAffectReplacement proves a queued message from
// a replaced review cannot act on the one now docked.
func TestStaleReviewMessagesCannotAffectReplacement(t *testing.T) {
	m := newAuthoringModel(t)
	m, stale, stalePath := authoredReview(t, m)

	// A different review now owns the dock. Its candidate lives in its own
	// temp plans directory, so removing it would be visible.
	replacementPath := filepath.Join(t.TempDir(), "live.md")
	if err := os.WriteFile(replacementPath, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write replacement candidate: %v", err)
	}
	m.dock.Open(sddreview.New(acpwire.AuthoredPlan{PlanPath: replacementPath}))
	if live, ok := m.dock.Panel().(*sddreview.Panel); !ok || live == stale {
		t.Fatalf("fixture: the replacement review must be a different panel, got %T", m.dock.Panel())
	}

	// A delayed keep and a delayed discard from the replaced review.
	updated, _ := m.Update(sddreview.KeepMsg{Panel: stale})
	m = asModel(t, updated)
	if !m.dock.IsOpen() {
		t.Fatal("a stale keep must not close the replacement review")
	}
	updated, _ = m.Update(sddreview.DiscardMsg{Panel: stale})
	m = asModel(t, updated)
	if !m.dock.IsOpen() {
		t.Fatal("a stale discard must not close the replacement review")
	}
	for _, path := range []string{stalePath, replacementPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a stale message must not touch %q: %v", path, err)
		}
	}
	// A stale accept must not close it either.
	updated, _ = m.Update(sddreview.AcceptMsg{Panel: stale})
	m = asModel(t, updated)
	if !m.dock.IsOpen() {
		t.Fatal("a stale accept must not close the replacement review")
	}
}

// TestDiscardHiddenOrResizedCannotConfirm drives the discard gate through the
// parent model: a hidden frame consumes the key, a resize disarms, and the
// removal only happens after a fresh visible two-press confirmation.
func TestDiscardHiddenOrResizedCannotConfirm(t *testing.T) {
	m := newAuthoringModel(t)
	m, _, path := authoredReview(t, m)

	// A supported geometry that has actually rendered the review.
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.viewString()
	m, _ = press(m, tea.KeyPressMsg{Code: 'd'}) // arm

	// Below the supported minimum the frame is hidden: the key is consumed
	// before the dock can confirm.
	m, _ = press(m, tea.WindowSizeMsg{Width: 60, Height: 15})
	m, cmd := press(m, tea.KeyPressMsg{Code: 'd'})
	if cmd != nil {
		t.Fatalf("a hidden review must not arm or confirm, got %T", cmd())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("hidden geometry must not remove the candidate: %v", err)
	}

	// Back at a supported size the old arm is gone, and no render has
	// happened yet, so the key still cannot act.
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, cmd = press(m, tea.KeyPressMsg{Code: 'd'})
	if cmd != nil {
		t.Fatalf("a resized review must not confirm before it renders again, got %T", cmd())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a resized review must not remove the candidate: %v", err)
	}

	// A fresh visible render earns the permission again.
	m.viewString()
	m, _ = press(m, tea.KeyPressMsg{Code: 'd'})
	m, cmd = press(m, tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("a freshly rendered visible review must still be able to confirm")
	}
	discarded, ok := cmd().(sddreview.DiscardMsg)
	if !ok {
		t.Fatalf("msg = %T, want sddreview.DiscardMsg", cmd())
	}
	m, _ = press(m, discarded)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the visible confirmation should have removed the candidate, got %v", err)
	}
}

func TestSDDNewFromLastPlanUsesFinalAssistantMessage(t *testing.T) {
	m, rec := authoringModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "approved plan content", session.ContentTypePlain)
	updated, cmd := m.dispatchCommand("/sdd new --from-last-plan")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd for /sdd new --from-last-plan")
	}
	if !m.busy {
		t.Fatal("model should be busy during authoring")
	}
	m = runAuthoringCmd(t, m, cmd)
	if len(rec.authors) != 1 {
		t.Fatalf("AuthorPlan calls = %+v, want one", rec.authors)
	}
	want := acpwire.AuthorPlanParams{
		Goal:   "Convert the approved plan into an executable SDD plan.",
		Design: "approved plan content",
	}
	if rec.authors[0] != want {
		t.Fatalf("AuthorPlan params = %+v, want %+v", rec.authors[0], want)
	}
}

func TestSDDNewFromLastPlanWithoutPlanErrors(t *testing.T) {
	m := newAuthoringModel(t)
	updated, cmd := m.dispatchCommand("/sdd new --from-last-plan")
	m = asModel(t, updated)
	if cmd != nil {
		t.Fatal("expected nil cmd when no final assistant plan exists")
	}
	if m.busy {
		t.Fatal("model should not be busy when authoring cannot start")
	}
	msgs := m.state.Messages()
	found := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "No approved plan found") {
			found = true
		}
	}
	if !found {
		t.Error("expected a clear system error when no plan exists")
	}
}
