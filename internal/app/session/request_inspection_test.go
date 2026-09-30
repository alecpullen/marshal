package session

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"marshal/internal/app/config"
)

// inspectionRequest builds a request inspection with content sized so caps can
// be exercised without allocating megabytes by accident.
func inspectionRequest(msgs ...InspectionMessage) RequestInspection {
	return RequestInspection{
		AttemptID:  1,
		At:         time.Unix(1000, 0),
		Provider:   "somewhere",
		Model:      "some-model",
		Generation: "gen-1",
		LeafID:     7,
		Messages:   msgs,
	}
}

// --- the core contract: bounded copying --------------------------------

// TestRequestInspectionIsBoundedAndSaysSo is the acceptance criterion the whole
// feature rests on: a snapshot must never claim to be complete when it is a
// prefix. A reader who believes a truncated prompt is the whole prompt draws
// the wrong conclusion about what the model saw, and there is nothing on screen
// to warn them.
func TestRequestInspectionIsBoundedAndSaysSo(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	huge := strings.Repeat("x", MaxInspectionFieldBytes*2)
	s.SetRequestInspection(inspectionRequest(
		InspectionMessage{Role: "user", Content: huge},
	))

	got, ok := s.RequestInspection()
	if !ok {
		t.Fatal("no inspection was stored")
	}
	if got.Truncated != true {
		t.Fatal("an oversized field did not mark the snapshot truncated")
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Messages))
	}
	msg := got.Messages[0]
	if !msg.Truncated {
		t.Fatal("the capped message is not marked truncated")
	}
	if len(msg.Content) > MaxInspectionFieldBytes {
		t.Fatalf("the field kept %d bytes, want <= %d", len(msg.Content), MaxInspectionFieldBytes)
	}
	if msg.OmittedBytes == 0 {
		t.Fatal("the omitted byte count is zero, so the reader cannot tell how much was dropped")
	}
	// The count must be ARITHMETICALLY right, not merely non-zero: it is the
	// only number that tells the reader the scale of what is missing.
	if want := len(huge) - len(msg.Content); msg.OmittedBytes != want {
		t.Fatalf("omitted %d bytes, want %d", msg.OmittedBytes, want)
	}
	// The content retained is the LEADING content: a prompt's instruction is at
	// the start, and keeping the tail would drop exactly the part that matters.
	if !strings.HasPrefix(huge, msg.Content) {
		t.Fatal("the retained content is not a leading prefix of the original")
	}
}

// TestRequestInspectionTotalBudgetIsEnforced pins the OVERALL bound, not just
// the per-field one. Many fields each within their own cap can still add up to
// an unbounded snapshot, and the budget is what stops a 400-message
// conversation from pinning the whole thing in memory.
func TestRequestInspectionTotalBudgetIsEnforced(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	// Enough large-but-legal messages to exceed the total budget. The count is
	// derived from the constants rather than hard-coded, so the test still
	// exercises the budget if either number is retuned.
	per := MaxInspectionFieldBytes
	enough := MaxInspectionTotalBytes/per + 4
	msgs := make([]InspectionMessage, 0, enough)
	for i := 0; i < enough; i++ {
		msgs = append(msgs, InspectionMessage{
			Role:    "user",
			Content: strings.Repeat("y", per),
		})
	}
	s.SetRequestInspection(inspectionRequest(msgs...))

	got, ok := s.RequestInspection()
	if !ok {
		t.Fatal("no inspection was stored")
	}
	if total := got.TotalContentBytes(); total > MaxInspectionTotalBytes {
		t.Fatalf("the snapshot holds %d bytes of content, want <= %d", total, MaxInspectionTotalBytes)
	}
	if !got.Truncated {
		t.Fatal("a snapshot that hit the total budget is not marked truncated")
	}
	if got.OmittedBytes == 0 {
		t.Fatal("hitting the total budget dropped content with no record of how much")
	}
}

