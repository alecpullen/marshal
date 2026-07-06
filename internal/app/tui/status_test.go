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
	if !strings.Contains(line, "✗ error") {
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
