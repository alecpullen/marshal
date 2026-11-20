package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFactoryWaitsForIntegrationAwareContinuation pins the factory's part in the
// parent's integration decision: the existing waiting-delegated stage holds the
// turn until its children settle, the continuation asks the same worker for a
// fresh report for the original operation and the same candidate, and only that
// report completes the effect. The factory never merges a child branch itself —
// it asks the parent to account for what the children established.
func TestFactoryWaitsForIntegrationAwareContinuation(t *testing.T) {
	f := newContinuationFixture(t, "s1")
	// The turn wrote a report before its child settled: that message is on the
	// transcript, and it names the effect's operation.
	f.transcript(0,
		delegatedTurnStack(t, "turn:1", []int64{7}, delegatedReport("op1")),
		delegatedStatus(t, true, delegatedChild(7, "running")))

	// A child still running is work the turn waits on: no continuation is sent,
	// and the effect keeps the dispatch it was given.
	f.tick()
	if prompts := f.frames(0, "session/prompt"); len(prompts) != 0 {
		t.Fatalf("a continuation was sent while the child was running: %v", prompts)
	}
	waiting := f.current()
	last := waiting.Effects[len(waiting.Effects)-1]
	if last.Status != "dispatched" || last.Outcome != nil {
		t.Fatalf("a running child let the effect settle: %+v", last)
	}
	if last.Delegated == nil || last.Delegated.State != factoryDelegatedWaiting {
		t.Fatalf("the turn was not waiting on its child: %+v", last.Delegated)
	}
	if last.ExpectedBase != "base" {
		t.Fatalf("the waiting effect changed its candidate: %+v", last)
	}

	// The child settles. The continuation is journalled — prepared, then
	// dispatched — before the prompt, and it asks the parent for a fresh report
	// naming the same operation and candidate.
	f.children(0, delegatedStatus(t, true, delegatedChild(7, "done")))
	f.answer(0, func(_ int, id string) json.RawMessage {
		return delegatedTurnStack(t, "turn:2", nil, continuationReport(id, "Which candidate should I integrate first?"))
	})
	f.tick()

	prompts := f.prompted()
	if len(prompts) != 1 {
		t.Fatalf("continuation prompts = %d, want exactly one", len(prompts))
	}
	if prompts[0].State != factoryDelegatedDispatched || prompts[0].ContinuationID == "" {
		t.Fatalf("the prompt was sent before its dispatched record existed: %+v", prompts[0])
	}
	params := f.frames(0, "session/prompt")
	if len(params) != 1 {
		t.Fatalf("session/prompt frames = %v, want one", params)
	}
	// The prompt tells the parent what settled and asks it to account for it —
	// the evidence the parent's explicit integration decision is made from.
	for _, want := range []string{
		"Continuation ID: " + prompts[0].ContinuationID,
		"Child 7",
		"has finished",
		"Read each child's report",
		"Assess every child that failed",
		"the original operation op1",
	} {
		if !strings.Contains(params[0], want) {
			t.Fatalf("the continuation prompt lacks %q:\n%s", want, params[0])
		}
	}
	// The factory asks; it does not merge. Its instruction explicitly forbids
	// the worker from pushing or merging on its behalf.
	if !strings.Contains(params[0], "do not push or merge") {
		t.Fatalf("the continuation let the worker merge for the factory:\n%s", params[0])
	}

	// Only the fresh report is the effect's result: the message written before
	// the child settled never is.
	saved := f.current()
	last = saved.Effects[len(saved.Effects)-1]
	if last.Status != "completed" || last.Outcome == nil || last.Outcome.Report == nil {
		t.Fatalf("the fresh report was not accepted: %+v", last)
	}
	if last.Outcome.Report.Question != "Which candidate should I integrate first?" {
		t.Fatalf("the effect took the pre-settlement message as its result: %+v", last.Outcome.Report)
	}
	// The candidate and worker the continuation asked about are the effect's,
	// unchanged by the extra turn.
	if last.ExpectedBase != "base" {
		t.Fatalf("the continuation moved the effect's base: %+v", last)
	}
	if last.Worker == nil || last.Worker.SessionID != "s1" {
		t.Fatalf("the continuation changed the effect's worker: %+v", last.Worker)
	}

	// The journal records the order the prompt followed, so a restart cannot
	// send a second continuation for the same settled children.
	prepared, dispatched := false, false
	for _, event := range saved.History {
		switch event.Kind {
		case "continuation_prepared":
			prepared = true
			if dispatched {
				t.Fatal("the continuation was dispatched before it was prepared")
			}
		case "continuation_dispatched":
			dispatched = true
		}
	}
	if !prepared || !dispatched {
		t.Fatalf("continuation events = %+v, want prepared then dispatched", saved.History)
	}
}
