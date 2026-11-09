package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/help"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/theme"
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
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModel(t, state)
	m.resize(100, 30)
	return m
}

// newStatusTestModelWithGit builds a status model whose git info is git.
func newStatusTestModelWithGit(t *testing.T, git *gitinfo.Info) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModelWith(t, service.Deps{
		State:   state,
		GitInfo: func(string) gitinfo.Info { return *git },
	})
	m.resize(100, 30)
	return m
}

// newStatusTestModelStub builds a status model whose session is stub, so a
// test can patch the values the status line reads.
func newStatusTestModelStub(t *testing.T, stub *stubSession) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModelStub(t, state, stub)
	m.resize(100, 30)
	return m
}

func TestStatusLineShowsRouteAndContext(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	stateOf(m).SetTurnBudget(128000, 100000, "derived")
	stateOf(m).SetTurnUsage(42000)

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
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "gpt-5.6-luna", Provider: "codex"})
	stateOf(m).SetTurnQuota(&schema.QuotaInfo{
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
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen3:8b", Provider: "ollama"})

	line := m.renderStatusLine(120)
	if strings.Contains(line, "⚡") {
		t.Fatalf("status line should not show a quota segment:\n%s", line)
	}
}

// TestStatusLineQuotaClearedByNil: switching to a provider that reports no
// quota must clear the previous reading rather than leaving it stale.
func TestStatusLineQuotaClearedByNil(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "gpt-5.6-luna", Provider: "codex"})
	stateOf(m).SetTurnQuota(&schema.QuotaInfo{PrimaryUsedPercent: 50, PrimaryResetAfterSecs: 600})
	if line := m.renderStatusLine(120); !strings.Contains(line, "⚡") {
		t.Fatalf("expected a quota segment:\n%s", line)
	}

	stateOf(m).SetTurnQuota(nil)
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
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", Thinking: "high", ReasoningCapable: true})
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
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", ReasoningCapable: true})
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "think default") {
		t.Fatalf("status line missing 'think default' for unset effort:\n%s", line)
	}
}

// "off" is a real knob position (Anthropic maps it to no thinking block),
// so it must be visible rather than collapsed into the unset case.
func TestStatusLineShowsThinkingOff(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", Thinking: "off", ReasoningCapable: true})
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
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "m", Provider: "p", Thinking: "high"})
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "think ") {
		t.Fatalf("status line shows thinking effort the wire would never send:\n%s", line)
	}
}

func TestStatusLineShowsToolActivityWithElapsed(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(100, 0)})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "shell.run: go test") {
		t.Fatalf("status line must not show tool activity; got:\n%s", line)
	}
}

func TestStatusLineShowsApprovalState(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "rm -rf build", ResponseChan: make(chan session.UserApprovalDecision, 1)})
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "⚠ approval") {
		t.Fatalf("status line missing approval state:\n%s", line)
	}
}

func TestStatusLineShowsErrorNotice(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeProvider, Message: "connection refused"})
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "✘ error") {
		t.Fatalf("status line missing error state:\n%s", line)
	}
}

func TestStatusLineOmitsThinkingActivity(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityThinking})

	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "thinking") {
		t.Fatalf("status line must not show thinking activity (pinned row owns it):\n%s", line)
	}
}

func TestStatusLineShowsToolBudgetCounter(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: "file.read", StartedAt: time.Unix(100, 0)})
	stateOf(m).SetToolBudget(session.ToolBudget{Used: 13, Max: 16})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "tools") {
		t.Fatalf("status line must not show tool budget when tool activity is removed from footer:\n%s", line)
	}
}

func TestStatusLineOmitsToolBudgetWhenMaxIsZero(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: "file.read", StartedAt: time.Unix(100, 0)})
	stateOf(m).SetToolBudget(session.ToolBudget{Used: 0, Max: 0})

	line := m.renderStatusLine(100)
	if strings.Contains(line, "tools") {
		t.Fatalf("status line must not show budget when Max is 0:\n%s", line)
	}
}

