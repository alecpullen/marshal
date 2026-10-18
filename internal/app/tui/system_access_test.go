// internal/app/tui/system_access_test.go — TUI surfaces for the per-session
// system-access modifier: the /system command, the status badge, the
// agent-requested elevation, and the exit-postmortem grant.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/accesspanel"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/postmortempanel"
	"marshal/internal/commands"
	"marshal/internal/tools/policy"
)

// TestSystemCommandTogglesFlagAndNotes pins the /system command: it flips the
// session flag and confirms the new state in the transcript.
func TestSystemCommandTogglesFlagAndNotes(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)

	if state.SystemAccess() {
		t.Fatal("SystemAccess() = true on a fresh session, want false")
	}

	updated, _ := m.dispatchCommand("/system")
	m = asModel(t, updated)
	if !state.SystemAccess() {
		t.Fatal("/system did not enable system access")
	}
	if got := lastMessageContent(state.Messages()); !strings.Contains(got, "System access enabled") {
		t.Fatalf("transcript note after enabling = %q, want the enabled note", got)
	}

	updated, _ = m.dispatchCommand("/system")
	m = asModel(t, updated)
	if state.SystemAccess() {
		t.Fatal("/system did not disable system access")
	}
	if got := lastMessageContent(state.Messages()); !strings.Contains(got, "System access disabled") {
		t.Fatalf("transcript note after disabling = %q, want the disabled note", got)
	}
}

func lastMessageContent(msgs []session.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

// TestStatusBadgeSystemSuffix pins the status-bar disclosure: the mode cue
// gains a " · system" suffix only while the flag is on.
func TestStatusBadgeSystemSuffix(t *testing.T) {
	m := newStatusTestModel(t)

	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "· system") {
		t.Fatalf("status line shows the system suffix with the flag off:\n%s", line)
	}

	m.state.SetSystemAccess(true)
	line = stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, "· system") {
		t.Fatalf("status line missing the system suffix with the flag on:\n%s", line)
	}
}

// TestModePickerExcludesSystemModifier pins that the modifier is not offered
// as a rung on the mode ladder — it must not be confusable with a mode.
func TestModePickerExcludesSystemModifier(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	model := New(state)
	for _, item := range model.modePickerItems() {
		if item.Value == "system" {
			t.Fatal("modePickerItems must not offer the system modifier as a mode")
		}
	}
}

// TestModeElevationSystemRequestGrantsFlag pins the agent-requested
// elevation: the access panel discloses the system grant, and approving it
// applies the capability for the session.
func TestModeElevationSystemRequestGrantsFlag(t *testing.T) {
	f := newAccessTestFixture(t, 200, 30, config.Default())
	f.register(t, session.AccessIntent{System: true, Reason: "The agent requests system access."})

	// A runtime tick is enough: no keypress is needed to surface the request.
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatalf("access panel did not open on a runtime tick; dock panel = %T", f.m.dock.Panel())
	}
	view := stripANSI(f.m.View().Content)
	if !strings.Contains(view, "Allow system access?") {
		t.Fatalf("panel does not disclose the system-access request:\n%s", view)
	}

	f.choose(true, "")
	if !f.state.SystemAccess() {
		t.Fatal("approving a system request did not grant system access")
	}
	if f.state.AccessState().Mode != "default" {
		t.Fatalf("mode = %q, want the configured default preserved", f.state.AccessState().Mode)
	}
}

// TestModeElevationPlainRequestDoesNotGrantSystem pins that an editing-only
// request leaves the system flag alone.
func TestModeElevationPlainRequestDoesNotGrantSystem(t *testing.T) {
	f := newAccessTestFixture(t, 200, 30, config.Default())
	f.register(t, session.AccessIntent{Editing: true, Reason: "The agent requests editing capability."})

	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatalf("access panel did not open on a runtime tick; dock panel = %T", f.m.dock.Panel())
	}
	f.choose(true, "edit")

	if f.state.SystemAccess() {
		t.Fatal("an editing-only request granted system access")
	}
	if got := f.state.AccessState().Mode; got != "edit" {
		t.Fatalf("mode = %q, want edit", got)
	}
}