// TestRequestInspectionKeepsTheLeadingMessages pins WHICH messages survive the
// budget. The system prompt and the first user turn are what the request is
// FOR; dropping the head to keep the tail would produce a snapshot that
// explains nothing about why the model answered as it did.
func TestRequestInspectionKeepsTheLeadingMessages(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	per := MaxInspectionFieldBytes - 1
	msgs := []InspectionMessage{{Role: "system", Content: "FIRST-SYSTEM-PROMPT"}}
	for i := 0; i < 64; i++ {
		msgs = append(msgs, InspectionMessage{Role: "user", Content: strings.Repeat("y", per)})
	}
	s.SetRequestInspection(inspectionRequest(msgs...))

	got, _ := s.RequestInspection()
	if len(got.Messages) == 0 {
		t.Fatal("every message was dropped")
	}
	if got.Messages[0].Content != "FIRST-SYSTEM-PROMPT" {
		t.Fatalf("the first message is %q, want the system prompt", got.Messages[0].Content)
	}
	// And the drop, when it happens, is a TAIL drop: the kept messages are a
	// leading run of the original.
	for i, m := range got.Messages {
		if m.Role != msgs[i].Role {
			t.Fatalf("message %d is %q, want the original's %q: the snapshot is not a leading run",
				i, m.Role, msgs[i].Role)
		}
	}
}

// --- no aliasing --------------------------------------------------------

// TestRequestInspectionDoesNotAliasTheCaller is the ownership rule. The runtime
// keeps building on the request it handed in (retries mutate nothing today, but
// the snapshot must not depend on that), and the reader must never be able to
// change what is displayed by mutating a slice they still hold.
func TestRequestInspectionDoesNotAliasTheCaller(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	msgs := []InspectionMessage{
		{Role: "user", Content: "original"},
		{Role: "assistant", Content: "answer"},
	}
	tools := []InspectionTool{{Name: "file.read", Description: "read a file"}}
	toolCalls := []InspectionToolCall{{ID: "c1", Name: "file.read", Args: `{"path":"a.go"}`}}
	req := inspectionRequest(msgs...)
	req.Tools = tools
	req.Messages[1].ToolCalls = toolCalls

	s.SetRequestInspection(req)

	// Mutate EVERY input the caller still holds.
	msgs[0].Content = "MUTATED"
	tools[0].Name = "MUTATED"
	toolCalls[0].Name = "MUTATED"

	got, _ := s.RequestInspection()
	if got.Messages[0].Content == "MUTATED" {
		t.Fatal("the snapshot aliases the caller's message slice")
	}
	if len(got.Tools) == 0 || got.Tools[0].Name == "MUTATED" {
		t.Fatal("the snapshot aliases the caller's tool slice")
	}
	if len(got.Messages) < 2 || len(got.Messages[1].ToolCalls) == 0 {
		t.Fatal("the tool calls were lost")
	}
	if got.Messages[1].ToolCalls[0].Name == "MUTATED" {
		t.Fatal("the snapshot aliases the caller's tool-call slice")
	}

	// And a caller who mutates what they READ must not change the snapshot.
	read, _ := s.RequestInspection()
	read.Messages[0].Content = "READ-MUTATED"
	read.Tools[0].Name = "READ-MUTATED"
	again, _ := s.RequestInspection()
	if again.Messages[0].Content == "READ-MUTATED" {
		t.Fatal("RequestInspection handed out the panel's own message slice")
	}
	if again.Tools[0].Name == "READ-MUTATED" {
		t.Fatal("RequestInspection handed out the panel's own tool slice")
	}
}

// --- outcome races ------------------------------------------------------

// TestRequestInspectionOutcomeIsGuardedByAttemptID is the race rule: a late
// completion from a superseded attempt must not relabel the newer request. The
// provider may take minutes to fail, and by then the user is looking at a
// different request — labelling it "failed" would be a lie about the thing on
// screen.
func TestRequestInspectionOutcomeIsGuardedByAttemptID(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	first := inspectionRequest(InspectionMessage{Role: "user", Content: "first"})
	first.AttemptID = 1
	s.SetRequestInspection(first)

	second := inspectionRequest(InspectionMessage{Role: "user", Content: "second"})
	second.AttemptID = 2
	s.SetRequestInspection(second)

	// The FIRST attempt's completion arrives now.
	if s.SetRequestInspectionOutcome(1, InspectionOutcome{
		Status: InspectionFailed, Err: "the first attempt blew up",
	}) {
		t.Fatal("a superseded attempt's outcome was applied")
	}

	got, _ := s.RequestInspection()
	if got.Messages[0].Content != "second" {
		t.Fatalf("the snapshot is %q, want the newer request", got.Messages[0].Content)
	}
	if got.Outcome.Status == InspectionFailed {
		t.Fatalf("the newer request was relabelled by an older attempt's failure: %+v", got.Outcome)
	}

	// The CURRENT attempt's outcome is applied.
	if !s.SetRequestInspectionOutcome(2, InspectionOutcome{Status: InspectionFailed, Err: "boom"}) {
		t.Fatal("the current attempt's outcome was refused")
	}
	got, _ = s.RequestInspection()
	if got.Outcome.Status != InspectionFailed {
		t.Fatalf("outcome = %v, want failed", got.Outcome.Status)
	}
	if got.Outcome.Err != "boom" {
		t.Fatalf("outcome error = %q, want the failure text", got.Outcome.Err)
	}
}

