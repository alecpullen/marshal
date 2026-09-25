package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// failingPatchTool registers a file.write_patch that always fails with a
// search-block-not-found error, so executeToolCall's failure path (and the
// tier-3 retry hint) can be exercised without a real workspace.
func failingPatchTool(t *testing.T, reg *registry.Registry) {
	t.Helper()
	tool := registry.Tool{
		Name:   "file.write_patch",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, fmt.Errorf("search block not found in target.go")
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register file.write_patch: %v", err)
	}
}

func patchArgs(t *testing.T, patchText string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatalf("marshal patch args: %v", err)
	}
	return raw
}

// runFailingPatch drives n identical failing patches and returns the content of
// the LAST tool-result message.
func runFailingPatch(t *testing.T, r *Runner, args json.RawMessage, n int) string {
	t.Helper()
	var last []schema.ChatMessage
	for i := 0; i < n; i++ {
		msgs, err := r.executeToolCall(context.Background(), ModelAction{
			Type: ActionToolCall,
			Tool: "file.write_patch",
			Args: args,
		})
		if err != nil {
			t.Fatalf("executeToolCall %d: %v", i, err)
		}
		last = msgs
	}
	if len(last) != 1 {
		t.Fatalf("expected 1 message, got %d", len(last))
	}
	return last[0].Content
}

type hintRange struct {
	path       string
	start, end int
}

var hintRangeRe = regexp.MustCompile(`file\.read (\S+) start_line=(\d+) end_line=(\d+)`)

// parseHintRange pulls the advertised path and line range out of a retry hint.
func parseHintRange(t *testing.T, hint string) hintRange {
	t.Helper()
	m := hintRangeRe.FindStringSubmatch(hint)
	if m == nil {
		t.Fatalf("no file.read range in hint:\n%s", hint)
	}
	start, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("bad start_line %q: %v", m[2], err)
	}
	end, err := strconv.Atoi(m[3])
	if err != nil {
		t.Fatalf("bad end_line %q: %v", m[3], err)
	}
	return hintRange{path: m[1], start: start, end: end}
}

// TestFailedPatchHintPointsAtTargetAtTier3 pins the shape of the tier-3 retry
// hint: on the third identical failing patch the model is told exactly which
// bytes to re-read. It also pins the redesign — the hint must NOT paste a
// second copy of the region, because the tool's own error already embeds it.
func TestFailedPatchHintPointsAtTargetAtTier3(t *testing.T) {
	state := newTestState(t)
	dir := state.WorkingDir
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "target.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	reg := registry.New()
	failingPatchTool(t, reg)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	patchText := "File: target.go\n<<<<<<< SEARCH\nthis line is not on disk\n=======\nreplacement\n>>>>>>> REPLACE\n"
	got := runFailingPatch(t, r, patchArgs(t, patchText), 3)

	if !strings.Contains(got, "re-read the exact bytes before retrying") {
		t.Fatalf("3rd failure did not carry the retry hint:\n%s", got)
	}
	if strings.Contains(got, "current on-disk content near the target") {
		t.Fatalf("hint duplicated the region the tool error already embeds:\n%s", got)
	}
	rng := parseHintRange(t, got)
	if rng.path != "target.go" {
		t.Fatalf("hint named %q, want target.go", rng.path)
	}
	if rng.start > 1 || rng.end < 1 {
		t.Fatalf("advertised range %d-%d does not cover line 1", rng.start, rng.end)
	}
}

// TestFailedPatchHintTargetsFailingFileNotFirst is the regression for the
// original defect: the hint took patches[0] unconditionally, so a multi-file
// patch whose FIRST file applies cleanly pointed the model at the wrong file.
func TestFailedPatchHintTargetsFailingFileNotFirst(t *testing.T) {
	state := newTestState(t)
	dir := state.WorkingDir
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("alpha := 1\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("beta := 2\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}

	reg := registry.New()
	failingPatchTool(t, reg)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	// a.go applies cleanly; b.go's SEARCH block is stale. The failing file is
	// b.go even though a.go comes first.
	patchText := "File: a.go\n<<<<<<< SEARCH\nalpha := 1\n=======\nalpha := 2\n>>>>>>> REPLACE\n\n" +
		"File: b.go\n<<<<<<< SEARCH\nbeta := 999\n=======\nbeta := 3\n>>>>>>> REPLACE\n"
	got := runFailingPatch(t, r, patchArgs(t, patchText), 3)

	rng := parseHintRange(t, got)
	if rng.path != "b.go" {
		t.Fatalf("hint named %q, want the file that actually failed (b.go):\n%s", rng.path, got)
	}
	if strings.Contains(got, "file.read a.go") {
		t.Fatalf("hint pointed at the file that applied cleanly:\n%s", got)
	}
}

