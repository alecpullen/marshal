package bridge

import (
	"context"
	"errors"
	"testing"
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

type racingFactoryForge struct {
	pr                 FactoryPR
	mutations          []bool
	failCompensation   bool
	retargetAfterReady bool
}

func (f *racingFactoryForge) ObserveFactoryCI(context.Context, Repo, string, Credential) (FactoryCI, error) {
	return FactoryCI{HeadSHA: "old", Complete: true}, nil
}
func (f *racingFactoryForge) FindFactoryPR(context.Context, Repo, string, string, Credential) (*FactoryPR, error) {
	return &f.pr, nil
}
func (f *racingFactoryForge) GetFactoryPR(context.Context, Repo, int, Credential) (FactoryPR, error) {
	return f.pr, nil
}
func (f *racingFactoryForge) SetFactoryDraft(_ context.Context, _ Repo, _ FactoryPR, draft bool, _ Credential) error {
	f.mutations = append(f.mutations, draft)
	if draft && f.failCompensation {
		return errors.New("compensation failed")
	}
	f.pr.Draft = draft
	if !draft {
		if f.retargetAfterReady {
			f.pr.BaseRef = "release"
		} else {
			f.pr.HeadSHA = "new"
		}
	}
	return nil
}
func TestFactoryEffectReadyRejectsRetargetBeforeMutation(t *testing.T) {
	for _, changed := range []string{"head", "base", "identity"} {
		t.Run(changed, func(t *testing.T) {
			i := loopItem()
			p := FactoryPR{Number: 1, NodeID: "node", HeadRef: i.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "old", BaseSHA: "base"}
			i.PRs = []FactoryPR{p}
			forge := &racingFactoryForge{pr: p}
			switch changed {
			case "head":
				forge.pr.HeadRef = "elsewhere"
			case "base":
				forge.pr.BaseRef = "release"
			case "identity":
				forge.pr.NodeID = "different"
			}
			effects := &factoryEffects{openForge: func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
				return Repo{}, nil, forge, Credential{}, nil
			}}
			_, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
			if !errors.Is(err, ErrFactoryRefused) || len(forge.mutations) != 0 {
				t.Fatalf("precheck: %v mutations=%v", err, forge.mutations)
			}
		})
	}
}
func TestFactoryEffectReadyRetargetAfterMutationCompensates(t *testing.T) {
	i := loopItem()
	p := FactoryPR{Number: 1, NodeID: "node", HeadRef: i.Branch, BaseRef: "main", State: "open", Draft: true, HeadSHA: "old", BaseSHA: "base"}
	i.PRs = []FactoryPR{p}
	forge := &racingFactoryForge{pr: p, retargetAfterReady: true}
	effects := &factoryEffects{openForge: func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
		return Repo{}, nil, forge, Credential{}, nil
	}}
	out, err := effects.Execute(context.Background(), i, FactoryEffect{ID: "ready", Kind: "set_ready", ExpectedHead: "old", ExpectedBase: "base"})
	if err != nil || !out.Confirmed || out.Error == "" || len(forge.mutations) != 2 || forge.mutations[0] || !forge.mutations[1] {
		t.Fatalf("postcheck: %+v %v mutations=%v", out, err, forge.mutations)
	}
}
func TestFactoryEffectReadyRaceCompensates(t *testing.T) {
	for _, fail := range []bool{false, true} {
		forge := &racingFactoryForge{pr: FactoryPR{Number: 1, HeadRef: "marshal/factory/item", BaseRef: "main", State: "open", Draft: true, HeadSHA: "old", BaseSHA: "base"}, failCompensation: fail}
		effects := &factoryEffects{openForge: func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
			return Repo{}, nil, forge, Credential{}, nil
		}}
		i := loopItem()
		i.PRs = []FactoryPR{forge.pr}
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