// TestPostmortemAgentPassScopedElevation pins the motivating case: the exit
// postmortem's agent pass writes a report at an absolute path outside the
// workspace, so the elevation must be in place for the duration of the pass —
// and it belongs to the RUNTIME's scoped operation, not to the model. The
// model therefore performs no elevation of its own.
func TestPostmortemAgentPassScopedElevation(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	state := postmortemTestState(t, "sess-sysgrant")
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.Config.Postmortem.OnExit = "prompt"
	ctrl := &endTestController{}
	m := New(state, WithEndController(ctrl))
	m.resize(80, 24)
	m.approvalMode = policy.ModeDefault

	m, cmd := runEndCmd(t, m, m.beginEnd(&endIntent{kind: EndIntentExit}))
	if cmd != nil {
		t.Fatalf("prompt path returned a cmd (%T), want nil", cmd)
	}
	m, analysisCmd := answerPostmortem(t, m, postmortempanel.ResultWithAgent)
	if analysisCmd == nil {
		t.Fatal("ResultWithAgent should return the analysis cmd")
	}
	// Running the command performs the scoped pass; the runtime owns the
	// elevation, so the model itself never grants one.
	if msg := analysisCmd(); msg == nil {
		t.Fatal("analysis cmd returned no message")
	}

	if state.SystemAccess() {
		t.Fatal("the model granted system access itself; the elevation belongs to the runtime's scoped pass")
	}
	if m.approvalMode != policy.ModeDefault {
		t.Fatalf("approvalMode = %q, want the user's own mode preserved", m.approvalMode)
	}
	if len(ctrl.analysisGoals) != 1 {
		t.Fatalf("analysis goals = %d, want the pass requested through the end controller", len(ctrl.analysisGoals))
	}
}

// TestEndAnalysisFailureLeavesNoElevation pins the cleanup half. The model
// never grants the elevation, so a failure or refusal cannot leave one behind;
// the runtime's own operation restores it whatever the outcome.
func TestEndAnalysisFailureLeavesNoElevation(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	state := postmortemTestState(t, "sess-sysrefused")
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	ctrl := &endTestController{analysisErr: errors.New("refused")}
	m := New(state, WithEndController(ctrl))
	m.resize(80, 24)
	m.endAnalyzing = true

	updated, cmd := m.Update(endAnalysisFinishedMsg{err: ctrl.analysisErr})
	m = asModel(t, updated)

	if state.SystemAccess() {
		t.Fatal("system access survived a failed analysis pass")
	}
	if m.endAnalyzing {
		t.Fatal("endAnalyzing survived a failed analysis pass")
	}
	if cmd == nil {
		t.Fatal("a failed analysis must still quit")
	}
}

// ---- access panel: fixtures ----

// accessTestFixture drives the TUI's access seam against a real session, with
// a deterministic resolver standing in for the app runtime. The resolver
// commits exactly the target the session computed, so the TUI's own behavior
// is what the assertions observe.
type accessTestFixture struct {
	t     *testing.T
	m     Model
	state *session.State
	call  *session.PendingToolCall
	// barrier, when set, runs inside Apply so a test can hold the commit open
	// and prove the acknowledgment waits for it.
	barrier func()
	// modeSetterCalls counts manual mode changes routed through the injected
	// runtime setter.
	modeSetterCalls atomic.Int64
}

func newAccessTestFixture(t *testing.T, width, height int, cfg config.Config) *accessTestFixture {
	t.Helper()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	f := &accessTestFixture{t: t, state: state}
	m := New(state,
		WithAccessRequests(
			func() (session.AccessRequest, bool) { return state.AccessRequest() },
			func(choice session.AccessChoice) (session.AccessResult, error) {
				return state.ResolveAccess(choice, f.prepare())
			},
		),
		WithSessionModeSetter(func(mode string) error {
			f.modeSetterCalls.Add(1)
			return state.SetSessionMode(mode, func() {})
		}),
	)
	m.resize(width, height)
	m.refreshViewport()
	f.m = m
	return f
}

