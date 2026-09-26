package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// runAskParent runs one AskParent call to completion on its own goroutine
// and returns a channel carrying the result, so tests can interleave asks.
func runAskParent(ctx context.Context, pq ParentQuestioner, ref SubagentRef, questions []session.Question) chan askParentResult {
	out := make(chan askParentResult, 1)
	go func() {
		answers, err := pq.AskParent(ctx, ref, questions)
		out <- askParentResult{answers: answers, err: err}
	}()
	return out
}

type askParentResult struct {
	answers []session.Answer
	err     error
}

// waitQueued polls until the session holds exactly n queued child questions.
func waitQueued(t *testing.T, state *session.State, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(state.ChildQuestions()) == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("never saw %d queued child questions; have %d", n, len(state.ChildQuestions()))
}

func TestStateParentQuestionerAnswered(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pq := NewStateParentQuestioner(state, time.Minute)

	out := runAskParent(context.Background(), pq, SubagentRef{ID: 1, Desc: "research"}, []session.Question{{Question: "Which DB?"}})
	waitQueued(t, state, 1)

	state.PendingChildQuestion().Respond([]session.Answer{{Question: "Which DB?", Answer: "sqlite"}})
	res := <-out
	if res.err != nil {
		t.Fatalf("AskParent: %v", res.err)
	}
	if len(res.answers) != 1 || res.answers[0].Answer != "sqlite" {
		t.Fatalf("answers = %+v, want the sqlite answer", res.answers)
	}
	// The answered question must leave the queue when the child unblocks.
	waitQueued(t, state, 0)
}

func TestStateParentQuestionerTimeout(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pq := NewStateParentQuestioner(state, 20*time.Millisecond)

	_, err := pq.AskParent(context.Background(), SubagentRef{ID: 1, Desc: "research"}, []session.Question{{Question: "Which DB?"}})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "did not answer within") || !strings.Contains(err.Error(), "best judgement") {
		t.Errorf("timeout error should be actionable; got: %v", err)
	}
	if queued := state.ChildQuestions(); len(queued) != 0 {
		t.Fatalf("a timed-out ask must remove itself from the queue; have %d", len(queued))
	}
}

func TestStateParentQuestionerSessionEnded(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pq := NewStateParentQuestioner(state, time.Minute)

	out := runAskParent(context.Background(), pq, SubagentRef{ID: 1, Desc: "research"}, []session.Question{{Question: "Which DB?"}})
	waitQueued(t, state, 1)

	state.Shutdown()
	res := <-out
	if res.err == nil {
		t.Fatal("expected an error when the parent session ends")
	}
	if !strings.Contains(res.err.Error(), "parent session ended") {
		t.Errorf("error should name the session end; got: %v", res.err)
	}
}

func TestStateParentQuestionerCtxCancelled(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pq := NewStateParentQuestioner(state, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	out := runAskParent(ctx, pq, SubagentRef{ID: 1, Desc: "research"}, []session.Question{{Question: "Which DB?"}})
	waitQueued(t, state, 1)
	cancel()

	res := <-out
	if res.err == nil {
		t.Fatal("expected an error when the child turn is cancelled")
	}
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("error should wrap the ctx error; got: %v", res.err)
	}
	if queued := state.ChildQuestions(); len(queued) != 0 {
		t.Fatalf("a cancelled ask must remove itself from the queue; have %d", len(queued))
	}
}

