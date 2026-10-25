package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/contextpack"
	"marshal/internal/interaction"
	"marshal/internal/llm/schema"
	"marshal/internal/repo/gitinfo"
)

func newStatusTestModel(t *testing.T) Model {
	t.Helper()
	// Some status tests assert on color styling; force the default
	// warm-sunset palette even when the outer environment sets NO_COLOR,
	// which would otherwise resolve every slot to NoColor{} and emit no SGR.
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := testModel(state)
	m.resize(100, 30)
	return m
}

// newStatusTestModelWithGit builds a status model whose git info is git.
func newStatusTestModelWithGit(t *testing.T, git *gitinfo.Info) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := testModelWith(inproc.Deps{
		State:   state,
		GitInfo: func(string) gitinfo.Info { return *git },
	})
	m.resize(100, 30)
	return m
}

func TestStatusLineShowsRouteAndContext(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	m.state.SetTurnBudget(128000, 100000, "derived")
	m.state.SetTurnUsage(42000)

	line := m.renderStatusLine(100)
	for _, want := range []string{"Act", "qwen2.5-coder:14b @ ollama", "local", "ctx 42k/128k"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q:\n%s", want, line)
		}
	}
}

// TestStatusLineShowsQuota: an OAuth provider's quota reading renders as
// percent-used plus a reset countdown and the plan tier.
func TestStatusLineShowsQuota(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "gpt-5.6-luna", Provider: "codex"})
	m.state.SetTurnQuota(&schema.QuotaInfo{
		PlanType:              "plus",
		PrimaryUsedPercent:    50,
		PrimaryResetAfterSecs: 14845, // 4h7m
		PrimaryWindowMinutes:  300,
	})

	line := m.renderStatusLine(120)
	for _, want := range []string{"⚡ 50%", "4h7m", "plus"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q:\n%s", want, line)
		}
	}
}

// TestStatusLineOmitsQuotaWhenAbsent: providers that report no quota must
// not render an empty or zeroed segment.
func TestStatusLineOmitsQuotaWhenAbsent(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen3:8b", Provider: "ollama"})

	line := m.renderStatusLine(120)
	if strings.Contains(line, "⚡") {
		t.Fatalf("status line should not show a quota segment:\n%s", line)
	}
}

// TestStatusLineQuotaClearedByNil: switching to a provider that reports no
// quota must clear the previous reading rather than leaving it stale.
func TestStatusLineQuotaClearedByNil(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "gpt-5.6-luna", Provider: "codex"})
	m.state.SetTurnQuota(&schema.QuotaInfo{PrimaryUsedPercent: 50, PrimaryResetAfterSecs: 600})
	if line := m.renderStatusLine(120); !strings.Contains(line, "⚡") {
		t.Fatalf("expected a quota segment:\n%s", line)
	}

	m.state.SetTurnQuota(nil)
	if line := m.renderStatusLine(120); strings.Contains(line, "⚡") {
		t.Fatalf("quota segment should be gone after SetTurnQuota(nil):\n%s", line)
	}
}

// TestQuotaSegmentOmitsZeroReset: a reading with no reset countdown must not
// render a "now" placeholder that reads as a real countdown.
func TestQuotaSegmentOmitsZeroReset(t *testing.T) {
	got := quotaSegment(&acpwire.Quota{PrimaryUsedPercent: 12})
	if got != "⚡ 12%" {
		t.Fatalf("quotaSegment = %q, want %q", got, "⚡ 12%")
	}
}

// TestQuotaSegmentNilIsEmpty guards the nil path used by the footer.
func TestQuotaSegmentNilIsEmpty(t *testing.T) {
	if got := quotaSegment(nil); got != "" {
		t.Fatalf("quotaSegment(nil) = %q, want empty", got)
	}
}

func TestStatusLineShowsThinkingEffort(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", Thinking: "high", ReasoningCapable: true})
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "think high") {
		t.Fatalf("status line missing thinking flag:\n%s", line)
	}
}

// An effort-capable route must always show the thinking segment so the
// footer answers "is thinking on, and at what level?" — including when the
// preset leaves the effort unset and the backend default applies.
func TestStatusLineShowsThinkingDefaultWhenUnset(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", ReasoningCapable: true})
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "think default") {
		t.Fatalf("status line missing 'think default' for unset effort:\n%s", line)
	}
}