// TestFailedPatchHintTargetsFailingChunkNotFirst is the second half of the
// original defect: in a multi-hunk patch whose earlier hunks still apply, the
// hint must describe the hunk that actually failed.
func TestFailedPatchHintTargetsFailingChunkNotFirst(t *testing.T) {
	state := newTestState(t)
	dir := state.WorkingDir

	var file strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&file, "line_%02d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "hunks.txt"), []byte(file.String()), 0o644); err != nil {
		t.Fatalf("write hunks.txt: %v", err)
	}

	reg := registry.New()
	failingPatchTool(t, reg)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	// Chunk 1 is a no-op that applies; chunk 2 references line_16_MODIFIED,
	// which is not on disk. Line 16 is the stale line the model must see.
	patchText := "File: hunks.txt\n" +
		"<<<<<<< SEARCH\nline_03\n=======\nline_03\n>>>>>>> REPLACE\n" +
		"<<<<<<< SEARCH\nline_15\nline_16_MODIFIED\n=======\nline_15\nline_16\n>>>>>>> REPLACE\n"
	got := runFailingPatch(t, r, patchArgs(t, patchText), 3)

	rng := parseHintRange(t, got)
	if rng.path != "hunks.txt" {
		t.Fatalf("hint named %q, want hunks.txt", rng.path)
	}
	if rng.start > 16 || rng.end < 16 {
		t.Fatalf("advertised range %d-%d does not cover the stale line 16:\n%s", rng.start, rng.end, got)
	}
}

// TestFailedPatchHintSkipsAmbiguousFailures guards the gate: an ambiguous match
// has a different remedy (more context lines), so it must not trigger a
// re-read hint or any content refresh.
func TestFailedPatchHintSkipsAmbiguousFailures(t *testing.T) {
	state := newTestState(t)
	dir := state.WorkingDir
	if err := os.WriteFile(filepath.Join(dir, "dupe.go"), []byte("x := 1\nx := 1\n"), 0o644); err != nil {
		t.Fatalf("write dupe.go: %v", err)
	}

	reg := registry.New()
	tool := registry.Tool{
		Name:   "file.write_patch",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, fmt.Errorf("ambiguous match: search block matched 2 locations in dupe.go; add more context lines")
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	patchText := "File: dupe.go\n<<<<<<< SEARCH\nx := 1\n=======\nx := 2\n>>>>>>> REPLACE\n"
	got := runFailingPatch(t, r, patchArgs(t, patchText), 3)
	if strings.Contains(got, "re-read the exact bytes") {
		t.Fatalf("ambiguous failure must not trigger a re-read hint:\n%s", got)
	}
}

// TestFailureLadderNudgeTextAtTier2 pins the tier-2 coaching text, which
// previously existed only incidentally inside asserted output.
func TestFailureLadderNudgeTextAtTier2(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	failingPatchTool(t, reg)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	patchText := "File: target.go\n<<<<<<< SEARCH\nabsent\n=======\nreplacement\n>>>>>>> REPLACE\n"
	got := runFailingPatch(t, r, patchArgs(t, patchText), 2)
	if !strings.Contains(got, "this identical call already failed 2 times") {
		t.Fatalf("tier-2 nudge missing:\n%s", got)
	}
}

// TestFailurePathSuppressesIdenticalOutputReminder pins that the failure path
// stops claiming the repeated call produced identical output: on the spiral
// this ladder exists to catch the error text differs every attempt, so the
// success-side reminder would contradict the failure-streak line beside it.
func TestFailurePathSuppressesIdenticalOutputReminder(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var calls int
	tool := registry.Tool{
		Name:   "file.write_patch",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		calls++
		// Distinct error text every attempt, exactly like the real spiral.
		return registry.ToolResult{}, fmt.Errorf("search block not found in target.go (attempt %d)", calls)
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	patchText := "File: target.go\n<<<<<<< SEARCH\nabsent\n=======\nreplacement\n>>>>>>> REPLACE\n"
	got := runFailingPatch(t, r, patchArgs(t, patchText), 3)
	if strings.Contains(got, "identical arguments and identical output") {
		t.Fatalf("failure path still claims identical output:\n%s", got)
	}
	if !strings.Contains(got, "already failed 3 times") {
		t.Fatalf("failure-streak coaching missing:\n%s", got)
	}
}

// nudgeThenRecoverRegistry fails an identical file.write_patch failTimes times
// and then lets it succeed, so a turn can be driven through the ladder's nudge
// tier and back out again.
func nudgeThenRecoverRegistry(t *testing.T, failTimes int) *registry.Registry {
	t.Helper()
	reg := registry.New()
	var calls atomic.Int64

	patchTool := registry.Tool{
		Name:   "file.write_patch",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	patchTool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		n := int(calls.Add(1))
		if n <= failTimes {
			return registry.ToolResult{}, fmt.Errorf("search block not found in a.go (attempt %d)", n)
		}
		return registry.ToolResult{Summary: "ok", Content: "ok"}, nil
	}

	readTool := registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly}
	readTool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ok", Content: "ok"}, nil
	}

	for _, tool := range []registry.Tool{readTool, patchTool} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name, err)
		}
	}
	return reg
}