func TestStatusLineFitsWidth(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "a-very-long-model-name:70b-instruct-q4", Provider: "ollama", LocalOnly: true})
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("x", 80), StartedAt: time.Unix(100, 0)})
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
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: strings.Repeat("x", 40), Provider: "ollama", LocalOnly: true})
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("x", 40), StartedAt: time.Unix(100, 0)})
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
	stateOf(m).SetSwarmProgress(session.SwarmProgress{
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
	m = openTestApprovalEdit(t, m, "go test")

	// Editing never hides the pending decision: the status line keeps the
	// approval cue (the P1 visibility invariant) while the edit owns input.
	if !m.editingApproval() {
		t.Fatal("the approval editor does not own the input")
	}
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "approval") {
		t.Fatalf("status line hid the pending approval while editing:\n%s", line)
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
	m = openTestApprovalEdit(t, m, "go test")
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: strings.Repeat("model", 8), Provider: strings.Repeat("provider", 6), LocalOnly: true})
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("very-long-activity ", 3), StartedAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(105, 0) }
	m.spinnerFrame = "⠋"
	line := stripANSI(m.renderStatusLine(80))
	if !strings.Contains(line, "Act") {
		t.Fatalf("status line dropped the access mode segment under collapse:\n%s", line)
	}
}

// The context counter outlives the locality cue: on a narrow line "ctx
// 42k/200k" is the number a user acts on, while "local" only restates the
// route drawn beside it. Width 55 is narrow enough that, once the footer
// hints are dropped, something still has to go.
func TestStatusLineDropsLocalBeforeCtx(t *testing.T) {
	m := newViewTestModel(t, 50, 24)
	stateOf(m).SetTrusted(true)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder-7b", Provider: "ollama", LocalOnly: true})
	stateOf(m).SetTurnBudget(128000, 100000, "derived")
	stateOf(m).SetTurnUsage(42000)
	line := stripANSI(m.renderStatusLine(55))
	if !strings.Contains(line, "qwen") || !strings.Contains(line, "ollama") {
		t.Fatalf("route dropped on narrow line:\n%s", line)
	}
	if !strings.Contains(line, "ctx") {
		t.Fatalf("ctx must survive the shed that local loses:\n%s", line)
	}
	if strings.Contains(line, "local") {
		t.Fatalf("local must shed before ctx:\n%s", line)
	}
	// Nothing mid-truncated with a dangling partial token:
	if strings.Contains(line, "ol") && !strings.Contains(line, "ollama") {
		t.Fatalf("route was mid-truncated:\n%s", line)
	}
}

// The border form closes the console at exactly the row's width in every
// state: ╰─ <clusters> ─╯. Narrow widths are included because the border is
// the one status form with no ansi.Cut net of its own: the width guarantee
// comes from chrome.BorderLine, and pinned segments can outgrow a cramped row.
func TestStatusBorderIsExactlyTheWidth(t *testing.T) {
	for _, busy := range []bool{false, true} {
		for _, width := range []int{40, 80, 96, 120} {
			m := newViewTestModel(t, width, 24)
			stateOf(m).SetTrusted(true)
			m.busy = busy
			line := stripANSI(m.renderStatusBorder(width, theme.Current().BorderMuted))
			if !strings.HasPrefix(line, "╰─ ") || !strings.HasSuffix(line, "─╯") {
				t.Fatalf("busy=%v width=%d: not a bottom border: %q", busy, width, line)
			}
			if got := visibleRunes(line); got != width {
				t.Fatalf("busy=%v width=%d: border is %d cells, want %d: %q", busy, width, got, width, line)
			}
		}
	}
}

// The border draws the same clusters the plain row does, six cells narrower:
// shedding is one rule, not two.
func TestStatusBorderShedsLikeTheStatusLine(t *testing.T) {
	const width = 80
	m := newViewTestModel(t, width, 24)
	stateOf(m).SetTrusted(true)
	m.busy = true
	m.turnStartedAt = m.now().Add(-12 * time.Second)
	m.spinnerFrame = "⠋"
	stateOf(m).SetActiveRoute(session.RouteInfo{
		Active:    true,
		Model:     "qwen2.5-coder:14b-instruct-q8_0",
		Provider:  "ollama",
		LocalOnly: true,
	})
	left, _ := m.statusClusters(width - 6)
	border := stripANSI(m.renderStatusBorder(width, theme.Current().BorderMuted))
	if !strings.Contains(border, stripANSI(left)) {
		t.Fatalf("border does not carry statusClusters(%d) on the left:\nborder=%q\nleft=%q", width-6, border, stripANSI(left))
	}
	if !strings.Contains(border, "Act") {
		t.Fatalf("border lost the mode cue: %q", border)
	}
	if !strings.Contains(border, "Ctrl+C stop") {
		t.Fatalf("busy border lost the stop key: %q", border)
	}
}

