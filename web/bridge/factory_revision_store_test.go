package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// revisionFixture is one approved factory item, the store that holds it and a
// controller over that store. The item is approved rather than merely created
// because a proposal is written against an approved revision.
type revisionFixture struct {
	t     *testing.T
	dir   string
	store *FactoryStore
	ctl   *FactoryController
	item  FactoryItem
}

func newRevisionFixture(t *testing.T) *revisionFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	req := factoryReq()
	req.Config.TargetBranch = "main"
	created, err := store.Create(req, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctl := NewFactoryController(store, nil, func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) })
	approved, err := ctl.Command(context.Background(), created.ID, FactoryCommand{RequestID: "approve", Kind: "approve", ExpectedVersion: created.Version})
	if err != nil {
		t.Fatal(err)
	}
	return &revisionFixture{t: t, dir: dir, store: store, ctl: ctl, item: approved}
}

// current reads the item as the journal holds it now.
func (f *revisionFixture) current() FactoryItem {
	f.t.Helper()
	i, err := f.store.Get(f.item.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return i
}

// command runs one command against the version this fixture's test just read.
func (f *revisionFixture) command(cmd FactoryCommand) (FactoryItem, error) {
	f.t.Helper()
	current := f.current()
	cmd.ExpectedVersion = current.Version
	return f.ctl.Command(context.Background(), current.ID, cmd)
}

// proposeCommand names a candidate against the revision that is active now.
func (f *revisionFixture) proposeCommand(requestID, plan, spec string) FactoryCommand {
	f.t.Helper()
	current := f.current()
	active := current.Approvals[len(current.Approvals)-1]
	return FactoryCommand{
		RequestID: requestID, Kind: "revision_propose",
		Revision: &FactoryRevisionCommand{
			BaseRevision: active.Revision, BaseHash: active.PlanHash,
			Plan: plan, Spec: spec, CandidateHash: FactoryPlanHash(plan, spec),
		},
	}
}

// propose saves one candidate and returns the item as stored.
func (f *revisionFixture) propose(requestID, plan, spec string) (FactoryItem, error) {
	f.t.Helper()
	return f.command(f.proposeCommand(requestID, plan, spec))
}

// revisionAt names one stored proposal's identity and current version.
func revisionAt(i FactoryItem, n int) *FactoryRevisionCommand {
	return &FactoryRevisionCommand{ProposalID: i.Proposals[n].ID, ProposalVersion: i.Proposals[n].Version}
}

// pendingProposal is a proposal as the journal records one: a candidate bound
// to the item's active revision whose assessment intent is pending.
func pendingProposal(i FactoryItem) FactoryRevisionProposal {
	active := i.Approvals[len(i.Approvals)-1]
	plan, spec := "revised plan\n", "revised spec"
	id := factoryRevisionProposalID(i.ID, "fixture")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	return FactoryRevisionProposal{
		ID: id, Version: 1, BaseRevision: active.Revision, BaseHash: active.PlanHash,
		Plan: plan, Spec: spec, CandidateHash: FactoryPlanHash(plan, spec),
		CreatedBy: "studio", AutoAssess: true, Status: "proposed",
		AssessmentState: "pending", AssessmentAttemptID: factoryRevisionAssessmentAttemptID(id),
		CreatedAt: now, UpdatedAt: now,
	}
}

// TestFactoryRevisionLegacyJournalReplay checks an item journalled before
// proposals existed keeps its bytes, replays, and accepts a proposal
// afterwards: the field is additive, so nothing migrates.
func TestFactoryRevisionLegacyJournalReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.Create(factoryReq(), now)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Update(created.ID, "approve", created.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = append(dst.Approvals, FactoryApproval{Revision: 1, Plan: dst.Draft.Plan, Spec: dst.Draft.Spec, PlanHash: FactoryPlanHash(dst.Draft.Plan, dst.Draft.Spec), Config: dst.Draft.Config, ApprovedAt: now})
		dst.State = "queued"
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"proposals"`)) {
		t.Fatalf("an item without proposals marshalled one: %s", raw)
	}
	journal, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journal, []byte(`"proposals"`)) {
		t.Fatal("the journal grew a proposals field without one being saved")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatalf("legacy journal did not replay: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(got)
	if !bytes.Equal(raw, again) {
		t.Fatalf("replay changed the item:\n%s\n%s", raw, again)
	}
	if len(got.Proposals) != 0 {
		t.Fatalf("legacy item replays with proposals: %+v", got.Proposals)
	}
}

// TestFactoryRevisionProposalImmutableAndSupersede checks a saved proposal is
// journal history: its candidate cannot be rewritten, a second unresolved one
// needs an explicit supersede, and the retired proposal is retained.
func TestFactoryRevisionProposalImmutableAndSupersede(t *testing.T) {
	f := newRevisionFixture(t)
	plan, spec := "plan v2\n", "spec v2"
	saved, err := f.propose("rev-1", plan, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Proposals) != 1 {
		t.Fatalf("proposals = %+v", saved.Proposals)
	}
	proposal := saved.Proposals[0]
	if proposal.Status != "proposed" || proposal.Version != 1 || proposal.BaseRevision != 1 || proposal.BaseHash != FactoryPlanHash(" plan\n", "spec") {
		t.Fatalf("proposal = %+v", proposal)
	}
	if proposal.Plan != plan || proposal.Spec != spec || proposal.CandidateHash != FactoryPlanHash(plan, spec) {
		t.Fatalf("candidate = %+v", proposal)
	}
	// A saved proposal approves nothing: the active revision is untouched.
	if saved.State != "queued" || len(saved.Approvals) != 1 || saved.Approvals[0].Plan != " plan\n" {
		t.Fatalf("a proposal changed the active revision: state=%s approvals=%+v", saved.State, saved.Approvals)
	}

	// The candidate is journal history; a mutation that rewrites it is refused.
	if _, err := f.store.Update(saved.ID, "tamper", saved.Version, json.RawMessage(`{"kind":"tamper"}`), func(dst *FactoryItem) error {
		dst.Proposals[0].Plan = "rewritten"
		return nil
	}, time.Now().UTC()); err == nil {
		t.Fatal("a stored proposal was rewritten")
	}

	// A second unresolved proposal needs an explicit supersede.
	if _, err := f.propose("rev-2", "plan v3\n", "spec v3"); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("second unresolved proposal accepted: %v", err)
	}

	// The supersede names the proposal version it read.
	stale := f.proposeCommand("rev-supersede", "", "")
	stale.Kind = "revision_supersede"
	stale.Revision = &FactoryRevisionCommand{ProposalID: proposal.ID, ProposalVersion: proposal.Version + 1}
	if _, err := f.command(stale); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("stale supersede accepted: %v", err)
	}

	superseded, err := f.command(FactoryCommand{RequestID: "rev-supersede", Kind: "revision_supersede", Revision: revisionAt(saved, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(superseded.Proposals) != 1 || superseded.Proposals[0].Status != "superseded" || superseded.Proposals[0].Version != 2 {
		t.Fatalf("superseded = %+v", superseded.Proposals)
	}
	if superseded.Proposals[0].Plan != plan {
		t.Fatal("supersede rewrote the retired candidate")
	}

	replaced, err := f.propose("rev-3", "plan v3\n", "spec v3")
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.Proposals) != 2 {
		t.Fatalf("proposals = %d", len(replaced.Proposals))
	}
	if old := replaced.Proposals[0]; old.Status != "superseded" || old.Plan != plan {
		t.Fatalf("supersede lost the old proposal: %+v", old)
	}
	if replaced.Proposals[1].BaseRevision != 1 || replaced.Proposals[1].Plan != "plan v3\n" {
		t.Fatalf("replacement = %+v", replaced.Proposals[1])
	}

	rejected, err := f.command(FactoryCommand{RequestID: "rev-reject", Kind: "revision_reject", Revision: revisionAt(replaced, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Proposals[1].Status != "rejected" {
		t.Fatalf("rejected = %+v", rejected.Proposals[1])
	}
	if _, err := f.command(FactoryCommand{RequestID: "rev-assess-rejected", Kind: "revision_assess_request", Revision: revisionAt(rejected, 1)}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a rejected proposal was assessed: %v", err)
	}

	// Work that is over cannot carry a new proposal, and says why.
	stopped, err := f.store.Update(rejected.ID, "stop-item", rejected.Version, json.RawMessage(`{"kind":"stop"}`), func(dst *FactoryItem) error {
		dst.State = "stopped"
		return nil
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.command(f.proposeCommand("rev-4", "plan v4\n", "spec v4"))
	if !errors.Is(err, ErrFactoryConflict) || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("proposal saved on a stopped item: %v", err)
	}
	if stopped.Proposals[0].Status != "superseded" {
		t.Fatal("refusing the save disturbed a retained proposal")
	}
}

// TestFactoryRevisionDefaultAutoAssess checks the project preference is on
// unless a project says otherwise, that a save records it, and that the
// assessment intent is journalled with the proposal in the same mutation.
func TestFactoryRevisionDefaultAutoAssess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting *bool
		want    bool
	}{
		{"absent", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	} {
		if got := autoAssessPlanChanges(ProjectSettings{AutoAssessPlanChanges: tc.setting}); got != tc.want {
			t.Fatalf("%s: effective setting = %v, want %v", tc.name, got, tc.want)
		}
	}

	f := newRevisionFixture(t)
	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	proposal := saved.Proposals[0]
	if !proposal.AutoAssess || proposal.AssessmentState != "pending" || proposal.AssessmentAttemptID != proposal.ID+":assessment:1" {
		t.Fatalf("default proposal = %+v", proposal)
	}
	// The intent is an intent: the proposal blocks nothing and dispatches
	// nothing.
	if saved.State != "queued" || len(saved.Effects) != 0 || saved.Decision != nil {
		t.Fatalf("pending proposal moved the item: %+v", saved)
	}
	// A second intent is refused while the first is pending.
	if _, err := f.command(FactoryCommand{RequestID: "rev-assess", Kind: "revision_assess_request", Revision: revisionAt(saved, 0)}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("second assessment intent accepted: %v", err)
	}

	// A project that turned the preference off saves the proposal without an
	// intent, and a manual request journals the same deterministic one.
	off := newRevisionFixture(t)
	off.ctl.projectSettings = func(root string) (ProjectSettings, bool) {
		return ProjectSettings{AutoAssessPlanChanges: boolPtr(false)}, true
	}
	quiet, err := off.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if p := quiet.Proposals[0]; p.AutoAssess || p.AssessmentState != "not_requested" || p.AssessmentAttemptID != "" {
		t.Fatalf("proposal under an explicit false = %+v", p)
	}
	requested, err := off.command(FactoryCommand{RequestID: "rev-assess", Kind: "revision_assess_request", Revision: revisionAt(quiet, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if p := requested.Proposals[0]; p.AssessmentState != "pending" || p.AssessmentAttemptID != p.ID+":assessment:1" || p.Version != 2 {
		t.Fatalf("manual intent = %+v", p)
	}
}

// TestFactoryRevisionProjectPreferenceRoundTrip checks the setting survives the
// existing conditional settings path, keeps unrelated fields, and that a stale
// write changes nothing.
func TestFactoryRevisionProjectPreferenceRoundTrip(t *testing.T) {
	e, s, root := projectEnv(t)
	if err := e.f.ws.PutRepo(Repo{ID: "r1", URL: "https://example.com/r1.git", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/settings?root=" + root
	if got := e.f.ws.ProjectSettingsFor(root); got.AutoAssessPlanChanges != nil || !autoAssessPlanChanges(got) {
		t.Fatalf("an unset project does not default to on: %+v", got)
	}

	off := doReq(t, s, http.MethodPut, path, ProjectSettings{Mode: "act", Intake: ProjectIntake{RepoID: "r1"}, AutoAssessPlanChanges: boolPtr(false)}, nil)
	if off.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", off.Code, off.Body.String())
	}
	tag := off.Header().Get("ETag")
	var stored ProjectSettings
	decodeBody(t, off, &stored)
	if stored.AutoAssessPlanChanges == nil || *stored.AutoAssessPlanChanges {
		t.Fatalf("stored = %+v", stored)
	}
	if autoAssessPlanChanges(stored) {
		t.Fatal("the override was ignored")
	}

	// The conditional path carries the setting and preserves the rest.
	next := doReq(t, s, http.MethodPut, path, ProjectSettings{Mode: "act", ShipTarget: "patch", Intake: ProjectIntake{RepoID: "r1"}, AutoAssessPlanChanges: boolPtr(false)}, map[string]string{"If-Match": tag})
	if next.Code != http.StatusOK {
		t.Fatalf("conditional put = %d: %s", next.Code, next.Body.String())
	}
	tag = next.Header().Get("ETag")
	var reread ProjectSettings
	decodeBody(t, doReq(t, s, http.MethodGet, path, nil, nil), &reread)
	if reread.ShipTarget != "patch" || reread.Mode != "act" || reread.AutoAssessPlanChanges == nil || *reread.AutoAssessPlanChanges {
		t.Fatalf("reread = %+v", reread)
	}

	// A write from the stale tag lands nothing.
	fresh := doReq(t, s, http.MethodPut, path, ProjectSettings{Mode: "act", Intake: ProjectIntake{RepoID: "r1"}, AutoAssessPlanChanges: boolPtr(true)}, map[string]string{"If-Match": tag})
	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh tag refused: %d: %s", fresh.Code, fresh.Body.String())
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.AutoAssessPlanChanges == nil || !*got.AutoAssessPlanChanges {
		t.Fatalf("override = %+v", got)
	}

	// Writing an older tag is refused and leaves the override in place.
	if stale := doReq(t, s, http.MethodPut, path, ProjectSettings{Mode: "act", Intake: ProjectIntake{RepoID: "r1"}, AutoAssessPlanChanges: boolPtr(false)}, map[string]string{"If-Match": tag}); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale tag = %d: %s", stale.Code, stale.Body.String())
	}
	if got := e.f.ws.ProjectSettingsFor(root); got.AutoAssessPlanChanges == nil || !*got.AutoAssessPlanChanges {
		t.Fatalf("a stale write landed: %+v", got)
	}

	// Clearing the field restores the documented default. The refused write
	// above changed nothing, so the tag the last successful write returned is
	// still current.
	if tag = fresh.Header().Get("ETag"); tag == "" {
		t.Fatal("a successful settings write named no ETag")
	}
	cleared := doReq(t, s, http.MethodPut, path, ProjectSettings{Mode: "act", Intake: ProjectIntake{RepoID: "r1"}}, map[string]string{"If-Match": tag})
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear = %d: %s", cleared.Code, cleared.Body.String())
	}
	var after ProjectSettings
	decodeBody(t, cleared, &after)
	if after.AutoAssessPlanChanges != nil || !autoAssessPlanChanges(after) {
		t.Fatalf("after clear = %+v", after)
	}
}

// TestFactoryRevisionStaleBase checks a candidate written against anything but
// the active approved revision conflicts rather than rebasing, and that an
// intervening approval makes the base stale.
func TestFactoryRevisionStaleBase(t *testing.T) {
	f := newRevisionFixture(t)
	current := f.current()
	active := current.Approvals[len(current.Approvals)-1]
	sound := FactoryRevisionCommand{BaseRevision: active.Revision, BaseHash: active.PlanHash, Plan: "plan v2\n", Spec: "spec v2", CandidateHash: FactoryPlanHash("plan v2\n", "spec v2")}

	for _, tc := range []struct {
		name string
		cmd  FactoryRevisionCommand
		want error
	}{
		{"future base revision", func() FactoryRevisionCommand { c := sound; c.BaseRevision = active.Revision + 1; return c }(), ErrFactoryConflict},
		{"unapproved base revision", func() FactoryRevisionCommand { c := sound; c.BaseRevision = 0; return c }(), ErrFactoryConflict},
		{"wrong base hash", func() FactoryRevisionCommand { c := sound; c.BaseHash = FactoryPlanHash("other", "other"); return c }(), ErrFactoryConflict},
		{"unbound candidate", func() FactoryRevisionCommand {
			c := sound
			c.CandidateHash = FactoryPlanHash("other", "other")
			return c
		}(), errFactoryInput},
		{"no candidate", func() FactoryRevisionCommand { c := sound; c.Plan = ""; return c }(), errFactoryInput},
		{"oversized candidate", func() FactoryRevisionCommand {
			c := sound
			c.Plan = strings.Repeat("x", factoryImportLimit+1)
			c.CandidateHash = FactoryPlanHash(c.Plan, c.Spec)
			return c
		}(), errFactoryInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := f.proposeCommand("rev-stale", "", "")
			cmd.Revision = &tc.cmd
			if _, err := f.command(cmd); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if got := f.current(); len(got.Proposals) != 0 || got.Version != current.Version {
		t.Fatalf("a refused candidate was journalled: %+v", got.Proposals)
	}

	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(FactoryCommand{RequestID: "rev-supersede", Kind: "revision_supersede", Revision: revisionAt(saved, 0)}); err != nil {
		t.Fatal(err)
	}
	// A new approval moves the active revision, so the old base is stale.
	grown, err := f.store.Update(saved.ID, "approve-2", f.current().Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = append(dst.Approvals, FactoryApproval{Revision: 2, Plan: "plan v9\n", Spec: "spec v9", PlanHash: FactoryPlanHash("plan v9\n", "spec v9"), Config: dst.Draft.Config, ApprovedAt: time.Now().UTC()})
		return nil
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// A candidate still naming the superseded approval conflicts rather than
	// rebasing: proposeCommand reads the revision active now, so override it.
	staleBase := f.proposeCommand("rev-2", "plan v3\n", "spec v3")
	staleBase.Revision.BaseRevision = active.Revision
	staleBase.Revision.BaseHash = active.PlanHash
	if _, err := f.command(staleBase); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a candidate written against the superseded base was saved: %v", err)
	}
	fresh, err := f.command(f.proposeCommand("rev-3", "plan v3\n", "spec v3"))
	if err != nil {
		t.Fatal(err)
	}
	if p := fresh.Proposals[len(fresh.Proposals)-1]; p.BaseRevision != 2 || p.BaseHash != grown.Approvals[1].PlanHash {
		t.Fatalf("fresh proposal base = %d/%s", p.BaseRevision, p.BaseHash)
	}
}

// TestFactoryRevisionConcurrentProposals checks two writers naming one item
// version cannot both save a proposal: one appends, the other conflicts.
func TestFactoryRevisionConcurrentProposals(t *testing.T) {
	f := newRevisionFixture(t)
	current := f.current()
	active := current.Approvals[len(current.Approvals)-1]
	const writers = 8
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for n := 0; n < writers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			plan := fmt.Sprintf("plan %d\n", n)
			_, err := f.ctl.Command(context.Background(), current.ID, FactoryCommand{
				RequestID: fmt.Sprintf("concurrent-%d", n), Kind: "revision_propose", ExpectedVersion: current.Version,
				Revision: &FactoryRevisionCommand{BaseRevision: active.Revision, BaseHash: active.PlanHash, Plan: plan, Spec: "spec", CandidateHash: FactoryPlanHash(plan, "spec")},
			})
			results <- err
		}(n)
	}
	close(start)
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrFactoryConflict):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d proposals won, want exactly one", won)
	}
	saved := f.current()
	if len(saved.Proposals) != 1 || saved.Proposals[0].Status != "proposed" {
		t.Fatalf("proposals = %+v", saved.Proposals)
	}
	if saved.Version != current.Version+1 {
		t.Fatalf("version = %d, want %d", saved.Version, current.Version+1)
	}
}

// TestFactoryRevisionPendingDoesNotBlockWorkOrReady checks a saved proposal
// changes no scheduling: the item derives the same next effect and a ready
// item stays ready under its existing approval.
func TestFactoryRevisionPendingDoesNotBlockWorkOrReady(t *testing.T) {
	now := time.Now().UTC()
	obs := FactoryObservation{BaseSHA: "base"}

	plain, plainEffect, err := AdvanceFactory(loopItem(), obs, now)
	if err != nil {
		t.Fatal(err)
	}
	withProposal := loopItem()
	withProposal.Proposals = []FactoryRevisionProposal{pendingProposal(withProposal)}
	next, effect, err := AdvanceFactory(withProposal, obs, now)
	if err != nil {
		t.Fatal(err)
	}
	if plainEffect == nil || effect == nil || effect.Kind != plainEffect.Kind || next.State != plain.State {
		t.Fatalf("pending proposal changed scheduling: %+v vs %+v", effect, plainEffect)
	}
	if len(next.Proposals) != 1 || next.Proposals[0].AssessmentState != "pending" {
		t.Fatalf("advance dropped the proposal: %+v", next.Proposals)
	}
	if factoryPick([]FactoryItem{withProposal}, func(FactoryItem) bool { return true }) == nil {
		t.Fatal("a pending proposal hid the item from the scheduler")
	}

	// An item that is ready under its old revision stays ready, and the
	// proposal stays available for a decision.
	ready := loopItem()
	ready.State = "ready"
	ready.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: ready.Branch, BaseRef: "main", State: "open", HeadSHA: "head", BaseSHA: "base"}}
	ready.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, HeadSHA: "head", BaseSHA: "base"}}
	ready.Proposals = []FactoryRevisionProposal{pendingProposal(ready)}
	pr := ready.PRs[0]
	readyObs := FactoryObservation{PR: &pr, RemoteHead: "head", CI: &FactoryCI{HeadSHA: "head", Complete: true}}
	settled, effect, err := AdvanceFactory(ready, readyObs, now)
	if err != nil {
		t.Fatal(err)
	}
	if effect != nil || settled.State != "ready" || len(settled.Proposals) != 1 {
		t.Fatalf("a pending proposal disturbed readiness: %+v state=%s proposals=%d", effect, settled.State, len(settled.Proposals))
	}
}

// TestFactoryRevisionIdempotentRequestReplay checks one request identity saves
// one proposal — before and after a restart — and that a different request for
// the same bytes is a conflict, not a second proposal.
func TestFactoryRevisionIdempotentRequestReplay(t *testing.T) {
	f := newRevisionFixture(t)
	before := f.current()
	active := before.Approvals[len(before.Approvals)-1]
	cmd := FactoryCommand{
		RequestID: "rev-1", Kind: "revision_propose", ExpectedVersion: before.Version,
		Revision: &FactoryRevisionCommand{BaseRevision: active.Revision, BaseHash: active.PlanHash, Plan: "plan v2\n", Spec: "spec v2", CandidateHash: FactoryPlanHash("plan v2\n", "spec v2")},
	}
	first, err := f.ctl.Command(context.Background(), before.ID, cmd)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.ctl.Command(context.Background(), before.ID, cmd)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(first)
	y, _ := json.Marshal(replay)
	if !bytes.Equal(x, y) {
		t.Fatalf("replay returned a different item:\n%s\n%s", x, y)
	}
	if len(replay.Proposals) != 1 || !replay.Proposals[0].CreatedAt.Equal(first.Proposals[0].CreatedAt) {
		t.Fatalf("replay = %+v", replay.Proposals)
	}

	// A different request that proposes the same bytes is a second proposal,
	// and one unresolved proposal is the limit.
	again := cmd
	again.RequestID = "rev-2"
	if _, err := f.ctl.Command(context.Background(), before.ID, again); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("second request = %v", err)
	}

	// The journal replays the proposal as the same immutable record.
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(before.ID)
	if err != nil {
		t.Fatal(err)
	}
	z, _ := json.Marshal(got)
	if !bytes.Equal(x, z) {
		t.Fatalf("journal replay changed the item:\n%s\n%s", x, z)
	}
	if len(got.Proposals) != 1 || got.Proposals[0].ID != first.Proposals[0].ID {
		t.Fatalf("replayed proposals = %+v", got.Proposals)
	}
}