// TestFailureLadderTelemetryRecordsNudgeAndRecovery is the reason the ladder
// reports its tier at all: a turn that is nudged at 2 and then RECOVERS must be
// distinguishable from one whose calls never failed twice. Without the metric,
// both report an ordinary success and the 2/3/4 thresholds can never be tuned
// from real sessions.
//
// The metrics are read from the turn's emitted TurnMetrics rather than from
// turnStats directly, because withStats is a documented no-op outside RunTask.
func TestFailureLadderTelemetryRecordsNudgeAndRecovery(t *testing.T) {
	patch := `{"rationale":"apply","action":{"type":"patch","content":"File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}}`
	responses := []string{patch, patch, `{"rationale":"done","action":{"type":"final","content":"Answer."}}`}

	p := &agenttest.ScriptedProvider{Responses: responses}
	state := newTestState(t)
	r := NewRunner(p, nudgeThenRecoverRegistry(t, 2), evalPolicy(), state, "test-model")
	r.Role = RoleRepoScout
	r.SetForceClass(string(ClassQuestion))

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	if _, err := r.RunTask(context.Background(), "eval goal"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got == nil {
		t.Fatal("no TurnMetrics emitted")
	}
	if got.HighestFailedRepeatTier != failedRepeatNudge {
		t.Fatalf("HighestFailedRepeatTier = %d, want %d", got.HighestFailedRepeatTier, failedRepeatNudge)
	}
	if got.FailedRepeatStreak < failedRepeatNudge {
		t.Fatalf("FailedRepeatStreak = %d, want >= %d", got.FailedRepeatStreak, failedRepeatNudge)
	}
	if got.Outcome == "salvaged" {
		t.Fatalf("a nudged-then-recovered turn must not salvage: %+v", *got)
	}
}

// TestFailureBreakerIsNotPatchSpecific pins the breaker's scope explicitly.
// The design discussed starting with file.write_patch, but the shipped ladder
// is tool-agnostic: a read/search/subagent tool that fails identically four
// times must break the loop too. This test fixes that intent in code.
func TestFailureBreakerIsNotPatchSpecific(t *testing.T) {
	tracker := newProgressTracker()
	const args = `{"path":"a.go"}`

	for i := 0; i < failedRepeatStall; i++ {
		tracker.record("file.read", args, hashToolResult("boom"), false)
	}
	if got := tracker.failedRepeatTier("file.read", args); got != failedRepeatStall {
		t.Fatalf("failedRepeatTier = %d, want %d for a non-patch tool", got, failedRepeatStall)
	}
	tracker.noteFailedRepeatStall()
	if !tracker.failedRepeatStallActive() {
		t.Fatal("failed-repeat stall did not arm for a non-patch tool")
	}
	if tracker.assess() != assessHardStall {
		t.Fatal("assess() did not report a hard stall for a non-patch tool")
	}
}