// TestTwoConcurrentChildrenQueueFIFO is the Critical 2 regression: two
// children ask at once, the second queues behind the first, neither wipes
// the other, and each answer lands on the right child.
func TestTwoConcurrentChildrenQueueFIFO(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pq := NewStateParentQuestioner(state, time.Minute)

	firstOut := runAskParent(context.Background(), pq, SubagentRef{ID: 1, Desc: "first child"}, []session.Question{{Question: "Which DB?"}})
	waitQueued(t, state, 1)
	secondOut := runAskParent(context.Background(), pq, SubagentRef{ID: 2, Desc: "second child"}, []session.Question{{Question: "Which port?"}})
	waitQueued(t, state, 2)

	// The first asker is the head; the second queued behind it.
	head := state.PendingChildQuestion()
	if head == nil || head.ChildID != 1 {
		t.Fatalf("queue head = %+v, want child 1", head)
	}

	// Answering the head delivers ONLY to the first child.
	tool := NewParentRespondTool(state)
	if _, err := tool.Handler(context.Background(), parentRespondCall(`{"answers":[{"question":"Which DB?","answer":"sqlite"}]}`)); err != nil {
		t.Fatalf("respond: %v", err)
	}
	first := <-firstOut
	if first.err != nil || len(first.answers) != 1 || first.answers[0].Answer != "sqlite" {
		t.Fatalf("first child got %+v (err %v), want the sqlite answer", first.answers, first.err)
	}
	// The second child is still queued and still waiting.
	if queued := state.ChildQuestions(); len(queued) != 1 || queued[0].ChildID != 2 {
		t.Fatalf("after answering the head, queue = %+v, want child 2 only", queued)
	}
	select {
	case got := <-secondOut:
		t.Fatalf("second child unblocked prematurely with %+v", got)
	default:
	}

	// The first child's teardown (its AskParent defer) must not wipe the
	// second child's question. waitQueued proves the queue still holds it.
	waitQueued(t, state, 1)

	// Answer the second child too.
	if _, err := tool.Handler(context.Background(), parentRespondCall(`{"child_id":2,"answers":[{"question":"Which port?","answer":"8443"}]}`)); err != nil {
		t.Fatalf("respond to second: %v", err)
	}
	second := <-secondOut
	if second.err != nil || len(second.answers) != 1 || second.answers[0].Answer != "8443" {
		t.Fatalf("second child got %+v (err %v), want the 8443 answer", second.answers, second.err)
	}
	waitQueued(t, state, 0)
}

func TestParentRespondChildIDGuard(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	first := pendingChildQuestion(1, "first child", "Which DB?")
	second := pendingChildQuestion(2, "second child", "Which port?")
	state.PushChildQuestion(first)
	state.PushChildQuestion(second)

	tool := NewParentRespondTool(state)
	_, err := tool.Handler(context.Background(), parentRespondCall(`{"child_id":2,"answers":[{"question":"Which port?","answer":"8443"}]}`))
	if err == nil {
		t.Fatal("expected a child_id mismatch rejection")
	}
	if !strings.Contains(err.Error(), "not the queue head") {
		t.Errorf("error should name the mismatch; got: %v", err)
	}
	// No answer may have been delivered and the queue must be intact.
	select {
	case got := <-first.ResponseChan:
		t.Fatalf("answers leaked to child 1: %+v", got)
	default:
	}
	if queued := state.ChildQuestions(); len(queued) != 2 {
		t.Fatalf("queue after rejected respond = %d, want 2", len(queued))
	}
}

func TestParentRespondValidatesAnswerCount(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	state.PushChildQuestion(pendingChildQuestion(5, "both questions", "Which DB?", "Which port?"))

	tool := NewParentRespondTool(state)
	_, err := tool.Handler(context.Background(), parentRespondCall(`{"answers":[{"question":"Which DB?","answer":"sqlite"}]}`))
	if err == nil {
		t.Fatal("expected a count-mismatch rejection")
	}
	if !strings.Contains(err.Error(), "asked 2 question(s)") || !strings.Contains(err.Error(), "1 answer(s)") {
		t.Errorf("error should name the mismatch; got: %v", err)
	}
	if queued := state.ChildQuestions(); len(queued) != 1 {
		t.Fatalf("queue after rejected respond = %d, want 1", len(queued))
	}
}

// TestChildQuestionTextReachesParent pins Critical 1: both the synthetic
// subagent report and the loop-top hint carry the question text verbatim so
// the parent can answer from context, never blind.
func TestChildQuestionTextReachesParent(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pq := NewStateParentQuestioner(state, time.Minute)

	out := runAskParent(context.Background(), pq, SubagentRef{ID: 9, Desc: "migrate the schema"}, []session.Question{{Question: "Which DB?", Options: []session.QuestionOption{{Label: "sqlite"}, {Label: "postgres"}}}})
	defer func() {
		state.PendingChildQuestion().Respond([]session.Answer{{Question: "Which DB?", Answer: "sqlite"}})
		<-out
	}()
	waitQueued(t, state, 1)

	// The synthetic report names the questions.
	var report string
	for _, r := range state.SubagentReports() {
		if strings.Contains(r, "subagent 9 asked") {
			report = r
		}
	}
	if report == "" {
		t.Fatal("expected a synthetic subagent report announcing the question")
	}
	if !strings.Contains(report, "Which DB?") || !strings.Contains(report, "sqlite") {
		t.Errorf("report should carry the question and its options; got:\n%s", report)
	}

	// The loop-top hint names the questions too.
	runner := &Runner{State: state}
	hint, ok := runner.pendingChildQuestionHint()
	if !ok {
		t.Fatal("expected a hint while a child is queued")
	}
	if !strings.Contains(hint.Content, "Which DB?") || !strings.Contains(hint.Content, "postgres") {
		t.Errorf("hint should carry the question text verbatim; got:\n%s", hint.Content)
	}
}

