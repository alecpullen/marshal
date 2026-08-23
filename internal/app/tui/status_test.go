package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/contextpack"
)

func newStatusTestModel(t *testing.T) Model {
	t.Helper()
	// Some status tests assert on color styling; force the default
	// warm-sunset palette even when the outer environment sets NO_COLOR,
	// which would otherwise resolve every slot to NoColor{} and emit no SGR.
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	return m
}

func TestStatusLineShowsRouteAndContext(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	m.state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections:   []contextpack.Section{{Title: "x", EstimatedTokens: 18000}},
	})

	line := m.renderStatusLine(100)
	for _, want := range []string{"default", "qwen2.5-coder:14b @ ollama", "local", "ctx 18k/32k"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q:\n%s", want, line)
		}
	}
}

func TestStatusLineShowsToolActivityWithElapsed(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(100, 0)})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "shell.run: go test") {
		t.Fatalf("status line must not show tool activity; got:\n%s", line)
	}
}

func TestStatusLineShowsApprovalState(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "rm -rf build", ResponseChan: make(chan session.UserApprovalDecision, 1)})
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "⚠ approval") {
		t.Fatalf("status line missing approval state:\n%s", line)
	}
}

func TestStatusLineShowsProviderError(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "connection refused"})
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "✘ error") {
		t.Fatalf("status line missing error state:\n%s", line)
	}
}

func TestStatusLineOmitsThinkingActivity(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking})

	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "thinking") {
		t.Fatalf("status line must not show thinking activity (pinned row owns it):\n%s", line)
	}
}

func TestStatusLineShowsToolBudgetCounter(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "file.read", StartedAt: time.Unix(100, 0)})
	m.state.SetToolBudget(session.ToolBudget{Used: 13, Max: 16})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "tools") {
		t.Fatalf("status line must not show tool budget when tool activity is removed from footer:\n%s", line)
	}
}

func TestStatusLineOmitsToolBudgetWhenMaxIsZero(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "file.read", StartedAt: time.Unix(100, 0)})
	m.state.SetToolBudget(session.ToolBudget{Used: 0, Max: 0})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "tools") {
		t.Fatalf("status line must not show budget when Max is 0:\n%s", line)
	}
}

func TestStatusLineFitsWidth(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "a-very-long-model-name:70b-instruct-q4", Provider: "ollama", LocalOnly: true})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("x", 80), StartedAt: time.Unix(100, 0)})
	m.spinnerFrame = "⠋"
	for _, width := range []int{40, 60, 80, 120} {
		line := m.renderStatusLine(width)
		for _, l := range strings.Split(line, "\n") {
			if visibleRunes(l) > width {
				t.Fatalf("status line exceeds width %d (%d): %q", width, visibleRunes(l), l)
			}
		}
	}
}

func TestStatusLineFitsVeryNarrowWidths(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: strings.Repeat("x", 40), Provider: "ollama", LocalOnly: true})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("x", 40), StartedAt: time.Unix(100, 0)})
	m.spinnerFrame = "⠋"
	for _, width := range []int{0, 1} {
		line := m.renderStatusLine(width)
		for _, l := range strings.Split(line, "\n") {
			if visibleRunes(l) > max(width, 1) {
				t.Fatalf("status line exceeds width %d (%d): %q", width, visibleRunes(l), l)
			}
		}
	}
}

func TestStatusLineShowsSwarmTokenBudget(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetSwarmProgress(session.SwarmProgress{
		Active:     true,
		Goal:       "test goal",
		TokensUsed: 5000,
		TokensMax:  100000,
	})
	line := m.renderStatusLine(120)
	if !strings.Contains(line, "tokens") {
		t.Fatalf("status line missing token segment:\n%s", line)
	}
	if !strings.Contains(line, "5k") {
		t.Fatalf("status line missing compacted used count:\n%s", line)
	}
	if !strings.Contains(line, "100k") {
		t.Fatalf("status line missing compacted max count:\n%s", line)
	}
}

