package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

// ---- fixtures ----

// modeRequestFixture is a session plus a registry whose mode.request is
// allowed to publish capability requests, wired to an event broker so a test
// can wait for registration without polling.
type modeRequestFixture struct {
	state  *session.State
	reg    *registry.Registry
	broker *pubsub.Broker[session.Event]
}

func newModeRequestFixture(t *testing.T, cfg config.Config) *modeRequestFixture {
	t.Helper()
	return newModeRequestFixtureWithState(t, cfg, nil)
}

// newModeRequestFixtureWithState is newModeRequestFixture for tests that need
// a differently-constructed session (a child, whose depth is fixed at
// construction). A nil opts slice builds a top-level state.
func newModeRequestFixtureWithState(t *testing.T, cfg config.Config, opts []session.Option) *modeRequestFixture {
	t.Helper()
	root := t.TempDir()
	state := session.New(cfg, root, time.Now(), session.Persistence{}, opts...)
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		SessionState:   state,
		AccessRequests: true,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return &modeRequestFixture{state: state, reg: reg, broker: broker}
}

// subscribe returns a channel of pending-approval events. Subscribe before
// starting the tool so no registration can be missed.
func (f *modeRequestFixture) subscribe(t *testing.T) <-chan pubsub.Event[session.Event] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return f.broker.Subscribe(ctx)
}

type toolOutcome struct {
	res registry.ToolResult
	err error
}

// invokeModeRequest calls the tool handler directly. It takes no *testing.T so
// it is safe to call from the goroutine a blocking handler needs.
func invokeModeRequest(reg *registry.Registry, ctx context.Context, callID, args string) (registry.ToolResult, error) {
	tool, ok := reg.Lookup("mode.request")
	if !ok {
		return registry.ToolResult{}, fmt.Errorf("mode.request is not registered")
	}
	return tool.Handler(ctx, registry.ToolCall{ID: callID, Name: "mode.request", Args: []byte(args)})
}

// startModeRequest runs the handler in the background and returns its outcome
// channel.
func startModeRequest(reg *registry.Registry, ctx context.Context, callID, args string) <-chan toolOutcome {
	done := make(chan toolOutcome, 1)
	go func() {
		res, err := invokeModeRequest(reg, ctx, callID, args)
		done <- toolOutcome{res, err}
	}()
	return done
}

// awaitOutcome waits for the handler to return, failing the test on a hang.
func awaitOutcome(t *testing.T, done <-chan toolOutcome) toolOutcome {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("mode.request did not return")
		return toolOutcome{}
	}
}

// awaitAccessRequest waits for the registration event and returns the
// published request. The deadline is a deadlock guard, not a race ordering
// primitive: the event is published before the handler blocks.
func awaitAccessRequest(t *testing.T, events <-chan pubsub.Event[session.Event]) session.AccessRequest {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type != session.EventPendingApprovalChanged {
				continue
			}
			pa := ev.Payload.PendingApproval
			if pa == nil || pa.Access == nil {
				continue
			}
			return *pa.Access
		case <-deadline:
			t.Fatal("mode.request never published an access request")
			return session.AccessRequest{}
		}
	}
}

// testAccessPrepare is a deterministic resolver: it commits exactly the target
// the session computed from the request's missing flags and the chosen mode,
// with an optional barrier standing in for the application step.
func testAccessPrepare(barrier func()) session.AccessPrepare {
	return func(req session.AccessRequest, choice session.AccessChoice) (session.AccessCommit, error) {
		target := req.Current
		if req.MissingEditing {
			target.Mode = choice.Mode
		}
		if req.MissingSystem {
			target.System = true
		}
		apply := func() {}
		if barrier != nil {
			apply = barrier
		}
		return session.AccessCommit{Effective: target, Apply: apply}, nil
	}
}

// resolveAccess settles req through the canonical session path.
func resolveAccess(t *testing.T, state *session.State, req session.AccessRequest, approved bool, mode string, prepare session.AccessPrepare) session.AccessResult {
	t.Helper()
	result, err := state.ResolveAccess(session.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Current:   req.Current,
		Approved:  approved,
		Mode:      mode,
	}, prepare)
	if err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}
	return result
}

