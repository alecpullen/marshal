package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
)

func newStatusTestModel(t *testing.T) Model {
	t.Helper()
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
	for _, want := range []string{"auto", "qwen2.5-coder:14b @ ollama", "local", "ctx 18k/32k"} {
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
	if !strings.Contains(line, "⠋") || !strings.Contains(line, "shell.run: go test") || !strings.Contains(line, "4s") {
		t.Fatalf("status line missing tool activity:\n%s", line)
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
	m.state.SetProviderError(errors.New("connection refused"))
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "✘ error") {
		t.Fatalf("status line missing error state:\n%s", line)
	}
}

func TestStatusLineShowsThinkingActivity(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking})

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "⠋") || !strings.Contains(line, "thinking") {
		t.Fatalf("status line missing thinking activity:\n%s", line)
	}
}

func TestStatusLineShowsToolBudgetCounter(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "file.read", StartedAt: time.Unix(100, 0)})
	m.state.SetToolBudget(session.ToolBudget{Used: 13, Max: 16})

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "tools 13/16") {
		t.Fatalf("status line missing tool budget counter:\n%s", line)
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

func TestStatusLineShowsHelpOpenModeWhenHelpIsOpen(t *testing.T) {
	m := newStatusTestModel(t)
	m.helpOpen = true

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "help open") {
		t.Fatalf("status line missing 'help open' mode:\n%s", line)
	}
}

func TestStatusLineShowsCompletingModeWhenPopupIsVisible(t *testing.T) {
	m := newStatusTestModel(t)
	m.cmdPopup = newCompletionPopup([]completionItem{{Text: "/plan", Kind: completionCommand}})
	m.cmdPopup.update("pl") // triggers filtering and sets visible=true

	line := m.renderStatusLine(100)
	if !strings.Contains(line, "completing") {
		t.Fatalf("status line missing 'completing' mode:\n%s", line)
	}
}

func TestStatusLinePreservesModeSegmentUnderCollapse(t *testing.T) {
	m := newStatusTestModel(t)
	m.helpOpen = true
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: strings.Repeat("model", 8), Provider: strings.Repeat("provider", 6), LocalOnly: true})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: strings.Repeat("very-long-activity ", 3), StartedAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(105, 0) }
	m.spinnerFrame = "⠋"
	line := stripANSI(m.renderStatusLine(80))
	if !strings.Contains(line, "help open") {
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
	line := m.renderStatusLine(50)
	// mode + route must remain; ctx segment should be dropped (priority 3 vs 0/1/2)
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

func TestStatusLineShowsBrowserSegmentWhenSessionOpen(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Mode:        "standalone",
	})
	line := m.renderStatusLine(100)
	stripped := stripANSI(line)
	if !strings.Contains(stripped, "🌐") {
		t.Fatalf("status line missing 🌐 when browser session open:\n%s", line)
	}
	if !strings.Contains(stripped, "example.com/docs") {
		t.Fatalf("status line missing browser URL:\n%s", line)
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
	line := m.renderStatusLine(38)
	stripped := stripANSI(line)
	if !strings.Contains(stripped, "qwen2.5-coder:14b @ ollama") {
		t.Fatalf("model segment should survive on narrow width:\n%s", line)
	}
	if strings.Contains(stripped, "example.com") {
		t.Fatalf("browser segment should be dropped on narrow width:\n%s", line)
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
