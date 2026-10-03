package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// inspectionRunner builds a runner with a scripted provider and a real session
// state, so the capture path runs exactly as it does in production.
func inspectionRunner(t *testing.T, p *agenttest.ScriptedProvider, state *session.State) *Runner {
	t.Helper()
	if state == nil {
		state = session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	}
	reg := registry.New()
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = false
	return r
}

// TestChatOnceAttemptSnapshotMatchesTheRequestTheProviderReceived is the
// acceptance criterion for the whole task: the snapshot must describe the
// request that actually went out. The fake provider records what it was handed,
// so the comparison is against the wire request, not against a re-derivation.
func TestChatOnceAttemptSnapshotMatchesTheRequestTheProviderReceived(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"an answer"}}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	r := inspectionRunner(t, p, state)

	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "SYSTEM-PROMPT"},
		{Role: schema.RoleUser, Content: "the user's question"},
	}
	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model", msgs, nil, false); err != nil {
		t.Fatalf("chatOnceAttempt: %v", err)
	}

	snap, ok := state.RequestInspection()
	if !ok {
		t.Fatal("no request inspection was captured")
	}
	if len(p.Requests) != 1 {
		t.Fatalf("the provider saw %d requests, want 1", len(p.Requests))
	}
	wire := p.Requests[0]

	if snap.Model != wire.Model {
		t.Fatalf("snapshot model %q, wire model %q", snap.Model, wire.Model)
	}
	if snap.Provider != p.Name() {
		t.Fatalf("snapshot provider %q, want %q", snap.Provider, p.Name())
	}
	if len(snap.Messages) != len(wire.Messages) {
		t.Fatalf("snapshot has %d messages, the wire request had %d", len(snap.Messages), len(wire.Messages))
	}
	for i, m := range wire.Messages {
		if snap.Messages[i].Role != string(m.Role) {
			t.Fatalf("message %d role: snapshot %q, wire %q", i, snap.Messages[i].Role, m.Role)
		}
		if snap.Messages[i].Content != m.Content {
			t.Fatalf("message %d content: snapshot %q, wire %q", i, snap.Messages[i].Content, m.Content)
		}
	}
	if !snap.Options.Streaming {
		t.Fatal("the wire request was streaming and the snapshot does not say so")
	}
}

// TestChatOnceAttemptSnapshotRecordsDroppedOptions pins the reason the request
// is captured rather than re-derived: a capability gate that removes the
// temperature or the thinking effort must be visible in the snapshot. A
// snapshot that showed the CONFIGURED values would describe a request that was
// never sent, and the user would have no way to discover the gate.
func TestChatOnceAttemptSnapshotRecordsDroppedOptions(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{"an answer"},
		// The backend accepts only its own temperature and does not do
		// reasoning: both gates fire.
		ProviderCaps: schema.ProviderCapabilities{TemperatureLocked: true, Reasoning: false},
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	r := inspectionRunner(t, p, state)

	temp := 0.9
	r.turnRequestOptions.temperature = &temp
	r.turnRequestOptions.thinking = "high"

	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false); err != nil {
		t.Fatalf("chatOnceAttempt: %v", err)
	}

	snap, _ := state.RequestInspection()
	wire := p.Requests[0]
	if wire.Temperature != nil {
		t.Fatal("precondition: the temperature should have been dropped on the wire")
	}
	if snap.Options.Temperature != nil {
		t.Fatalf("the snapshot shows a temperature of %v that was never sent", *snap.Options.Temperature)
	}
	if wire.Thinking != "" {
		t.Fatal("precondition: the thinking effort should have been dropped on the wire")
	}
	if snap.Options.Thinking != "" {
		t.Fatalf("the snapshot shows thinking %q that was never sent", snap.Options.Thinking)
	}
}

// TestChatOnceAttemptSnapshotRecordsFailureAndCancellationDistinctly pins the
// outcome vocabulary. "I stopped it" and "it broke" produce the same error
// value from a cancelled context, and a reader who cannot tell them apart looks
// for a bug that is not there.
func TestChatOnceAttemptSnapshotRecordsFailureAndCancellationDistinctly(t *testing.T) {
	t.Run("stream error is a failure", func(t *testing.T) {
		p := &agenttest.ScriptedProvider{
			Responses: []string{""},
			Errs:      []error{errScripted{}},
		}
		state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
		r := inspectionRunner(t, p, state)
		_, _ = r.chatOnceAttempt(context.Background(), p, "test-model",
			[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)

		snap, ok := state.RequestInspection()
		if !ok {
			t.Fatal("no snapshot")
		}
		if snap.Outcome.Status != session.InspectionFailed {
			t.Fatalf("status = %q, want failed", snap.Outcome.Status)
		}
		if !strings.Contains(snap.Outcome.Err, "scripted") {
			t.Fatalf("the failure text is missing: %q", snap.Outcome.Err)
		}
	})

	t.Run("a cancelled context is a cancellation", func(t *testing.T) {
		p := &agenttest.ScriptedProvider{Responses: []string{"an answer"}}
		state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
		r := inspectionRunner(t, p, state)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancelled before the attempt starts
		_, _ = r.chatOnceAttempt(ctx, p, "test-model",
			[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)

		snap, ok := state.RequestInspection()
		if !ok {
			t.Fatal("no snapshot: a request was still submitted and must be recorded")
		}
		if snap.Outcome.Status != session.InspectionCancelled {
			t.Fatalf("status = %q, want cancelled", snap.Outcome.Status)
		}
	})
}

// TestChatOnceAttemptSnapshotRecordsCompletion pins the happy path's outcome,
// and that it is worded about the ADAPTER rather than about a remote server.
func TestChatOnceAttemptSnapshotRecordsCompletion(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"an answer"}}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	r := inspectionRunner(t, p, state)

	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false); err != nil {
		t.Fatalf("chatOnceAttempt: %v", err)
	}
	snap, _ := state.RequestInspection()
	if snap.Outcome.Status != session.InspectionCompleted {
		t.Fatalf("status = %q, want completed", snap.Outcome.Status)
	}
	// The vocabulary must not claim the model or a server saw it.
	if strings.Contains(strings.ToLower(string(snap.Outcome.Status)), "deliver") ||
		strings.Contains(strings.ToLower(string(snap.Outcome.Status)), "acknowledg") {
		t.Fatalf("the status vocabulary claims a remote acknowledgement: %q", snap.Outcome.Status)
	}
}