// decodeAccessResult parses the typed payload the tool returns.
func decodeAccessResult(t *testing.T, res registry.ToolResult) session.AccessResult {
	t.Helper()
	var out session.AccessResult
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("tool content %q is not an AccessResult: %v", res.Content, err)
	}
	return out
}

// ---- the three rewritten tests ----

// An approved request reports the state the runtime actually committed, not
// the mode the agent hinted at.
func TestModeRequestApprovedReportsCommittedState(t *testing.T) {
	f := newModeRequestFixture(t, config.Default())
	events := f.subscribe(t)

	done := startModeRequest(f.reg, context.Background(), "call-1", `{"mode":"edit"}`)
	req := awaitAccessRequest(t, events)
	if !req.MissingEditing {
		t.Fatalf("request flags = %+v, want editing missing from the configured default mode", req)
	}

	// The user picks Copilot even though the agent hinted at Edit.
	result := resolveAccess(t, f.state, req, true, "copilot", testAccessPrepare(nil))
	if result.Status != session.AccessGranted {
		t.Fatalf("status = %q, want %q", result.Status, session.AccessGranted)
	}

	out := awaitOutcome(t, done)
	if out.err != nil {
		t.Fatalf("mode.request error: %v", out.err)
	}
	got := decodeAccessResult(t, out.res)
	if got.Status != session.AccessGranted {
		t.Fatalf("reported status = %q, want %q", got.Status, session.AccessGranted)
	}
	if want := (session.AccessState{Mode: "copilot"}); got.Effective != want {
		t.Fatalf("reported effective = %+v, want the committed %+v", got.Effective, want)
	}
	if !strings.Contains(out.res.Summary, "copilot") {
		t.Fatalf("summary %q should name the committed mode", out.res.Summary)
	}
}

// A denial reports the mode the session is actually in, not a hardcoded
// "default".
func TestModeRequestDeniedReportsActualState(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.ApprovalMode = "copilot"
	f := newModeRequestFixture(t, cfg)
	events := f.subscribe(t)

	// System access is the only thing missing in Copilot, so that is what the
	// request asks for.
	done := startModeRequest(f.reg, context.Background(), "call-1", `{"system":true}`)
	req := awaitAccessRequest(t, events)
	if req.MissingEditing || !req.MissingSystem {
		t.Fatalf("request flags = editing %t / system %t, want false/true", req.MissingEditing, req.MissingSystem)
	}

	result := resolveAccess(t, f.state, req, false, "", testAccessPrepare(nil))
	if result.Status != session.AccessDenied {
		t.Fatalf("status = %q, want %q", result.Status, session.AccessDenied)
	}

	out := awaitOutcome(t, done)
	if out.err != nil {
		t.Fatalf("mode.request error: %v", out.err)
	}
	got := decodeAccessResult(t, out.res)
	if got.Status != session.AccessDenied {
		t.Fatalf("reported status = %q, want %q", got.Status, session.AccessDenied)
	}
	if want := (session.AccessState{Mode: "copilot"}); got.Effective != want {
		t.Fatalf("reported effective = %+v, want the actual %+v (denial must not claim a mode change)", got.Effective, want)
	}
	if got.Effective.System {
		t.Fatal("a denial reported system access as granted")
	}
}

// The system variant reports the typed scope and the actual state.
func TestModeRequestSystemReasonAndGrant(t *testing.T) {
	f := newModeRequestFixture(t, config.Default())
	events := f.subscribe(t)

	done := startModeRequest(f.reg, context.Background(), "call-1", `{"mode":"edit","system":true}`)
	req := awaitAccessRequest(t, events)
	if !req.MissingEditing || !req.MissingSystem {
		t.Fatalf("request flags = editing %t / system %t, want true/true", req.MissingEditing, req.MissingSystem)
	}
	if !strings.Contains(req.Reason, "system access") {
		t.Fatalf("request reason = %q, want the system-access wording", req.Reason)
	}

	result := resolveAccess(t, f.state, req, true, "edit", testAccessPrepare(nil))
	if result.Status != session.AccessGranted {
		t.Fatalf("status = %q, want %q", result.Status, session.AccessGranted)
	}

	out := awaitOutcome(t, done)
	if out.err != nil {
		t.Fatalf("mode.request error: %v", out.err)
	}
	got := decodeAccessResult(t, out.res)
	if want := (session.AccessState{Mode: "edit", System: true}); got.Effective != want {
		t.Fatalf("reported effective = %+v, want %+v", got.Effective, want)
	}
	if !strings.Contains(out.res.Summary, "system access true") {
		t.Fatalf("summary %q should name the system-access grant", out.res.Summary)
	}
}