func TestStatusLineShowsEditCmdModeWhenEditingCommand(t *testing.T) {
	m := newStatusTestModel(t)
	m.editingCommand = true

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "edit cmd") {
		t.Fatalf("status line missing 'edit cmd' mode:\n%s", line)
	}
}

func TestStatusLineShowsCompletingModeWhenPopupIsVisible(t *testing.T) {
	m := newStatusTestModel(t)
	m.cmdPopup = newCompletionPopup([]completionItem{{Text: "plan", Kind: completionCommand}})
	m.cmdPopup.update("pl") // triggers filtering and sets visible=true

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "completing") {
		t.Fatalf("status line missing 'completing' mode:\n%s", line)
	}
}

func TestStatusLinePreservesModeSegmentUnderCollapse(t *testing.T) {
	m := newStatusTestModel(t)
	m.editingCommand = true
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: strings.Repeat("model", 8), Provider: strings.Repeat("provider", 6), LocalOnly: true})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("very-long-activity ", 3), StartedAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(105, 0) }
	m.spinnerFrame = "⠋"
	line := stripANSI(m.renderStatusLine(80))
	if !strings.Contains(line, "edit cmd") {
		t.Fatalf("status line dropped mode segment under collapse:\n%s", line)
	}
}

func TestStatusLineDropsLowPrioritySegment(t *testing.T) {
	m := newViewTestModel(t, 50, 24)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder-7b", Provider: "ollama", LocalOnly: true})
	m.state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 1000, MaxTokens: 8000},
		Sections:   []contextpack.Section{{Title: "ctx", EstimatedTokens: 1000}},
	})
	line := m.renderStatusLine(55)
	// mode + route must remain; ctx segment should be dropped (priority 3 vs 0/1/2).
	// Width 55 is narrow enough that even after dropping the footer hints
	// (now dropped first on narrow terminals), ctx still doesn't fit.
	if !strings.Contains(line, "qwen") || !strings.Contains(line, "ollama") {
		t.Fatalf("route dropped on narrow line:\n%s", line)
	}
	if strings.Contains(line, "ctx") {
		t.Fatalf("low-priority ctx segment should have been dropped:\n%s", line)
	}
	// Nothing mid-truncated with a dangling partial token:
	if strings.Contains(line, "ol") && !strings.Contains(line, "ollama") {
		t.Fatalf("route was mid-truncated:\n%s", line)
	}
}

func TestStatusLineHidesBrowserSegmentWhenStripShowsBrowser(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Mode:        "standalone",
	})
	line := m.renderStatusLine(100)
	stripped := stripANSI(line)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("status line should not show 🌐 when strip shows browser:\n%s", line)
	}
	if strings.Contains(stripped, "example.com/docs") {
		t.Fatalf("status line should not show browser URL when strip shows browser:\n%s", line)
	}
}

func TestStatusLineHidesBrowserSegmentWhenNoSession(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{SessionOpen: false})
	line := m.renderStatusLine(100)
	stripped := stripANSI(line)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("status line should not show 🌐 when no browser session:\n%s", line)
	}
}

func TestStatusLineDropsBrowserSegmentFirst(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	// Browser segment is never added when the strip shows the browser
	// (SessionOpen=true means ShouldShowStatusURL() returns false).
	// Verify the route survives.
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com",
		Mode:        "standalone",
	})
	line := m.renderStatusLine(70)
	stripped := stripANSI(line)
	if !strings.Contains(stripped, "qwen2.5-coder:14b @ ollama") {
		t.Fatalf("model segment should survive on narrow width:\n%s", line)
	}
	if strings.Contains(stripped, "example.com") {
		t.Fatalf("browser segment should not appear when strip shows browser:\n%s", line)
	}
}

func TestShouldShowStatusURLReturnsFalseWhenStripShowsBrowser(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "http://example.com",
	})
	if m.ShouldShowStatusURL() {
		t.Fatal("ShouldShowStatusURL should return false when strip shows browser")
	}
}

func TestShouldShowStatusURLReturnsTrueWhenNoBrowserSession(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{SessionOpen: false})
	if !m.ShouldShowStatusURL() {
		t.Fatal("ShouldShowStatusURL should return true when no browser session")
	}
}

