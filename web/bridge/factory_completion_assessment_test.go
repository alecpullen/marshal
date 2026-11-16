package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// assessmentFake is the deterministic completion assessment adapter these
// tests inject. It records what it was asked about and answers with a fixed
// result; during is what makes a late result testable, because it moves the
// item's inputs while the adapter is working, outside every store update — the
// same window a real adapter occupies.
type assessmentFake struct {
	calls      int
	assessment FactoryAssessment
	err        error
	during     func(FactoryItem)
	sawLinks   []FactoryPRLink
}

func (f *assessmentFake) AssessFactoryCompletion(_ context.Context, item FactoryItem, links []FactoryPRLink) (FactoryAssessment, error) {
	f.calls++
	f.sawLinks = links
	if f.during != nil {
		f.during(item)
	}
	return f.assessment, f.err
}

// assessmentAPI is the transport over s with the given adapter, driven exactly
// as the Studio route drives it.
func assessmentAPI(s *FactoryStore, now time.Time, assessor FactoryCompletionAssessor) *factoryAPI {
	return &factoryAPI{store: s, controller: completionController(s, now), assessor: assessor}
}

func assessCompletionCommand(t *testing.T, api *factoryAPI, itemID, requestID string, expectedVersion uint64) FactoryItem {
	t.Helper()
	i, err := api.Command(context.Background(), itemID, FactoryCommand{RequestID: requestID, Kind: "assess_completion", ExpectedVersion: expectedVersion})
	if err != nil {
		t.Fatalf("assess_completion %s: %v", requestID, err)
	}
	return i
}

// addAssessmentLink journals one linked pull request, which is one of the
// inputs an assessment is about: it moves the item's input digest.
func addAssessmentLink(t *testing.T, s *FactoryStore, item FactoryItem, requestID string, number int, now time.Time) FactoryItem {
	t.Helper()
	i, err := s.Update(item.ID, requestID, item.Version, json.RawMessage(`{"kind":"link_pr"}`), func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		completion.Links = append(completion.Links, FactoryPRLink{RepoID: "repo", PR: reviewedPR(number), Origin: "user", ObservedAt: now, Fresh: true})
		dst.Completion = &completion
		return nil
	}, now)
	if err != nil {
		t.Fatalf("link %s: %v", requestID, err)
	}
	return i
}

// TestCompletionAssessmentUnavailable covers an explicitly requested
// assessment with no adapter: the request journals that it is unavailable
// against the inputs it was asked about, nothing is asked and nothing is
// invented, and the completion that follows snapshots that state.
func TestCompletionAssessmentUnavailable(t *testing.T) {
	now := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	api := assessmentAPI(s, now, nil)
	assessed := assessCompletionCommand(t, api, i.ID, "assess-1", i.Version)
	// The request is durable before anything could run, and its answer is
	// recorded: one append for the intent, one for the outcome.
	if assessed.Version != i.Version+2 {
		t.Fatalf("assessment appends: %d -> %d", i.Version, assessed.Version)
	}
	a := assessed.Completion.Assessment
	if a == nil || a.Status != "unavailable" || a.Summary != "" || a.Source != "" {
		t.Fatalf("unavailable assessment: %+v", a)
	}
	if a.InputDigest != factoryAssessmentInputDigest(assessed) || !a.Fresh {
		t.Fatalf("assessment digest/freshness: %+v", a)
	}
	if kinds := []string{assessed.History[len(assessed.History)-2].Kind, assessed.History[len(assessed.History)-1].Kind}; kinds[0] != "completion_assessment_requested" || kinds[1] != "completion_assessment_recorded" {
		t.Fatalf("assessment events: %v", kinds)
	}
	// An absent assessment starts nothing: no effect, worker or grant appears.
	if len(assessed.Effects) != 0 || len(assessed.Workers) != 0 || len(assessed.Grants) != 0 || assessed.State != "queued" {
		t.Fatalf("assessment moved execution: %+v", assessed)
	}
	// Completion proceeds, and its record snapshots the state it stands at.
	completed, err := api.Command(context.Background(), i.ID, FactoryCommand{RequestID: "complete-1", Kind: "complete", ExpectedVersion: assessed.Version})
	if err != nil {
		t.Fatalf("completion after an unavailable assessment: %v", err)
	}
	if factoryCompletionStatus(completed) != "complete" || len(completed.Completion.Records) != 1 {
		t.Fatalf("completion: %+v", completed.Completion)
	}
	record := completed.Completion.Records[0].Assessment
	if record == nil || record.Status != "unavailable" || record.InputDigest != a.InputDigest {
		t.Fatalf("record assessment: %+v", record)
	}
	if record == completed.Completion.Assessment {
		t.Fatal("the record shares the item's live assessment")
	}
}