// prepare is the deterministic resolver: it commits the target implied by the
// request's missing flags and the chosen mode.
func (f *accessTestFixture) prepare() session.AccessPrepare {
	return func(req session.AccessRequest, choice session.AccessChoice) (session.AccessCommit, error) {
		target := req.Current
		if req.MissingEditing {
			target.Mode = choice.Mode
		}
		if req.MissingSystem {
			target.System = true
		}
		apply := func() {}
		if f.barrier != nil {
			apply = f.barrier
		}
		return session.AccessCommit{Effective: target, Apply: apply}, nil
	}
}

// register publishes a typed access request on the fixture's session.
func (f *accessTestFixture) register(t *testing.T, intent session.AccessIntent) session.AccessRequest {
	t.Helper()
	call := &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		Reason:       intent.Reason,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	req, waiting, err := f.state.RegisterAccess(context.Background(), call, intent)
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	if !waiting {
		t.Fatalf("RegisterAccess published no request for intent %+v", intent)
	}
	f.call = call
	return req
}

// tick delivers one runtime tick, the message that must surface a request
// without any keypress.
func (f *accessTestFixture) tick() {
	updated, _ := f.m.Update(agentTickMsg{})
	f.m = updated.(Model)
}

// panel returns the open access panel, failing the test when none is open.
func (f *accessTestFixture) panel() *accesspanel.Panel {
	f.t.Helper()
	panel, ok := f.m.dock.Panel().(*accesspanel.Panel)
	if !ok {
		f.t.Fatalf("dock panel = %T, want *accesspanel.Panel", f.m.dock.Panel())
	}
	return panel
}

// choose submits a decision for the panel that is currently open.
func (f *accessTestFixture) choose(approved bool, mode string) {
	f.t.Helper()
	req := f.panel().Request()
	updated, _ := f.m.Update(accesspanel.ChoiceMsg{Choice: session.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Current:   req.Current,
		Approved:  approved,
		Mode:      mode,
	}})
	f.m = updated.(Model)
}

// ---- access panel: tests ----

// A pending request surfaces on the next runtime tick, with no keypress, at
// every supported terminal size, and only one decision UI is on screen.
func TestAccessPanelAppearsOnRuntimeTickAtSupportedSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}, {200, 60}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			f := newAccessTestFixture(t, size.w, size.h, config.Default())
			f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})

			f.tick()
			if !f.m.accessPanelOwnsDock() {
				t.Fatalf("no access panel after a runtime tick; dock panel = %T", f.m.dock.Panel())
			}
			view := stripANSI(f.m.View().Content)
			if !strings.Contains(view, "Allow system access?") {
				t.Fatalf("view does not show the system-access title:\n%s", view)
			}
			// Exactly one decision UI: the panel's Deny row, and no second
			// approve/deny form in the input area.
			if got := strings.Count(view, "Deny"); got != 1 {
				t.Fatalf("view shows %d Deny rows, want exactly 1:\n%s", got, view)
			}
			area := stripANSI(f.m.renderInputArea())
			if strings.Contains(area, "Allow once") || strings.Contains(area, "Deny") {
				t.Fatalf("input area renders a second decision UI:\n%s", area)
			}
		})
	}
}

// A request queued behind another dock panel leaves that panel open, discloses
// the wait, and preserves the user's draft; closing the panel opens the access
// panel without touching the draft or focus.
func TestAccessPanelQueuesBehindOtherDockAndPreservesDraft(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())

	// A FullFrame doc panel stands in for any unrelated panel holding the dock.
	doc := commands.Doc{Title: "Inspector", FullFrame: true, Rows: []commands.Row{{Text: "row"}}}
	f.m.dock.Open(docpanel.New(doc, f.state))
	f.m.input.SetValue("half-written prompt")
	f.m.input.Focus()

	f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	f.tick()

	if _, ok := f.m.dock.Panel().(*docpanel.Panel); !ok {
		t.Fatalf("the unrelated panel was replaced by %T", f.m.dock.Panel())
	}
	if got := stripANSI(f.m.statusRightSegment()); !strings.Contains(got, "Access waiting") {
		t.Fatalf("status right segment = %q, want the queued-access disclosure", got)
	}
	if got := stripANSI(f.m.renderInputArea()); !strings.Contains(got, "Access request waiting") {
		t.Fatalf("input area does not disclose the queued request:\n%s", got)
	}
	if got := f.m.input.Value(); got != "half-written prompt" {
		t.Fatalf("draft = %q, want it preserved while the request is queued", got)
	}

	// Closing the unrelated panel lets the request through.
	f.m.dock.CloseNow()
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatalf("access panel did not open after the dock freed; panel = %T", f.m.dock.Panel())
	}
	if got := f.m.input.Value(); got != "half-written prompt" {
		t.Fatalf("draft = %q, want it preserved when the access panel opens", got)
	}
}

