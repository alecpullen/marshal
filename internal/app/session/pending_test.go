package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"marshal/internal/pubsub"
)

func TestQuestionOptionString(t *testing.T) {
	var o QuestionOption
	if err := json.Unmarshal([]byte(`"foo"`), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Label != "foo" {
		t.Fatalf("Label = %q, want foo", o.Label)
	}
	if o.Description != "" {
		t.Fatalf("Description = %q, want empty", o.Description)
	}
}

func TestQuestionOptionObject(t *testing.T) {
	var o QuestionOption
	if err := json.Unmarshal([]byte(`{"label":"foo","description":"bar"}`), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Label != "foo" {
		t.Fatalf("Label = %q, want foo", o.Label)
	}
	if o.Description != "bar" {
		t.Fatalf("Description = %q, want bar", o.Description)
	}
}

func TestQuestionOptionMixedArray(t *testing.T) {
	var q Question
	if err := json.Unmarshal([]byte(`{"question":"pick","options":["simple",{"label":"rich","description":"more"}]}`), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(q.Options) != 2 {
		t.Fatalf("options len = %d, want 2", len(q.Options))
	}
	if q.Options[0].Label != "simple" || q.Options[0].Description != "" {
		t.Fatalf("options[0] = %+v, want {simple }", q.Options[0])
	}
	if q.Options[1].Label != "rich" || q.Options[1].Description != "more" {
		t.Fatalf("options[1] = %+v, want {rich more}", q.Options[1])
	}
}

func TestQuestionOptionInvalidShape(t *testing.T) {
	// null is the subtle one: json.Unmarshal treats it as a no-op success
	// for every target type, so without an explicit guard it would take the
	// string branch and decode as an empty label.
	for _, in := range []string{`{"description":"x"}`, `123`, `null`, `{"label":""}`} {
		var o QuestionOption
		if err := json.Unmarshal([]byte(in), &o); err == nil {
			t.Fatalf("unmarshal(%s) = nil error, want error", in)
		}
	}
}

func TestQuestionOptionNullInArrayIsRejected(t *testing.T) {
	var q Question
	err := json.Unmarshal([]byte(`{"question":"pick","options":["a",null]}`), &q)
	if err == nil {
		t.Fatalf("unmarshal with null option = nil error, want error; got %+v", q.Options)
	}
}

// ── Canonical prompt settlement ────────────────────────────────────────

// promptFixture is a session with one active eligible turn and one open
// operation, so a prompt registered against it belongs to a real operation the
// Send now controller can cancel.
type promptFixture struct {
	t     *testing.T
	state *State
	turn  string
	ref   OperationRef
	opCtx context.Context
}

func newPromptFixture(t *testing.T) *promptFixture {
	t.Helper()
	s := newTestState()
	turn, err := s.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	opCtx, ref, err := s.BeginSteeringOperation(context.Background(), turn, OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation = %v, want nil", err)
	}
	return &promptFixture{t: t, state: s, turn: turn, ref: ref, opCtx: opCtx}
}

// submit accepts one Send now request for the fixture's turn.
func (f *promptFixture) submit(id, text string) {
	f.t.Helper()
	if _, err := f.state.SubmitSendNow(SendNowRequest{TurnID: f.turn, RequestID: id, Text: text}); err != nil {
		f.t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
}

// operationToken returns the fixture's currently open operation token, so a
// test can record steering intent on it without starting the interruption
// worker.
func (f *promptFixture) operationToken() *steeringOperation {
	f.state.steeringMu.Lock()
	defer f.state.steeringMu.Unlock()
	return f.state.steering.current
}

// TestPromptSteeringCancellationWinsBeforeGrant pins the shared claim boundary:
// cancellation intent recorded at acceptance prevents the grant and its callback
// even while the cancellation signal itself is still delayed.
func TestPromptSteeringCancellationWinsBeforeGrant(t *testing.T) {
	f := newPromptFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.state.SetOperationInterrupt(func(context.Context, OperationRef) (OperationStop, error) {
		close(entered)
		<-release
		return OperationStop{}, nil
	})

	call := &PendingToolCall{ID: "call-1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	key, err := f.state.RegisterApproval(f.opCtx, call)
	if err != nil {
		t.Fatalf("RegisterApproval = %v, want nil", err)
	}
	if key.ID == "" || key.Kind != PromptApproval || key.Operation != f.ref {
		t.Fatalf("key = %+v, want an approval key owned by %+v", key, f.ref)
	}

	// Accept steering; the handler's Begin is blocked, so the cancellation
	// signal has not been sent yet — but the intent is already recorded.
	f.submit("r1", "stop and steer")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the interruption handler never ran")
	}

	applied := false
	won, err := f.state.ResolveApproval(key, UserApprovalDecision{Approved: true}, func() error {
		applied = true
		return nil
	})
	if err != nil {
		t.Fatalf("ResolveApproval = %v, want nil", err)
	}
	if won {
		t.Fatal("the approval claim won after steering intent was recorded")
	}
	if applied {
		t.Fatal("the approval's apply callback ran after steering intent was recorded")
	}
	select {
	case d := <-call.ResponseChan:
		t.Fatalf("the grant was delivered after steering intent was recorded: %+v", d)
	default:
	}

	// Releasing Begin lets the worker settle the prompt as steered.
	close(release)
	select {
	case d := <-call.ResponseChan:
		if !d.Canceled || d.Approved {
			t.Fatalf("delivered decision = %+v, want a canceled non-grant", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the canceled prompt never released its waiter")
	}
	if _, ok := <-call.ResponseChan; ok {
		t.Fatal("the canceled prompt delivered twice")
	}
	if !errors.Is(f.state.PromptCause(key), ErrSendNow) {
		t.Fatalf("PromptCause = %v, want ErrSendNow", f.state.PromptCause(key))
	}
	if f.state.PendingApproval() != nil {
		t.Fatal("the settled prompt still occupies the slot")
	}
}

// TestPromptAcceptedApplyFinishesBeforeCancelReturns pins that a grant whose
// claim was accepted first runs to completion, and that the cancellation which
// lost waits for it instead of rolling it back.
func TestPromptAcceptedApplyFinishesBeforeCancelReturns(t *testing.T) {
	f := newPromptFixture(t)

	call := &PendingToolCall{ID: "call-1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	key, err := f.state.RegisterApproval(f.opCtx, call)
	if err != nil {
		t.Fatalf("RegisterApproval = %v, want nil", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	won := make(chan bool, 1)
	go func() {
		ok, _ := f.state.ResolveApproval(key, UserApprovalDecision{Approved: true, Edited: "edited"}, func() error {
			close(entered)
			<-release
			return nil
		})
		won <- ok
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the accepted claim never started applying")
	}

	// The cancellation must not return while the accepted grant is applying.
	canceled := make(chan bool, 1)
	go func() { canceled <- f.state.CancelPrompt(key, ErrSendNow) }()
	select {
	case <-canceled:
		t.Fatal("CancelPrompt returned while an accepted grant was still applying")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if ok := <-won; !ok {
		t.Fatal("the accepted grant lost its claim")
	}
	if ok := <-canceled; ok {
		t.Fatal("the cancellation claimed a request the grant already owned")
	}

	select {
	case d := <-call.ResponseChan:
		if !d.Approved || d.Edited != "edited" {
			t.Fatalf("delivered decision = %+v, want the accepted grant unchanged", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the accepted grant never reached its waiter")
	}
	if _, ok := <-call.ResponseChan; ok {
		t.Fatal("the channel carried a second value")
	}
}

// TestPromptLateEventCopyCannotAnswerNewRequest pins that an event copy retained
// across a settlement can neither answer nor clear the request that replaced it.
func TestPromptLateEventCopyCannotAnswerNewRequest(t *testing.T) {
	f := newPromptFixture(t)

	first := &PendingToolCall{ID: "first", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	key, err := f.state.RegisterApproval(f.opCtx, first)
	if err != nil {
		t.Fatalf("RegisterApproval = %v, want nil", err)
	}
	// The copy an old subscriber retained: key plus canonical reference, no
	// response channel.
	stale := approvalSnapshot(first)
	if stale.ResponseChan != nil {
		t.Fatal("the registered event copy carried a response channel")
	}
	if stale.Key != key || stale.canonical == nil {
		t.Fatalf("the registered event copy = %+v, want the canonical key and responder", stale)
	}

	if ok, err := f.state.ResolveApproval(key, UserApprovalDecision{Approved: true}, nil); !ok || err != nil {
		t.Fatalf("ResolveApproval = (%v, %v), want the first prompt settled", ok, err)
	}
	<-first.ResponseChan

	second := &PendingToolCall{ID: "second", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	key2, err := f.state.RegisterApproval(f.opCtx, second)
	if err != nil {
		t.Fatalf("second RegisterApproval = %v, want nil", err)
	}

	stale.Respond(UserApprovalDecision{Approved: true})
	select {
	case d := <-second.ResponseChan:
		t.Fatalf("a stale event copy answered the newer request: %+v", d)
	default:
	}
	f.state.ClearPrompt(key)
	if got := f.state.PendingApproval(); got != second {
		t.Fatal("a stale ClearPrompt cleared the newer request")
	}
	if ok, err := f.state.ResolveApproval(key2, UserApprovalDecision{Approved: false}, nil); !ok || err != nil {
		t.Fatalf("the newer request was no longer answerable: (%v, %v)", ok, err)
	}
}

// TestPromptCancelKindsHaveDistinctCause pins that every prompt kind unblocks
// exactly once on cancellation, with its own compatibility sentinel and a
// distinct recorded cause — never a fabricated answer or a sticky denial.
func TestPromptCancelKindsHaveDistinctCause(t *testing.T) {
	f := newPromptFixture(t)
	s := f.state

	approval := &PendingToolCall{ID: "a", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	approvalKey, err := s.RegisterApproval(f.opCtx, approval)
	if err != nil {
		t.Fatalf("RegisterApproval = %v, want nil", err)
	}
	question := &PendingQuestion{
		Questions:    []Question{{Question: "which file?"}},
		ResponseChan: make(chan []Answer, 1),
	}
	questionKey, err := s.RegisterQuestion(f.opCtx, question)
	if err != nil {
		t.Fatalf("RegisterQuestion = %v, want nil", err)
	}
	skill := &PendingSkillGate{Skill: "demo", ResponseChan: make(chan SkillGateChoice, 1)}
	skillKey, err := s.RegisterSkillGate(f.opCtx, skill)
	if err != nil {
		t.Fatalf("RegisterSkillGate = %v, want nil", err)
	}
	sudo := &PendingSudoAuth{Command: "systemctl restart nginx", ResponseChan: make(chan SudoAuthDecision, 1)}
	sudoKey, err := s.RegisterSudoAuth(f.opCtx, sudo)
	if err != nil {
		t.Fatalf("RegisterSudoAuth = %v, want nil", err)
	}

	causes := map[PromptKind]error{
		PromptApproval: errors.New("steering interrupted the approval"),
		PromptQuestion: errors.New("steering interrupted the question"),
		PromptSkill:    errors.New("steering interrupted the skill gate"),
		PromptSudo:     errors.New("steering interrupted the privileged prompt"),
	}
	keys := map[PromptKind]PromptKey{
		PromptApproval: approvalKey,
		PromptQuestion: questionKey,
		PromptSkill:    skillKey,
		PromptSudo:     sudoKey,
	}
	for kind, key := range keys {
		if !s.CancelPrompt(key, causes[kind]) {
			t.Fatalf("CancelPrompt(%s) = false, want the cancellation to win", kind)
		}
		if s.CancelPrompt(key, causes[kind]) {
			t.Fatalf("CancelPrompt(%s) won twice for one request", kind)
		}
		if !errors.Is(s.PromptCause(key), causes[kind]) {
			t.Fatalf("PromptCause(%s) = %v, want %v", kind, s.PromptCause(key), causes[kind])
		}
	}

	// Approval: a canceled non-grant, never an approval.
	d := <-approval.ResponseChan
	if d.Approved || !d.Canceled {
		t.Fatalf("canceled approval = %+v, want a canceled non-grant", d)
	}
	// Question: the compatibility sentinel, never a fabricated answer.
	answers := <-question.ResponseChan
	if len(answers) != 1 || answers[0].Answer != AnswerUnanswered {
		t.Fatalf("canceled question answers = %+v, want one Unanswered sentinel", answers)
	}
	// Skill: a denial that leaves no sticky rule behind.
	if got := <-skill.ResponseChan; got != SkillGateDeny {
		t.Fatalf("canceled skill gate = %v, want SkillGateDeny", got)
	}
	if got := s.SkillGateDecisions(); len(got) != 0 {
		t.Fatalf("a canceled skill gate recorded a sticky decision: %+v", got)
	}
	// Privileged auth: an empty decision, no password bytes.
	sd := <-sudo.ResponseChan
	if sd.OK || len(sd.Password) != 0 {
		t.Fatalf("canceled privileged prompt = %+v, want an empty denial", sd)
	}

	for range keys {
		// Every canceled prompt must be closed after a single delivery.
		for _, ch := range []<-chan UserApprovalDecision{approval.ResponseChan} {
			if _, ok := <-ch; ok {
				t.Fatal("the canceled approval prompt delivered a second value")
			}
		}
		for _, ch := range []<-chan []Answer{question.ResponseChan} {
			if _, ok := <-ch; ok {
				t.Fatal("the canceled question prompt delivered a second value")
			}
		}
		for _, ch := range []<-chan SkillGateChoice{skill.ResponseChan} {
			if _, ok := <-ch; ok {
				t.Fatal("the canceled skill prompt delivered a second value")
			}
		}
		for _, ch := range []<-chan SudoAuthDecision{sudo.ResponseChan} {
			if _, ok := <-ch; ok {
				t.Fatal("the canceled privileged prompt delivered a second value")
			}
		}
		break
	}
}

// TestPromptSteeringSudoCleanupHasNoSecretSnapshot pins that a canceled
// privileged-auth prompt releases its waiter with an empty decision and that no
// snapshot, event or registry record retains password bytes.
func TestPromptSteeringSudoCleanupHasNoSecretSnapshot(t *testing.T) {
	f := newPromptFixture(t)
	s := f.state

	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx)
	s.SetEventBroker(broker)

	sudo := &PendingSudoAuth{
		Command:      "systemctl restart nginx",
		Reason:       "restart the web server",
		ResponseChan: make(chan SudoAuthDecision, 1),
	}
	key, err := s.RegisterSudoAuth(f.opCtx, sudo)
	if err != nil {
		t.Fatalf("RegisterSudoAuth = %v, want nil", err)
	}

	// A registered snapshot carries no response channel and no secret: only the
	// canonical key and the display metadata.
	snap := sudoAuthSnapshot(sudo)
	if snap.ResponseChan != nil {
		t.Fatal("the registered privileged event copy carried a response channel")
	}
	if snap.Key != key {
		t.Fatalf("the event copy key = %+v, want %+v", snap.Key, key)
	}

	if !s.CancelPrompt(key, ErrSendNow) {
		t.Fatal("CancelPrompt = false, want the steering cancellation to win")
	}
	d := <-sudo.ResponseChan
	if d.OK || len(d.Password) != 0 {
		t.Fatalf("canceled privileged prompt = %+v, want an empty denial with no password", d)
	}
	if _, ok := <-sudo.ResponseChan; ok {
		t.Fatal("the canceled privileged prompt delivered a second value")
	}
	if s.PendingSudoAuth() != nil {
		t.Fatal("the canceled privileged prompt still occupies the slot")
	}

	// Drain the published events and require that none carries secret material.
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Payload.PendingSudoAuth == nil {
				continue
			}
			if len(ev.Payload.PendingSudoAuth.Command) == 0 && ev.Payload.PendingSudoAuth.ResponseChan == nil {
				// The clear event: nothing secret, nothing to answer.
				continue
			}
			if ev.Payload.PendingSudoAuth.ResponseChan != nil {
				t.Fatal("a published privileged snapshot carried a response channel")
			}
		case <-deadline:
			return
		}
	}
}

// TestPromptCancelPreservesIndependentChild pins that an operation-scoped
// cancellation settles only the prompts owned by that exact operation, leaving
// an independently-owned request pending.
func TestPromptCancelPreservesIndependentChild(t *testing.T) {
	f := newPromptFixture(t)
	s := f.state

	parent := &PendingToolCall{ID: "parent", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	parentKey, err := s.RegisterApproval(f.opCtx, parent)
	if err != nil {
		t.Fatalf("RegisterApproval = %v, want nil", err)
	}
	// A prompt belonging to no operation — an independent child's request.
	childCtx := context.Background()
	child := &PendingQuestion{
		Questions:    []Question{{Question: "child wants to know"}},
		ResponseChan: make(chan []Answer, 1),
	}
	childKey, err := s.RegisterQuestion(childCtx, child)
	if err != nil {
		t.Fatalf("RegisterQuestion = %v, want nil", err)
	}
	if childKey.Operation.ID != "" {
		t.Fatalf("the independent request was attached to %+v", childKey.Operation)
	}

	s.CancelOperationPrompts(f.ref)

	if !errors.Is(s.PromptCause(parentKey), ErrSendNow) {
		t.Fatalf("the parent's request was not canceled: %v", s.PromptCause(parentKey))
	}
	if got := <-parent.ResponseChan; !got.Canceled {
		t.Fatalf("the parent's request = %+v, want a cancellation", got)
	}
	// The independently-owned request is untouched: still pending and still
	// answerable.
	if got := s.PendingQuestion(); got != child {
		t.Fatal("the independent request was cleared by another operation's cancellation")
	}
	if !s.ResolveQuestion(childKey, []Answer{{Question: "child wants to know", Answer: "yes"}}) {
		t.Fatal("the independent request was no longer answerable")
	}
	if got := <-child.ResponseChan; len(got) != 1 || got[0].Answer != "yes" {
		t.Fatalf("the independent request received %+v, want its own answer", got)
	}
}

// TestAccessSteeringClaimSharesCanonicalGate pins both sides of the
// application-acceptance race on a typed access request: intent recorded first
// prevents the grant, and a grant already applying is never rolled back.
func TestAccessSteeringClaimSharesCanonicalGate(t *testing.T) {
	f := newPromptFixture(t)
	s := f.state

	call := &PendingToolCall{ID: "call-1", Name: "file.edit", ResponseChan: make(chan UserApprovalDecision, 1)}
	req, waiting, err := s.RegisterAccess(f.opCtx, call, AccessIntent{System: true})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (waiting %v, err %v), want a published request", waiting, err)
	}
	if !SteeringRequested(f.opCtx) {
		// The fixture's operation is open and its intent is not yet recorded;
		// the claim boundary is what this test isolates.
		if f.operationToken() == nil {
			t.Fatal("the fixture's operation is not open")
		}
	}

	applied := false
	prepare := func(AccessRequest, AccessChoice) (AccessCommit, error) {
		return AccessCommit{Effective: AccessState{Mode: "default", System: true}, Apply: func() { applied = true }}, nil
	}

	// (a) Cancellation intent wins first. Recording it exactly as SubmitSendNow
	// does — without starting the interruption worker — isolates the shared
	// claim boundary.
	token := f.operationToken()
	if token == nil {
		t.Fatal("the fixture has no open operation token")
	}
	token.requested.Store(true)

	result, err := s.ResolveAccess(choiceFor(req, req.Current, ""), prepare)
	if err != nil {
		t.Fatalf("ResolveAccess = %v, want nil", err)
	}
	if result.Status != AccessCanceled {
		t.Fatalf("ResolveAccess = %+v, want a cancellation while intent is recorded", result)
	}
	if applied {
		t.Fatal("the grant was applied after steering intent was recorded")
	}
	if got := s.AccessState(); got.System {
		t.Fatalf("AccessState() = %+v, want the pair unchanged", got)
	}

	// (b) A grant whose claim was accepted first is not rolled back. The
	// resolver holds the access gate for its whole body, so a concurrent
	// cancellation cannot reach the request until the grant has been applied
	// and acknowledged.
	second := &PendingToolCall{ID: "call-2", Name: "file.edit", ResponseChan: make(chan UserApprovalDecision, 1)}
	req2, waiting, err := s.RegisterAccess(context.Background(), second, AccessIntent{System: true})
	if err != nil || !waiting {
		t.Fatalf("second RegisterAccess = (waiting %v, err %v), want a published request", waiting, err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	granted := make(chan AccessResult, 1)
	go func() {
		res, _ := s.ResolveAccess(choiceFor(req2, req2.Current, ""), func(AccessRequest, AccessChoice) (AccessCommit, error) {
			return AccessCommit{Effective: AccessState{Mode: "default", System: true}, Apply: func() {
				close(entered)
				<-release
			}}, nil
		})
		granted <- res
	}()
	<-entered

	canceled := make(chan bool, 1)
	go func() { canceled <- s.CancelAccess(req2.RequestID, AccessCanceled, ErrSendNow.Error()) }()
	close(release)

	res := <-granted
	if res.Status != AccessGranted {
		t.Fatalf("the accepted grant = %+v, want AccessGranted", res)
	}
	if <-canceled {
		t.Fatal("a cancellation arriving after the grant was acknowledged claimed the request")
	}
	if got := s.AccessState(); !got.System {
		t.Fatalf("AccessState() = %+v, want the granted pair retained", got)
	}
	d := <-second.ResponseChan
	if !d.Approved || d.Access == nil || d.Access.Status != AccessGranted {
		t.Fatalf("delivered decision = %+v, want the committed grant", d)
	}
}
