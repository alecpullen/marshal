package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/activeworkpanel"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/trustpanel"
	"marshal/internal/commands"
	"marshal/internal/trust"
	"marshal/internal/viewmodel"
)

func TestParseReviewArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		model  string
		base   string
		rangeV string
		focus  []string
	}{
		{
			name:  "model only",
			args:  []string{"--model", "ollama/codellama", "main.go"},
			model: "ollama/codellama",
			focus: []string{"main.go"},
		},
		{
			name:  "model equals",
			args:  []string{"--model=ollama/codellama", "main.go"},
			model: "ollama/codellama",
			focus: []string{"main.go"},
		},
		{
			name:  "base only",
			args:  []string{"--base", "main", "src"},
			base:  "main",
			focus: []string{"src"},
		},
		{
			name:  "base equals",
			args:  []string{"--base=main", "src"},
			base:  "main",
			focus: []string{"src"},
		},
		{
			name:   "range only",
			args:   []string{"main...HEAD", "src"},
			rangeV: "main...HEAD",
			focus:  []string{"src"},
		},
		{
			name:   "all flags",
			args:   []string{"--model=ollama/codellama", "--base=main", "main...HEAD", "focus", "words"},
			model:  "ollama/codellama",
			base:   "main",
			rangeV: "main...HEAD",
			focus:  []string{"focus", "words"},
		},
		{
			name:  "no flags",
			args:  []string{"just", "focus"},
			focus: []string{"just", "focus"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, base, rangeV, remaining := parseReviewArgs(tc.args)
			if model != tc.model {
				t.Fatalf("model = %q, want %q", model, tc.model)
			}
			if base != tc.base {
				t.Fatalf("base = %q, want %q", base, tc.base)
			}
			if rangeV != tc.rangeV {
				t.Fatalf("range = %q, want %q", rangeV, tc.rangeV)
			}
			if !reflect.DeepEqual(remaining, tc.focus) {
				t.Fatalf("remaining = %v, want %v", remaining, tc.focus)
			}
		})
	}
}

func TestTrustCommandOpensTrustPanelWhenPending(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{SessionID: unnamedSessionID})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	var decided trust.Decision
	m := testModelWith(t,
		service.Deps{
			State:       state,
			Commands:    reg,
			TrustPrompt: "/some/project",
			DecideTrust: func(d trust.Decision) { decided = d },
		},
		WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"),
	)

	m.dispatchCommand("/trust")
	if !m.dock.IsOpen() {
		t.Fatal("expected dock to be open after /trust with a pending decision")
	}
	if _, ok := m.dock.Panel().(*trustpanel.Panel); !ok {
		t.Fatalf("expected trustpanel, got %T", m.dock.Panel())
	}
	// Opening the panel must not itself trigger a decision.
	if decided != "" {
		t.Fatalf("expected no decision yet, got %q", decided)
	}
}

// TestTrustCommandReopensAfterDecline pins the post-decline path: declining
// closes the panel, but the host still reports the decision as pending, so
// /trust asks again instead of falling back to the "nothing pending" note.
func TestTrustCommandReopensAfterDecline(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{SessionID: unnamedSessionID})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	var decided trust.Decision
	m := testModelWith(t,
		service.Deps{
			State:       state,
			Commands:    reg,
			TrustPrompt: "/some/project",
			DecideTrust: func(d trust.Decision) { decided = d },
		},
		WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"),
	)
	if !m.dock.IsOpen() {
		t.Fatal("expected the trust panel open at startup")
	}

	// Decline through the panel, then run the close it queues.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	m = asModel(t, mm)
	if cmd == nil {
		t.Fatal("decline should yield a ClosedMsg command")
	}
	mm, _ = m.Update(cmd())
	m = asModel(t, mm)
	if m.dock.IsOpen() {
		t.Fatal("dock should close after declining")
	}
	if decided != trust.DecisionDontTrust {
		t.Fatalf("decision = %v, want dont_trust", decided)
	}

	m.dispatchCommand("/trust")
	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*trustpanel.Panel); !ok {
		t.Fatalf("expected the trust panel after /trust following a decline, got %T", m.dock.Panel())
	}
	if decided != trust.DecisionDontTrust {
		t.Fatalf("reopening must not decide again, got %q", decided)
	}
}