func TestStatusLineHidesBrowserSegmentWhenNoSession(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetBrowserInfo(session.BrowserInfo{SessionOpen: false})
	line := m.renderStatusLine(100)
	stripped := stripANSI(line)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("status line should not show 🌐 when no browser session:\n%s", line)
	}
}

func TestStatusLineDropsBrowserSegmentFirst(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).SetTrusted(true)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	stateOf(m).SetBrowserInfo(session.BrowserInfo{
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
		stateOf(m).SetBrowserInfo(session.BrowserInfo{
			SessionOpen: true,
			URL:         "https://example.com/docs",
			Mode:        "standalone",
		})
		stateOf(m).SetSwarmProgress(session.SwarmProgress{
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
	stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(100, 0)})
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "shell.run: go test") {
		t.Fatalf("status line must not show tool activity:\n%s", line)
	}
}

func TestStatusLineShowsWorkingDir(t *testing.T) {
	dir := t.TempDir()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModel(t, state)
	m.resize(100, 30)
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, filepath.Base(dir)) {
		t.Fatalf("status line missing working dir base %q:\n%s", filepath.Base(dir), line)
	}
}

func TestStatusLineHasNoBackgroundFill(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
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
	// Ctrl+X is only actionable during a turn: the handler declines it while
	// idle, so the hint belongs to the busy cluster.
	m.busy = true
	stateOf(m).PushSteering("a")
	stateOf(m).PushSteering("b")
	line := stripANSI(m.renderStatusLine(140))
	if !strings.Contains(line, "Ctrl+X") || !strings.Contains(line, "clear queue") {
		t.Fatalf("status line missing queue hint:\n%s", line)
	}
}

// ctrlXDrill names the drill state a status-hint fixture stands in.
type ctrlXDrill uint8

const (
	ctrlXUndrilled ctrlXDrill = iota
	ctrlXDrilledRunning
	ctrlXDrilledDone
)

// ctrlXStatusCase describes one state the status row must render a matching
// Ctrl+X hint (or no hint at all) for.
type ctrlXStatusCase struct {
	name  string
	drill ctrlXDrill
	idle  bool
	queue int
	// own is true when a surface ahead of the global handler holds the key.
	own    bool
	browse bool
	setup  func(t *testing.T, m *Model)
}

// ctrlXStatusModel builds a real model in the case's state.
func ctrlXStatusModel(t *testing.T, width, height int, c ctrlXStatusCase) Model {
	t.Helper()
	m := newViewTestModel(t, width, height)
	stateOf(m).SetTrusted(true)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
	m.busy = !c.idle
	for _, msg := range []string{"queued one", "queued two"}[:c.queue] {
		stateOf(m).PushSteering(msg)
	}
	if c.drill != ctrlXUndrilled {
		child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
		view := stateOf(m).RegisterSubagent("explore repo", child)
		m.drillIntoSubagent(view.ID)
		m.refreshViewport()
		if c.drill == ctrlXDrilledDone {
			m.viewStack[len(m.viewStack)-1].Status = acpwire.SubagentDone
		}
	}
	m.browsing = c.browse
	if c.setup != nil {
		c.setup(t, &m)
	}
	return m
}

// ctrlXStatusWant maps the model's own decision (ctrlXAction, task 1) to the
// one label the row may carry, or "" when the key does nothing right now.
func ctrlXStatusWant(m Model) string {
	switch m.ctrlXAction() {
	case ctrlXStopAgent:
		return "stop agent"
	case ctrlXClearQueue:
		if len(m.sess.Status().Steering.Queue) > 0 {
			return "clear queue"
		}
	}
	return ""
}