func TestParentQuestionDefaultsMatchDocumentedValues(t *testing.T) {
	if DefaultParentQuestionTimeout != 5*time.Minute {
		t.Fatalf("DefaultParentQuestionTimeout = %s, want 5m", DefaultParentQuestionTimeout)
	}
	if DefaultParentQuestionMaxPerTask != 3 {
		t.Fatalf("DefaultParentQuestionMaxPerTask = %d, want 3", DefaultParentQuestionMaxPerTask)
	}
}

// stubParentQuestioner is a ParentQuestioner whose behaviour each test
// dials in: canned answers, a canned error, or a block channel that holds
// the call open until the test releases it.
type stubParentQuestioner struct {
	mu      sync.Mutex
	calls   int
	answers []session.Answer
	err     error
	block   chan struct{}
}

func (s *stubParentQuestioner) AskParent(ctx context.Context, _ SubagentRef, _ []session.Question) ([]session.Answer, error) {
	s.mu.Lock()
	s.calls++
	block := s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.answers, nil
}

func (s *stubParentQuestioner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func parentAskCall(args string) registry.ToolCall {
	return registry.ToolCall{ID: "call_parent", Name: "parent.ask", Args: json.RawMessage(args)}
}

const oneQuestion = `{"questions":[{"question":"Which DB?"}]}`

func TestParentAskAnswered(t *testing.T) {
	stub := &stubParentQuestioner{answers: []session.Answer{{Question: "Which DB?", Answer: "sqlite"}}}
	tool := NewParentAskTool(stub, time.Minute, 3)

	res, err := tool.Handler(context.Background(), parentAskCall(oneQuestion))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "sqlite") {
		t.Errorf("result should carry the answer text; got:\n%s", res.Content)
	}
	if stub.callCount() != 1 {
		t.Errorf("stub called %d times, want 1", stub.callCount())
	}
}

func TestParentAskTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	stub := &stubParentQuestioner{block: block}
	tool := NewParentAskTool(stub, 20*time.Millisecond, 3)

	_, err := tool.Handler(context.Background(), parentAskCall(oneQuestion))
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "did not answer within") {
		t.Errorf("timeout error should be actionable; got: %v", err)
	}
	if !strings.Contains(err.Error(), "best judgement") {
		t.Errorf("timeout error should tell the child how to proceed; got: %v", err)
	}
}

func TestParentAskParentGone(t *testing.T) {
	stub := &stubParentQuestioner{err: errors.New("parent no longer available")}
	tool := NewParentAskTool(stub, time.Minute, 3)

	_, err := tool.Handler(context.Background(), parentAskCall(oneQuestion))
	if err == nil {
		t.Fatal("expected a teardown error")
	}
	if !strings.Contains(err.Error(), "parent no longer available") {
		t.Errorf("teardown error should be clean; got: %v", err)
	}
}

func TestParentAskMaxPerTask(t *testing.T) {
	stub := &stubParentQuestioner{answers: []session.Answer{{Question: "Which DB?", Answer: "sqlite"}}}
	tool := NewParentAskTool(stub, time.Minute, 2)
	call := parentAskCall(oneQuestion)

	for i := 1; i <= 2; i++ {
		if _, err := tool.Handler(context.Background(), call); err != nil {
			t.Fatalf("call %d should be within budget: %v", i, err)
		}
	}
	_, err := tool.Handler(context.Background(), call)
	if err == nil {
		t.Fatal("expected the per-task cap to reject the third call")
	}
	if !strings.Contains(err.Error(), "budget exhausted") {
		t.Errorf("cap error should name the budget; got: %v", err)
	}
	if stub.callCount() != 2 {
		t.Errorf("stub called %d times, want 2 (the capped call must not reach it)", stub.callCount())
	}
}