// "off" is a real knob position (Anthropic maps it to no thinking block),
// so it must be visible rather than collapsed into the unset case.
func TestStatusLineShowsThinkingOff(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", Thinking: "off", ReasoningCapable: true})
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "think off") {
		t.Fatalf("status line missing 'think off':\n%s", line)
	}
}

// When the provider cannot accept an effort control (e.g. native Ollama,
// whose think toggle is a separate mechanism), the chat path drops the
// field entirely. The footer must not show a value that is never sent.
func TestStatusLineHidesThinkingWhenRouteNotReasoningCapable(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", Thinking: "high"})
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "think ") {
		t.Fatalf("status line shows thinking effort the wire would never send:\n%s", line)
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

func TestStatusLineShowsErrorNotice(t *testing.T) {
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
	if !strings.Contains(line, "Act") {
		t.Fatalf("status line dropped the access mode segment under collapse:\n%s", line)
	}
}

func TestStatusLineDropsLowPrioritySegment(t *testing.T) {
	m := newViewTestModel(t, 50, 24)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder-7b", Provider: "ollama", LocalOnly: true})
	m.state.SetTurnBudget(128000, 100000, "derived")
	m.state.SetTurnUsage(42000)
	line := m.renderStatusLine(55)
	// mode + route must remain; the ctx segment should be dropped (priority
	// 3 vs 0/1/2). Width 55 is narrow enough that even after dropping the
	// footer hints (now dropped first on narrow terminals), ctx still
	// doesn't fit.
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
}

// The status line carries the browser URL on every frame height while a
// session is open.
func TestStatusLineShowsTheBrowserWhileASessionIsOpen(t *testing.T) {
	for _, height := range []int{24, 40} {
		m := newStatusTestModel(t)
		m.resize(120, height)
		m.state.SetBrowserInfo(session.BrowserInfo{
			SessionOpen: true,
			URL:         "https://example.com/docs",
			Mode:        "standalone",
		})
		m.state.SetSwarmProgress(session.SwarmProgress{
			Active: true,
			Roles:  []session.SwarmRole{{Name: "planner", Status: session.SwarmRoleActive}},
		})
		line := stripANSI(m.renderStatusLine(120))
		if !strings.Contains(line, "example.com/docs") {
			t.Fatalf("height %d: the status line must carry the browser URL:\n%s", height, line)
		}
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
	m := testModel(state)
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
	m.state.PushSteering("a")
	m.state.PushSteering("b")
	line := stripANSI(m.renderStatusLine(140))
	if !strings.Contains(line, "Ctrl+X") || !strings.Contains(line, "clear queue") {
		t.Fatalf("status line missing queue hint:\n%s", line)
	}
}

func TestStatusLineShowsGitBranch(t *testing.T) {
	git := gitinfo.Info{Branch: "main", Worktree: "", InRepo: true}
	m := newStatusTestModelWithGit(t, &git)
	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "⎇ main") {
		t.Fatalf("status line missing git branch:\n%s", line)
	}
}

func TestStatusLineShowsWorktree(t *testing.T) {
	git := gitinfo.Info{Branch: "feature", Worktree: "linked", InRepo: true}
	m := newStatusTestModelWithGit(t, &git)
	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "wt:linked") {
		t.Fatalf("status line missing worktree:\n%s", line)
	}
}

func TestStatusLineOmitsGitSegmentsWhenNotInRepo(t *testing.T) {
	git := gitinfo.Info{}
	m := newStatusTestModelWithGit(t, &git)
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "⎇") {
		t.Fatalf("status line shows branch glyph when not in repo:\n%s", line)
	}
	if strings.Contains(line, "wt:") {
		t.Fatalf("status line shows worktree when not in repo:\n%s", line)
	}
}