// ---- argument validation ----

// Malformed arguments are rejected before any pending request is published.
func TestModeRequestArgumentValidation(t *testing.T) {
	long := strings.Repeat("日", 400)
	tooLong := strings.Repeat("日", 401)

	for _, tc := range []struct {
		name    string
		args    string
		wantErr bool
	}{
		{name: "empty object", args: `{}`, wantErr: true},
		{name: "false-only system", args: `{"system":false}`, wantErr: true},
		{name: "empty mode", args: `{"mode":""}`, wantErr: true},
		{name: "unknown mode", args: `{"mode":"bogus"}`, wantErr: true},
		{name: "null mode", args: `{"mode":null}`, wantErr: true},
		{name: "malformed mode type", args: `{"mode":123}`, wantErr: true},
		{name: "malformed system type", args: `{"system":"yes"}`, wantErr: true},
		{name: "unknown property", args: `{"mode":"edit","bogus":1}`, wantErr: true},
		{name: "reason 401 runes", args: fmt.Sprintf(`{"mode":"edit","reason":%q}`, tooLong), wantErr: true},
		{name: "reason 400 runes", args: fmt.Sprintf(`{"mode":"edit","reason":%q}`, long), wantErr: false},
		{name: "valid mode", args: `{"mode":"edit"}`, wantErr: false},
		{name: "valid system only", args: `{"system":true}`, wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModeRequestFixture(t, config.Default())
			// Subscribe before starting the tool: registration publishes the
			// event, so a later subscription could miss it.
			events := f.subscribe(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := startModeRequest(f.reg, ctx, "call-1", tc.args)
			if tc.wantErr {
				out := awaitOutcome(t, done)
				if out.err == nil {
					t.Fatalf("args %s were accepted; want a validation error", tc.args)
				}
				if _, pending := f.state.AccessRequest(); pending {
					t.Fatal("a rejected call published a pending request")
				}
				return
			}
			// Valid arguments publish a request; settle it so the handler
			// returns and the fixture leaves nothing pending.
			req := awaitAccessRequest(t, events)
			resolveAccess(t, f.state, req, false, "", testAccessPrepare(nil))
			if out := awaitOutcome(t, done); out.err != nil {
				t.Fatalf("valid args %s were rejected: %v", tc.args, out.err)
			}
		})
	}
}

// ---- capability matrix ----

// The missing-capability flags follow the effective pair, and an
// already-satisfied request returns without publishing anything.
func TestModeRequestCapabilityMatrix(t *testing.T) {
	for _, mode := range []string{"plan", "default", "edit", "copilot", "auto"} {
		for _, systemOn := range []bool{false, true} {
			for _, kind := range []string{"editing", "system", "combined"} {
				name := fmt.Sprintf("%s/system=%t/%s", mode, systemOn, kind)
				t.Run(name, func(t *testing.T) {
					cfg := config.Default()
					cfg.Agent.ApprovalMode = mode
					f := newModeRequestFixture(t, cfg)
					if systemOn {
						f.state.SetSystemAccess(true)
					}

					args := `{"mode":"edit"}`
					wantEditing := mode == "plan" || mode == "default"
					wantSystem := false
					switch kind {
					case "system":
						args, wantEditing = `{"system":true}`, false
						wantSystem = !systemOn
					case "combined":
						args = `{"mode":"edit","system":true}`
						wantSystem = !systemOn
					}

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					// Subscribe before starting the tool: registration
					// publishes the event, so a later subscription could miss
					// it.
					events := f.subscribe(t)
					done := startModeRequest(f.reg, ctx, "call-1", args)

					if !wantEditing && !wantSystem {
						// Already satisfied: the tool answers immediately and
						// publishes nothing.
						out := awaitOutcome(t, done)
						if out.err != nil {
							t.Fatalf("already-satisfied request failed: %v", out.err)
						}
						got := decodeAccessResult(t, out.res)
						if got.Status != session.AccessAvailable {
							t.Fatalf("status = %q, want %q", got.Status, session.AccessAvailable)
						}
						if _, pending := f.state.AccessRequest(); pending {
							t.Fatal("an already-satisfied request published a pending request")
						}
						return
					}

					req := awaitAccessRequest(t, events)
					if req.MissingEditing != wantEditing || req.MissingSystem != wantSystem {
						t.Fatalf("flags = editing %t / system %t, want %t/%t",
							req.MissingEditing, req.MissingSystem, wantEditing, wantSystem)
					}
					resolveAccess(t, f.state, req, false, "", testAccessPrepare(nil))
					if out := awaitOutcome(t, done); out.err != nil {
						t.Fatalf("mode.request error: %v", out.err)
					}
				})
			}
		}
	}
}