func TestStatusLineShowsBrowserSegmentWhenStripIsBusyWithSwarm(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Mode:        "standalone",
	})
	m.state.SetSwarmProgress(session.SwarmProgress{
		Active: true,
		Roles:  []session.SwarmRole{{Name: "planner", Status: session.SwarmRoleActive}},
	})
	if !m.ShouldShowStatusURL() {
		t.Fatal("status URL must reappear when the live strip is showing swarm progress")
	}
	if !strings.Contains(stripANSI(m.renderStatusLine(100)), "example.com") {
		t.Fatal("status line should carry the browser URL while swarm owns the strip")
	}
}

func TestStatusLineShowsIdleHints(t *testing.T) {
	m := newStatusTestModel(t)
	line := stripANSI(m.renderStatusLine(100))
	for _, want := range []string{"Tab mode", "/ cmd", "? help"} {
		if !strings.Contains(line, want) {
			t.Fatalf("idle status line missing hint %q:\n%s", want, line)
		}
	}
}

func TestStatusLineHintsYieldToActivity(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(100, 0)})
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "shell.run: go test") {
		t.Fatalf("status line must not show tool activity:\n%s", line)
	}
}

func TestStatusLineShowsWorkingDir(t *testing.T) {
	dir := t.TempDir()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, filepath.Base(dir)) {
		t.Fatalf("status line missing working dir base %q:\n%s", filepath.Base(dir), line)
	}
}

func TestStatusLineHasNoBackgroundFill(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
	out := m.renderStatusLine(80)
	if strings.Contains(out, "48;5;237") || strings.Contains(out, ";237m") {
		t.Fatalf("status line still emits statusBar background fill:\n%q", out)
	}
	if !strings.Contains(stripANSI(out), "qwen @ ollama") {
		t.Fatalf("status line missing route:\n%q", stripANSI(out))
	}
}

func TestStatusLineShowsQueueHint(t *testing.T) {
	m := newStatusTestModel(t)
	m.queuedCount = 2
	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "Ctrl+X") || !strings.Contains(line, "clear queue") {
		t.Fatalf("status line missing queue hint:\n%s", line)
	}
}

func TestStatusLineShowsGitBranch(t *testing.T) {
	m := newStatusTestModel(t)
	m.gitInfo = gitinfo.Info{Branch: "main", Worktree: "", InRepo: true}
	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "⎇ main") {
		t.Fatalf("status line missing git branch:\n%s", line)
	}
}

func TestStatusLineShowsWorktree(t *testing.T) {
	m := newStatusTestModel(t)
	m.gitInfo = gitinfo.Info{Branch: "feature", Worktree: "linked", InRepo: true}
	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "wt:linked") {
		t.Fatalf("status line missing worktree:\n%s", line)
	}
}

func TestStatusLineOmitsGitSegmentsWhenNotInRepo(t *testing.T) {
	m := newStatusTestModel(t)
	m.gitInfo = gitinfo.Info{}
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "⎇") {
		t.Fatalf("status line shows branch glyph when not in repo:\n%s", line)
	}
	if strings.Contains(line, "wt:") {
		t.Fatalf("status line shows worktree when not in repo:\n%s", line)
	}
}

func TestStatusLineCollapseDropsWorktreeBeforeBranch(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
	m.gitInfo = gitinfo.Info{Branch: "feature", Worktree: "linked", InRepo: true}
	line := stripANSI(m.renderStatusLine(58))
	// branch shares priority 5 with dir; worktree is 6. On a tight width the
	// worktree should be dropped while the branch survives (branch + dir both 5).
	// Width 58 is narrow enough that even after dropping the footer hints
	// (now dropped first on narrow terminals), worktree still doesn't fit.
	if !strings.Contains(line, "⎇ feature") {
		t.Fatalf("branch dropped before worktree on narrow width:\n%s", line)
	}
	if strings.Contains(line, "wt:linked") {
		t.Fatalf("worktree should have been dropped first on narrow width:\n%s", line)
	}
}

