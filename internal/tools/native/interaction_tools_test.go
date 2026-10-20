package native

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/registry"
)

// TestModeRequestOnlyRequestsNormalActAndSystem pins the canonical request: an
// Act request, in its canonical spelling and in both legacy spellings, asks for
// editable access and nothing more — it never chooses a mode.
func TestModeRequestOnlyRequestsNormalActAndSystem(t *testing.T) {
	for _, mode := range []string{"act", "edit", "copilot"} {
		t.Run(mode, func(t *testing.T) {
			f := newModeRequestFixture(t, config.Default())
			events := f.subscribe(t)

			done := startModeRequest(f.reg, context.Background(), "call-1", fmt.Sprintf(`{"mode":%q}`, mode))
			req := awaitAccessRequest(t, events)
			if !req.MissingEditing {
				t.Fatalf("mode %q flags = %+v, want editable access requested", mode, req)
			}
			if req.MissingSystem {
				t.Fatalf("mode %q flags = %+v, want no system request", mode, req)
			}
			if !strings.Contains(req.Reason, "editable access") {
				t.Fatalf("reason = %q, want the editable-access wording", req.Reason)
			}

			resolveAccess(t, f.state, req, false, "", testAccessPrepare(nil))
			if out := awaitOutcome(t, done); out.err != nil {
				t.Fatalf("mode.request %q: %v", mode, out.err)
			}
			// A refused request must not have granted the modifier the model
			// was never allowed to ask for.
			if got := f.state.InteractionState(); got.Auto {
				t.Fatalf("session tuple = %+v, want no Auto from a mode hint", got)
			}
		})
	}
}

// TestModeRequestCannotRequestAuto pins that Auto is the user's choice: asking
// for it is refused outright, before a request is published or any state moves.
func TestModeRequestCannotRequestAuto(t *testing.T) {
	f := newModeRequestFixture(t, config.Default())

	out := awaitOutcome(t, startModeRequest(f.reg, context.Background(), "call-1", `{"mode":"auto"}`))
	if out.err == nil || !strings.Contains(out.err.Error(), "Auto") {
		t.Fatalf("err = %v, want an explicit Auto refusal", out.err)
	}
	if _, pending := f.state.AccessRequest(); pending {
		t.Fatal("an Auto request published a pending access request")
	}
	if got := f.state.InteractionState(); got.Auto || got.Mode != interaction.ModeAsk {
		t.Fatalf("session tuple = %+v, want it unchanged by the refused request", got)
	}
}

// TestQuestionUsesCurrentAutoModifier pins that clarification follows the LIVE
// modifier: Auto suppresses the prompt, and clearing Auto admits the very same
// tool again with no rebuild.
func TestQuestionUsesCurrentAutoModifier(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.questionAskTool()
	args := []byte(`{"questions":[{"question":"Proceed?"}]}`)

	on := true
	if _, err := state.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatalf("enable Auto: %v", err)
	}
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: args}); err == nil || !strings.Contains(err.Error(), "Auto is enabled") {
		t.Fatalf("err = %v, want the Auto refusal", err)
	}
	if state.PendingQuestion() != nil {
		t.Fatal("an Auto-suppressed question left a pending prompt behind")
	}

	// Clearing Auto reaches this same tool immediately: the call now passes the
	// modifier gate and fails only because its context is already done, never
	// with the Auto refusal.
	off := false
	if _, err := state.ApplyInteraction(interaction.Patch{Auto: &off}); err != nil {
		t.Fatalf("disable Auto: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Handler(ctx, registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("a question with a done context reported success")
	}
	if strings.Contains(err.Error(), "Auto is enabled") {
		t.Fatalf("err = %v, want the gate to admit the question once Auto is cleared", err)
	}
}

// TestAskAllowsReadOnlyDelegationBookkeeping pins that Ask still asks: a
// read-only clarification passes the modifier gate, so it is only Auto that
// suppresses questions. A done context keeps the assertion non-blocking.
func TestAskAllowsReadOnlyDelegationBookkeeping(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	ask := interaction.ModeAsk
	if _, err := state.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("set Ask: %v", err)
	}
	if got := state.InteractionState(); got.Mode != interaction.ModeAsk || got.Auto {
		t.Fatalf("session tuple = %+v, want Ask without Auto", got)
	}

	tools := &toolSet{sessionState: state}
	tool := tools.questionAskTool()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Handler(ctx, registry.ToolCall{Args: []byte(`{"questions":[{"question":"Which path?"}]}`)})
	if err == nil {
		t.Fatal("a question with a done context reported success")
	}
	if strings.Contains(err.Error(), "Auto is enabled") {
		t.Fatalf("err = %v, want Ask to admit the read-only clarification", err)
	}
}
