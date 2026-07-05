package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func newViewTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(width, height)
	m.refreshViewport()
	return m
}

func TestViewIsSingleColumn(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	view := m.View()

	for _, gone := range []string{"inspector", "1 Plan", "2 Context", "3 Log", "live transcript", "● ● ●", "MARSHAL"} {
		if strings.Contains(view, gone) {
			t.Fatalf("view still contains removed chrome %q", gone)
		}
	}
	if !strings.Contains(view, "❯") {
		t.Fatal("view missing input prompt / transcript")
	}
}

func TestViewContainsStatusLine(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
	view := m.View()
	if !strings.Contains(view, "qwen @ ollama") {
		t.Fatalf("view missing status line route info:\n%s", view)
	}
}

func TestTranscriptHasSubtleFrame(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()

	view := m.View()
	if !strings.Contains(view, "┌") || !strings.Contains(view, "└") {
		t.Fatalf("view missing transcript frame:\n%s", view)
	}
}

func TestTranscriptFrameDoesNotMoveWhenActivityStarts(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, strings.Repeat("hello ", 120), session.ContentTypePlain)
	m.refreshViewport()
	idleLines := strings.Split(strings.TrimRight(m.View(), "\n"), "\n")

	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking, StartedAt: time.Unix(100, 0)})
	m.busy = true
	m.spinnerFrame = "⠋"
	m.refreshViewport()
	busyLines := strings.Split(strings.TrimRight(m.View(), "\n"), "\n")

	if len(busyLines) != 30 {
		t.Fatalf("busy view height = %d, want fixed terminal height 30", len(busyLines))
	}
	if len(idleLines) != len(busyLines) {
		t.Fatalf("view height changed from %d to %d when activity started", len(idleLines), len(busyLines))
	}
	if idleLines[0] != busyLines[0] {
		t.Fatalf("transcript top frame moved:\nidle: %q\nbusy: %q", idleLines[0], busyLines[0])
	}
	activityRow := 30 - m.inputAreaRows() - statusLineRows
	if !strings.Contains(busyLines[activityRow], "thinking") {
		t.Fatalf("activity row moved; line %d = %q", activityRow, busyLines[activityRow])
	}
	inputTop := activityRow + activityStripRows
	if !strings.HasPrefix(busyLines[inputTop], "╭") {
		t.Fatalf("input box top moved; line %d = %q", inputTop, busyLines[inputTop])
	}
}

func TestViewFitsTerminalSizesSingleColumn(t *testing.T) {
	sizes := [][2]int{{40, 10}, {80, 24}, {100, 30}, {120, 40}}
	for _, size := range sizes {
		m := newViewTestModel(t, size[0], size[1])
		m.state.AddMessage(session.RoleUser, strings.Repeat("wide input ", 30), session.ContentTypePlain)
		m.state.AddMessage(session.RoleAssistant, strings.Repeat("wide answer ", 30), session.ContentTypeMarkdown)
		m.refreshViewport()
		view := m.View()
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) > size[1] {
			t.Fatalf("view has %d lines for height %d", len(lines), size[1])
		}
		for _, line := range lines {
			if visibleRunes(line) > size[0] {
				t.Fatalf("line exceeds width %d (%d): %q", size[0], visibleRunes(line), line)
			}
		}
	}
}

func TestProviderErrorShowsInlineNotFullScreen(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.SetProviderError(errors.New("connection refused"))
	m.lastTranscriptHash = 0
	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "✗ provider: connection refused") {
		t.Fatalf("provider error not rendered inline:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Fatal("provider error must not hide the transcript")
	}
}

func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width != 96 {
		t.Fatalf("viewport.Width = %d, want 96 (width-4 for transcript frame)", m.viewport.Width)
	}
	wantHeight := 30 - transcriptFrameRows - m.inputAreaRows() - statusLineRows
	if m.viewport.Height != wantHeight {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height, wantHeight)
	}
}
