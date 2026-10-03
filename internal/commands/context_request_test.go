package commands

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func runContext(t *testing.T, state *session.State, args ...string) Result {
	t.Helper()
	cmdReg := New()
	RegisterAll(cmdReg, registry.New())
	cmd, ok := cmdReg.Lookup("context")
	if !ok {
		t.Fatal("/context not registered")
	}
	return cmd.Handler(state, args)
}

func findRow(rows []Row, text string) (Row, bool) {
	for _, r := range rows {
		if r.Text == text {
			return r, true
		}
	}
	return Row{}, false
}

func rowTexts(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Header + r.Text + "  " + r.Detail + "\n")
		for _, c := range r.Children {
			b.WriteString("    " + c.Header + c.Text + "  " + c.Detail + "\n")
		}
	}
	return b.String()
}

func sampleInspection() session.RequestInspection {
	maxTokens := 4096
	return session.RequestInspection{
		AttemptID: 1,
		At:        time.Date(2026, 10, 4, 9, 30, 0, 0, time.UTC),
		Provider:  "ollama",
		Model:     "qwen3:32b",
		Messages: []session.InspectionMessage{
			{Role: "system", Content: "You are Marshal.\nBe brief."},
			{Role: "user", Content: "fix the parser"},
			{Role: "assistant", ToolCalls: []session.InspectionToolCall{{ID: "c1", Name: "file.read", Args: `{"path":"parser.go"}`}}},
			{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", session.MaxInspectionFieldBytes+2048)},
		},
		Tools: []session.InspectionTool{
			{Name: "file.read", Description: "Read a file.", Parameters: `{"type":"object"}`},
		},
		Options:   session.InspectionOptions{Thinking: "high", Streaming: true, MaxTokens: &maxTokens},
		Outcome:   session.InspectionOutcome{Status: session.InspectionFailed, Err: "connection refused"},
		PackKnown: true, PackTokens: 1200, PackWindow: 32000, PackSections: 3,
	}
}

func TestContextRequestWithNothingSentSaysSo(t *testing.T) {
	res := runContext(t, newTestState(), "request")
	if res.Doc == nil {
		t.Fatalf("want a panel, got %+v", res)
	}
	if _, ok := findRow(res.Doc.Rows, "No request sent yet"); !ok {
		t.Fatalf("an empty snapshot must say nothing was sent:\n%s", rowTexts(res.Doc.Rows))
	}
}

func TestContextRequestShowsTheLastAttempt(t *testing.T) {
	state := newTestState()
	state.SetRequestInspection(sampleInspection())

	res := runContext(t, state, "request")
	if res.Doc == nil || res.Doc.Title != "Last request" {
		t.Fatalf("want the Last request panel, got %+v", res)
	}
	rows := res.Doc.Rows
	sent, ok := findRow(rows, "Sent")
	if !ok || !strings.Contains(sent.Detail, "ollama/qwen3:32b") {
		t.Fatalf("Sent row should name provider/model:\n%s", rowTexts(rows))
	}
	outcome, ok := findRow(rows, "Outcome")
	if !ok || !strings.Contains(outcome.Detail, "failed") || !strings.Contains(outcome.Detail, "connection refused") {
		t.Fatalf("Outcome row should carry status and error:\n%s", rowTexts(rows))
	}
	opts, ok := findRow(rows, "Options")
	if !ok || !strings.Contains(opts.Detail, "thinking high") || !strings.Contains(opts.Detail, "max 4096") {
		t.Fatalf("Options row should list the sent options:\n%s", rowTexts(rows))
	}

	sys, ok := findRow(rows, "1  system")
	if !ok || len(sys.Children) == 0 {
		t.Fatalf("each message is a drill row with its content:\n%s", rowTexts(rows))
	}
	if _, ok := findRow(sys.Children, "Be brief."); !ok {
		t.Fatalf("message content should be one row per line:\n%s", rowTexts(sys.Children))
	}

	asst, _ := findRow(rows, "3  assistant")
	if !strings.Contains(rowTexts(asst.Children), `file.read {"path":"parser.go"}`) {
		t.Fatalf("assistant tool calls should be listed:\n%s", rowTexts(asst.Children))
	}

	tool, _ := findRow(rows, "4  tool")
	if !strings.Contains(rowTexts(tool.Children), "2048 bytes omitted") {
		t.Fatalf("a capped message must say so:\n%s", rowTexts(tool.Children))
	}

	def, ok := findRow(rows, "file.read")
	if !ok || !strings.Contains(rowTexts(def.Children), `{"type":"object"}`) {
		t.Fatalf("tool definitions should show their parameters:\n%s", rowTexts(rows))
	}
}

func TestContextPanelLinksToTheLastRequest(t *testing.T) {
	state := newTestState()
	state.SetRequestInspection(sampleInspection())

	res := runContext(t, state)
	row, ok := findRow(res.Doc.Rows, "Last request")
	if !ok || row.Action == nil {
		t.Fatalf("/context should offer the last request:\n%s", rowTexts(res.Doc.Rows))
	}
	if got := row.Action(state); got.Doc == nil || got.Doc.Title != "Last request" {
		t.Fatalf("the row should open the Last request panel, got %+v", got)
	}
}

func TestContextRequestReportsACancelledAttemptAsCancelled(t *testing.T) {
	state := newTestState()
	snap := sampleInspection()
	snap.Outcome = session.InspectionOutcome{Status: session.InspectionCancelled, Err: errors.New("context canceled").Error()}
	state.SetRequestInspection(snap)

	outcome, _ := findRow(runContext(t, state, "request").Doc.Rows, "Outcome")
	if !strings.HasPrefix(outcome.Detail, "cancelled") {
		t.Fatalf("outcome = %q", outcome.Detail)
	}
}
