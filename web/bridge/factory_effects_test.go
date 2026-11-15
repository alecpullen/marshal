package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFactoryEffectAdapterConstruction(t *testing.T) {
	var effects FactoryEffects = NewFactoryEffects(nil)
	if effects == nil {
		t.Fatal("missing effects adapter")
	}
	_, err := effects.Execute(context.Background(), FactoryItem{}, FactoryEffect{Kind: "unknown"})
	if err == nil {
		t.Fatal("unsupported effect accepted")
	}
}

// factoryForgeStubs satisfies the Forge methods the factory effect path never
// calls. They panic rather than silently passing.
type factoryForgeStubs struct{}

func (factoryForgeStubs) CreatePR(context.Context, Repo, PRRequest, Credential) (PR, error) {
	panic("factory effect called Forge.CreatePR")
}
func (factoryForgeStubs) GetIssue(context.Context, Repo, int, Credential) (Issue, error) {
	panic("factory effect called Forge.GetIssue")
}
func (factoryForgeStubs) ListIssues(context.Context, Repo, IssueQuery, Credential) ([]Issue, error) {
	panic("factory effect called Forge.ListIssues")
}
func (factoryForgeStubs) CommentIssue(context.Context, Repo, int, string, Credential) error {
	panic("factory effect called Forge.CommentIssue")
}
func (factoryForgeStubs) RepoSize(context.Context, Repo, Credential) (int64, error) {
	panic("factory effect called Forge.RepoSize")
}
func (factoryForgeStubs) ListPRs(context.Context, Repo, PRQuery, Credential) ([]PRInfo, error) {
	panic("factory effect called Forge.ListPRs")
}
func (factoryForgeStubs) GetPR(context.Context, Repo, int, Credential) (PRInfo, error) {
	panic("factory effect called Forge.GetPR")
}
func (factoryForgeStubs) PostReview(context.Context, Repo, int, Review, Credential) (string, error) {
	panic("factory effect called Forge.PostReview")
}
func (factoryForgeStubs) ListFailedChecks(context.Context, Repo, string, Credential) ([]CheckInfo, error) {
	panic("factory effect called Forge.ListFailedChecks")
}
func (factoryForgeStubs) CheckLog(context.Context, Repo, CheckInfo, Credential) (string, error) {
	panic("factory effect called Forge.CheckLog")
}

// factoryForgeCore is the shared Forge+FactoryForge surface of the effect
// doubles. The description capability is added only by the doubles that carry
// it, so a double without it can embed this core unchanged.
type factoryForgeCore struct {
	factoryForgeStubs
	pr                 FactoryPR
	mutations          []bool
	order              []string
	bodies             []string
	failDescription    bool
	failCompensation   bool
	retargetAfterReady bool
	advanceAfterReady  bool
}

func (f *factoryForgeCore) ObserveFactoryCI(context.Context, Repo, string, Credential) (FactoryCI, error) {
	return FactoryCI{HeadSHA: "old", Complete: true}, nil
}
func (f *factoryForgeCore) FindFactoryPR(context.Context, Repo, string, string, Credential) (*FactoryPR, error) {
	return &f.pr, nil
}
func (f *factoryForgeCore) GetFactoryPR(context.Context, Repo, int, Credential) (FactoryPR, error) {
	return f.pr, nil
}
func (f *factoryForgeCore) SetFactoryDraft(_ context.Context, _ Repo, _ FactoryPR, draft bool, _ Credential) error {
	f.mutations = append(f.mutations, draft)
	f.order = append(f.order, "draft")
	if draft && f.failCompensation {
		return errors.New("compensation failed")
	}
	f.pr.Draft = draft
	if !draft {
		if f.retargetAfterReady {
			f.pr.BaseRef = "release"
		} else if f.advanceAfterReady {
			f.pr.HeadSHA = "new"
		}
	}
	return nil
}

// racingFactoryForge carries the description capability as well.
type racingFactoryForge struct{ *factoryForgeCore }

func (f racingFactoryForge) UpdateFactoryPRDescription(_ context.Context, _ Repo, _ int, body string, _ Credential) error {
	f.bodies = append(f.bodies, body)
	f.order = append(f.order, "description")
	if f.failDescription {
		return errors.New("description write failed")
	}
	return nil
}