func TestStatusLineCollapseDropsWorktreeBeforeBranch(t *testing.T) {
	git := gitinfo.Info{Branch: "feature", Worktree: "linked", InRepo: true}
	m := newStatusTestModelWithGit(t, &git)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
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
	if !strings.Contains(stripANSI(out), "Act") {
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

func TestStatusLineFollowsTheActiveRootsBranchWhenDockOpen(t *testing.T) {
	// gitinfo.Read reads real git state from the filesystem, so build a real
	// temp repo and point the workspace at it.
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

	m := newStatusTestModel(t)
	m.dock.Open(&picker.Model{})

	// No message is sent: the status line reads the workspace's active root
	// through the session on the next render.
	m.state.SetWorkspace(session.Workspace{ActiveRoot: dir})

	line := stripANSI(m.renderStatusLine(120))
	if !strings.Contains(line, "⎇ pipeline/feature") {
		t.Fatalf("status line missing the active root's branch:\n%s", line)
	}
}

// initSheetTestRepo builds a real git repo in dir with one committed file and
// returns the base commit SHA. Mirrors the inline pattern in
// changedfiles_test.go without exporting from that package.
func initSheetTestRepo(t *testing.T, dir string) string {
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

func TestRefreshSheetChangedUsesActiveRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	base := initSheetTestRepo(t, dir)

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
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: wt, Branch: "feat-x"})
	m.sheetBaseRef = base

	m.refreshSheetChanged()

	found := false
	for _, f := range m.sheetChanged {
		if f.Path == "a.txt" {
			found = true
		}
		if f.Path == "main-only.txt" {
			t.Errorf("sheetChanged includes main-checkout file %q, want only worktree changes", f.Path)
		}
	}
	if !found {
		t.Errorf("sheetChanged missing worktree-modified a.txt: %+v", m.sheetChanged)
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
	git := gitinfo.Info{Branch: "feature", Worktree: "linked", InRepo: true}
	m := newStatusTestModelWithGit(t, &git)
	m.state.SetTrusted(true)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})

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

// The pack segment is gone from the status bar: pack composition lives in
// /context and the side rail. A built pack must not add a second ctx-ish
// number next to the live-usage one.
func TestStatusLineHasNoPackSegment(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections:   []contextpack.Section{{Title: "x", EstimatedTokens: 18000}},
	})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "18k/32k") {
		t.Fatalf("pack estimate must not appear in the status line:\n%s", line)
	}
}

func TestStatusShowsGenerationAfterCompaction(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.BeginGeneration("gen-3", 3, "digest")

	out := m.renderStatusLine(100)

	if !strings.Contains(out, "gen 3") {
		t.Errorf("status line does not show the generation: %q", out)
	}
}

func TestStatusHidesGenerationBeforeAnyCompaction(t *testing.T) {
	m := newStatusTestModel(t)
	// Generation 0 is the session's first window — not a compaction.
	if out := m.renderStatusLine(100); strings.Contains(out, "gen ") {
		t.Errorf("generation 0 should not be shown: %q", out)
	}
}

func TestStatusLineShowsChangedFileCount(t *testing.T) {
	m := newStatusTestModel(t)
	if strings.Contains(stripANSI(m.renderStatusLine(140)), "±") {
		t.Fatal("±N files must be hidden when nothing changed")
	}
	m.sheetChanged = []sessionsheet.ChangedFile{{Path: "a"}, {Path: "b"}, {Path: "c"}}
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±3 files") {
		t.Fatalf("status line missing ±3 files:\n%s", got)
	}
	m.sheetChanged = m.sheetChanged[:1]
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±1 file") || strings.Contains(got, "±1 files") {
		t.Fatalf("one changed file must read ±1 file:\n%s", got)
	}
}

// ±N files drops before the route and mode segments when the line is tight.
func TestStatusLineChangedFileCountDropsBeforeRoute(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
	m.sheetChanged = []sessionsheet.ChangedFile{{Path: "a"}}
	var sawBoth bool
	for w := 120; w >= 20; w -= 2 {
		line := stripANSI(m.renderStatusLine(w))
		hasFiles := strings.Contains(line, "±1 file")
		hasRoute := strings.Contains(line, "qwen @ ollama")
		if hasFiles && !hasRoute {
			t.Fatalf("width %d kept ±1 file but dropped the route:\n%s", w, line)
		}
		sawBoth = sawBoth || (hasFiles && hasRoute)
	}
	if !sawBoth {
		t.Fatal("never saw ±1 file alongside the route; the test's width sweep is stale")
	}
}