// TestRequestInspectionWithNoRequestYetIsExplicit pins the empty state. The
// caller must be able to tell "nothing has been sent yet" from "a request was
// sent and had no messages", because the panel shows different words for them
// and one of them is an empty panel that looks broken.
func TestRequestInspectionWithNoRequestYetIsExplicit(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	if _, ok := s.RequestInspection(); ok {
		t.Fatal("a fresh state reports a request inspection")
	}
	// An outcome for a request that does not exist must be refused rather than
	// creating one: the outcome describes a request, and there is none.
	if s.SetRequestInspectionOutcome(1, InspectionOutcome{Status: InspectionDispatched}) {
		t.Fatal("an outcome was applied with no request to apply it to")
	}
	if _, ok := s.RequestInspection(); ok {
		t.Fatal("applying an outcome manufactured a request inspection")
	}
}

// TestRequestInspectionOutcomeDefaultsToDispatched pins what "no outcome yet"
// means. The snapshot is written immediately before the adapter call, so its
// truthful state at that moment is "submitted, not yet known" — never
// "completed", which would claim an acknowledgement the adapter has not given.
func TestRequestInspectionOutcomeDefaultsToDispatched(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	s.SetRequestInspection(inspectionRequest(InspectionMessage{Role: "user", Content: "hi"}))

	got, _ := s.RequestInspection()
	if got.Outcome.Status != InspectionDispatched {
		t.Fatalf("status = %v before any outcome, want dispatched", got.Outcome.Status)
	}
}

// --- child isolation ----------------------------------------------------

// TestRequestInspectionIsPerStateSoChildrenAreIsolated pins the scope rule: a
// child runner writes to its OWN state. A snapshot stored on the parent by a
// child would make the parent's Context tab describe a request the parent never
// sent, which is exactly the mislabelling Task 10's scope label exists to
// prevent.
func TestRequestInspectionIsPerStateSoChildrenAreIsolated(t *testing.T) {
	parent := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	parentReq := inspectionRequest(InspectionMessage{Role: "user", Content: "PARENT-REQUEST"})
	parentReq.AttemptID = 1
	parent.SetRequestInspection(parentReq)

	childReq := inspectionRequest(InspectionMessage{Role: "user", Content: "CHILD-REQUEST"})
	childReq.AttemptID = 1 // same id: the states are the scope, not the id
	child.SetRequestInspection(childReq)

	pg, _ := parent.RequestInspection()
	cg, _ := child.RequestInspection()
	if pg.Messages[0].Content != "PARENT-REQUEST" {
		t.Fatalf("the parent sees %q", pg.Messages[0].Content)
	}
	if cg.Messages[0].Content != "CHILD-REQUEST" {
		t.Fatalf("the child sees %q", cg.Messages[0].Content)
	}
}

// --- concurrency --------------------------------------------------------

// TestRequestInspectionIsSafeUnderConcurrentAccess pins that the setter can be
// called from a runner goroutine while the TUI reads, which is the actual
// arrangement: the request is captured on the turn's goroutine and rendered by
// the UI. Run with -race to make this meaningful.
func TestRequestInspectionIsSafeUnderConcurrentAccess(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			req := inspectionRequest(InspectionMessage{
				Role:    "user",
				Content: strings.Repeat("z", 4096),
			})
			req.AttemptID = uint64(i + 1)
			s.SetRequestInspection(req)
			s.SetRequestInspectionOutcome(uint64(i+1), InspectionOutcome{Status: InspectionCompleted})
		}
	}()

	for i := 0; i < 200; i++ {
		got, _ := s.RequestInspection()
		// Touch the slices the snapshot owns, so a shallow copy would be caught
		// here rather than in the field.
		for _, m := range got.Messages {
			_ = len(m.Content)
		}
	}
	close(stop)
	wg.Wait()
}