// Ordinary rerenders and resizes keep the same panel and selection; a changed
// permission tuple re-presents the request with Deny selected again.
func TestAccessPanelKeepsIdentityUntilPermissionRefresh(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())
	f.register(t, session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})

	f.tick()
	first := f.panel()
	// Move the selection off Deny so a reset is observable.
	updated, _ := f.m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	f.m = updated.(Model)

	f.tick()
	if f.panel() != first {
		t.Fatal("an ordinary tick replaced the panel")
	}
	f.m.resize(200, 60)
	f.tick()
	if f.panel() != first {
		t.Fatal("a resize replaced the panel")
	}

	// The permission tuple moves underneath the request: system access is
	// granted elsewhere, so the request no longer needs it.
	f.state.SetSystemAccess(true)
	f.tick()
	refreshed := f.panel()
	if refreshed == first {
		t.Fatal("a changed permission tuple did not re-present the request")
	}
	if refreshed.Request().MissingSystem {
		t.Fatal("the refreshed panel still reports system access as missing")
	}
	// Deny is selected again: the fresh presentation must not inherit the
	// previous selection. The panel renders either the full list ("> Deny") or
	// the compact one-selected-action form ("> [1/4] Deny"), so accept both.
	view := stripANSI(f.m.View().Content)
	if !strings.Contains(view, "> Deny") && !strings.Contains(view, "> [1/") {
		t.Fatalf("refreshed panel did not reset the selection to Deny:\n%s", view)
	}
}

// A choice for a request that is no longer pending cannot grant anything or
// close a newer panel.
func TestAccessPanelIgnoresExpiredAndForeignChoice(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())
	old := f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatal("no access panel to start from")
	}

	// The request is canceled out from under the panel.
	if !f.state.CancelAccess(old.RequestID, session.AccessCanceled, "canceled elsewhere") {
		t.Fatal("could not cancel the request")
	}
	f.tick()
	if f.m.dock.IsOpen() {
		t.Fatalf("the canceled request's panel stayed open: %T", f.m.dock.Panel())
	}

	// A newer request opens its own panel.
	newer := f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	f.tick()
	panel := f.panel()
	if panel.Request().RequestID != newer.RequestID {
		t.Fatalf("panel shows request %q, want the newer %q", panel.Request().RequestID, newer.RequestID)
	}

	// The stale choice names the retired request: it must not grant, and must
	// not close the newer panel.
	updated, _ := f.m.Update(accesspanel.ChoiceMsg{Choice: session.AccessChoice{
		RequestID: old.RequestID,
		SessionID: old.SessionID,
		Current:   old.Current,
		Approved:  true,
	}})
	f.m = updated.(Model)

	if f.state.SystemAccess() {
		t.Fatal("a stale choice granted system access")
	}
	if !f.m.accessPanelOwnsDock() {
		t.Fatal("a stale choice closed the newer panel")
	}
	if f.panel().Request().RequestID != newer.RequestID {
		t.Fatal("a stale choice replaced the newer panel")
	}
}