func TestParentAskOneOutstanding(t *testing.T) {
	block := make(chan struct{})
	stub := &stubParentQuestioner{block: block}
	tool := NewParentAskTool(stub, time.Minute, 3)
	call := parentAskCall(oneQuestion)

	first := make(chan error, 1)
	go func() {
		_, err := tool.Handler(context.Background(), call)
		first <- err
	}()

	// Wait until the first call is genuinely in flight before racing it.
	deadline := time.Now().Add(2 * time.Second)
	for stub.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stub.callCount() == 0 {
		t.Fatal("first call never reached the stub")
	}

	_, err := tool.Handler(context.Background(), call)
	if err == nil {
		t.Fatal("expected the second concurrent call to be rejected")
	}
	if !strings.Contains(err.Error(), "already outstanding") {
		t.Errorf("error should name the outstanding call; got: %v", err)
	}

	close(block)
	if err := <-first; err != nil {
		t.Fatalf("first call should succeed once released: %v", err)
	}
}

// pendingChildQuestion builds the published slot a child's parent.ask leaves
// behind, mirroring the real publisher's buffered cap-1 channel.
func pendingChildQuestion(id int64, desc string, q ...string) *session.PendingChildQuestion {
	questions := make([]session.Question, 0, len(q))
	for _, one := range q {
		questions = append(questions, session.Question{Question: one})
	}
	return &session.PendingChildQuestion{
		ChildID:      id,
		ChildDesc:    desc,
		Questions:    questions,
		ResponseChan: make(chan []session.Answer, 1),
	}
}

func parentRespondCall(args string) registry.ToolCall {
	return registry.ToolCall{ID: "call_respond", Name: "parent.respond", Args: json.RawMessage(args)}
}