func TestStatusLineModeIsCoralColored(t *testing.T) {
	m := newStatusTestModel(t)
	out := m.renderStatusLine(100)
	// The mode segment should carry a foreground color SGR (any tier).
	// In 256-color mode it's 38;5;209; in 16-color it's 35 (magenta).
	// Check for either tier.
	has256 := strings.Contains(out, "38;5;209")
	has16 := strings.Contains(out, "[1;35m") || strings.Contains(out, "[35m")
	if !has256 && !has16 {
		t.Fatalf("mode segment not coral-colored (expected 38;5;209 or 35m):\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "default") {
		t.Fatalf("mode label text missing:\n%s", out)
	}
}

func TestStatusFooterOmitsToolActivity(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActivity(session.Activity{
		Kind:      session.ActivityTool,
		Label:     "shell.run: go test ./...",
		StartedAt: time.Now().Add(-time.Second),
	})
	m.spinnerFrame = "⠋"
	out := stripANSI(m.renderStatusLine(80))
	if strings.Contains(out, "shell.run") || strings.Contains(out, "go test") {
		t.Fatalf("status line must not render active tool details; got %q", out)
	}
}

func TestStatusLineShowsSDDModeOnly(t *testing.T) {
	m := newTestModel(t)
	m.state.SetSDDProgress(session.SDDProgress{Active: true, Phase: "branch review", CurrentTask: 2, TotalTasks: 5})
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "sdd") {
		t.Errorf("status line missing sdd mode cue: %q", line)
	}
	if strings.Contains(line, "branch review") || strings.Contains(line, "task 2/5") {
		t.Errorf("status line must not duplicate the run panel's task/phase: %q", line)
	}
}

func TestStatusLineUntrustedIsWarningColored(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetTrusted(false)
	out := m.renderStatusLine(100)
	// The untrusted segment should carry a foreground color SGR (any tier).
	// In 256-color mode it's 38;5;172; in 16-color it's 33 (yellow).
	has256 := strings.Contains(out, "38;5;172")
	has16 := strings.Contains(out, "[1;33m") || strings.Contains(out, "[33m")
	if !has256 && !has16 {
		t.Fatalf("untrusted segment not warning-colored (expected 38;5;172 or 33m):\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "untrusted") {
		t.Fatalf("untrusted text missing:\n%s", out)
	}
}

func TestStatusShowsDiagnosticsIndicator(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.Config.Providers = map[string]config.ProviderConfig{
		"broken": {Type: "openai_compatible"}, // no base URL -> diagnostic
	}
	m.refreshDiagnostics()

	if m.diagnosticCount == 0 {
		t.Fatal("diagnostics not counted")
	}
	// Render at a wide enough width that the priority-10 segment is not dropped.
	line := m.renderStatusLine(120)
	if !strings.Contains(stripANSI(line), "/doctor") {
		t.Error("status line should point at /doctor when diagnostics exist")
	}
	if !strings.Contains(stripANSI(line), "config issues") {
		t.Error("status line should mention config issues when diagnostics exist")
	}
}

func TestStatusCleanWhenNoDiagnostics(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.Config.Providers = map[string]config.ProviderConfig{
		"ok": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	m.refreshDiagnostics()

	if m.diagnosticCount != 0 {
		t.Errorf("diagnosticCount = %d, want 0", m.diagnosticCount)
	}
}

func TestWorkspaceMsgUpdatesGitInfoWhenDockOpen(t *testing.T) {
	// gitinfo.Read reads real git state from the filesystem, so build a real
	// temp repo and point the workspace message at it.
	dir := t.TempDir()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "pipeline/feature", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "test"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	m := newTestModel(t)
	m.gitInfo = gitinfo.Info{Branch: "main", InRepo: true}

	mm, _ := m.Update(workspaceMsg{activeRoot: dir})
	m = mm.(Model)

	if !m.gitInfo.InRepo {
		t.Fatalf("gitInfo.InRepo = false, want true after workspace change")
	}
	if m.gitInfo.Branch != "pipeline/feature" {
		t.Errorf("gitInfo.Branch = %q, want pipeline/feature", m.gitInfo.Branch)
	}
}

// initRailTestRepo builds a real git repo in dir with one committed file and
// returns the base commit SHA. Mirrors the inline pattern in
// changedfiles_test.go without exporting from that package.
func initRailTestRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "test"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmdArgs := append([]string{"-C", dir}, args...)
		if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return string(out[:len(out)-1])
}

func TestRefreshRailChangedUsesActiveRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	base := initRailTestRepo(t, dir)

	// Create a linked worktree on a new branch.
	wt := filepath.Join(dir, "wt")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", "-b", "feat-x", wt).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	// Modify a file only in the worktree.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write worktree a.txt: %v", err)
	}
	// Modify a file only in the main checkout.
	if err := os.WriteFile(filepath.Join(dir, "main-only.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main-only.txt: %v", err)
	}

	m := newTestModel(t)
	m.railWidth = 40 // enable the rail
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: wt, Branch: "feat-x"})
	m.railBaseRef = base

	m.refreshRailChanged()

	found := false
	for _, f := range m.railChanged {
		if f.Path == "a.txt" {
			found = true
		}
		if f.Path == "main-only.txt" {
			t.Errorf("railChanged includes main-checkout file %q, want only worktree changes", f.Path)
		}
	}
	if !found {
		t.Errorf("railChanged missing worktree-modified a.txt: %+v", m.railChanged)
	}
}