// factoryForgeWithoutDescription has the Forge and FactoryForge capabilities
// but not the description capability.
type factoryForgeWithoutDescription struct{ *factoryForgeCore }

func newRacingFactoryForge(pr FactoryPR) *racingFactoryForge {
	return &racingFactoryForge{factoryForgeCore: &factoryForgeCore{pr: pr}}
}

func newFactoryForgeWithoutDescription(pr FactoryPR) *factoryForgeWithoutDescription {
	return &factoryForgeWithoutDescription{factoryForgeCore: &factoryForgeCore{pr: pr}}
}

// openForgeWith pins both forge slots to the same value, as production does.
func openForgeWith(f Forge, factory FactoryForge) func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
	return func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
		return Repo{}, f, factory, Credential{}, nil
	}
}

// narratedItem is a set_ready candidate with a published draft PR and one
// round per narration, each round's implementation report carrying it.
func narratedItem(issues ...string) FactoryItem {
	i := loopItem()
	i.PRs = []FactoryPR{{Number: 1, NodeID: "node", HeadRef: i.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "old", BaseSHA: "base"}}
	for n, issue := range issues {
		i.Rounds = append(i.Rounds, FactoryRound{Number: n + 1, HeadSHA: "old", Implementation: &FactoryReport{OperationID: "op", Outcome: "complete", Narration: &FactoryNarration{Issue: issue, Approach: "approach"}}})
	}
	return i
}

func TestFactoryEffectReadyRejectsRetargetBeforeMutation(t *testing.T) {
	for _, changed := range []string{"head", "base", "identity"} {
		t.Run(changed, func(t *testing.T) {
			i := narratedItem("the issue")
			forge := newRacingFactoryForge(i.PRs[0])
			switch changed {
			case "head":
				forge.pr.HeadRef = "elsewhere"
			case "base":
				forge.pr.BaseRef = "release"
			case "identity":
				forge.pr.NodeID = "different"
			}
			effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
			_, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
			if !errors.Is(err, ErrFactoryRefused) || len(forge.mutations) != 0 {
				t.Fatalf("precheck: %v mutations=%v", err, forge.mutations)
			}
		})
	}
}

func TestFactoryEffectReadyRetargetAfterMutationCompensates(t *testing.T) {
	i := narratedItem("the issue")
	forge := newRacingFactoryForge(i.PRs[0])
	forge.retargetAfterReady = true
	effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
	out, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
	if err != nil || !out.Confirmed || out.Error == "" || len(forge.mutations) != 2 || forge.mutations[0] || !forge.mutations[1] {
		t.Fatalf("postcheck: %+v %v mutations=%v", out, err, forge.mutations)
	}
}

func TestFactoryEffectReadyRaceCompensates(t *testing.T) {
	for _, fail := range []bool{false, true} {
		i := narratedItem("the issue")
		forge := newRacingFactoryForge(i.PRs[0])
		forge.failCompensation = fail
		forge.advanceAfterReady = true
		effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
		e := FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"}
		out, err := effects.Execute(context.Background(), i, e)
		if fail {
			if !errors.Is(err, ErrFactoryUncertain) {
				t.Fatalf("failure should be uncertain: %v", err)
			}
		} else if err != nil || !out.Confirmed || out.Error == "" || out.PR == nil || !out.PR.Draft {
			t.Fatalf("compensated outcome: %+v %v", out, err)
		}
		if len(forge.mutations) != 2 || forge.mutations[0] || !forge.mutations[1] {
			t.Fatalf("mutations=%v", forge.mutations)
		}
	}
}