func TestTrustCommandFallsBackWhenNoPendingDecision(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{SessionID: unnamedSessionID})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := testModelWith(t, service.Deps{State: state, Commands: reg}, WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"))

	// No trustDecide wired: the handler's docpanel opens, then the effect
	// must NOT open a trustpanel and must steer to the shell path.
	m.dispatchCommand("/trust")
	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*docpanel.Panel); !ok {
		t.Fatalf("expected the handler's docpanel to stay docked, got %T", m.dock.Panel())
	}
	if _, ok := m.dock.Panel().(*trustpanel.Panel); ok {
		t.Fatal("expected no trustpanel when no decision is pending")
	}
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a system message steering to the shell path")
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "marshal --trust") {
		t.Fatalf("expected fallback message to mention marshal --trust, got %q", last.Content)
	}
}

func TestTrustCommandTrustedDoesNothingExtra(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{SessionID: unnamedSessionID})
	state.SetTrusted(true)
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := testModelWith(t, service.Deps{State: state, Commands: reg}, WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"))

	m.dispatchCommand("/trust")
	if _, ok := m.dock.Panel().(*trustpanel.Panel); ok {
		t.Fatal("expected no trustpanel when already trusted")
	}
}

// ── Task 10: command entry points reach the warning ─────────────────────

// TestExitCommandsReachTheWarning pins that /exit and /quit go through the same
// ending path as Ctrl+C: with live work they warn instead of exiting, and the
// goodbye line is not written until the user has decided.
func TestExitCommandsReachTheWarning(t *testing.T) {
	for _, name := range []string{"/exit", "/quit"} {
		t.Run(name, func(t *testing.T) {
			m, ctrl, _ := newSessionWarningModel(t, workSnapshot("job-1"))

			updated, cmd := m.dispatchCommand(name)
			m = asModel(t, updated)

			if cmd != nil {
				t.Fatalf("%s returned a cmd (%T), want nil so the warning can be answered", name, cmd)
			}
			if _, ok := m.dock.Panel().(*activeworkpanel.Panel); !ok {
				t.Fatalf("%s: dock panel = %T, want the active-work warning", name, m.dock.Panel())
			}
			if len(ctrl.accepted) != 0 {
				t.Fatalf("%s accepted the ending before the user decided", name)
			}
			for _, msg := range stateOf(m).Messages() {
				if strings.Contains(msg.Content, "Goodbye") {
					t.Fatalf("%s wrote the farewell before the user decided", name)
				}
			}
		})
	}
}

// TestExitCommandsWithoutWorkExitDirectly pins that the warning is not added to
// a conversation with nothing to stop — the existing direct behavior remains.
func TestExitCommandsWithoutWorkExitDirectly(t *testing.T) {
	state := postmortemTestState(t, "sess-quit-no-work")
	state.Config.Postmortem.OnExit = "never"
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := testModelWith(t, service.Deps{State: state, End: &endTestController{}, Commands: reg})
	m.resize(80, 24)

	updated, cmd := m.dispatchCommand("/quit")
	m = asModel(t, updated)

	m, cmd = runEndCmd(t, m, cmd)
	if _, ok := m.dock.Panel().(*activeworkpanel.Panel); ok {
		t.Fatal("/quit with no live work must not warn")
	}
	if cmd == nil {
		t.Fatal("/quit with no live work must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

// TestClearAliasWarnsLikeNew pins that /clear shares /new's replacement path,
// including the warning.
func TestClearAliasWarnsLikeNew(t *testing.T) {
	m, ctrl, f := newSessionWarningModel(t, workSnapshot("job-1"))

	updated, cmd := m.dispatchCommand("/clear")
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatalf("/clear returned a cmd (%T), want nil", cmd)
	}
	if _, ok := m.dock.Panel().(*activeworkpanel.Panel); !ok {
		t.Fatalf("dock panel = %T, want the replacement warning", m.dock.Panel())
	}
	if f.called > 0 {
		t.Fatal("/clear built a session before the user decided")
	}
	if len(ctrl.accepted) != 0 {
		t.Fatal("/clear accepted an ending before the user decided")
	}
}

// TestResumeCommandReachesTheWarning pins the /resume <id> entry point.
func TestResumeCommandReachesTheWarning(t *testing.T) {
	m, ctrl, _ := newSessionWarningModel(t, workSnapshot("job-1"))

	updated, cmd := m.dispatchCommand("/resume sess-other")
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatalf("/resume returned a cmd (%T), want nil", cmd)
	}
	if _, ok := m.dock.Panel().(*activeworkpanel.Panel); !ok {
		t.Fatalf("dock panel = %T, want the replacement warning", m.dock.Panel())
	}
	if m.ResumeSession() != "" {
		t.Fatal("/resume recorded the target before acceptance")
	}
	if len(ctrl.accepted) != 0 {
		t.Fatal("/resume accepted an ending before the user decided")
	}
}

// TestSendNowCommandTokenBoundary pins the raw-line recognition: only the exact
// /send-now token is intercepted, so a longer command name is dispatched
// normally instead of having its argument swallowed as steering text.
func TestSendNowCommandTokenBoundary(t *testing.T) {
	m := newTestModel(t)
	// "/send-nowhere" is not /send-now: it must fall through to the ordinary
	// command path and be reported as an unknown command.
	if m.isSendNowCommand("/send-nowhere steer") {
		t.Fatal("a longer command name was mistaken for /send-now")
	}
	if !m.isSendNowCommand("/send-now") || !m.isSendNowCommand("/send-now text") {
		t.Fatal("/send-now was not recognized")
	}
	// A registry without the entry refuses the recognition too.
	bare := testModelWith(t, service.Deps{State: newTestState(t), Commands: commands.New()})
	if bare.isSendNowCommand("/send-now text") {
		t.Fatal("recognized /send-now without the registry entry")
	}
}

// openWorkDetail drives the panel from open to a detail view for the given
// item, returning the model and the live panel.
func openWorkDetail(t *testing.T, m Model, item acpwire.WorkItem) (Model, *tasksPanelModel) {
	t.Helper()
	m = toggleTasks(m)
	if !m.tasksOpen() {
		t.Fatal("the Tasks panel did not open")
	}
	p := m.tasksPanel
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter on the item emitted no inspect request")
	}
	req := cmd().(workRequestMsg)
	updated, _ = m.Update(workDetailMsg{
		conversationID: req.conversationID,
		panelID:        req.panelID,
		requestID:      req.requestID,
		detail:         acpwire.WorkDetail{Item: item},
	})
	m = updated.(Model)
	if !p.detailOpen {
		t.Fatal("the detail did not open")
	}
	return m, p
}

