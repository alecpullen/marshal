package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// assertGutterContract checks rendered output obeys the transcript's
// indentation contract: the first content line carries the " X "-shaped
// gutter with text at column gutterWidth, and EVERY content line stays out
// of column 0 — nested-rail lines (3 spaces + ▍), bullet lines ("  – "),
// and indented continuations all satisfy this; only an un-indented wrapped
// line violates it.
func assertGutterContract(t *testing.T, rendered string) {
	t.Helper()
	seenFirst := false
	for _, line := range strings.Split(stripANSI(rendered), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Glyphs are multibyte, so the contract is measured in runes.
		r := []rune(line)
		if len(r) <= gutterWidth {
			t.Fatalf("line shorter than the gutter: %q", line)
		}
		if r[0] != ' ' {
			t.Errorf("line starts in column 0: %q", line)
		}
		if seenFirst {
			continue
		}
		seenFirst = true
		if r[gutterWidth-1] != ' ' {
			t.Errorf("gutter %q is not \" X \"-shaped in %q", string(r[:gutterWidth]), line)
		}
		if r[gutterWidth] == ' ' {
			t.Errorf("text does not begin at column %d in %q", gutterWidth, line)
		}
	}
	if !seenFirst {
		t.Fatalf("rendered output had no content lines")
	}
}

// TestTranscriptIndentContract pins P-01: every top-level transcript item
// starts in the same column. Renderers used to each compute their own indent,
// so a single turn staircased across columns 2, 3, and 5 — the largest single
// contributor to the transcript reading as messy.
func TestTranscriptIndentContract(t *testing.T) {
	const width = 80

	cases := []struct {
		name     string
		rendered string
	}{
		{"user message", renderUserMessage("hello there", width)},
		{"system notice", renderSystemNotice("config reloaded", width)},
		{"skill tag", renderSkillTag("debugging", width)},
		{"tool result", renderToolResultLine("read 42 lines", width)},
		{"plan block", renderPlanBlock("step one\nstep two", width)},
		{"plain prose", renderPlainProse("some fallback prose", width)},
		{"queued messages", renderQueuedMessages([]string{"do the thing"}, width)},
		{"final answer", renderFinalAnswer(session.Message{
			Content: "the answer", Final: true,
		}, width)},
		{"completed tool call", renderCompletedToolCall(registry.AuditEvent{
			ToolName: "file.read", ResultSummary: "ok",
		}, false, width)},
		{"thinking summary", renderThinkingSummary("reasoned", time.Second, false, width)},
		{"agent markdown", renderAgentMarkdown("plain paragraph", width)},
		{"tool group with wrapped bullets", renderToolGroup([]registry.AuditEvent{
			{ToolName: "file.read", ResultSummary: "read the entire contents of this very long file to understand the structure",
				Args: json.RawMessage(`{"path":"internal/app/tui/transcript_render_pipeline_quite_long.go"}`)},
			{ToolName: "file.read", ResultSummary: "ok",
				Args: json.RawMessage(`{"path":"internal/app/tui/another_rather_long_runner_implementation_file.go"}`)},
		}, false, width)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGutterContract(t, tc.rendered)
		})
	}
}

// TestTranscriptContentFitsWidth pins P-02: no renderer emits a line wider
// than the frame it was given. Content-width math used to be a scatter of
// width-2/-3/-4/-5 literals that did not match each renderer's actual gutter.
func TestTranscriptContentFitsWidth(t *testing.T) {
	long := strings.Repeat("supercalifragilistic ", 20)
	for _, width := range []int{40, 80, 120} {
		rendered := []string{
			renderUserMessage(long, width),
			renderSystemNotice(long, width),
			renderToolResultLine(long, width),
			renderPlanBlock(long, width),
			renderPlainProse(long, width),
			renderSkillTag(long, width),
		}
		for i, r := range rendered {
			for _, line := range strings.Split(stripANSI(r), "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("width=%d renderer#%d emitted a %d-cell line: %q",
						width, i, w, line)
				}
			}
		}
	}
}

// TestTurnSeparatorMarksNewTurns pins P-13: the transcript is otherwise one
// undifferentiated flow with nothing to scan back to.
func TestTurnSeparatorMarksNewTurns(t *testing.T) {
	out := stripANSI(renderTurnSeparator(40))
	if !strings.Contains(out, "─") {
		t.Fatalf("separator has no rule: %q", out)
	}
	if w := ansi.StringWidth(strings.TrimRight(out, "\n")); w != 40 {
		t.Errorf("separator width = %d, want 40", w)
	}
}

// TestApprovalActionRowSurfacesKeys pins P-16: the row used to be uniformly
// muted with the armed action marked only by a leading glyph and no mnemonic
// keys shown at all.
func TestApprovalActionRowSurfacesKeys(t *testing.T) {
	out := stripANSI(approvalActionRow(0))
	for _, a := range approvalActions {
		if !strings.Contains(out, a.key+" "+a.label) {
			t.Errorf("action row missing %q: %q", a.key+" "+a.label, out)
		}
	}
	if !strings.Contains(out, glyphRunningMarker()) {
		t.Errorf("armed action not marked: %q", out)
	}
	// The armed entry must be visually distinct, not just glyph-marked.
	if styled := approvalActionRow(0); styled == out {
		t.Error("armed action carries no styling")
	}
}

func glyphRunningMarker() string { return "▸" }