// Auto mode does not auto-grant a read-only mode.request: the request still
// waits for an explicit decision.
func TestModeRequestAutoStillWaitsForSystemDecision(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.ApprovalMode = "auto"
	f := newModeRequestFixture(t, cfg)
	events := f.subscribe(t)

	done := startModeRequest(f.reg, context.Background(), "call-1", `{"system":true}`)
	req := awaitAccessRequest(t, events)
	if !req.MissingSystem {
		t.Fatalf("request flags = %+v, want system access missing in Auto", req)
	}

	// The handler is still blocked: nothing has been granted yet.
	select {
	case out := <-done:
		t.Fatalf("mode.request returned without a decision: %+v", out)
	default:
	}
	if f.state.SystemAccess() {
		t.Fatal("Auto mode granted system access without a decision")
	}

	resolveAccess(t, f.state, req, true, "", testAccessPrepare(nil))
	out := awaitOutcome(t, done)
	if out.err != nil {
		t.Fatalf("mode.request error: %v", out.err)
	}
	if !f.state.SystemAccess() {
		t.Fatal("the approved grant did not reach the session")
	}
}

// ---- cancellation and settlement identity ----

// Cancelling the original context returns the handler, and the old request's
// cleanup cannot clear a newer ordinary approval.
func TestModeRequestCanceledCleanupCannotEraseLaterApproval(t *testing.T) {
	f := newModeRequestFixture(t, config.Default())
	events := f.subscribe(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := startModeRequest(f.reg, ctx, "call-1", `{"mode":"edit"}`)
	req := awaitAccessRequest(t, events)

	cancel()
	out := awaitOutcome(t, done)
	if out.err == nil {
		t.Fatal("a cancelled request returned success")
	}
	if _, pending := f.state.AccessRequest(); pending {
		t.Fatal("the cancelled request is still pending")
	}

	// A newer ordinary approval must survive the old request's cleanup.
	generic := &session.PendingToolCall{
		ID:           "generic-1",
		Name:         "file.write",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	f.state.SetPendingApproval(generic)
	if got := f.state.PendingApproval(); got == nil || got.ID != "generic-1" {
		t.Fatalf("the later ordinary approval was cleared by the old request's cleanup: %+v", got)
	}
	// The retired request ID cannot settle anything either.
	if f.state.CancelAccess(req.RequestID, session.AccessCanceled, "late cleanup") {
		t.Fatal("a retired request ID settled a later approval")
	}
	if got := f.state.PendingApproval(); got == nil || got.ID != "generic-1" {
		t.Fatalf("a late cancellation erased the later approval: %+v", got)
	}
}

// An untyped Approved/Edited value on the canonical channel is not a grant.
func TestModeRequestUntypedApprovalCannotReportGrant(t *testing.T) {
	f := newModeRequestFixture(t, config.Default())
	events := f.subscribe(t)

	done := startModeRequest(f.reg, context.Background(), "call-1", `{"mode":"edit"}`)
	req := awaitAccessRequest(t, events)

	// Inject the legacy shape straight onto the canonical channel, bypassing
	// the session's settlement helper.
	canonical := f.state.PendingApproval()
	if canonical == nil || canonical.ResponseChan == nil {
		t.Fatal("no canonical pending call to inject into")
	}
	canonical.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: "copilot"}

	out := awaitOutcome(t, done)
	if out.err == nil {
		t.Fatal("an untyped approval produced a successful result")
	}
	if got := f.state.AccessState(); got != (session.AccessState{Mode: "default"}) {
		t.Fatalf("session pair = %+v, want it unchanged by an untyped approval", got)
	}

	// Clean up the still-pending canonical request.
	if !f.state.CancelAccess(req.RequestID, session.AccessCanceled, "test cleanup") {
		t.Fatal("could not cancel the canonical request during cleanup")
	}
}

// A child session and a standalone registration both fail closed.
func TestModeRequestChildAndStandaloneFailClosed(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		// Depth is fixed at construction (WithDepth), not by EnterSubagent —
		// that only tracks concurrency.
		f := newModeRequestFixtureWithState(t, config.Default(), []session.Option{session.WithDepth(1)})
		if got := f.state.SubagentDepth(); got != 1 {
			t.Fatalf("SubagentDepth() = %d, want 1", got)
		}

		out := awaitOutcome(t, startModeRequest(f.reg, context.Background(), "call-1", `{"mode":"edit"}`))
		if out.err == nil {
			t.Fatal("a child session published an access request")
		}
		if !strings.Contains(out.err.Error(), "top-level") {
			t.Fatalf("error = %q, want an actionable top-level-only message", out.err)
		}
		if _, pending := f.state.AccessRequest(); pending {
			t.Fatal("a child request published a pending approval")
		}
	})

	t.Run("standalone", func(t *testing.T) {
		root := t.TempDir()
		state := session.New(config.Default(), root, time.Now(), session.Persistence{})
		reg := registry.New()
		// AccessRequests omitted: the default is false.
		if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
			t.Fatalf("RegisterAll: %v", err)
		}

		out := awaitOutcome(t, startModeRequest(reg, context.Background(), "call-1", `{"mode":"edit"}`))
		if out.err == nil {
			t.Fatal("a standalone registration published an access request")
		}
		if !strings.Contains(out.err.Error(), "resolver") {
			t.Fatalf("error = %q, want an actionable resolver message", out.err)
		}
		if _, pending := state.AccessRequest(); pending {
			t.Fatal("a standalone registration published a pending approval")
		}
	})
}