// TestStatusCtrlXHintMatchesDrillAndWidth pins that the status row shows at
// most one Ctrl+X pair, that it names the action the handler would take, and
// that it is absent when a modal, the browser cue or an empty queue makes it
// a dead key. Where the width fallback sheds the pair, the stop key must
// survive whole and no fragment may remain.
func TestStatusCtrlXHintMatchesDrillAndWidth(t *testing.T) {
	geometries := []struct {
		name string
		w, h int
		rowW int
		// must is true for the wide row, which is wide enough that an
		// actionable hint has to be there rather than shed.
		must bool
	}{
		{name: "80x24", w: 80, h: 24, rowW: 80},
		{name: "120x40", w: 120, h: 40, rowW: 120},
		{name: "wide", w: 120, h: 40, rowW: 200, must: true},
	}

	cases := []ctrlXStatusCase{
		{name: "busy undrilled empty queue"},
		{name: "busy undrilled queued", queue: 2},
		{name: "idle undrilled", idle: true},
		{name: "idle undrilled queued", idle: true, queue: 2},
		{name: "running drill empty queue", drill: ctrlXDrilledRunning},
		{name: "running drill queued", drill: ctrlXDrilledRunning, queue: 2},
		{name: "completed drill empty queue", drill: ctrlXDrilledDone},
		{name: "completed drill queued", drill: ctrlXDrilledDone, queue: 2},
		{
			name:  "pending approval",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				stateOf(m).SetPendingApproval(&session.PendingToolCall{
					ID: "call-1", Name: "shell.run",
					ResponseChan: make(chan session.UserApprovalDecision, 1),
				})
			},
		},
		{
			name:  "pending question",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				stateOf(m).SetPendingQuestion(&session.PendingQuestion{
					Questions:    []session.Question{{Question: "Proceed?"}},
					ResponseChan: make(chan []session.Answer, 1),
				})
			},
		},
		{
			name:  "skill gate",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				stateOf(m).SetPendingSkillGate(&session.PendingSkillGate{
					Skill:        "demo",
					ResponseChan: make(chan session.SkillGateChoice, 1),
				})
			},
		},
		{
			name:  "sudo input",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				stateOf(m).SetPendingSudoAuth(&session.PendingSudoAuth{Command: "id"})
			},
		},
		{
			name:  "approval editor",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				stateOf(m).SetPendingApproval(&session.PendingToolCall{
					ID: "call-1", Name: "shell.run",
					ResponseChan: make(chan session.UserApprovalDecision, 1),
				})
				*m = openTestApprovalEdit(t, *m, "go test")
			},
		},
		{
			name:  "completion popup",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				m.cmdPopup = newCompletionPopup([]completionItem{{Text: "plan", Kind: completionCommand}})
				m.cmdPopup.update("pl")
			},
		},
		{
			name:  "doctor key field",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				m.doctorSecret = newDoctorSecret("openai", 40)
			},
		},
		{
			name:  "open dock",
			queue: 2,
			own:   true,
			setup: func(t *testing.T, m *Model) {
				m.dock.Open(&picker.Model{})
			},
		},
		{name: "browse running drill queued", drill: ctrlXDrilledRunning, queue: 2, browse: true},
		{name: "browse undrilled queued", queue: 2, browse: true},
	}

	for _, c := range cases {
		for _, g := range geometries {
			t.Run(c.name+"/"+g.name, func(t *testing.T) {
				m := ctrlXStatusModel(t, g.w, g.h, c)
				row := stripANSI(m.renderStatusLine(g.rowW))

				if c.browse {
					// The status row's browse cue replaces the hint cluster,
					// so the row itself never carries the pair; the footer a
					// browse surface draws stays eligible for the real action.
					if !strings.Contains(row, "browse") {
						t.Fatalf("browsing row lost the browse cue:\n%s", row)
					}
					if strings.Contains(row, "Ctrl+X") {
						t.Fatalf("browsing row advertised Ctrl+X:\n%s", row)
					}
					want := help.CtrlXHintNone
					switch m.ctrlXAction() {
					case ctrlXStopAgent:
						want = help.CtrlXHintStopAgent
					case ctrlXClearQueue:
						if len(m.sess.Status().Steering.Queue) > 0 {
							want = help.CtrlXHintClearQueue
						}
					}
					if got := m.footerHints().CtrlXHint; got != want {
						t.Fatalf("footerHints().CtrlXHint = %v, want %v while browsing", got, want)
					}
					return
				}

				want := ""
				if !c.own {
					want = ctrlXStatusWant(m)
				}
				other := "stop agent"
				if want == "stop agent" {
					other = "clear queue"
				}
				if want != "" && strings.Contains(row, other) {
					t.Fatalf("row shows the %q label while the action is %q:\n%s", other, want, row)
				}
				switch strings.Count(row, "Ctrl+X") {
				case 0:
					if want == "" {
						break
					}
					if !m.busy || !strings.Contains(row, "Ctrl+C stop") {
						t.Fatalf("row shed the Ctrl+X hint without keeping the stop key:\n%s", row)
					}
					if g.must {
						t.Fatalf("a wide row that fits the cluster shed the Ctrl+X %q hint:\n%s", want, row)
					}
				case 1:
					if want == "" {
						t.Fatalf("row advertised Ctrl+X while no action is reachable:\n%s", row)
					}
					if !strings.Contains(row, "Ctrl+X "+want) {
						t.Fatalf("row carries a Ctrl+X fragment instead of the whole %q pair:\n%s", want, row)
					}
				default:
					t.Fatalf("row shows more than one Ctrl+X key:\n%s", row)
				}
			})
		}
	}

	t.Run("no color", func(t *testing.T) {
		m := ctrlXStatusModel(t, 120, 40, ctrlXStatusCase{name: "busy undrilled queued", queue: 2})
		t.Setenv("NO_COLOR", "1")
		row := stripANSI(m.renderStatusLine(200))
		if strings.Contains(row, "stop agent") {
			t.Fatalf("row shows the stop-agent label while the action is clear queue:\n%s", row)
		}
		if got := strings.Count(row, "Ctrl+X clear queue"); got != 1 {
			t.Fatalf("monochrome row has %d complete clear-queue pairs, want exactly 1:\n%s", got, row)
		}
	})
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
	stateOf(m).SetTrusted(true)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
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
	stateOf(m).SetActivity(session.Activity{
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
	stateOf(m).SetSDDProgress(session.SDDProgress{Active: true, Phase: "branch review", CurrentTask: 2, TotalTasks: 5})
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "sdd") {
		t.Errorf("status line missing sdd mode cue: %q", line)
	}
	if strings.Contains(line, "branch review") || strings.Contains(line, "task 2/5") {
		t.Errorf("status line must not duplicate the run panel's task/phase: %q", line)
	}
}

