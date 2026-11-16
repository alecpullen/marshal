package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// completionLinkForge is a forge double for the evidence commands: it answers
// GetFactoryPR from a table of pull requests, records what it was asked for and
// can be made to fail, which is the stale-evidence path.
type completionLinkForge struct {
	factoryForgeStubs
	prs  map[int]FactoryPR
	err  error
	gets []int
}

func (f *completionLinkForge) ObserveFactoryCI(context.Context, Repo, string, Credential) (FactoryCI, error) {
	return FactoryCI{}, errors.New("completion link evidence does not observe CI")
}

func (f *completionLinkForge) FindFactoryPR(context.Context, Repo, string, string, Credential) (*FactoryPR, error) {
	return nil, errors.New("completion link evidence does not find pull requests")
}

func (f *completionLinkForge) SetFactoryDraft(context.Context, Repo, FactoryPR, bool, Credential) error {
	return errors.New("completion link evidence does not set drafts")
}

func (f *completionLinkForge) GetFactoryPR(_ context.Context, _ Repo, number int, _ Credential) (FactoryPR, error) {
	f.gets = append(f.gets, number)
	if f.err != nil {
		return FactoryPR{}, f.err
	}
	pr, ok := f.prs[number]
	if !ok {
		return FactoryPR{}, fmt.Errorf("pull request %d not found", number)
	}
	return pr, nil
}

// completionLinkEffects is a controller effects double whose forge opener is
// pinned, so no test reaches the network. Production passes *factoryEffects,
// which opens the forge from the fleet.
type completionLinkEffects struct {
	*loopEffects
	repo    Repo
	factory FactoryForge
	err     error
}

func (e *completionLinkEffects) forge(_ context.Context, _ FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
	if e.err != nil {
		return Repo{}, nil, nil, Credential{}, e.err
	}
	return e.repo, nil, e.factory, Credential{}, nil
}

// linkRepo is the repository the item is configured for.
func linkRepo() Repo {
	return Repo{ID: "repo", URL: "https://github.com/owner/repo", Forge: "github", Branch: "main"}
}

// linkPR is one pull request of that repository, with the immutable identity a
// link deduplicates on.
func linkPR(number int) FactoryPR {
	return FactoryPR{
		Number:  number,
		URL:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", number),
		NodeID:  fmt.Sprintf("node-%d", number),
		HeadRef: "marshal/factory/item",
		BaseRef: "main",
		HeadSHA: "head",
		BaseSHA: "base",
		State:   "open",
		Draft:   true,
	}
}

// linkController is a controller over s whose clock the caller can move.
func linkController(s *FactoryStore, forge FactoryForge, clock func() time.Time) *FactoryController {
	effects := &completionLinkEffects{loopEffects: &loopEffects{store: s}, repo: linkRepo(), factory: forge}
	return NewFactoryController(s, effects, clock)
}

// linkCommand applies one evidence command and requires it to succeed.
func linkCommand(t *testing.T, c *FactoryController, itemID, requestID, kind string, version uint64, payload *FactoryCompletionCommand) FactoryItem {
	t.Helper()
	i, err := c.Command(context.Background(), itemID, FactoryCommand{RequestID: requestID, Kind: kind, ExpectedVersion: version, Completion: payload})
	if err != nil {
		t.Fatalf("%s %s: %v", kind, requestID, err)
	}
	return i
}