// TestWorkStopAndAckStayAsyncAndExact pins that stop and acknowledge run
// outside Update, capture the exact target, and that a reply from a different
// panel is dropped.
func TestWorkStopStaysAsyncAndExact(t *testing.T) {
	item := acpwire.WorkItem{
		TaskKey: "work:a", Kind: "command", Label: "Alpha", State: "running",
		Capabilities: acpwire.WorkCapabilities{Inspect: true, Stop: true},
	}
	svc := &fakeWorkService{
		snap:       viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1},
		stopResult: viewmodel.WorkStopResult{TaskKey: "work:a", State: "stopping", Accepted: true},
	}
	m := newWorkModel(t, svc)
	m.workSnapshot = acpwire.WorkSnapshot{ConversationID: "conv", Revision: 1, Items: []acpwire.WorkItem{item}}
	m, p := openWorkDetail(t, m, item)

	// Stop: the model returns a cmd; the service is untouched until it runs.
	stop := panelAction(p, "d-stop")
	if stop == nil {
		t.Fatal("no stop action on the detail")
	}
	sreq := stop().(workRequestMsg)
	if sreq.kind != workReqStop || sreq.taskKey != "work:a" {
		t.Fatalf("stop request = %s/%s, want stop/work:a", sreq.kind, sreq.taskKey)
	}
	updated, scmd := m.Update(sreq)
	m = updated.(Model)
	if scmd == nil {
		t.Fatal("stop produced no service cmd")
	}
	if len(svc.stopCalls) != 0 {
		t.Fatal("StopWork ran inside Update")
	}
	smsg, ok := scmd().(workActionMsg)
	if !ok {
		t.Fatal("stop cmd did not produce a workActionMsg")
	}
	if len(svc.stopCalls) != 1 || svc.stopCalls[0].TaskKey != "work:a" || svc.stopCalls[0].ConversationID != sreq.conversationID {
		t.Fatalf("stop call = %+v, want work:a/%s", svc.stopCalls, sreq.conversationID)
	}
	if smsg.kind != workReqStop || smsg.requestID != sreq.requestID {
		t.Fatalf("stop reply = %+v, want the accepted request's identity", smsg)
	}
	updated, _ = m.Update(smsg)
	m = updated.(Model)
	// The accepted stop does not fabricate state: the item still reads running
	// until a source snapshot settles it.
	if m.workSnapshot.Items[0].State != "running" {
		t.Fatalf("stop reply changed the item state to %q without a snapshot", m.workSnapshot.Items[0].State)
	}
	if p.err != "" {
		t.Fatalf("an accepted stop left an error: %q", p.err)
	}

	// A reply whose request ID names a different panel is dropped.
	p.err = "keep"
	updated, _ = m.Update(workActionMsg{
		conversationID: sreq.conversationID,
		taskKey:        "work:a",
		requestID:      "otherpanel.r9",
		kind:           workReqStop,
		stop:           acpwire.WorkStopResult{TaskKey: "work:a", Accepted: true},
	})
	m = updated.(Model)
	if p.err != "keep" {
		t.Fatalf("a foreign stop reply mutated the panel: err = %q", p.err)
	}
}