// TestStatusLineUntrustedIsWarningColored asserts that the untrusted segment
// follows the semantic StatusWarning slot rather than one palette's literal
// value. Each fixture renders an expectation from its own theme, so the test
// fails if untrustedStyle() reads a different slot — including under the
// fixture whose warning colour is overridden to a value found nowhere in the
// built-in palettes.
func TestStatusLineUntrustedIsWarningColored(t *testing.T) {
	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })

	m := newStatusTestModel(t)
	stateOf(m).SetTrusted(false)

	// A foreground-colour SGR: 30–37/90–97 plus the 38;5;n and 38;2;r;g;b
	// extended forms. Bold (1) is deliberately not a match.
	fgSGR := regexp.MustCompile("\x1b\\[(3[0-9]|9[0-9]|38;[0-9;]*)m")

	// A warning colour that no palette defines, so a match cannot be
	// satisfied by an unrelated segment or a coincidental palette collision.
	overridden := theme.LoadFor(false, "xterm-256color")
	overridden.StatusWarning = lipgloss.Color("45")
	if overridden.StatusWarning == overridden.FGDefault || overridden.StatusWarning == overridden.StatusError {
		t.Fatalf("fixture warning colour collides with another slot")
	}

	cases := []struct {
		name       string
		theme      theme.Theme
		monochrome bool
	}{
		{name: "256-color", theme: theme.LoadFor(false, "xterm-256color")},
		{name: "16-color", theme: theme.LoadFor(false, "xterm")},
		{name: "overridden warning slot", theme: overridden},
		{name: "monochrome", theme: theme.LoadFor(true, "xterm-256color"), monochrome: true},
	}

	// Subtests deliberately do not run in parallel: they share the global
	// theme.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reload after model construction so initialization cannot
			// replace the fixture.
			theme.Reload(tc.theme)
			out := m.renderStatusLine(100)
			if !strings.Contains(stripANSI(out), "untrusted") {
				t.Fatalf("untrusted text missing:\n%s", out)
			}
			if tc.monochrome {
				bold := lipgloss.NewStyle().Bold(true).Render("untrusted")
				if !strings.Contains(out, bold) {
					t.Fatalf("monochrome untrusted text must stay visible without colour styling:\n%q", out)
				}
				if fgSGR.MatchString(bold) {
					t.Fatalf("monochrome untrusted token carries a foreground colour: %q", bold)
				}
				return
			}
			want := lipgloss.NewStyle().Foreground(tc.theme.StatusWarning).Bold(true).Render("untrusted")
			if !strings.Contains(out, want) {
				t.Fatalf("untrusted segment not rendered with the selected StatusWarning slot (%q):\n%s", want, out)
			}
		})
	}
}

