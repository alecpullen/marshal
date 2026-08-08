package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/sddreview"
	"marshal/internal/commands"
	"marshal/internal/pipeline"
	"marshal/internal/sddauthor"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// fakePlanAuthorFactory returns a PlanAuthorFactory whose runner writes a
// valid executable candidate plan to the requested path.
func fakePlanAuthorFactory() PlanAuthorFactory {
	return func(req sddauthor.Request) (*sddauthor.Runner, error) {
		model := fakeModelRunner{run: func(_ context.Context, _ string) error {
			if err := os.MkdirAll(filepath.Dir(req.PlanPath), 0o755); err != nil {
				return err
			}
			return os.WriteFile(req.PlanPath, []byte("# Plan\n\n## Task 1: Add file\n\n"+
				"```marshal.file path=\"created.txt\"\nhello\n```\n"), 0o644)
		}}
		return sddauthor.NewRunner(model), nil
	}
}

type fakeModelRunner struct {
	run func(context.Context, string) error
}

func (f fakeModelRunner) Run(ctx context.Context, prompt string) error {
	return f.run(ctx, prompt)
}

func newAuthoringModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state,
		WithCommandRegistry(cmdReg),
		WithPlanAuthorFactory(context.Background(), fakePlanAuthorFactory()),
		WithPipelineFactory(context.Background(), func(planPath string) AgentRunner {
			c, err := pipeline.NewController(pipeline.ControllerOpts{
				PlanPath: planPath,
				RepoRoot: state.WorkingDir,
				Git:      worktree.NewFakeGitOps(),
				Strategy: pipeline.StrategyAuto,
			})
			if err != nil {
				return nil
			}
			return pipeline.NewControllerAdapter(c, state)
		}),
	)
	m.resize(100, 40)
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
	updated, _ = m.Update(sddreview.AcceptMsg{})
	m = asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("dock should be closed after accept")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("accepted candidate must remain on disk: %v", err)
	}
}

func TestSDDNewCancelRemovesOnlyCandidate(t *testing.T) {
	m := newAuthoringModel(t)
	updated, cmd := m.dispatchCommand("/sdd new add marker file")
	m = asModel(t, updated)
	m = runAuthoringCmd(t, m, cmd)
	panel := m.dock.Panel().(*sddreview.Panel)
	path := panel.CandidatePath()
	// Cancel removes the generated candidate and closes review.
	updated, _ = m.Update(sddreview.CancelMsg{})
	m = asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("dock should be closed after cancel")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled candidate should be removed, got %v", err)
	}
}

func TestSDDNewFromLastPlanUsesFinalAssistantMessage(t *testing.T) {
	m := newAuthoringModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "approved plan content", session.ContentTypePlain)
	updated, cmd := m.dispatchCommand("/sdd new --from-last-plan")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd for /sdd new --from-last-plan")
	}
	if !m.busy {
		t.Fatal("model should be busy during authoring")
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
