package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
)

// TestSubagentReportBindingUsesExactPersistedMessage pins the report-link
// contract: the index records the persisted report message's DBID (never the
// transient in-memory id), the link is keyed to the exact report text, and a
// report that cannot be matched under an enabled persistence is explicitly
// unavailable rather than fabricated.
func TestSubagentReportBindingUsesExactPersistedMessage(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	v := s.RegisterSubagent("worker", nil)

	report := fmt.Sprintf("[subagent %d finished] two findings", v.ID)
	s.AddMessage(RoleUser, report, ContentTypeSubagentReport)

	// Find the persisted row id of the appended message.
	var dbid int64
	for _, m := range s.Messages() {
		if m.ContentType == ContentTypeSubagentReport && m.Content == report {
			dbid = m.DBID
		}
	}
	if dbid == 0 {
		t.Fatal("the persisted report message has no DBID")
	}

	if err := s.BindSubagentReport(v.ID, report); err != nil {
		t.Fatalf("BindSubagentReport: %v", err)
	}
	got, ok := s.Subagent(v.ID)
	if !ok {
		t.Fatal("subagent vanished")
	}
	if got.Report.MessageID != dbid {
		t.Fatalf("report reference = %d, want persisted DBID %d", got.Report.MessageID, dbid)
	}
	entry, ok := agentResultByKey(s.WorkView(), v.WorkKey)
	if !ok || entry.Reference.MessageID != dbid {
		t.Fatalf("work index reference = %+v, want DBID %d", entry.Reference, dbid)
	}

	// The link is keyed to the exact report text: an unmatched body under an
	// enabled persistence is explicitly unavailable and returns an error, and
	// the earlier good link is not clobbered.
	if err := s.BindSubagentReport(v.ID, report+" (different)"); err == nil {
		t.Fatal("binding an unmatched report body claimed a durable link")
	}
	after, _ := s.Subagent(v.ID)
	if after.Report.MessageID != dbid {
		t.Fatalf("failed bind clobbered the reference: %d, want %d", after.Report.MessageID, dbid)
	}

	// With persistence off there is no durable store to link to; nothing is
	// claimed and no error is raised.
	plain := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	pv := plain.RegisterSubagent("worker", nil)
	preport := fmt.Sprintf("[subagent %d finished] in-memory only", pv.ID)
	plain.AddMessage(RoleUser, preport, ContentTypeSubagentReport)
	if err := plain.BindSubagentReport(pv.ID, preport); err != nil {
		t.Fatalf("BindSubagentReport without persistence: %v", err)
	}
	pgot, _ := plain.Subagent(pv.ID)
	if pgot.Report.MessageID != 0 {
		t.Fatalf("unavailable report reference = %d, want 0", pgot.Report.MessageID)
	}
}

// TestSubagentWorkHistorySurvivesChildGC pins the retention trade-off: the
// drill-down Child pointer is capped by maxRetainedChildStates, but the
// lightweight work-index entry — key, epoch, summary preview, terminal state —
// survives for every child, so history is never lost to garbage collection.
func TestSubagentWorkHistorySurvivesChildGC(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	const n = maxRetainedChildStates + 2
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
		v := s.RegisterSubagent(fmt.Sprintf("task %d", i), child)
		ids = append(ids, v.ID)
		s.FinishSubagent(v.ID, fmt.Sprintf("summary %d", i), nil)
	}

	for i, id := range ids {
		got, ok := s.Subagent(id)
		if !ok {
			t.Fatalf("subagent %d not found", id)
		}
		if got.WorkKey == "" || got.Summary == "" || got.Outcome != "completed" {
			t.Fatalf("work facts lost for %d: %+v", id, got)
		}
		wantGC := i < n-maxRetainedChildStates
		if wantGC && got.Child != nil {
			t.Fatalf("child %d kept beyond the retention cap", id)
		}
		if !wantGC && got.Child == nil {
			t.Fatalf("recent child %d was GC'd too early", id)
		}
	}

	if got := len(s.WorkView().AgentResults); got != n {
		t.Fatalf("work index has %d agent results, want %d (one per child)", got, n)
	}
}

func TestRegisterSubagentWithMeta(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	meta := SubagentMeta{
		Role:     routing.RoleImplementer,
		Provider: "ollama",
		Model:    "qwen2.5-coder",
		Fallback: true,
	}
	v := state.RegisterSubagentWithMeta("task 1", child, meta)
	if v.Role != routing.RoleImplementer {
		t.Errorf("Role = %q, want %q", v.Role, routing.RoleImplementer)
	}
	if v.Model != "qwen2.5-coder" {
		t.Errorf("Model = %q, want qwen2.5-coder", v.Model)
	}
	if !v.Fallback {
		t.Error("Fallback = false, want true")
	}
}

func TestAddSubagentTokensUpdatesView(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	state.AddSubagentTokens(v.ID, 150)
	got, ok := state.Subagent(v.ID)
	if !ok {
		t.Fatal("subagent not found")
	}
	if got.TokensUsed != 150 {
		t.Fatalf("TokensUsed = %d, want 150", got.TokensUsed)
	}
	state.AddSubagentTokens(v.ID, 75)
	got, _ = state.Subagent(v.ID)
	if got.TokensUsed != 225 {
		t.Fatalf("TokensUsed = %d, want 225", got.TokensUsed)
	}
}

func TestRegisterSubagentDefaultsMeta(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	if v.Fallback {
		t.Error("Fallback should default to false")
	}
}

func TestCancelSubagentInvokesStoredFunc(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)

	called := false
	state.SetSubagentCancel(v.ID, func() { called = true })
	if !state.CancelSubagent(v.ID) {
		t.Fatal("CancelSubagent returned false for running subagent with cancel")
	}
	if !called {
		t.Fatal("cancel function was not called")
	}
}

func TestCancelSubagentReturnsFalseForFinished(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	state.FinishSubagent(v.ID, "done", nil)

	called := false
	state.SetSubagentCancel(v.ID, func() { called = true })
	if state.CancelSubagent(v.ID) {
		t.Fatal("CancelSubagent returned true for finished subagent")
	}
	if called {
		t.Fatal("cancel function should not have been called for finished subagent")
	}
}

func TestCancelSubagentReturnsFalseForUnknown(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	if state.CancelSubagent(999) {
		t.Fatal("CancelSubagent returned true for unknown ID")
	}
}

func TestRegisterSubagentWithMetaSingleEvent(t *testing.T) {
	b := pubsub.NewBroker[SubagentEvent]()
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	state.SetSubagentBroker(b)
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)

	state.RegisterSubagentWithMeta("worker", child, SubagentMeta{
		Role:     routing.RoleImplementer,
		Provider: "test",
		Model:    "test-model",
	})

	// Drain with a short deadline; expect exactly one event carrying meta.
	var events []SubagentEvent
	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-ch:
			events = append(events, ev.Payload)
		case <-deadline:
			break drain
		}
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1", len(events))
	}
	if events[0].View.Role != routing.RoleImplementer {
		t.Errorf("first event Role = %q, want %q (no empty first event)", events[0].View.Role, routing.RoleImplementer)
	}
	if events[0].View.Provider != "test" || events[0].View.Model != "test-model" {
		t.Errorf("first event Provider/Model = %q/%q, want test/test-model", events[0].View.Provider, events[0].View.Model)
	}
}