// --- content rules ------------------------------------------------------

// TestRequestInspectionCapsAtUTF8Boundaries pins that cutting does not produce
// invalid UTF-8. A byte-count cap applied blindly splits a multi-byte rune, and
// the result is a snapshot that renders as a replacement character in the
// middle of the user's own prompt.
func TestRequestInspectionCapsAtUTF8Boundaries(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	// Three-byte runes: a byte cap landing mid-rune is very likely.
	huge := strings.Repeat("日", MaxInspectionFieldBytes)
	s.SetRequestInspection(inspectionRequest(InspectionMessage{Role: "user", Content: huge}))

	got, _ := s.RequestInspection()
	content := got.Messages[0].Content
	if !utf8.ValidString(content) {
		t.Fatal("the capped content is not valid UTF-8")
	}
	if !strings.HasPrefix(huge, content) {
		t.Fatal("the retained content is not a prefix of the original")
	}
	// The omitted count still accounts for every dropped BYTE, so the number
	// the reader sees matches the byte count of the real content.
	if want := len(huge) - len(content); got.Messages[0].OmittedBytes != want {
		t.Fatalf("omitted %d bytes, want %d", got.Messages[0].OmittedBytes, want)
	}
}

// TestRequestInspectionRecordsOptionsAndTools pins that the snapshot carries
// what the request actually SAID, not what the config says. Options are gated
// by capability at dispatch time (a temperature-locked backend, a provider with
// no reasoning support), so the only truthful source is the request itself.
func TestRequestInspectionRecordsOptionsAndTools(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	temp := 0.3
	maxTok := 4096
	req := inspectionRequest(InspectionMessage{Role: "user", Content: "hi"})
	req.Tools = []InspectionTool{
		{Name: "file.read", Description: "read a file", Parameters: `{"type":"object"}`},
	}
	req.Options = InspectionOptions{
		Thinking:    "high",
		Streaming:   true,
		MaxTokens:   &maxTok,
		Temperature: &temp,
	}
	s.SetRequestInspection(req)

	got, _ := s.RequestInspection()
	if len(got.Tools) != 1 || got.Tools[0].Name != "file.read" {
		t.Fatalf("tools = %+v, want the one that was sent", got.Tools)
	}
	if got.Options.Thinking != "high" {
		t.Fatalf("thinking = %q, want high", got.Options.Thinking)
	}
	if !got.Options.Streaming {
		t.Fatal("streaming is not recorded")
	}
	if got.Options.MaxTokens == nil || *got.Options.MaxTokens != 4096 {
		t.Fatalf("max tokens = %v, want 4096", got.Options.MaxTokens)
	}
	if got.Options.Temperature == nil || *got.Options.Temperature != 0.3 {
		t.Fatalf("temperature = %v, want 0.3", got.Options.Temperature)
	}
}

// TestRequestInspectionRecordsIdentityAndTiming pins the identifiers a reader
// needs to place the snapshot: when it was sent, which provider and model, and
// which generation and leaf it belonged to. Without them the panel can show a
// request but not say WHICH request.
func TestRequestInspectionRecordsIdentityAndTiming(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	req := inspectionRequest(InspectionMessage{Role: "user", Content: "hi"})
	s.SetRequestInspection(req)

	got, _ := s.RequestInspection()
	if got.Provider != "somewhere" {
		t.Fatalf("provider = %q", got.Provider)
	}
	if got.Model != "some-model" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.Generation != "gen-1" {
		t.Fatalf("generation = %q", got.Generation)
	}
	if got.LeafID != 7 {
		t.Fatalf("leaf = %d, want 7", got.LeafID)
	}
	if got.At.IsZero() {
		t.Fatal("the dispatch time is not recorded")
	}
	if got.AttemptID != 1 {
		t.Fatalf("attempt id = %d, want 1", got.AttemptID)
	}
}

