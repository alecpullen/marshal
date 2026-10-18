package agent

import (
	"context"
	"encoding/json"
	"errors"
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

func TestInternalToolsAreNeverOfferedToTheModel(t *testing.T) {
	reg := registry.New()
	ok := func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ok"}, nil
	}
	for _, tl := range []registry.Tool{
		{Name: "file.read", Description: "read", Risk: registry.RiskReadOnly, Schema: json.RawMessage(`{"type":"object"}`), Handler: ok},
		{Name: "hidden.tool", Description: "hidden", Risk: registry.RiskReadOnly, Internal: true, Schema: json.RawMessage(`{"type":"object"}`), Handler: ok},
	} {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "m")
	for _, d := range r.buildToolDefinitions() {
		if strings.Contains(d.Name, "hidden") {
			t.Fatalf("internal tool offered as a definition: %s", d.Name)
		}
	}
	sys := BuildSystemPromptWithSystem(SystemPromptOptions{Role: RoleGeneral, Tools: reg.List(), Mode: policy.ModeEdit})
	if strings.Contains(sys.Content, "hidden.tool") {
		t.Fatalf("internal tool listed in the envelope prompt:\n%s", sys.Content)
	}
	if _, ok := reg.Lookup("hidden.tool"); !ok {
		t.Fatal("internal tool must stay dispatchable")
	}
}

// --- Task 8: model operations are registered once per logical call ---------

// TestModelOperationIsRegisteredOncePerLogicalCall pins the scope of the
// steering operation: one per logical model call, not one per transport
// attempt. Retries, the reconnect wait and the backoff all run beneath the
// same operation, so an interruption cancels the call the user is waiting on
// rather than one attempt of it whose siblings keep going.
func TestModelOperationIsRegisteredOncePerLogicalCall(t *testing.T) {
	f := newSteeringModelFixture(t)
	p := &agenttest.ScriptedProvider{
		Errs:      []error{errors.New("connection reset"), errors.New("timeout"), nil},
		Responses: []string{"", "", okAnswer},
	}
	r := f.runner(t, p)
	r.MaxRetries = 2
	r.Sleep = func(context.Context, time.Duration) error { return nil }

	seen := make(chan string, 4)
	p.OnChat = func(int, schema.ChatRequest) {
		snap := f.state.SteeringSnapshot()
		seen <- snap.OperationID
	}
	if _, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("chatWithRetry: %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("provider calls = %d, want 3", p.Calls)
	}
	close(seen)
	first := ""
	for id := range seen {
		if first == "" {
			first = id
			if first == "" {
				t.Fatal("no operation was open while the request was dispatched")
			}
			continue
		}
		if id != first {
			t.Fatalf("attempt used operation %q, want the same %q as the first — a retry must not replace the logical call's operation", id, first)
		}
	}
	// The operation is joined once the logical call ends.
	if snap := f.state.SteeringSnapshot(); snap.OperationID != "" {
		t.Fatalf("operation %q still open after chatWithRetry returned", snap.OperationID)
	}
}

// TestModelOperationReusesNestedOperation pins the nesting rule: a logical call
// whose context already carries this turn's operation — a finalize/handoff
// request made under a setup scope — runs inside it instead of trying to open a
// second simultaneous operation, which the state would refuse.
func TestModelOperationReusesNestedOperation(t *testing.T) {
	f := newSteeringModelFixture(t)
	outerCtx, outerRef, err := f.state.BeginSteeringOperation(context.Background(), f.turnID, session.OperationSetup)
	if err != nil {
		t.Fatalf("BeginSteeringOperation: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{okAnswer}}
	r := f.runner(t, p)
	var ranWithOperation bool
	_, err = r.withModelOperation(outerCtx, func(ctx context.Context) (chatResult, error) {
		ref, ok := session.OperationFromContext(ctx)
		ranWithOperation = ok && ref == outerRef
		// The nested call must not have opened a second operation.
		if snap := f.state.SteeringSnapshot(); snap.OperationID != outerRef.ID {
			t.Errorf("nested call replaced the open operation: snapshot = %q, want %q", snap.OperationID, outerRef.ID)
		}
		return chatResult{Text: "ok"}, nil
	})
	if err != nil {
		t.Fatalf("withModelOperation: %v", err)
	}
	if !ranWithOperation {
		t.Fatal("the nested call ran without the enclosing operation's context")
	}
	// The enclosing scope still owns the operation: the nested call must not
	// have joined it early.
	if snap := f.state.SteeringSnapshot(); snap.OperationID != outerRef.ID {
		t.Fatalf("operation = %q after the nested call, want the enclosing %q still open", snap.OperationID, outerRef.ID)
	}
	f.state.FinishSteeringOperation(outerRef, nil)
}

// TestModelOperationAbsentTurnLeavesBehaviourUnchanged pins the default: a
// runner with no eligible turn (every existing construction path today) runs
// the call untouched and registers nothing, so nothing about today's behaviour
// changes until a turn opts in.
func TestModelOperationAbsentTurnLeavesBehaviourUnchanged(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{Responses: []string{okAnswer}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")

	reqs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}
	res, err := r.chatWithRetry(context.Background(), p, "test-model", reqs, nil)
	if err != nil {
		t.Fatalf("chatWithRetry: %v", err)
	}
	if res.Text != okAnswer {
		t.Fatalf("text = %q, want the scripted answer", res.Text)
	}
	if snap := state.SteeringSnapshot(); snap.Active || snap.OperationID != "" {
		t.Fatalf("a runner with no eligible turn registered steering state: %+v", snap)
	}
}

// TestModelOperationRefusesToStrandAcceptedSteering pins the one case the
// operation scope must not turn into a hard failure: an accepted message that
// is waiting to be delivered. BeginSteeringOperation refuses a new operation
// while accepted steering is pending, but the refused call IS the delivery — so
// it runs without an operation instead of returning an error that would leave
// the user's text undeliverable forever.
func TestModelOperationRefusesToStrandAcceptedSteering(t *testing.T) {
	f := newSteeringModelFixture(t)
	if _, err := f.state.SubmitSendNow(session.SendNowRequest{
		TurnID: f.turnID, RequestID: "r1", Text: "deliver me",
	}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	if _, err := f.state.StageSteering(f.turnID); err != nil {
		t.Fatalf("StageSteering: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{okAnswer}}
	r := f.runner(t, p)
	res, err := r.chatWithRetry(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chatWithRetry = %v, want the delivery request to run", err)
	}
	if res.Text != okAnswer {
		t.Fatalf("text = %q, want the scripted answer", res.Text)
	}
	// Running it delivered the pending text, which is the whole point.
	if n := countSteeringMessages(f.state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want the pending message delivered", n)
	}
}
