package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// runSlotHarness drives one session through both run paths: the service's,
// which a marshal connection reaches, and ACP's own TurnManager. Each path has
// its own runner, so a test can tell which one started.
type runSlotHarness struct {
	state   *session.State
	served  *service.Session
	turns   *TurnManager
	service *controlRunner
	acp     *controlRunner
}

// newRunSlotHarness builds the harness over one state shared by both paths.
func newRunSlotHarness(t *testing.T) *runSlotHarness {
	t.Helper()
	state := seamState(t, controlSession)
	onService := newControlRunner()
	onACP := newControlRunner()

	_, served := service.New(service.Deps{
		State:  state,
		Runner: func() service.Runner { return onService },
	})
	broker := pubsub.NewBroker[session.Event]()
	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: id,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, goal string) error {
					return onACP.Run(ctx, goal)
				}),
				Events: broker,
				State:  state,
			}, id == controlSession
		},
		Notify: func(string, any) error { return nil },
	})
	return &runSlotHarness{
		state:   state,
		served:  served,
		turns:   turns,
		service: onService,
		acp:     onACP,
	}
}

// promptOnACP starts session/prompt on the turn manager and reports its error.
func (h *runSlotHarness) promptOnACP(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.turns.PromptTurn(ctx, json.RawMessage(
			`{"sessionId":"s1","prompt":[{"type":"text","text":"hello"}]}`))
		done <- err
	}()
	return done
}

// promptOnService starts the service's Prompt and reports its error.
func (h *runSlotHarness) promptOnService(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.served.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
		done <- err
	}()
	return done
}

// requireFree fails when the session's run slot is still claimed, and frees a
// claim it had to take to find out.
func (h *runSlotHarness) requireFree(t *testing.T) {
	t.Helper()
	release, ok := h.state.ClaimRun()
	if !ok {
		t.Fatal("the session's run slot is still claimed")
	}
	release()
}

// waitErr waits for a started path to report, failing the test if it does not.
func waitErr(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return", what)
		return nil
	}
}

// TestAnACPTurnRefusesAServiceRun pins one direction of the shared run slot
// (P4 D2): while an ACP turn runs, a service run is refused as busy and its
// runner never starts; once the turn ends the service run proceeds.
func TestAnACPTurnRefusesAServiceRun(t *testing.T) {
	h := newRunSlotHarness(t)
	release := make(chan struct{})
	h.acp.setBody(func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	turn := h.promptOnACP(context.Background())
	h.acp.waitStart(t)

	_, err := h.served.Prompt(context.Background(), acpwire.PromptParams{Text: "second"})
	var busy *acpwire.Error
	if !errors.As(err, &busy) || busy.Code != acpwire.CodeBusy {
		t.Fatalf("the service's prompt error = %v, want CodeBusy", err)
	}
	if got := h.service.goalsSeen(); len(got) != 0 {
		t.Fatalf("the service's runner ran %v while an ACP turn held the slot", got)
	}

	close(release)
	if err := waitErr(t, turn, "the ACP turn"); err != nil {
		t.Fatalf("the ACP turn: %v", err)
	}

	ran := h.promptOnService(context.Background())
	if err := waitErr(t, ran, "the service run"); err != nil {
		t.Fatalf("the service run after the ACP turn: %v", err)
	}
	if got, want := h.service.goalsSeen(), []string{"hello"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("the service's runner saw %v, want %v", got, want)
	}
}

// TestAServiceRunRefusesAnACPTurn pins the other direction: while a service
// run is in flight, ACP's turn is refused with the sentence it already uses,
// and its runner never starts; once the run ends the turn proceeds.
func TestAServiceRunRefusesAnACPTurn(t *testing.T) {
	h := newRunSlotHarness(t)
	release := make(chan struct{})
	h.service.setBody(func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	run := h.promptOnService(context.Background())
	h.service.waitStart(t)

	_, err := h.turns.PromptTurn(context.Background(), json.RawMessage(
		`{"sessionId":"s1","prompt":[{"type":"text","text":"second"}]}`))
	if err == nil || !strings.Contains(err.Error(), "already has an active turn") {
		t.Fatalf("the ACP turn's error = %v, want the active-turn refusal", err)
	}
	if got := h.acp.goalsSeen(); len(got) != 0 {
		t.Fatalf("the ACP runner ran %v while a service run held the slot", got)
	}

	close(release)
	if err := waitErr(t, run, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}

	turn := h.promptOnACP(context.Background())
	if err := waitErr(t, turn, "the ACP turn"); err != nil {
		t.Fatalf("the ACP turn after the service run: %v", err)
	}
	if got := h.acp.goalsSeen(); len(got) != 1 {
		t.Fatalf("the ACP runner saw %v, want one goal", got)
	}
}

// TestARefusedStartLeavesTheSlotFree pins the refusal paths' cleanup: a
// refusal on either path, repeated, leaves no claim behind, so the other path's
// next run starts as soon as the holder ends.
func TestARefusedStartLeavesTheSlotFree(t *testing.T) {
	h := newRunSlotHarness(t)
	release := make(chan struct{})
	h.service.setBody(func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	run := h.promptOnService(context.Background())
	h.service.waitStart(t)

	for i := 0; i < 2; i++ {
		if _, err := h.turns.PromptTurn(context.Background(), json.RawMessage(
			`{"sessionId":"s1","prompt":[{"type":"text","text":"refused"}]}`)); err == nil {
			t.Fatal("a refused ACP turn reported no error")
		}
	}

	// The refusals deleted their own slots and took no claim: with the holder
	// still running, the slot is claimed exactly once, by the holder.
	if releaseSlot, ok := h.state.ClaimRun(); ok {
		releaseSlot()
		t.Fatal("a refused ACP start left the run slot free while the service run held it")
	}

	close(release)
	if err := waitErr(t, run, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}
	h.requireFree(t)

	turn := h.promptOnACP(context.Background())
	if err := waitErr(t, turn, "the ACP turn"); err != nil {
		t.Fatalf("the ACP turn after the refusals: %v", err)
	}
	h.requireFree(t)
}

// TestTheWatchResumeFindsTheSlotFree pins the release's place in ACP's cleanup
// (P4 D2): the claim is freed before slot.done closes, so the post-turn
// watch-resume waiter starts its follow-on turn rather than being refused.
func TestTheWatchResumeFindsTheSlotFree(t *testing.T) {
	h := newRunSlotHarness(t)
	var runs atomic.Int32
	h.acp.setBody(func(context.Context) error {
		// Arm the latch on the first turn only: every run would arm it again
		// and the resumes would never stop.
		if runs.Add(1) == 1 {
			h.state.SetWatchResume("nightly", false)
		}
		return nil
	})

	turn := h.promptOnACP(context.Background())
	if err := waitErr(t, turn, "the ACP turn"); err != nil {
		t.Fatalf("the ACP turn: %v", err)
	}

	waitFor(t, "the post-turn resume to start its follow-on turn", func() bool {
		return len(h.acp.goalsSeen()) == 2
	})
	if got := runs.Load(); got != 2 {
		t.Fatalf("the ACP runner ran %d times, want 2", got)
	}
}