// deliveryItem creates an approved item that owns a draft delivery PR in the
// configured repository, which is what delivery association observes.
func deliveryItem(t *testing.T, s *FactoryStore, requestID string, now time.Time) FactoryItem {
	t.Helper()
	req := factoryReq()
	req.RequestID = requestID
	req.Config.TargetBranch = "main"
	created, err := s.Create(req, now)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Update(created.ID, requestID+"-approve", created.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}
		dst.Branch = "marshal/factory/" + created.ID
		dst.BaseSHA = "base"
		dst.State = "checking"
		dst.PRs = []FactoryPR{{
			Number:  1,
			URL:     "https://github.com/owner/repo/pull/1",
			NodeID:  "node-" + created.ID,
			HeadRef: dst.Branch,
			BaseRef: "main",
			State:   "open",
			Draft:   true,
			HeadSHA: "head",
			BaseSHA: "base",
		}}
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// TestCompletionLinksSameRepo covers a link command naming a pull request by
// number and by URL of the configured repository, the immutable identity a link
// is deduplicated on, the journalled event, replay of an identical request and
// a command written against a version the item has left.
func TestCompletionLinksSameRepo(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	forge := &completionLinkForge{prs: map[int]FactoryPR{12: linkPR(12), 13: linkPR(13)}}
	c := linkController(s, forge, func() time.Time { return now })

	linked := linkCommand(t, c, i.ID, "link-url", "link_pr", i.Version, &FactoryCompletionCommand{Link: "https://github.com/owner/repo/pull/12"})
	if len(forge.gets) != 1 || forge.gets[0] != 12 {
		t.Fatalf("resolved %v, want the URL's own number", forge.gets)
	}
	if len(linked.Completion.Links) != 1 {
		t.Fatalf("links: %+v", linked.Completion)
	}
	l := linked.Completion.Links[0]
	if l.RepoID != "repo" || l.Origin != "user" || !l.Fresh || l.PR.NodeID != "node-12" || !l.ObservedAt.Equal(now) {
		t.Fatalf("link: %+v", l)
	}
	if ev := linked.History[len(linked.History)-1]; ev.Kind != "completion_pr_linked" || !strings.Contains(ev.Message, "#12") {
		t.Fatalf("link event: %+v", ev)
	}

	// A bare number is always the configured repository's.
	two := linkCommand(t, c, i.ID, "link-number", "link_pr", linked.Version, &FactoryCompletionCommand{Link: "13"})
	if len(two.Completion.Links) != 2 || two.Completion.Links[1].PR.NodeID != "node-13" {
		t.Fatalf("second link: %+v", two.Completion.Links)
	}

	// Re-linking a linked pull request refreshes its observation: same link,
	// no second entry and no second event.
	again := linkCommand(t, c, i.ID, "link-again", "link_pr", two.Version, &FactoryCompletionCommand{Link: "12"})
	if len(again.Completion.Links) != 2 {
		t.Fatalf("re-link appended: %+v", again.Completion.Links)
	}
	if len(again.History) != len(two.History) {
		t.Fatalf("re-link journalled a second link: %d events became %d", len(two.History), len(again.History))
	}

	// An identical request replays the journalled item rather than writing.
	replay := linkCommand(t, c, i.ID, "link-url", "link_pr", i.Version, &FactoryCompletionCommand{Link: "https://github.com/owner/repo/pull/12"})
	if !reflect.DeepEqual(replay, linked) {
		t.Fatalf("replay: %+v", replay.Completion)
	}

	// A command written against a version the item has left conflicts.
	if _, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "link-stale", Kind: "link_pr", ExpectedVersion: i.Version, Completion: &FactoryCompletionCommand{Link: "12"}}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("stale version: %v", err)
	}
	saved, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != again.Version || len(saved.Completion.Links) != 2 {
		t.Fatalf("a conflicting command wrote: %+v", saved.Completion)
	}
}

// TestCompletionLinksRejectCrossRepo covers a URL naming another repository:
// it is refused as input before any request is made, so a number can never be
// resolved against the wrong repository. Case, a .git suffix and a trailing
// slash name the same repository.
func TestCompletionLinksRejectCrossRepo(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	forge := &completionLinkForge{prs: map[int]FactoryPR{12: linkPR(12)}}
	c := linkController(s, forge, func() time.Time { return now })

	for _, link := range []string{
		"https://github.com/other/repo/pull/12",
		"https://gitlab.com/owner/repo/pull/12",
		"https://github.com/owner/repo",
	} {
		_, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "cross-" + link, Kind: "link_pr", ExpectedVersion: i.Version, Completion: &FactoryCompletionCommand{Link: link}})
		if !errors.Is(err, errFactoryInput) {
			t.Fatalf("%s: %v", link, err)
		}
	}
	if len(forge.gets) != 0 {
		t.Fatalf("a URL another repository names was queried: %v", forge.gets)
	}
	saved, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != i.Version || saved.Completion != nil {
		t.Fatalf("a refused link was journalled: %+v", saved.Completion)
	}

	// The same repository, spelled with other case, a .git suffix and a
	// trailing slash, is the configured repository.
	effects := &completionLinkEffects{loopEffects: &loopEffects{store: s}, repo: Repo{ID: "repo", URL: "https://github.com/Owner/Repo.git", Forge: "github"}, factory: forge}
	dotted := NewFactoryController(s, effects, func() time.Time { return now })
	linked := linkCommand(t, dotted, i.ID, "link-same-repo", "link_pr", i.Version, &FactoryCompletionCommand{Link: "https://github.com/owner/repo/pull/12/"})
	if len(linked.Completion.Links) != 1 || linked.Completion.Links[0].RepoID != "repo" || linked.Completion.Links[0].PR.NodeID != "node-12" {
		t.Fatalf("same repository link: %+v", linked.Completion)
	}
}