func TestFactoryEffectReadyPublishesComposedDescription(t *testing.T) {
	i := narratedItem("the issue")
	forge := newRacingFactoryForge(i.PRs[0])
	effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
	out, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
	if err != nil || !out.Confirmed || out.Error != "" {
		t.Fatalf("ready: %+v %v", out, err)
	}
	want, err := composeFactoryPRBody(i.Rounds[0], nil, "old")
	if err != nil {
		t.Fatal(err)
	}
	if len(forge.bodies) != 1 || forge.bodies[0] != want {
		t.Fatalf("bodies=%q, want %q", forge.bodies, want)
	}
	if len(forge.order) < 2 || forge.order[0] != "description" || forge.order[1] != "draft" {
		t.Fatalf("description write did not precede the draft clear: %v", forge.order)
	}
}

func TestFactoryEffectReadyFetchesPreviewAtRender(t *testing.T) {
	i := narratedItem("the issue")
	forge := newRacingFactoryForge(i.PRs[0])
	report := &PreviewReport{
		Status: "ok",
		Ref:    i.PRs[0].HeadRef,
		SHA:    "old",
		Deployments: []PreviewDeployment{{
			Environment:    "preview",
			State:          "success",
			Ref:            i.PRs[0].HeadRef,
			SHA:            "old",
			EnvironmentURL: "https://preview.example.test",
		}},
	}
	var filters []PreviewFilter
	effects := &factoryEffects{
		openForge: openForgeWith(forge, forge),
		previewSource: func(_ context.Context, _ Repo, filter PreviewFilter) (*PreviewReport, error) {
			filters = append(filters, filter)
			return report, nil
		},
	}
	out, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
	if err != nil || !out.Confirmed {
		t.Fatalf("ready: %+v %v", out, err)
	}
	if len(filters) != 1 {
		t.Fatalf("preview fetches=%v, want exactly one", filters)
	}
	if want := (PreviewFilter{Ref: i.PRs[0].HeadRef}); filters[0] != want {
		t.Fatalf("filter=%+v, want %+v", filters[0], want)
	}
	if filters[0].SHA != "" {
		t.Fatalf("filter scoped by SHA: %+v", filters[0])
	}
	want, err := composeFactoryPRBody(i.Rounds[0], report, "old")
	if err != nil {
		t.Fatal(err)
	}
	if len(forge.bodies) != 1 || forge.bodies[0] != want {
		t.Fatalf("bodies=%q, want %q", forge.bodies, want)
	}
	// The row is badged against the pre-check head, which proves the section
	// rendered from what the fetch returned.
	if !strings.Contains(forge.bodies[0], "matches the PR head") {
		t.Fatalf("deployment not badged against the confirmed head: %q", forge.bodies[0])
	}
}

func TestFactoryEffectReadyPreviewFailureStillWritesDescription(t *testing.T) {
	const unreadable = "Preview and test environments could not be read when this description was written."
	const failureText = "preview transport exploded"
	for _, tc := range []struct {
		name   string
		source func(context.Context, Repo, PreviewFilter) (*PreviewReport, error)
	}{
		{"fetch error", func(context.Context, Repo, PreviewFilter) (*PreviewReport, error) {
			return nil, errors.New(failureText)
		}},
		{"unavailable report", func(context.Context, Repo, PreviewFilter) (*PreviewReport, error) {
			return &PreviewReport{Status: "unavailable", Error: failureText}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := narratedItem("the issue")
			forge := newRacingFactoryForge(i.PRs[0])
			effects := &factoryEffects{openForge: openForgeWith(forge, forge), previewSource: tc.source}
			out, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
			if err != nil || !out.Confirmed || out.Error != "" {
				t.Fatalf("ready: %+v %v", out, err)
			}
			if len(forge.bodies) != 1 {
				t.Fatalf("bodies=%q, want exactly one", forge.bodies)
			}
			if !strings.Contains(forge.bodies[0], unreadable) {
				t.Fatalf("body lacks the unreadable line: %q", forge.bodies[0])
			}
			if strings.Contains(forge.bodies[0], failureText) {
				t.Fatalf("internal error text reached the body: %q", forge.bodies[0])
			}
			if forge.pr.Draft {
				t.Fatal("draft flag still set after a confirmed set_ready")
			}
		})
	}
	t.Run("set_draft never fetches", func(t *testing.T) {
		i := narratedItem("the issue")
		i.PRs[0].Draft = false
		forge := newRacingFactoryForge(i.PRs[0])
		effects := &factoryEffects{
			openForge: openForgeWith(forge, forge),
			previewSource: func(context.Context, Repo, PreviewFilter) (*PreviewReport, error) {
				t.Error("set_draft fetched a preview report")
				return nil, errors.New(failureText)
			},
		}
		if _, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "draft", Kind: "set_draft"}); err != nil {
			t.Fatal(err)
		}
		if len(forge.bodies) != 0 {
			t.Fatalf("set_draft wrote a description: %q", forge.bodies)
		}
	})
}