// A turn that changes files surfaces ±N files once it finishes.
func TestChangedFileCountAppearsAfterTurn(t *testing.T) {
	m, dir := gitModel(t)
	m.resize(140, 40)
	if strings.Contains(stripANSI(m.renderStatusLine(140)), "±") {
		t.Fatal("clean tree must not show ±N files")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.busy = true
	mm, cmd := m.Update(agentFinishedMsg{})
	m = asModel(t, mm)
	for _, msg := range collectSheetBaseRefs(t, cmd) {
		mm, _ = m.Update(msg)
		m = asModel(t, mm)
	}
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±1 file") {
		t.Fatalf("status line missing ±1 file after the turn:\n%s", got)
	}
}

// newInteractionStatusModel builds a model whose effective tuple has every
// modifier active, so a status test can prove none of them disappear.
func newInteractionStatusModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	on := true
	if _, err := state.ApplyInteraction(interaction.Patch{Auto: &on, System: &on}); err != nil {
		t.Fatalf("seed interaction: %v", err)
	}
	m := testModel(state)
	m.resize(100, 30)
	return m
}

// TestInteractionStatusSurvivesTransientSurfaces pins that approval, browsing,
// answering, command editing and activity move to the right-hand surface and
// never replace the persistent access label.
func TestInteractionStatusSurvivesTransientSurfaces(t *testing.T) {
	const want = "Act · Auto · System"

	t.Run("approval", func(t *testing.T) {
		m := newInteractionStatusModel(t)
		m.state.SetPendingApproval(&session.PendingToolCall{
			ID: "call-1", Name: "shell.run",
			ResponseChan: make(chan session.UserApprovalDecision, 1),
		})
		if got := m.modeSegment(); got != want {
			t.Fatalf("modeSegment = %q, want %q during approval", got, want)
		}
		if got := stripANSI(m.statusRightSegment()); !strings.Contains(got, "approval") {
			t.Fatalf("statusRightSegment = %q, want the approval indicator", got)
		}
	})

	t.Run("browsing", func(t *testing.T) {
		m := newInteractionStatusModel(t)
		m.browsing = true
		if got := m.modeSegment(); got != want {
			t.Fatalf("modeSegment = %q, want %q while browsing", got, want)
		}
		if got := stripANSI(m.statusRightSegment()); !strings.Contains(got, "browse") {
			t.Fatalf("statusRightSegment = %q, want the browse indicator", got)
		}
	})

	t.Run("answering", func(t *testing.T) {
		m := newInteractionStatusModel(t)
		m.state.SetPendingQuestion(&session.PendingQuestion{
			Questions:    []session.Question{{Question: "Proceed?"}},
			ResponseChan: make(chan []session.Answer, 1),
		})
		if got := m.modeSegment(); got != want {
			t.Fatalf("modeSegment = %q, want %q while answering", got, want)
		}
		if got := stripANSI(m.statusRightSegment()); !strings.Contains(got, "answering") {
			t.Fatalf("statusRightSegment = %q, want the answering indicator", got)
		}
	})

	t.Run("editing command", func(t *testing.T) {
		m := newInteractionStatusModel(t)
		m.editingCommand = true
		if got := m.modeSegment(); got != want {
			t.Fatalf("modeSegment = %q, want %q while editing a command", got, want)
		}
		if got := stripANSI(m.statusRightSegment()); !strings.Contains(got, "edit cmd") {
			t.Fatalf("statusRightSegment = %q, want the edit-command indicator", got)
		}
	})

	t.Run("activity", func(t *testing.T) {
		m := newInteractionStatusModel(t)
		m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run", StartedAt: time.Now()})
		if got := m.modeSegment(); got != want {
			t.Fatalf("modeSegment = %q, want %q during work", got, want)
		}
	})
}

// TestInteractionStatusRetainsModifiersAtMinimumWidth pins that width pressure
// and an approval indicator never truncate the persistent access label.
func TestInteractionStatusRetainsModifiersAtMinimumWidth(t *testing.T) {
	m := newInteractionStatusModel(t)
	m.resize(24, 30)
	if got := stripANSI(m.renderStatusLine(24)); !strings.Contains(got, "Act · Auto · System") {
		t.Fatalf("minimum-width status line lost the access label:\n%s", got)
	}

	m.state.SetPendingApproval(&session.PendingToolCall{
		ID: "call-1", Name: "shell.run",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	if got := stripANSI(m.renderStatusLine(24)); !strings.Contains(got, "Act · Auto · System") {
		t.Fatalf("approval at minimum width concealed the access label:\n%s", got)
	}
}