// TestChatOnceAttemptSnapshotRecordsToolDefinitionsWhenNativeToolsAreOn pins
// that the tools offered to the model are visible. Without them, a reader
// asking "why did it not call the tool?" is looking at a request that does not
// show what it could have called.
func TestChatOnceAttemptSnapshotRecordsToolDefinitionsWhenNativeToolsAreOn(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"an answer"}}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "read a file",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Content: "ok"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true

	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, true); err != nil {
		t.Fatalf("chatOnceAttempt: %v", err)
	}

	snap, _ := state.RequestInspection()
	if len(snap.Tools) == 0 {
		t.Fatal("no tool definitions were recorded")
	}
	wire := p.Requests[0]
	if len(snap.Tools) != len(wire.Tools) {
		t.Fatalf("snapshot has %d tools, the wire request had %d", len(snap.Tools), len(wire.Tools))
	}
	for i, tool := range wire.Tools {
		if snap.Tools[i].Name != tool.Name {
			t.Fatalf("tool %d: snapshot %q, wire %q", i, snap.Tools[i].Name, tool.Name)
		}
	}
}

// TestChatOnceAttemptSnapshotIsNotMutatedByTheRuntime is the direction that
// matters most: the runtime keeps reading the request it sent while the reader
// looks at the snapshot. If the snapshot were a view over the same bytes, a
// later mutation would silently rewrite history on screen.
func TestChatOnceAttemptSnapshotIsNotMutatedByTheRuntime(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"an answer"}}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	r := inspectionRunner(t, p, state)

	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "ORIGINAL-SYSTEM"},
		{Role: schema.RoleUser, Content: "ORIGINAL-QUESTION"},
	}
	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model", msgs, nil, false); err != nil {
		t.Fatalf("chatOnceAttempt: %v", err)
	}

	// The caller (and the runtime) rewrite the slice they still hold.
	msgs[0].Content = "REWRITTEN-SYSTEM"
	msgs[1].Content = "REWRITTEN-QUESTION"

	snap, _ := state.RequestInspection()
	for _, m := range snap.Messages {
		if strings.Contains(m.Content, "REWRITTEN") {
			t.Fatalf("the snapshot changed when the runtime's own slice was rewritten: %q", m.Content)
		}
	}
}

// TestChatOnceAttemptSnapshotIsBoundedInPractice pins that the caps apply to
// the real capture path, not only to a hand-built snapshot. A 100 KiB user
// message is ordinary (a pasted file) and must not put 100 KiB on every
// subsequent frame.
func TestChatOnceAttemptSnapshotIsBoundedInPractice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"an answer"}}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	r := inspectionRunner(t, p, state)

	huge := strings.Repeat("q", session.MaxInspectionFieldBytes*3)
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: huge}}
	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model", msgs, nil, false); err != nil {
		t.Fatalf("chatOnceAttempt: %v", err)
	}

	snap, _ := state.RequestInspection()
	if !snap.Truncated {
		t.Fatal("an oversized message reached the snapshot unmarked")
	}
	if got := snap.TotalContentBytes(); got > session.MaxInspectionTotalBytes {
		t.Fatalf("the snapshot holds %d bytes, want <= %d", got, session.MaxInspectionTotalBytes)
	}
	if len(snap.Messages[0].Content) > session.MaxInspectionFieldBytes {
		t.Fatalf("the field kept %d bytes", len(snap.Messages[0].Content))
	}
	// And the WIRE request was untouched: bounding the snapshot must not bound
	// what the model is sent.
	if len(p.Requests[0].Messages[0].Content) != len(huge) {
		t.Fatalf("the snapshot's cap changed the request that went to the provider (%d bytes)",
			len(p.Requests[0].Messages[0].Content))
	}
}

// TestChatOnceAttemptSnapshotIsNotWrittenByInternalHelpers pins the plan's
// exclusion: the title, summarizer and digest helpers also call a provider, and
// folding them in would make "the last request" mean whichever internal helper
// ran most recently — the opposite of what the view is opened to see.
func TestChatOnceAttemptSnapshotIsNotWrittenByInternalHelpers(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"a title"}}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	r := inspectionRunner(t, p, state)

	// Runner.Chat is the separate digest helper: it calls the provider
	// directly and never goes through chatOnceAttempt. A state that has only
	// ever run a digest must therefore have NO conversation snapshot — the
	// digest is not a conversation request and presenting it as one would
	// describe a request the user never sent.
	if _, err := r.Chat(context.Background(), []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "summarise this"},
	}); err != nil {
		t.Fatalf("Runner.Chat: %v", err)
	}
	if _, ok := state.RequestInspection(); ok {
		t.Fatal("the digest helper wrote a conversation request snapshot")
	}
	// And it really did reach the provider, so the assertion above is about
	// the snapshot path rather than about a helper that never ran.
	if p.Calls == 0 {
		t.Fatal("precondition: the digest helper must have called the provider")
	}
}

// errScripted is a distinguishable provider failure.
type errScripted struct{}

func (errScripted) Error() string { return "scripted stream failure" }