// Below the supported terminal minimum no approval can be accepted from the
// hidden frame; resizing back reveals the panel and requires a visible
// decision.
func TestAccessPanelHiddenSizeCannotApprove(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())
	f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatal("no access panel to start from")
	}

	f.m.resize(60, 20)
	f.tick()
	// The size gate hides the whole frame, so the panel must not be visible
	// even if it is still docked.
	if view := stripANSI(f.m.View().Content); strings.Contains(view, "Allow system access?") {
		t.Fatalf("the access panel is visible below the supported minimum:\n%s", view)
	}

	// Enter, a paste and a queued choice all do nothing while hidden.
	updated, _ := f.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	f.m = updated.(Model)
	updated, _ = f.m.Update(tea.PasteMsg{Content: "enter"})
	f.m = updated.(Model)
	updated, _ = f.m.Update(accesspanel.ChoiceMsg{Choice: session.AccessChoice{
		RequestID: "whatever", SessionID: f.state.SessionID(), Approved: true,
	}})
	f.m = updated.(Model)

	if f.state.SystemAccess() {
		t.Fatal("a hidden panel granted system access")
	}
	if _, pending := f.state.AccessRequest(); !pending {
		t.Fatal("the request was settled while the panel was hidden")
	}

	// Resizing back reveals it, and the decision still has to be made.
	f.m.resize(120, 40)
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatal("the access panel did not return after the terminal grew")
	}
	f.choose(true, "")
	if !f.state.SystemAccess() {
		t.Fatal("the visible decision did not grant system access")
	}
}

// Esc denies the request without stopping the turn: the busy state, the
// cancel handle and the draft all survive.
func TestAccessPanelEscDeniesWithoutStoppingTurn(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())
	f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatal("no access panel to start from")
	}

	f.m.busy = true
	cancelled := false
	f.m.agentCancel = func() { cancelled = true }
	f.m.input.SetValue("still typing")

	f.m = sendKey(f.m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if f.state.SystemAccess() {
		t.Fatal("Esc granted system access")
	}
	if _, pending := f.state.AccessRequest(); pending {
		t.Fatal("Esc left the request pending")
	}
	if !f.m.busy {
		t.Fatal("Esc stopped the turn")
	}
	if cancelled {
		t.Fatal("Esc cancelled the turn")
	}
	if got := f.m.input.Value(); got != "still typing" {
		t.Fatalf("draft = %q, want it preserved", got)
	}
	// The denial is reported with the state the session is actually in.
	if got := lastMessageContent(f.state.Messages()); !strings.Contains(got, "Access denied") {
		t.Fatalf("transcript note = %q, want an accurate denial", got)
	}
}

// The grant is committed before the waiting tool can observe it, and the
// decision path performs no persistence.
func TestAccessPanelGrantOrderingAndNoPersistence(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.ApprovalMode = "copilot"
	f := newAccessTestFixture(t, 120, 40, cfg)

	entered := make(chan struct{})
	release := make(chan struct{})
	f.barrier = func() {
		close(entered)
		<-release
	}
	f.register(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatal("no access panel to start from")
	}

	req := f.panel().Request()
	done := make(chan Model, 1)
	go func() {
		updated, _ := f.m.Update(accesspanel.ChoiceMsg{Choice: session.AccessChoice{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Current:   req.Current,
			Approved:  true,
		}})
		done <- updated.(Model)
	}()

	<-entered
	// Apply is inside the resolver: the waiter must still be empty-handed.
	select {
	case d := <-f.call.ResponseChan:
		t.Fatalf("the grant was acknowledged before it was applied: %+v", d)
	default:
	}
	if f.state.SystemAccess() {
		t.Fatal("system access moved before Apply returned")
	}

	close(release)
	f.m = <-done

	d := <-f.call.ResponseChan
	if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
		t.Fatalf("delivered decision = %+v, want an approved typed grant", d)
	}
	if !f.state.SystemAccess() {
		t.Fatal("the grant did not reach the session")
	}
	// A system-only grant preserves the mode it was made in.
	if got := f.state.AccessState().Mode; got != "copilot" {
		t.Fatalf("mode = %q, want the actual copilot preserved", got)
	}
	if got := lastMessageContent(f.state.Messages()); !strings.Contains(got, "copilot") {
		t.Fatalf("transcript note = %q, want it to name the actual mode", got)
	}
}