// TestRequestInspectionToolCallArgumentsAreBounded pins that a tool call's
// arguments are subject to the same cap as any other content. A tool call
// carrying a whole file's contents as an argument is the ordinary case, not an
// exotic one.
func TestRequestInspectionToolCallArgumentsAreBounded(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	huge := strings.Repeat("a", MaxInspectionFieldBytes*2)
	msgs := []InspectionMessage{{
		Role:    "assistant",
		Content: "calling a tool",
		ToolCalls: []InspectionToolCall{{
			ID: "call-1", Name: "file.write", Args: `{"content":"` + huge + `"}`,
		}},
	}}
	s.SetRequestInspection(inspectionRequest(msgs...))

	got, _ := s.RequestInspection()
	tc := got.Messages[0].ToolCalls[0]
	if len(tc.Args) > MaxInspectionFieldBytes {
		t.Fatalf("tool-call args kept %d bytes, want <= %d", len(tc.Args), MaxInspectionFieldBytes)
	}
	if !tc.Truncated {
		t.Fatal("capped tool-call args are not marked truncated")
	}
	if tc.OmittedBytes == 0 {
		t.Fatal("capped tool-call args dropped content with no record")
	}
	if !strings.HasPrefix(`{"content":"`+huge+`"}`, tc.Args) {
		t.Fatal("the retained args are not a leading prefix")
	}
}

// TestRequestInspectionCappedFieldDoesNotRetainTheOriginal pins the memory
// property the cap's own rationale claims. MaxInspectionTotalBytes is documented
// as "the number that makes the snapshot's memory use something a caller can
// reason about", and that is only true if a capped field releases the bytes it
// dropped. capBytes returns a substring, and a Go substring shares its backing
// array — so the naive s[:cut] kept the entire original resident while
// TotalContentBytes (which measures the retained prefix) reported the capped
// size and saw nothing wrong.
//
// Retention is observable because a substring and its parent share the SAME
// bytes: unsafe.StringData points into the backing array, so a reslice reports
// the original's address and a clone reports a new allocation's. That makes the
// assertion below a real regression guard — revert capBytes to `return s[:cut]`
// and it fails — rather than a restatement of the value being correct, which a
// reslice also satisfies.
func TestRequestInspectionCappedFieldDoesNotRetainTheOriginal(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

	// Shaped like the reported case: a message several times the field cap whose
	// capped prefix is a small fraction of it.
	huge := strings.Repeat("x", MaxInspectionFieldBytes*3)
	s.SetRequestInspection(inspectionRequest(InspectionMessage{Role: "user", Content: huge}))

	got, _ := s.RequestInspection()
	kept := got.Messages[0].Content

	if want := huge[:MaxInspectionFieldBytes]; kept != want {
		t.Fatalf("the capped field kept %d bytes, want the leading %d", len(kept), MaxInspectionFieldBytes)
	}
	if sharesStorage(kept, huge) {
		t.Fatalf("the capped %d-byte field still references the original %d-byte string's storage, so the snapshot pins it",
			len(kept), len(huge))
	}
	// The documented accounting: the snapshot holds the capped content, not the
	// original, so the byte budget is enforced against what it retains.
	if total := got.TotalContentBytes(); total != len(kept) {
		t.Fatalf("TotalContentBytes = %d, want the capped %d", total, len(kept))
	}
	if total := got.TotalContentBytes(); total > MaxInspectionTotalBytes {
		t.Fatalf("TotalContentBytes = %d, want <= %d", total, MaxInspectionTotalBytes)
	}

	// The original stays untouched by the cap and by the snapshot: bounding a
	// field must not rewrite the request the runtime still holds.
	if len(huge) != MaxInspectionFieldBytes*3 {
		t.Fatalf("the caller's string changed length to %d", len(huge))
	}
}

// sharesStorage reports whether two non-empty strings are views over the same
// backing bytes — the property that makes a capped prefix pin its parent.
func sharesStorage(a, b string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return unsafe.StringData(a) == unsafe.StringData(b)
}

