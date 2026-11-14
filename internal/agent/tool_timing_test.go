package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/hooks"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
	"marshal/internal/pubsub"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type timingSnapshot struct{ advance func(time.Duration) }

func (s timingSnapshot) Track(context.Context) (string, error) {
	s.advance(25 * time.Millisecond)
	return "snapshot", nil
}
func (timingSnapshot) Diff(context.Context, string) (string, error) { return "", nil }
func (timingSnapshot) Restore(context.Context, string) error        { return nil }

type timingGate struct{ advance func(time.Duration) }

func (g timingGate) Acquire() func() {
	g.advance(40 * time.Millisecond)
	return func() { g.advance(5 * time.Millisecond) }
}

type timingCanceledGate struct{ advance func(time.Duration) }

func (g timingCanceledGate) Acquire() func() { panic("must use context-aware gate") }
func (g timingCanceledGate) AcquireContext(context.Context) (func(), error) {
	g.advance(41 * time.Millisecond)
	return nil, context.Canceled
}

func TestToolTimingCanceledGateHasNoHandlerDuration(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(100, 0)
	r := timingRunner(t, &buf, registry.Tool{Name: "demo.write", Risk: registry.RiskWorkspaceWrite, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		t.Error("unadmitted handler ran")
		return registry.ToolResult{}, nil
	}})
	r.Now = func() time.Time { return now }
	r.WriteGate = timingCanceledGate{func(d time.Duration) { now = now.Add(d) }}
	if _, err := r.executeToolCall(context.Background(), ModelAction{Type: ActionToolCall, Tool: "demo.write", Args: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rows := timingRecords(t, &buf)
	if len(rows) != 1 || rows[0]["total_ns"] != float64(41*time.Millisecond) || rows[0]["workspace_gate_ns"] != float64(41*time.Millisecond) {
		t.Fatalf("canceled gate wait missing: %v", rows)
	}
	if _, exists := rows[0]["handler_ns"]; exists {
		t.Fatal("canceled gate recorded an unexecuted handler")
	}
}

func timingRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row["msg"] == "tool timing" {
			records = append(records, row)
		}
	}
	return records
}

func timingRunner(t *testing.T, buf *bytes.Buffer, tool registry.Tool) *Runner {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{Logger: slog.New(slog.NewJSONHandler(buf, nil))})
	state.SetTrusted(true)
	reg := registry.New()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	return NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
}

func TestToolTimingIncludesPreflightAndCleanup(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(100, 0)
	advance := func(d time.Duration) { now = now.Add(d) }
	r := timingRunner(t, &buf, registry.Tool{Name: "demo.write", Risk: registry.RiskWorkspaceWrite, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		advance(3 * time.Millisecond)
		return registry.ToolResult{Summary: "ok"}, nil
	}})
	r.Now = func() time.Time { return now }
	r.Snapshotter, r.WriteGate = timingSnapshot{advance}, timingGate{advance}
	if _, err := r.executeToolCall(context.Background(), ModelAction{Type: ActionToolCall, Tool: "demo.write", Args: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rows := timingRecords(t, &buf)
	if len(rows) != 1 {
		t.Fatalf("got %d timing records, want 1: %s", len(rows), &buf)
	}
	for key, want := range map[string]time.Duration{"total_ns": 73 * time.Millisecond, "snapshot_ns": 25 * time.Millisecond, "workspace_gate_ns": 40 * time.Millisecond, "handler_ns": 3 * time.Millisecond} {
		if rows[0][key] != float64(want) {
			t.Errorf("%s = %v, want %d", key, rows[0][key], want)
		}
	}
}

func TestToolTimingNoHandlerPathsAndMeasuredZero(t *testing.T) {
	for _, tc := range []string{"unknown", "invalid", "held_cancel", "denied", "hook_blocked", "handler_error", "zero", "cached"} {
		t.Run(tc, func(t *testing.T) {
			var buf bytes.Buffer
			calls := 0
			r := timingRunner(t, &buf, registry.Tool{Name: "demo.read", Risk: registry.RiskReadOnly, Cacheable: tc == "cached", Schema: []byte(`{"type":"object"}`), Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
				calls++
				if tc == "handler_error" {
					return registry.ToolResult{}, errors.New("failure")
				}
				return registry.ToolResult{Summary: "ok", Content: "value"}, nil
			}})
			r.Now = func() time.Time { return time.Unix(100, 0) }
			action := ModelAction{Type: ActionToolCall, Tool: "demo.read", Args: []byte(`{}`)}
			ctx := context.Background()
			switch tc {
			case "denied":
				r.Policy.SetRules([]permissions.Rule{{Permission: "demo.read", Pattern: "*", Action: permissions.ActionDeny}})
			case "hook_blocked":
				r.HookRunner = fakeHookRunner{preOut: hooks.Output{Decision: hooks.DecisionBlock}}
			case "unknown":
				action.Tool = "unregistered-secret"
			case "invalid":
				action.Args = []byte(`[]`)
			case "held_cancel":
				r.State.SetHold(true)
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "cached":
				if _, err := r.executeToolCall(ctx, action); err != nil {
					t.Fatal(err)
				}
				buf.Reset()
			}
			_, err := r.executeToolCall(ctx, action)
			if tc == "held_cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
			rows := timingRecords(t, &buf)
			if len(rows) != 1 {
				t.Fatalf("got %d timing records, want 1: %s", len(rows), &buf)
			}
			row := rows[0]
			wantHandler := tc == "zero" || tc == "handler_error"
			value, ran := row["handler_ns"]
			if ran != wantHandler || ran && value != float64(0) {
				t.Fatalf("handler_ns=%v present=%v, want present=%v with measured zero", value, ran, wantHandler)
			}
			if tc == "cached" && (row["cache_hit"] != true || calls != 1) {
				t.Fatalf("cached call ran handler: record=%v calls=%d", row, calls)
			}
			if tc == "unknown" && bytes.Contains(buf.Bytes(), []byte("unregistered-secret")) {
				t.Fatal("timing logged unregistered input")
			}
		})
	}
}

type timingWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *timingWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestToolTimingIncludesHoldWait(t *testing.T) {
	var buf bytes.Buffer
	r := timingRunner(t, &buf, registry.Tool{Name: "demo.read", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		t.Error("held handler ran")
		return registry.ToolResult{}, nil
	}})
	var elapsed atomic.Int64
	r.Now = func() time.Time { return time.Unix(100, elapsed.Load()) }
	r.State.SetHold(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx := &timingWaitContext{Context: ctx, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := r.executeToolCall(waitCtx, ModelAction{Type: ActionToolCall, Tool: "demo.read"})
		done <- err
	}()
	select {
	case <-waitCtx.entered:
	case <-time.After(time.Second):
		t.Fatal("tool did not wait on hold")
	}
	elapsed.Store(int64(37 * time.Millisecond))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("held call did not stop")
	}
	rows := timingRecords(t, &buf)
	if len(rows) != 1 || rows[0]["total_ns"] != float64(37*time.Millisecond) || rows[0]["hold_ns"] != float64(37*time.Millisecond) {
		t.Fatalf("hold missing from timing: %v", rows)
	}
}

func TestToolTimingIncludesApprovalWait(t *testing.T) {
	var buf bytes.Buffer
	r := timingRunner(t, &buf, registry.Tool{Name: "web.fetch", Risk: registry.RiskNetwork, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		t.Error("denied handler ran")
		return registry.ToolResult{}, nil
	}})
	r.Policy.SetRules([]permissions.Rule{{Permission: "web.fetch", Pattern: "*", Action: permissions.ActionAsk}})
	var elapsed atomic.Int64
	r.Now = func() time.Time { return time.Unix(100, elapsed.Load()) }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	broker := pubsub.NewBroker[session.Event]()
	events := broker.Subscribe(ctx)
	r.State.SetEventBroker(broker)
	done := make(chan error, 1)
	go func() {
		_, err := r.executeToolCall(ctx, ModelAction{Type: ActionToolCall, Tool: "web.fetch", Args: []byte(`{"url":"https://fixture.invalid"}`)})
		done <- err
	}()
	for {
		select {
		case ev := <-events:
			if ev.Type != session.EventPendingApprovalChanged || ev.Payload.PendingApproval == nil {
				continue
			}
			elapsed.Store(int64(61 * time.Millisecond))
			r.State.PendingApproval().Respond(session.UserApprovalDecision{Approved: false})
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("approval did not settle")
			}
			rows := timingRecords(t, &buf)
			if len(rows) != 1 || rows[0]["total_ns"] != float64(61*time.Millisecond) || rows[0]["policy_approval_ns"] != float64(61*time.Millisecond) {
				t.Fatalf("approval missing from timing: %v", rows)
			}
			return
		case <-ctx.Done():
			t.Fatal("approval did not appear")
		}
	}
}