// TestParentRespondDeliversAnswers is the happy path: the answers reach the
// waiting child through the published slot, and the user gets a plain notice
// naming what was decided on their behalf (plus the override affordance).
func TestParentRespondDeliversAnswers(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	pending := pendingChildQuestion(7, "pick a database", "Which DB?")
	state.PushChildQuestion(pending)

	tool := NewParentRespondTool(state)
	res, err := tool.Handler(context.Background(), parentRespondCall(`{"answers":[{"question":"Which DB?","answer":"sqlite"}]}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "sqlite") {
		t.Errorf("result should carry the answer text; got:\n%s", res.Content)
	}

	select {
	case got := <-pending.ResponseChan:
		if len(got) != 1 || got[0].Answer != "sqlite" {
			t.Fatalf("child received %+v, want the sqlite answer", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answers never reached the waiting child")
	}

	var notice string
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "I answered on your behalf") {
			notice = m.Content
		}
	}
	if notice == "" {
		t.Fatal("the parent's answer must be recorded in the transcript so the user can override it")
	}
	if !strings.Contains(notice, "subagent 7") || !strings.Contains(notice, "pick a database") {
		t.Errorf("notice should attribute the question to the child; got: %s", notice)
	}
	if !strings.Contains(notice, "correction") {
		t.Errorf("notice should offer the override affordance; got: %s", notice)
	}
}

// TestParentRespondNotPendingIsClean pins the no-op path: answering when no
// child is waiting is a clean result, not an error, so a model that races a
// timeout is not punished for it.
func TestParentRespondNotPendingIsClean(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := NewParentRespondTool(state)

	res, err := tool.Handler(context.Background(), parentRespondCall(`{"answers":[{"question":"Which DB?","answer":"sqlite"}]}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "No subagent is waiting") {
		t.Errorf("result should say nothing was sent; got:\n%s", res.Content)
	}
	if len(state.Messages()) != 0 {
		t.Errorf("a no-op answer must not write a notice; got %d messages", len(state.Messages()))
	}
}

// TestParentRespondRejectsEmpty guards the schema's intent at the handler: an
// answer list with no entries would silently deliver nothing.
func TestParentRespondRejectsEmpty(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := NewParentRespondTool(state)
	if _, err := tool.Handler(context.Background(), parentRespondCall(`{"answers":[]}`)); err == nil {
		t.Fatal("expected an error for an empty answer list")
	}
}

// TestPendingChildQuestionHintOnlyWhenWaiting pins the loop-top affordance:
// the hint appears while a child question is outstanding, names the child,
// and never tells a subtask child to answer a question it cannot have.
func TestPendingChildQuestionHintOnlyWhenWaiting(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	runner := &Runner{State: state}

	if _, ok := runner.pendingChildQuestionHint(); ok {
		t.Fatal("no hint should be produced when nothing is waiting")
	}

	state.PushChildQuestion(pendingChildQuestion(4, "migrate the schema", "Which DB?"))
	hint, ok := runner.pendingChildQuestionHint()
	if !ok {
		t.Fatal("a waiting child must produce a loop-top hint")
	}
	if !strings.Contains(hint.Content, "subagent 4") || !strings.Contains(hint.Content, "migrate the schema") {
		t.Errorf("hint should attribute the question to the child; got: %s", hint.Content)
	}
	if !strings.Contains(hint.Content, "parent.respond") {
		t.Errorf("hint should name the answering tool; got: %s", hint.Content)
	}
	if !strings.Contains(hint.Content, "question.ask") {
		t.Errorf("hint should name the escalation path; got: %s", hint.Content)
	}

	// A subtask child has no children of its own to answer.
	subtask := &Runner{State: state, Role: RoleSubtask}
	if _, ok := subtask.pendingChildQuestionHint(); ok {
		t.Fatal("a subtask child must not be told to answer a pending child question")
	}
}

// TestParentCorrectRoutesToChildSteering is the override path: the correction
// lands on the LIVE child's own steering queue, enveloped so the child can
// tell it apart from ordinary user steering, and never on the parent's queue.
func TestParentCorrectRoutesToChildSteering(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	childState := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(1))
	view := state.RegisterSubagentWithMeta("migrate the schema", childState, session.SubagentMeta{Role: RoleSubtask})

	tool := NewParentCorrectTool(state)
	args := fmt.Sprintf(`{"child_id":%d,"correction":"use Postgres, not SQLite"}`, view.ID)
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "call_correct", Name: "parent.correct", Args: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, fmt.Sprintf("%d", view.ID)) {
		t.Errorf("summary should name the corrected child; got: %s", res.Summary)
	}

	queued := childState.SteeringQueue()
	if len(queued) != 1 {
		t.Fatalf("child steering queue = %v, want exactly one correction", queued)
	}
	if !strings.Contains(queued[0], "[parent correction]") || !strings.Contains(queued[0], "use Postgres, not SQLite") {
		t.Errorf("queued steering should be the enveloped correction; got: %s", queued[0])
	}
	if len(state.SteeringQueue()) != 0 {
		t.Error("the correction must go to the child, not the parent")
	}
}

// TestParentCorrectRejectsUnknownFinishedAndEmpty keeps every failure mode
// actionable rather than silently dropping the user's correction.
func TestParentCorrectRejectsUnknownFinishedAndEmpty(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := NewParentCorrectTool(state)

	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "c", Name: "parent.correct", Args: json.RawMessage(`{"child_id":99,"correction":"x"}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent") {
		t.Fatalf("unknown child error = %v, want a named 'unknown subagent'", err)
	}

	childState := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(1))
	view := state.RegisterSubagentWithMeta("finished child", childState, session.SubagentMeta{Role: RoleSubtask})

	_, err = tool.Handler(context.Background(), registry.ToolCall{ID: "c", Name: "parent.correct", Args: json.RawMessage(fmt.Sprintf(`{"child_id":%d,"correction":"  "}`, view.ID))})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty correction error = %v, want 'must not be empty'", err)
	}

	state.FinishSubagent(view.ID, "done", nil)
	_, err = tool.Handler(context.Background(), registry.ToolCall{ID: "c", Name: "parent.correct", Args: json.RawMessage(fmt.Sprintf(`{"child_id":%d,"correction":"x"}`, view.ID))})
	if err == nil || !strings.Contains(err.Error(), "no longer running") {
		t.Fatalf("finished child error = %v, want 'no longer running'", err)
	}
	if len(childState.SteeringQueue()) != 0 {
		t.Error("a finished child must not receive steering")
	}
}