// TestCompletionLinksDeduplicateDelivery covers the factory's own delivery PR
// becoming evidence: one link under its immutable identity, no change to
// delivery ownership, no churn when the same observation repeats, and no second
// link when the user attaches the same pull request.
func TestCompletionLinksDeduplicateDelivery(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := deliveryItem(t, s, "delivery", now)
	pr := i.PRs[0]

	next, effect, err := AdvanceFactory(i, FactoryObservation{PR: &pr}, now)
	if err != nil || effect != nil {
		t.Fatalf("advance: %v %v", effect, err)
	}
	if !factoryPRMatches(next, pr) || !factoryPRMatches(i, pr) {
		t.Fatal("association changed delivery ownership")
	}
	if !reflect.DeepEqual(next.PRs, i.PRs) || next.State != i.State || len(next.Effects) != len(i.Effects) {
		t.Fatalf("association changed what the scheduler owns: %+v", next)
	}
	if next.Completion == nil || len(next.Completion.Links) != 1 {
		t.Fatalf("delivery link: %+v", next.Completion)
	}
	if l := next.Completion.Links[0]; l.Origin != "factory" || l.RepoID != "repo" || l.PR.NodeID != pr.NodeID || !l.Fresh || !l.ObservedAt.Equal(now) {
		t.Fatalf("delivery link: %+v", l)
	}
	if ev := next.History[len(next.History)-1]; ev.Kind != "completion_pr_linked" {
		t.Fatalf("delivery link event: %+v", ev)
	}

	// The same observation again is not a new link, a new event or a change:
	// a pure observation must not make the item move forever.
	again, _, err := AdvanceFactory(next, FactoryObservation{PR: &pr}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, next) {
		t.Fatalf("a repeated observation moved the item: %+v", again.Completion)
	}

	// Persist the observation, then let the user attach the same pull request:
	// it is the one link, not a second one, and its origin stays the factory's.
	saved, err := s.Update(i.ID, "advance", i.Version, json.RawMessage(`{"advance":true}`), func(dst *FactoryItem) error {
		*dst = next
		dst.Version = i.Version
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	forge := &completionLinkForge{prs: map[int]FactoryPR{pr.Number: pr}}
	c := linkController(s, forge, func() time.Time { return now })
	user := linkCommand(t, c, i.ID, "link-owned", "link_pr", saved.Version, &FactoryCompletionCommand{Link: strconv.Itoa(pr.Number)})
	if len(user.Completion.Links) != 1 || user.Completion.Links[0].Origin != "factory" || !user.Completion.Links[0].Fresh {
		t.Fatalf("user link of the delivery PR: %+v", user.Completion)
	}
	if !reflect.DeepEqual(user.PRs, i.PRs) || user.State != i.State {
		t.Fatalf("user link changed delivery ownership: %+v", user)
	}

	// The tick's closure observation links the delivery PR it saw close, once.
	ts, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	closing := deliveryItem(t, ts, "closing", now)
	merged := closing.PRs[0]
	merged.State = "merged"
	tick := NewFactoryController(ts, &loopEffects{store: ts, observation: FactoryObservation{PR: &merged}}, func() time.Time { return now })
	tick.admitAfterRecovery()
	if err := tick.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed, err := ts.Get(closing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != "closed" || closed.Completion == nil || len(closed.Completion.Links) != 1 || closed.Completion.Links[0].PR.State != "merged" {
		t.Fatalf("closure link: %+v %+v", closed.Completion, closed.State)
	}
	if err := tick.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := ts.Get(closing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != closed.Version || len(second.Completion.Links) != 1 {
		t.Fatalf("a second tick moved the closed item: %+v", second.Completion)
	}
}

// TestCompletionLinkUnavailableIsStale covers the evidence that could not be
// refreshed: the command succeeds, the link keeps its last successful snapshot
// and is marked stale, and an unavailable forge makes every link stale.
func TestCompletionLinkUnavailableIsStale(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := completionItem(t, s, now)
	forge := &completionLinkForge{prs: map[int]FactoryPR{12: linkPR(12)}}
	clock := now
	c := linkController(s, forge, func() time.Time { return clock })

	linked := linkCommand(t, c, i.ID, "link-12", "link_pr", i.Version, &FactoryCompletionCommand{Link: "12"})
	observed := linked.Completion.Links[0]
	clock = now.Add(time.Hour)

	// A failing fetch is not a failing command: the evidence goes stale and
	// keeps the last successful observation.
	forge.err = errors.New("forge unreachable")
	stale := linkCommand(t, c, i.ID, "refresh-failed", "refresh_pr_evidence", linked.Version, nil)
	l := stale.Completion.Links[0]
	if l.Fresh {
		t.Fatalf("a failed refresh stayed fresh: %+v", l)
	}
	if !reflect.DeepEqual(l.PR, observed.PR) || !l.ObservedAt.Equal(observed.ObservedAt) {
		t.Fatalf("a failed refresh rewrote the snapshot: %+v", l)
	}
	if len(stale.History) != len(linked.History) {
		t.Fatalf("a refresh journalled an event: %+v", stale.History[len(stale.History)-1])
	}

	// The forge answers again and the snapshot moves.
	forge.err = nil
	moved := linkPR(12)
	moved.State = "merged"
	moved.HeadSHA = "next"
	forge.prs[12] = moved
	fresh := linkCommand(t, c, i.ID, "refresh-ok", "refresh_pr_evidence", stale.Version, nil)
	l = fresh.Completion.Links[0]
	if !l.Fresh || l.PR.State != "merged" || l.PR.HeadSHA != "next" || !l.ObservedAt.Equal(clock) {
		t.Fatalf("a successful refresh did not move the snapshot: %+v", l)
	}

	// A pull request whose identity changed is a different pull request: the
	// link keeps the identity it named and goes stale.
	changed := linkPR(12)
	changed.NodeID = "node-elsewhere"
	forge.prs[12] = changed
	clock = now.Add(2 * time.Hour)
	identity := linkCommand(t, c, i.ID, "refresh-identity", "refresh_pr_evidence", fresh.Version, nil)
	l = identity.Completion.Links[0]
	if l.Fresh || l.PR.NodeID != "node-12" || !l.ObservedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("an identity change was followed: %+v", l)
	}

	// An effects value with no forge leaves every link stale, and a link
	// command says so instead of storing an unverified pull request.
	plain := NewFactoryController(s, &loopEffects{store: s}, func() time.Time { return clock })
	unavailable := linkCommand(t, plain, i.ID, "refresh-unavailable", "refresh_pr_evidence", identity.Version, nil)
	if unavailable.Completion.Links[0].Fresh {
		t.Fatalf("an unavailable forge left the link fresh: %+v", unavailable.Completion)
	}
	if _, err := plain.Command(context.Background(), i.ID, FactoryCommand{RequestID: "link-unavailable", Kind: "link_pr", ExpectedVersion: unavailable.Version, Completion: &FactoryCompletionCommand{Link: "12"}}); err == nil {
		t.Fatal("link_pr without a forge was accepted")
	}
	// A response without its immutable identity is not evidence either.
	forge.prs[14] = FactoryPR{Number: 14, URL: "https://github.com/owner/repo/pull/14", State: "open"}
	before, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Command(context.Background(), i.ID, FactoryCommand{RequestID: "link-anonymous", Kind: "link_pr", ExpectedVersion: before.Version, Completion: &FactoryCompletionCommand{Link: "14"}}); !errors.Is(err, errFactoryInput) {
		t.Fatalf("a pull request without a node ID: %v", err)
	}
	saved, err := s.Get(i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Completion.Links) != 1 {
		t.Fatalf("an unverified link was stored: %+v", saved.Completion.Links)
	}
}