func TestFactoryEffectReadyPreviewTimeout(t *testing.T) {
	const unreadable = "Preview and test environments could not be read when this description was written."
	effect := FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"}
	// waitingSource never returns on its own; it waits for its context, so the
	// effect's bound and its treatment of a done context are observable.
	waitingSource := func(ctx context.Context, _ Repo, _ PreviewFilter) (*PreviewReport, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	t.Run("the fetch is bounded at five seconds and ready still writes", func(t *testing.T) {
		i := narratedItem("the issue")
		forge := newRacingFactoryForge(i.PRs[0])
		fetched := make(chan context.Context, 1)
		effects := &factoryEffects{
			openForge: openForgeWith(forge, forge),
			previewSource: func(ctx context.Context, _ Repo, _ PreviewFilter) (*PreviewReport, error) {
				fetched <- ctx
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(8 * time.Second):
					// The unbounded predecessor never cancelled the fetch
					// context; returning here makes that a clean failure
					// instead of a deadlock.
					return nil, errors.New("the fetch context was never done")
				}
			},
		}
		start := time.Now()
		out, err := effects.Execute(context.Background(), i, effect)
		if err != nil || !out.Confirmed || out.Error != "" {
			t.Fatalf("a child deadline must not fail set_ready: %+v %v", out, err)
		}
		deadline, ok := (<-fetched).Deadline()
		if !ok {
			t.Fatal("the fetch context carried no deadline")
		}
		// The child's deadline is bounded at five seconds from the fetch;
		// the check stays tolerant of scheduling.
		if remaining := deadline.Sub(start); remaining > 5*time.Second+250*time.Millisecond || remaining <= 0 {
			t.Fatalf("the fetch context deadline was %v after the effect started, want within five seconds", remaining)
		}
		if len(forge.order) < 2 || forge.order[0] != "description" || forge.order[1] != "draft" {
			t.Fatalf("ready transitions did not follow fetch: %v", forge.order)
		}
		if len(forge.bodies) != 1 || !strings.Contains(forge.bodies[0], unreadable) {
			t.Fatalf("the child timeout did not yield the exact unreadable line: %q", forge.bodies)
		}
		if forge.pr.Draft || len(forge.mutations) != 1 || forge.mutations[0] {
			t.Fatalf("the draft clear did not survive the child timeout: %+v %v", forge.pr, forge.mutations)
		}
	})

	t.Run("parent cancellation stops the effect", func(t *testing.T) {
		i := narratedItem("the issue")
		forge := newRacingFactoryForge(i.PRs[0])
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		effects := &factoryEffects{openForge: openForgeWith(forge, forge), previewSource: waitingSource}
		_, err := effects.Execute(parent, i, effect)
		if !errors.Is(err, ErrFactoryUncertain) || !errors.Is(err, context.Canceled) {
			t.Fatalf("parent cancellation must stop the effect normally, not be swallowed: %v", err)
		}
		if len(forge.bodies) != 0 || len(forge.mutations) != 0 || !forge.pr.Draft {
			t.Fatalf("a cancelled effect wrote anyway: bodies=%q mutations=%v draft=%v", forge.bodies, forge.mutations, forge.pr.Draft)
		}
	})
}

func TestFactoryEffectReadyWritesDescriptionForReviewedHead(t *testing.T) {
	i := narratedItem("older issue", "newer issue")
	forge := newRacingFactoryForge(i.PRs[0])
	effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
	if _, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"}); err != nil {
		t.Fatal(err)
	}
	if len(forge.bodies) != 1 || !strings.Contains(forge.bodies[0], "newer issue") || strings.Contains(forge.bodies[0], "older issue") {
		t.Fatalf("body did not describe the newest round: %q", forge.bodies)
	}
}