func TestNativeQuestionTimingIncludesRejectedInvocation(t *testing.T) {
	var buf bytes.Buffer
	r := timingRunner(t, &buf, registry.Tool{Name: "demo.read", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}})
	r.Now = func() time.Time { return time.Unix(100, 0) }
	if _, err := r.executeNativeQuestionAsk(context.Background(), schema.ToolCall{ID: "q1", Name: "question.ask", Args: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rows := timingRecords(t, &buf)
	if len(rows) != 1 || rows[0]["tool"] != "question.ask" || rows[0]["total_ns"] != float64(0) {
		t.Fatalf("native question timing missing: %v", rows)
	}
	if _, exists := rows[0]["handler_ns"]; exists {
		t.Fatal("rejected question recorded a handler duration")
	}
}

func TestNativeQuestionTimingIncludesHumanWait(t *testing.T) {
	for _, cancelQuestion := range []bool{false, true} {
		t.Run(map[bool]string{false: "answered", true: "canceled"}[cancelQuestion], func(t *testing.T) {
			var buf bytes.Buffer
			r := timingRunner(t, &buf, registry.Tool{Name: "demo.read", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
				return registry.ToolResult{}, nil
			}})
			var elapsed atomic.Int64
			r.Now = func() time.Time { return time.Unix(100, elapsed.Load()) }
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			broker := pubsub.NewBroker[session.Event]()
			events := broker.Subscribe(ctx)
			r.State.SetEventBroker(broker)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = r.executeNativeQuestionAsk(ctx, schema.ToolCall{ID: "q1", Name: "question.ask", Args: []byte(`{"questions":[{"question":"Which option?"}]}`)})
			}()
			waiting := false
			for !waiting {
				select {
				case ev := <-events:
					if ev.Type == session.EventPendingQuestionChanged && ev.Payload.PendingQuestion != nil {
						waiting = true
					}
				case <-ctx.Done():
					t.Fatal("question did not appear")
				}
			}
			elapsed.Store(int64(78 * time.Millisecond))
			if cancelQuestion {
				cancel()
			} else {
				r.State.PendingQuestion().Respond([]session.Answer{{Question: "Which option?", Answer: "A"}})
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("question did not settle")
			}
			rows := timingRecords(t, &buf)
			if len(rows) != 1 || rows[0]["total_ns"] != float64(78*time.Millisecond) || rows[0]["question_wait_ns"] != float64(78*time.Millisecond) {
				t.Fatalf("question wait missing from timing: %v", rows)
			}
		})
	}
}

type timingTitle func()

func (f timingTitle) OnUserTurn(context.Context, string) { f() }

func TestTurnTimingIncludesStartup(t *testing.T) {
	r := NewRunner(&agenttest.ScriptedProvider{Responses: []string{`{"action":{"type":"final","content":"hello"}}`}}, registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test")
	r.SetForceClass(string(ClassQuestion))
	now := time.Unix(100, 0)
	r.Now = func() time.Time { return now }
	r.TitleManager = timingTitle(func() { now = now.Add(11 * time.Millisecond) })
	r.Snapshotter = timingSnapshot{func(d time.Duration) { now = now.Add(d) }}
	m := captureMetrics(r)
	if _, err := r.RunTask(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if m.DurationMs != 36 || !m.StartedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("startup missing from turn timing: %+v", m)
	}
}

func TestTurnTimingEarlyStopResetsMetrics(t *testing.T) {
	state := newTestState(t)
	state.SetTurnStopService(&fakeStopService{})
	if _, err := state.RequestTurnStop("before start"); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test")
	r.ExecutionControl = NewSessionExecutionControl(state)
	r.stats = &turnStats{m: TurnMetrics{ToolCalls: 99}}
	m := captureMetrics(r)
	if _, err := r.RunTask(context.Background(), "stopped"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if m.ToolCalls != 0 || m.Goal != "stopped" || m.StartedAt.IsZero() {
		t.Fatalf("stopped turn reused prior metrics: %+v", m)
	}
}