// TestWorkPromptNavigationDoesNotResolveOrStealSteering pins that the prompt
// action navigates to the pending request but never answers it or takes over
// steering.
func TestWorkPromptNavigationDoesNotResolveOrStealSteering(t *testing.T) {
	item := acpwire.WorkItem{
		TaskKey: "work:p", Kind: "agent", Label: "child", State: "blocked",
		Capabilities: acpwire.WorkCapabilities{Inspect: true, ResolvePrompt: true},
		Prompt:       &acpwire.WorkPrompt{Kind: "question", RequestID: "q-1", AgentID: 2},
	}
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1}}
	m := newWorkModel(t, svc)
	m.workSnapshot = acpwire.WorkSnapshot{ConversationID: "conv", Revision: 1, Items: []acpwire.WorkItem{item}}
	m, p := openWorkDetail(t, m, item)

	// A question with the canonical request ID is pending.
	stateOf(m).SetPendingQuestion(&session.PendingQuestion{
		Key:       session.PromptKey{ID: "q-1", Kind: session.PromptQuestion},
		Questions: []session.Question{{Question: "what?"}},
	})

	prompt := panelAction(p, "d-prompt")
	if prompt == nil {
		t.Fatal("no prompt action on the detail")
	}
	pmt := prompt().(workRequestMsg)
	if pmt.kind != workReqPrompt || pmt.requestID != "q-1" {
		t.Fatalf("prompt request = %s/%q, want prompt/q-1", pmt.kind, pmt.requestID)
	}

	updated, cmd := m.Update(pmt)
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("prompt navigation produced a cmd (%T); it must not answer anything", cmd)
	}

	// The prompt is still pending: navigation never resolves it.
	if stateOf(m).PendingQuestion() == nil {
		t.Fatal("prompt navigation resolved the request")
	}
	// Steering is untouched.
	if m.steeringComposer != nil {
		t.Fatal("prompt navigation opened a Send now composer")
	}
	// The Tasks panel yielded the frame.
	if m.tasksOpen() || m.tasksPanel != nil {
		t.Fatal("prompt navigation left the Tasks panel up")
	}
	// No Work action reached the service.
	if len(svc.stopCalls) != 0 || len(svc.ackCalls) != 0 || len(svc.inspectCalls) != 0 {
		t.Fatal("prompt navigation performed a work action")
	}
}

// A command's row action runs agent side: Enter on the panel goes through the
// session, the handler sees the model's state, and its text lands as a note
// (decision 7).
func TestADocActionRunsAgentSide(t *testing.T) {
	state := modelTestState(t)
	reg := commands.New()
	var sawState *session.State
	mustRegister(t, reg, commands.Command{
		Name: "things",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("Things", false, []commands.Row{{
				Text: "act", ActionLabel: "↵ run",
				Action: func(s *session.State) commands.Result {
					sawState = s
					return commands.Text("done")
				},
			}})
		},
	})
	m := testModelWith(t, service.Deps{State: state, Commands: reg})
	m.resize(80, 24)

	updated, _ := m.dispatchCommand("/things")
	m = asModel(t, updated)
	m = settleCalls(t, m)
	if !m.dock.IsOpen() {
		t.Fatal("/things should open its doc panel")
	}
	panel, ok := m.dock.Panel().(*docpanel.Panel)
	if !ok {
		t.Fatalf("dock holds %T, want *docpanel.Panel", m.dock.Panel())
	}
	panel.View(100, 20)
	cmd := panel.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the action row returned no command")
	}
	updated, _ = m.Update(cmd())
	m = asModel(t, updated)

	if sawState != state {
		t.Fatalf("the action saw %p, want the session's state %p", sawState, state)
	}
	msgs := state.Messages()
	if len(msgs) == 0 || msgs[len(msgs)-1].Content != "done" {
		t.Fatalf("last message = %+v, want the action's text", msgs[len(msgs)-1:])
	}
}