func TestResizeNarrowToWideRefreshesRail(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	base := initRailTestRepo(t, dir)

	// Modify a file so the rail has something to show.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: dir, Branch: "main"})
	m.railBaseRef = base

	// Narrow resize: below the rail breakpoint (MinWidth=120), the rail is
	// disabled and the changed section must be empty.
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = mm.(Model)
	if m.railEnabled() {
		t.Fatal("rail enabled at narrow width, want disabled")
	}
	if len(m.railChanged) != 0 {
		t.Fatalf("railChanged at narrow width = %+v, want empty", m.railChanged)
	}

	// Wide resize: rail becomes enabled and the changed section refreshes
	// to list the modified file.
	mm, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	m = mm.(Model)
	if !m.railEnabled() {
		t.Fatal("rail disabled at wide width, want enabled")
	}
	found := false
	for _, f := range m.railChanged {
		if f.Path == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("railChanged after narrow→wide missing modified a.txt: %+v", m.railChanged)
	}
}

func TestStatusLineSDDTokensUnlimited(t *testing.T) {
	m := newTestModel(t)
	m.state.SetSDDProgress(session.SDDProgress{
		Active:     true,
		TokensUsed: 886000,
		TokensMax:  0, // unlimited
	})
	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "sdd tokens") {
		t.Fatalf("status line missing sdd tokens segment:\n%s", line)
	}
	if strings.Contains(line, "/0") {
		t.Errorf("unlimited budget should not show /0:\n%s", line)
	}
	if strings.Contains(line, "/∞") {
		t.Errorf("unlimited budget should not show /∞ (just omit denominator):\n%s", line)
	}
	// Should show the used amount.
	if !strings.Contains(line, "886k") {
		t.Errorf("status line should show used tokens:\n%s", line)
	}
}

func TestStatusLineDropsHintsBeforePathAndWorktree(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
	m.gitInfo = gitinfo.Info{Branch: "feature", Worktree: "linked", InRepo: true}

	// At a wide width, both the worktree segment and the footer hints
	// (e.g. "Tab mode", "? help") should be visible.
	wide := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(wide, "wt:linked") {
		t.Fatalf("wide status line missing worktree:\n%s", wide)
	}
	if !strings.Contains(wide, "Tab mode") {
		t.Fatalf("wide status line missing footer hints:\n%s", wide)
	}

	// At a narrow width, the footer hints should be dropped first so that
	// the worktree and branch info remain visible.
	narrow := stripANSI(m.renderStatusLine(75))
	if !strings.Contains(narrow, "⎇ feature") {
		t.Fatalf("narrow status line missing branch (should survive hint drop):\n%s", narrow)
	}
	if strings.Contains(narrow, "Tab mode") {
		t.Fatalf("narrow status line should have dropped footer hints before worktree:\n%s", narrow)
	}
}