func TestFactoryEffectReadyRefusesMovedRevisionBeforeDescriptionWrite(t *testing.T) {
	for _, changed := range []string{"head", "base", "identity"} {
		t.Run(changed, func(t *testing.T) {
			i := narratedItem("the issue")
			forge := newRacingFactoryForge(i.PRs[0])
			switch changed {
			case "head":
				forge.pr.HeadRef = "elsewhere"
			case "base":
				forge.pr.BaseRef = "release"
			case "identity":
				forge.pr.NodeID = "different"
			}
			effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
			_, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
			if !errors.Is(err, ErrFactoryRefused) || len(forge.bodies) != 0 || len(forge.mutations) != 0 {
				t.Fatalf("moved revision: %v bodies=%q mutations=%v", err, forge.bodies, forge.mutations)
			}
		})
	}
}

func TestFactoryEffectReadyDescriptionFailureLeavesDraft(t *testing.T) {
	i := narratedItem("the issue")
	forge := newRacingFactoryForge(i.PRs[0])
	forge.failDescription = true
	effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
	_, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
	if !errors.Is(err, ErrFactoryUncertain) {
		t.Fatalf("description write failure should be uncertain: %v", err)
	}
	if len(forge.bodies) != 1 || len(forge.mutations) != 0 || !forge.pr.Draft {
		t.Fatalf("failed write mutated the draft flag: bodies=%q mutations=%v draft=%v", forge.bodies, forge.mutations, forge.pr.Draft)
	}
}

func TestFactoryEffectReadyRefusesItemWithoutNarration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rounds []FactoryRound
	}{
		{"no round", nil},
		{"round without an implementation report", []FactoryRound{{Number: 1, HeadSHA: "old"}}},
		{"report without a narration", []FactoryRound{{Number: 1, HeadSHA: "old", Implementation: &FactoryReport{OperationID: "op", Outcome: "complete"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := narratedItem()
			i.Rounds = tc.rounds
			forge := newRacingFactoryForge(i.PRs[0])
			effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
			_, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
			if !errors.Is(err, ErrFactoryRefused) || !strings.Contains(err.Error(), "narration") {
				t.Fatalf("reason does not name the missing narration: %v", err)
			}
			if len(forge.bodies) != 0 || len(forge.mutations) != 0 {
				t.Fatalf("nothing may be written without a narration: bodies=%q mutations=%v", forge.bodies, forge.mutations)
			}
		})
	}
}

func TestFactoryEffectSetDraftWritesNoDescription(t *testing.T) {
	i := narratedItem("the issue")
	i.PRs[0].Draft = false
	forge := newRacingFactoryForge(i.PRs[0])
	effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
	if _, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "draft", Kind: "set_draft"}); err != nil {
		t.Fatal(err)
	}
	if len(forge.bodies) != 0 {
		t.Fatalf("set_draft wrote a description: %q", forge.bodies)
	}
	if len(forge.mutations) != 1 || !forge.mutations[0] {
		t.Fatalf("set_draft mutations=%v", forge.mutations)
	}
}

func TestFactoryEffectReadyRefusesForgeWithoutDescriptionCapability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rounds []FactoryRound
	}{
		{"narrated round", []FactoryRound{{Number: 1, HeadSHA: "old", Implementation: &FactoryReport{OperationID: "op", Outcome: "complete", Narration: &FactoryNarration{Issue: "i", Approach: "a"}}}}},
		{"missing narration too", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := narratedItem()
			i.Rounds = tc.rounds
			forge := newFactoryForgeWithoutDescription(i.PRs[0])
			effects := &factoryEffects{openForge: openForgeWith(forge, forge)}
			_, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
			if !errors.Is(err, ErrFactoryRefused) || !strings.Contains(err.Error(), "pull request descriptions") {
				t.Fatalf("reason does not name the unsupported capability: %v", err)
			}
			if len(forge.mutations) != 0 {
				t.Fatalf("mutation without the capability: %v", forge.mutations)
			}
		})
	}
}