func TestStatusShowsDiagnosticsIndicator(t *testing.T) {
	m := newStatusTestModel(t)
	stateOf(m).Config.Providers = map[string]config.ProviderConfig{
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
	stateOf(m).Config.Providers = map[string]config.ProviderConfig{
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
	stateOf(m).SetWorkspace(session.Workspace{ActiveRoot: dir})

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

func TestStatusLineSDDTokensUnlimited(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).SetSDDProgress(session.SDDProgress{
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
	stateOf(m).SetTrusted(true)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})

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
	stateOf(m).SetContextPack(contextpack.Pack{
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
	stateOf(m).BeginGeneration("gen-3", 3, "digest")

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
	changed := 0
	stub := &stubSession{patch: func(st *acpwire.SessionStatus) { st.ChangedFiles = changed }}
	m := newStatusTestModelStub(t, stub)
	if strings.Contains(stripANSI(m.renderStatusLine(140)), "±") {
		t.Fatal("±N files must be hidden when nothing changed")
	}
	changed = 3
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±3 files") {
		t.Fatalf("status line missing ±3 files:\n%s", got)
	}
	changed = 1
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±1 file") || strings.Contains(got, "±1 files") {
		t.Fatalf("one changed file must read ±1 file:\n%s", got)
	}
}

// ±N files drops before the route and mode segments when the line is tight.
func TestStatusLineChangedFileCountDropsBeforeRoute(t *testing.T) {
	stub := &stubSession{patch: func(st *acpwire.SessionStatus) { st.ChangedFiles = 1 }}
	m := newStatusTestModelStub(t, stub)
	stateOf(m).SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
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
	runCmdChain(t, cmd)
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±1 file") {
		t.Fatalf("status line missing ±1 file after the turn:\n%s", got)
	}
}

// newInteractionStatusModel builds a model whose effective tuple has every
// modifier active, so a status test can prove none of them disappear.
func newInteractionStatusModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	on := true
	if _, err := state.ApplyInteraction(interaction.Patch{Auto: &on, System: &on}); err != nil {
		t.Fatalf("seed interaction: %v", err)
	}
	m := testModel(t, state)
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
		stateOf(m).SetPendingApproval(&session.PendingToolCall{
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
		stateOf(m).SetPendingQuestion(&session.PendingQuestion{
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
		m = openTestApprovalEdit(t, m, "go test")
		if got := m.modeSegment(); got != want {
			t.Fatalf("modeSegment = %q, want %q while editing a command", got, want)
		}
		// The pending decision stays visible while its command is edited.
		if got := stripANSI(m.statusRightSegment()); !strings.Contains(got, "approval") {
			t.Fatalf("statusRightSegment = %q, want the pending-approval indicator", got)
		}
	})

	t.Run("activity", func(t *testing.T) {
		m := newInteractionStatusModel(t)
		stateOf(m).SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run", StartedAt: time.Now()})
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

	stateOf(m).SetPendingApproval(&session.PendingToolCall{
		ID: "call-1", Name: "shell.run",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	if got := stripANSI(m.renderStatusLine(24)); !strings.Contains(got, "Act · Auto · System") {
		t.Fatalf("approval at minimum width concealed the access label:\n%s", got)
	}
}