// A grant that already crossed its acceptance point wins over a later context
// cancellation: the tool reports the committed grant, not a false denial.
func TestModeRequestAcceptedGrantBeatsLaterContextCancel(t *testing.T) {
	f := newModeRequestFixture(t, config.Default())
	events := f.subscribe(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startModeRequest(f.reg, ctx, "call-1", `{"mode":"edit"}`)
	req := awaitAccessRequest(t, events)

	entered := make(chan struct{})
	release := make(chan struct{})
	prepare := testAccessPrepare(func() {
		close(entered)
		<-release
	})

	resolved := make(chan session.AccessResult, 1)
	go func() {
		result, _ := f.state.ResolveAccess(session.AccessChoice{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Current:   req.Current,
			Approved:  true,
			Mode:      "edit",
		}, prepare)
		resolved <- result
	}()

	// Apply is inside the resolver: cancel the tool's context now, then let
	// the accepted application finish.
	<-entered
	cancel()
	close(release)

	result := <-resolved
	if result.Status != session.AccessGranted {
		t.Fatalf("resolver status = %q, want %q", result.Status, session.AccessGranted)
	}

	out := awaitOutcome(t, done)
	if out.err != nil {
		t.Fatalf("the committed grant was reported as a failure: %v", out.err)
	}
	got := decodeAccessResult(t, out.res)
	if got.Status != session.AccessGranted {
		t.Fatalf("reported status = %q, want the committed %q", got.Status, session.AccessGranted)
	}
	if want := (session.AccessState{Mode: "edit"}); got.Effective != want {
		t.Fatalf("reported effective = %+v, want %+v", got.Effective, want)
	}
	if got := f.state.AccessState(); got != (session.AccessState{Mode: "edit"}) {
		t.Fatalf("session pair = %+v, want the accepted grant preserved", got)
	}
}
