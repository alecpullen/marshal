package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// completionItem creates an approved item in s. The approval is what a
// completion record copies its criteria from, and it is written through the
// store so the item validates like any other.
func completionItem(t *testing.T, s *FactoryStore, now time.Time) FactoryItem {
	t.Helper()
	i, err := s.Create(factoryReq(), now)
	if err != nil {
		t.Fatal(err)
	}
	i, err = s.Update(i.ID, "approve", i.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		req := factoryReq()
		req.Config.TargetBranch = "main"
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}
		dst.State = "queued"
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// completionController is a controller over s whose clock is fixed. It never
// ticks: these tests assert what a command journals, not what a loop does.
func completionController(s *FactoryStore, now time.Time) *FactoryController {
	return NewFactoryController(s, &loopEffects{store: s}, func() time.Time { return now })
}

func completionCommand(t *testing.T, c *FactoryController, itemID, requestID, kind string, expectedVersion uint64) FactoryItem {
	t.Helper()
	i, err := c.Command(context.Background(), itemID, FactoryCommand{RequestID: requestID, Kind: kind, ExpectedVersion: expectedVersion})
	if err != nil {
		t.Fatalf("%s %s: %v", kind, requestID, err)
	}
	return i
}

func reopenCompletionStore(t *testing.T, s *FactoryStore, dir string) *FactoryStore {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

// TestCompletionLegacyReplay covers an item journalled before completion
// existed, and one whose record was written with no status: both are open,
// both replay through the journal unchanged, and the first completion
// afterwards snapshots the goal and the approved criteria.
func TestCompletionLegacyReplay(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	s, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	i := completionItem(t, s, now)
	if i.Completion != nil || factoryCompletionStatus(i) != "open" {
		t.Fatalf("legacy item: %+v", i.Completion)
	}
	// A record carrying no status is open as well, and the store keeps it
	// exactly as it was written: "open" is a reading, never a rewrite.
	legacy, err := s.Update(i.ID, "legacy-completion", i.Version, json.RawMessage(`{"legacy":true}`), func(dst *FactoryItem) error {
		dst.Completion = &FactoryCompletion{Links: []FactoryPRLink{{RepoID: "repo", PR: FactoryPR{Number: 7, State: "open"}, Origin: "user"}}}
		return nil
	}, now)
	if err != nil {
		t.Fatalf("record without a status: %v", err)
	}
	if legacy.Completion == nil || legacy.Completion.Status != "" || factoryCompletionStatus(legacy) != "open" {
		t.Fatalf("empty status was rewritten: %+v", legacy.Completion)
	}
	// Both forms replay: the journal validates and never fills a field in.
	s = reopenCompletionStore(t, s, dir)
	replayed, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Completion == nil || replayed.Completion.Status != "" || len(replayed.Completion.Records) != 0 || factoryCompletionStatus(replayed) != "open" {
		t.Fatalf("replayed legacy record: %+v", replayed.Completion)
	}
	completed := completionCommand(t, completionController(s, now), i.ID, "complete-1", "complete", replayed.Version)
	if factoryCompletionStatus(completed) != "complete" || len(completed.Completion.Records) != 1 {
		t.Fatalf("completion: %+v", completed.Completion)
	}
	record := completed.Completion.Records[0]
	if record.ID != "completion-"+completed.ID+"-1" || !record.At.Equal(now) || record.Actor != "studio" {
		t.Fatalf("record identity: %+v", record)
	}
	if record.Goal != completed.Title || record.Criteria != "spec" {
		t.Fatalf("record snapshot: %+v", record)
	}
	// The record copies the evidence the item already carried, and the item
	// keeps it: a snapshot is a copy, not a move.
	if len(record.Links) != 1 || record.Links[0].PR.Number != 7 || record.Links[0].Origin != "user" || record.Assessment != nil {
		t.Fatalf("record evidence: %+v", record)
	}
	if len(completed.Completion.Links) != 1 {
		t.Fatalf("item evidence was consumed: %+v", completed.Completion)
	}
	// The completion, and its replay identity, are durable.
	s = reopenCompletionStore(t, s, dir)
	persisted, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if factoryCompletionStatus(persisted) != "complete" || len(persisted.Completion.Records) != 1 || persisted.Completion.Records[0].ID != record.ID {
		t.Fatalf("completion did not survive the reopen: %+v", persisted.Completion)
	}
	again := completionCommand(t, completionController(s, now), i.ID, "complete-1", "complete", replayed.Version)
	if again.Version != persisted.Version || len(again.Completion.Records) != 1 || len(again.History) != len(persisted.History) {
		t.Fatalf("replay appended a second completion: %+v", again)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCompletionWithOpenPRs covers a completion while the run is still going, a
// PR is open and no assessment exists, and the evidence an item does carry
// being snapshotted rather than required.
func TestCompletionWithOpenPRs(t *testing.T) {
	now := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	i, err = s.Update(i.ID, "running", i.Version, json.RawMessage(`{"running":true}`), func(dst *FactoryItem) error {
		dst.State = "checking"
		dst.Branch = "marshal/factory/item"
		dst.PRs = []FactoryPR{{Number: 3, NodeID: "node", HeadRef: dst.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "head", BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{ID: "implement-1", Kind: "implement", Status: "completed"}}
		dst.Workers = []FactoryWorker{{AgentID: "agent", SessionID: "session", Role: "implementer"}}
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	completed := completionCommand(t, completionController(s, now), i.ID, "complete-open-prs", "complete", i.Version)
	if factoryCompletionStatus(completed) != "complete" {
		t.Fatalf("completion with open PRs: %+v", completed.Completion)
	}
	// Completion is a statement about the work, not about the run: the state,
	// the open PR, the effect and the worker are all as they were.
	if completed.State != "checking" || len(completed.PRs) != 1 || completed.PRs[0].State != "open" || !completed.PRs[0].Draft {
		t.Fatalf("completion moved execution state: %+v", completed)
	}
	if len(completed.Effects) != 1 || len(completed.Workers) != 1 || len(completed.Grants) != 0 {
		t.Fatalf("completion spawned or stopped work: %+v", completed)
	}
	if record := completed.Completion.Records[0]; record.Assessment != nil || len(record.Links) != 0 {
		t.Fatalf("unknown evidence was invented: %+v", record)
	}
	// Evidence the item does carry is copied into the next record.
	evidence, err := s.Update(i.ID, "evidence", completed.Version, json.RawMessage(`{"evidence":true}`), func(dst *FactoryItem) error {
		dst.Completion.Assessment = &FactoryAssessment{Status: "available", Summary: "every task accounted for", Source: "assessor", InputDigest: "digest", ObservedAt: now}
		dst.Completion.Links = []FactoryPRLink{{RepoID: "repo", PR: dst.PRs[0], Origin: "factory", ObservedAt: now}}
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	c := completionController(s, now)
	reopened := completionCommand(t, c, i.ID, "reopen-evidence", "reopen", evidence.Version)
	second := completionCommand(t, c, i.ID, "complete-evidence", "complete", reopened.Version)
	record := second.Completion.Records[len(second.Completion.Records)-1]
	if record.Assessment == nil || record.Assessment.Status != "available" || record.Assessment.Summary != "every task accounted for" || len(record.Links) != 1 || record.Links[0].Origin != "factory" {
		t.Fatalf("evidence was not snapshotted: %+v", record)
	}
	if second.Completion.Assessment == nil || len(second.Completion.Links) != 1 {
		t.Fatalf("the snapshot consumed the item's evidence: %+v", second.Completion)
	}
	if record.Assessment == second.Completion.Assessment || &record.Links[0] == &second.Completion.Links[0] {
		t.Fatal("the record shares the item's live evidence")
	}
}

// TestCompletionReopenPreservesHistory covers reopening: it retires the
// decision, keeps every record and every effect and worker exactly as they
// were, and the completion that follows appends instead of replacing.
func TestCompletionReopenPreservesHistory(t *testing.T) {
	now := time.Date(2026, 5, 3, 8, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	c := completionController(s, now)
	completed := completionCommand(t, c, i.ID, "complete-1", "complete", i.Version)
	firstRecord, err := json.Marshal(completed.Completion.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	records, _ := json.Marshal(completed.Completion.Records)
	effects, workers, grants, history := len(completed.Effects), len(completed.Workers), len(completed.Grants), len(completed.History)
	if completed.History[len(completed.History)-1].Kind != "completion_completed" {
		t.Fatalf("completion event: %+v", completed.History)
	}
	reopened := completionCommand(t, c, i.ID, "reopen-1", "reopen", completed.Version)
	if factoryCompletionStatus(reopened) != "open" {
		t.Fatalf("reopen left the item %q", factoryCompletionStatus(reopened))
	}
	after, _ := json.Marshal(reopened.Completion.Records)
	if string(after) != string(records) {
		t.Fatalf("reopen rewrote the records: %s -> %s", records, after)
	}
	if reopened.State != completed.State {
		t.Fatalf("reopen moved execution state: %s -> %s", completed.State, reopened.State)
	}
	if len(reopened.Effects) != effects || len(reopened.Workers) != workers || len(reopened.Grants) != grants {
		t.Fatalf("reopen spawned or stopped work: %+v", reopened)
	}
	if len(reopened.History) != history+1 || reopened.History[len(reopened.History)-1].Kind != "completion_reopened" {
		t.Fatalf("reopen event: %+v", reopened.History)
	}
	// Completing again appends a second record and leaves the first alone.
	second := completionCommand(t, c, i.ID, "complete-2", "complete", reopened.Version)
	if len(second.Completion.Records) != 2 || second.Completion.Records[1].ID != "completion-"+i.ID+"-2" {
		t.Fatalf("second completion: %+v", second.Completion.Records)
	}
	if kept, _ := json.Marshal(second.Completion.Records[0]); string(kept) != string(firstRecord) {
		t.Fatalf("the first record was rewritten: %s -> %s", firstRecord, kept)
	}
	if len(second.Effects) != effects || len(second.Workers) != workers || len(second.Grants) != grants {
		t.Fatalf("the second completion spawned or stopped work: %+v", second)
	}
}

// TestCompletionConcurrentAndRequestReplay covers the version and request
// identities a completion is bound to, and that neither declaring nor
// withdrawing the business outcome is available to an MCP client.
func TestCompletionConcurrentAndRequestReplay(t *testing.T) {
	now := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	c := completionController(s, now)
	// A stale version conflicts and journals nothing.
	if _, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "stale", Kind: "complete", ExpectedVersion: i.Version - 1}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if saved, err := s.Get(i.ID); err != nil || saved.Completion != nil {
		t.Fatalf("stale completion was journalled: %+v %v", saved.Completion, err)
	}
	// An identical request replays instead of appending a second record.
	done := completionCommand(t, c, i.ID, "complete-1", "complete", i.Version)
	replay := completionCommand(t, c, i.ID, "complete-1", "complete", i.Version)
	if replay.Version != done.Version || len(replay.Completion.Records) != 1 {
		t.Fatalf("replay: %+v", replay)
	}
	// The same transition under a different request is a conflict, not a
	// second record.
	if _, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "complete-2", Kind: "complete", ExpectedVersion: done.Version}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("duplicate completion: %v", err)
	}
	// Reusing a request identity for a different command conflicts too.
	if _, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "complete-1", Kind: "reopen", ExpectedVersion: done.Version}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("reused request identity: %v", err)
	}
	// Reopening an open item is the duplicate transition the other way round.
	opened := completionCommand(t, c, i.ID, "reopen-1", "reopen", done.Version)
	if _, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "reopen-2", Kind: "reopen", ExpectedVersion: opened.Version}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("duplicate reopen: %v", err)
	}
	// Two completions at one version: exactly one wins, and the journal holds
	// one record for it.
	saved, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, errs[n] = c.Command(context.Background(), i.ID, FactoryCommand{RequestID: fmt.Sprintf("race-%d", n), Kind: "complete", ExpectedVersion: saved.Version})
		}(n)
	}
	wg.Wait()
	won := 0
	for n, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrFactoryConflict):
			t.Fatalf("race %d: %v", n, err)
		}
	}
	if won != 1 {
		t.Fatalf("race winners: %d", won)
	}
	final, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if factoryCompletionStatus(final) != "complete" || len(final.Completion.Records) != 2 {
		t.Fatalf("race journal: %+v", final.Completion)
	}
	// Declaring the business outcome is not an action an MCP client may take,
	// in either direction.
	for _, kind := range []string{"complete", "reopen"} {
		item, err := c.CommandAuthorized(context.Background(), i.ID, FactoryCommand{RequestID: "mcp-" + kind, Kind: kind, ExpectedVersion: final.Version}, factoryPrincipal{ClientID: "client", Action: "factory." + kind})
		if !errors.Is(err, ErrFactoryConflict) {
			t.Fatalf("mcp %s: %v", kind, err)
		}
		if item.Version != 0 {
			t.Fatalf("mcp %s returned an item: %+v", kind, item)
		}
	}
	unchanged, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != final.Version || factoryCompletionStatus(unchanged) != "complete" || len(unchanged.Completion.Records) != 2 || len(unchanged.History) != len(final.History) {
		t.Fatalf("an MCP command moved the completion: %+v", unchanged.Completion)
	}
}