// TestCompletionAssessmentStale covers freshness: a result is fresh while the
// inputs it was computed against stand, stale once they move, and a result
// that arrives after they moved is journalled with the digest it actually saw
// rather than made to look current. The stored freshness bit is never
// trusted.
func TestCompletionAssessmentStale(t *testing.T) {
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	fake := &assessmentFake{assessment: FactoryAssessment{Status: "available", Summary: "every task accounted for", Source: "assessor"}}
	api := assessmentAPI(s, now, fake)
	assessed := assessCompletionCommand(t, api, i.ID, "assess-1", i.Version)
	if fake.calls != 1 || len(fake.sawLinks) != 0 {
		t.Fatalf("adapter calls: %d, links: %+v", fake.calls, fake.sawLinks)
	}
	a := assessed.Completion.Assessment
	if a == nil || a.Status != "available" || a.Summary != "every task accounted for" || a.Source != "assessor" {
		t.Fatalf("available assessment: %+v", a)
	}
	if a.InputDigest != factoryAssessmentInputDigest(assessed) || !a.Fresh {
		t.Fatalf("assessment digest/freshness: %+v", a)
	}
	// Unchanged inputs: the served result is fresh.
	fresh, err := api.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Completion.Assessment; got == nil || !got.Fresh || got.Summary != "every task accounted for" {
		t.Fatalf("fresh read: %+v", got)
	}
	// A link added afterwards changes the inputs: the same summary is served,
	// marked stale rather than withdrawn.
	linked := addAssessmentLink(t, s, fresh, "link-late", 7, now)
	stale, err := api.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := stale.Completion.Assessment
	if got == nil || got.Fresh || got.Status != "available" || got.Summary != "every task accounted for" || got.InputDigest != a.InputDigest {
		t.Fatalf("stale read: %+v", got)
	}
	if stale.Version != linked.Version || got.InputDigest == factoryAssessmentInputDigest(stale) {
		t.Fatalf("staleness was not derived from the inputs: %+v", got)
	}
	// A stored freshness bit is a reading, never a fact: an item that carries
	// fresh true for moved inputs is still served stale.
	forged, err := s.Update(i.ID, "forged-fresh", stale.Version, json.RawMessage(`{"forged":true}`), func(dst *FactoryItem) error {
		dst.Completion.Assessment.Fresh = true
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	served, err := api.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if served.Version != forged.Version || served.Completion.Assessment == nil || served.Completion.Assessment.Fresh {
		t.Fatalf("stored freshness was trusted: %+v", served.Completion.Assessment)
	}
	// A result that arrives after the inputs changed keeps the digest of the
	// inputs it was computed against: it is stored, visible, and stale.
	before := factoryAssessmentInputDigest(served)
	late := &assessmentFake{assessment: FactoryAssessment{Status: "available", Summary: "late result", Source: "assessor"}}
	late.during = func(item FactoryItem) {
		addAssessmentLink(t, s, item, "link-during", 9, now)
	}
	api = assessmentAPI(s, now, late)
	lateResult := assessCompletionCommand(t, api, i.ID, "assess-late", served.Version)
	if late.calls != 1 {
		t.Fatalf("late adapter calls: %d", late.calls)
	}
	lateAssessment := lateResult.Completion.Assessment
	if lateAssessment == nil || lateAssessment.Status != "available" || lateAssessment.Summary != "late result" {
		t.Fatalf("late assessment: %+v", lateAssessment)
	}
	if lateAssessment.InputDigest != before || lateAssessment.InputDigest == factoryAssessmentInputDigest(lateResult) || lateAssessment.Fresh {
		t.Fatalf("a late result claimed current inputs: %+v", lateAssessment)
	}
}

// TestCompletionAssessmentDoesNotGate covers the rule that an assessment is
// evidence, never a precondition: completion succeeds while it is pending,
// after it failed, with no adapter at all, and once it went stale, and every
// record snapshots the state and the digest it stood at.
func TestCompletionAssessmentDoesNotGate(t *testing.T) {
	now := time.Date(2026, 5, 8, 13, 0, 0, 0, time.UTC)
	failing := &assessmentFake{err: errors.New("the assessor is unreachable")}
	available := &assessmentFake{assessment: FactoryAssessment{Status: "available", Summary: "every task accounted for", Source: "assessor"}}
	cases := []struct {
		name      string
		status    string
		assessor  FactoryCompletionAssessor
		requestID string
		prepare   func(t *testing.T, s *FactoryStore, api *factoryAPI, item FactoryItem) FactoryItem
	}{
		{
			// A pending intent is what a crash between the two appends leaves
			// behind, so the state is written directly to be completed across.
			name: "pending", status: "pending", requestID: "assess-pending",
			prepare: func(t *testing.T, s *FactoryStore, _ *factoryAPI, item FactoryItem) FactoryItem {
				t.Helper()
				i, err := s.Update(item.ID, "pending-intent", item.Version, json.RawMessage(`{"kind":"assess_completion"}`), func(dst *FactoryItem) error {
					completion := factoryCompletionOf(*dst)
					completion.Assessment = &FactoryAssessment{Status: "pending", InputDigest: factoryAssessmentInputDigest(*dst)}
					dst.Completion = &completion
					return nil
				}, now)
				if err != nil {
					t.Fatal(err)
				}
				return i
			},
		},
		{
			name: "failed", status: "failed", assessor: failing, requestID: "assess-failed",
			prepare: func(t *testing.T, s *FactoryStore, api *factoryAPI, item FactoryItem) FactoryItem {
				t.Helper()
				return assessCompletionCommand(t, api, item.ID, "assess-failed", item.Version)
			},
		},
		{
			name: "unavailable", status: "unavailable", requestID: "assess-unavailable",
			prepare: func(t *testing.T, s *FactoryStore, api *factoryAPI, item FactoryItem) FactoryItem {
				t.Helper()
				return assessCompletionCommand(t, api, item.ID, "assess-unavailable", item.Version)
			},
		},
		{
			name: "stale", status: "available", assessor: available, requestID: "assess-stale",
			prepare: func(t *testing.T, s *FactoryStore, api *factoryAPI, item FactoryItem) FactoryItem {
				t.Helper()
				assessed := assessCompletionCommand(t, api, item.ID, "assess-stale", item.Version)
				moved := addAssessmentLink(t, s, assessed, "link-after-assess", 11, now)
				got, err := api.Get(moved.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Completion.Assessment == nil || got.Completion.Assessment.Fresh {
					t.Fatalf("the assessment did not go stale: %+v", got.Completion.Assessment)
				}
				return got
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenFactoryStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			item := completionItem(t, s, now)
			api := assessmentAPI(s, now, tc.assessor)
			current := tc.prepare(t, s, api, item)
			if current.Completion == nil || current.Completion.Assessment == nil || current.Completion.Assessment.Status != tc.status {
				t.Fatalf("prepared assessment: %+v", current.Completion)
			}
			digest := current.Completion.Assessment.InputDigest
			completed, err := api.Command(context.Background(), current.ID, FactoryCommand{RequestID: "complete-" + tc.name, Kind: "complete", ExpectedVersion: current.Version})
			if err != nil {
				t.Fatalf("completion with a %s assessment: %v", tc.status, err)
			}
			if factoryCompletionStatus(completed) != "complete" || len(completed.Completion.Records) != 1 {
				t.Fatalf("completion with a %s assessment: %+v", tc.status, completed.Completion)
			}
			// Completion is a statement about the work: the run is untouched.
			if completed.State != current.State || len(completed.Effects) != 0 || len(completed.Workers) != 0 {
				t.Fatalf("completion moved execution: %+v", completed)
			}
			record := completed.Completion.Records[0].Assessment
			if record == nil || record.Status != tc.status || record.InputDigest != digest {
				t.Fatalf("record assessment: %+v", record)
			}
			if record.Fresh != (record.InputDigest == factoryAssessmentInputDigest(completed)) {
				t.Fatalf("record freshness was not derived: %+v", record)
			}
			if record == completed.Completion.Assessment {
				t.Fatal("the record shares the item's live assessment")
			}
		})
	}
}