// A model with no resolver fails closed: the request is settled as a failure
// and never reported as a success.
func TestAccessPanelUnavailableResolverFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{name: "no callbacks"},
		{name: "getter only", opts: []Option{WithAccessRequests(
			func() (session.AccessRequest, bool) { return session.AccessRequest{}, false }, nil)}},
		{name: "resolver only", opts: []Option{WithAccessRequests(nil,
			func(session.AccessChoice) (session.AccessResult, error) {
				return session.AccessResult{}, nil
			})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
			call := &session.PendingToolCall{
				ID:           "call-1",
				Name:         "mode.request",
				ResponseChan: make(chan session.UserApprovalDecision, 1),
			}
			if _, waiting, err := state.RegisterAccess(context.Background(), call,
				session.AccessIntent{System: true, Reason: "read outside"}); err != nil || !waiting {
				t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
			}

			m := New(state, tc.opts...)
			m.resize(120, 40)
			updated, _ := m.Update(agentTickMsg{})
			m = updated.(Model)

			if m.dock.IsOpen() {
				t.Fatalf("a resolver-less model opened %T", m.dock.Panel())
			}
			select {
			case d := <-call.ResponseChan:
				if d.Approved {
					t.Fatalf("a resolver-less model reported success: %+v", d)
				}
				if d.Access == nil || d.Access.Status != session.AccessFailed {
					t.Fatalf("delivered result = %+v, want a typed failure", d.Access)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the request was never settled; the tool would block forever")
			}
			if state.SystemAccess() {
				t.Fatal("a resolver-less model granted system access")
			}
		})
	}
}

// An ordinary file.write carrying the legacy mode-elevation reason stays an
// ordinary forced approval: no always/session options, and no access panel.
func TestAccessOrdinaryElevationReasonUsesForcedApproval(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())
	tc := &session.PendingToolCall{
		ID:           "call-write",
		Name:         "file.write",
		Reason:       "mode-elevation: agent requests an editing mode",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	if !forcedPromptClass(tc) {
		t.Fatal("a mode-elevation reason must keep the forced-prompt classification")
	}
	f.state.SetPendingApproval(tc)
	f.tick()

	if f.m.accessPanelOwnsDock() {
		t.Fatal("an ordinary forced approval opened the access panel")
	}
	model := newApprovalModel(tc, f.state.SandboxInfo(), false, false, 80)
	for _, choice := range model.candidates {
		if choice == choiceAlways || choice == choiceSessionAllow {
			t.Fatalf("forced prompt offers %q; it must not be bypassable", choice)
		}
	}
}

// A manual mode change goes through the injected runtime setter exactly once,
// and a config reload preserves an unchanged-baseline override while an
// explicit configured-mode change replaces it.
func TestAccessManualModeSetterAndReloadStayInSync(t *testing.T) {
	f := newAccessTestFixture(t, 120, 40, config.Default())

	f.m.setMode("copilot")
	if got := f.modeSetterCalls.Load(); got != 1 {
		t.Fatalf("mode setter calls = %d, want 1", got)
	}
	if got := f.state.AccessState().Mode; got != "copilot" {
		t.Fatalf("session mode = %q, want copilot", got)
	}
	if f.m.approvalMode != policy.ModeCopilot {
		t.Fatalf("displayed mode = %q, want copilot", f.m.approvalMode)
	}

	// An unrelated setting changes; the configured mode does not.
	unrelated := config.Default()
	unrelated.Agent.MaxToolIterations++
	f.m.applyNewConfig(unrelated)
	if got := f.m.approvalMode; got != policy.ModeCopilot {
		t.Fatalf("displayed mode after an unrelated reload = %q, want the override copilot", got)
	}
	if got := f.state.AccessState().Mode; got != "copilot" {
		t.Fatalf("session mode after an unrelated reload = %q, want copilot", got)
	}

	// An explicit configured-mode change replaces the override.
	explicit := config.Default()
	explicit.Agent.ApprovalMode = "edit"
	f.m.applyNewConfig(explicit)
	if got := f.m.approvalMode; got != policy.ModeEdit {
		t.Fatalf("displayed mode after an explicit change = %q, want edit", got)
	}
	if got := f.state.AccessState().Mode; got != "edit" {
		t.Fatalf("session mode after an explicit change = %q, want edit", got)
	}
}