// TestRequestInspectionToolDropsAreByteAccounted pins the arithmetic the
// OmittedBytes field promises: retained bytes plus omitted bytes reconstructs
// the ORIGINAL content, so a reader can see how much of the request is missing
// rather than only how many entries were dropped.
//
// The messages arms have always counted their dropped bytes (countMessageBytes).
// The tools arms did not: they recorded ToolsOmitted and nothing else, so a
// snapshot that dropped 50 tool definitions reported zero missing bytes for
// them and the sum came up short with no way to notice. Both tools arms are
// covered — the entry cap and the total budget — because they are separate code
// paths and only pinning one would leave the other free to regress.
func TestRequestInspectionToolDropsAreByteAccounted(t *testing.T) {
	// originalBytes is the same measure the snapshot's own accounting uses, so
	// the assertion is about the arithmetic rather than about a second opinion
	// on what a tool's bytes are.
	originalBytes := func(msgs []InspectionMessage, tools []InspectionTool) int {
		return countMessageBytes(msgs) + countToolBytes(tools)
	}

	t.Run("entry cap", func(t *testing.T) {
		s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

		// One more than the cap so a drop is forced, and small enough that the
		// total budget is nowhere near binding: this subtest must fail only if
		// the ENTRY cap's accounting is wrong.
		tools := make([]InspectionTool, 0, MaxInspectionTools+7)
		for i := 0; i < MaxInspectionTools+7; i++ {
			tools = append(tools, InspectionTool{
				Name:        fmt.Sprintf("tool-%d", i),
				Description: "a tool",
				Parameters:  `{"type":"object"}`,
			})
		}
		msgs := []InspectionMessage{{Role: "user", Content: "hi"}}
		req := inspectionRequest(msgs...)
		req.Tools = tools
		s.SetRequestInspection(req)

		got, _ := s.RequestInspection()
		if got.ToolsOmitted != 7 {
			t.Fatalf("ToolsOmitted = %d, want 7", got.ToolsOmitted)
		}
		if len(got.Tools) != MaxInspectionTools {
			t.Fatalf("kept %d tools, want the cap %d", len(got.Tools), MaxInspectionTools)
		}

		retained := got.TotalContentBytes()
		if want := originalBytes(msgs, tools) - retained; got.OmittedBytes != want {
			t.Fatalf("OmittedBytes = %d, want %d (original %d - retained %d): the dropped tools' bytes are unaccounted",
				got.OmittedBytes, want, originalBytes(msgs, tools), retained)
		}
	})

	t.Run("total budget", func(t *testing.T) {
		s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})

		// Parameters are not field-capped, so a run of large ones is the way to
		// overrun the total budget with the ENTRY cap untouched — the other arm.
		per := 128 * 1024
		tools := make([]InspectionTool, 0, 32)
		for i := 0; i < 32; i++ {
			tools = append(tools, InspectionTool{
				Name:        fmt.Sprintf("tool-%d", i),
				Description: "a tool",
				Parameters:  `{"blob":"` + strings.Repeat("p", per) + `"}`,
			})
		}
		msgs := []InspectionMessage{{Role: "user", Content: "hi"}}
		req := inspectionRequest(msgs...)
		req.Tools = tools
		s.SetRequestInspection(req)

		got, _ := s.RequestInspection()
		if got.ToolsOmitted == 0 {
			t.Fatal("precondition: the total budget must drop some tools here")
		}
		if len(got.Tools) == len(tools) {
			t.Fatal("precondition: no tool was dropped")
		}

		retained := got.TotalContentBytes()
		if retained > MaxInspectionTotalBytes {
			t.Fatalf("retained %d bytes, want <= %d", retained, MaxInspectionTotalBytes)
		}
		if want := originalBytes(msgs, tools) - retained; got.OmittedBytes != want {
			t.Fatalf("OmittedBytes = %d, want %d (original %d - retained %d): the budget-dropped tools' bytes are unaccounted",
				got.OmittedBytes, want, originalBytes(msgs, tools), retained)
		}
	})
}

// TestRequestInspectionHasNoPersistenceSideEffects pins the plan's constraint:
// no SQLite migration, no config credentials, no logging. The snapshot is
// in-memory only, so a State with no DB must work and nothing must be written.
func TestRequestInspectionHasNoPersistenceSideEffects(t *testing.T) {
	// Persistence{} has a nil DB: every write path in State checks for that.
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	if s.DB() != nil {
		t.Fatal("precondition: this state must have no database")
	}

	s.SetRequestInspection(inspectionRequest(InspectionMessage{Role: "user", Content: "hi"}))
	if _, ok := s.RequestInspection(); !ok {
		t.Fatal("an in-memory-only state cannot hold a request inspection")
	}
}
