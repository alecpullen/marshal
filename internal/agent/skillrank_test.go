package agent

import (
	"context"
	"fmt"
	"testing"

	"marshal/internal/skills"
)

type fakeSkillEmbedder struct {
	vecs      map[string][]float32
	calls     int
	lastTexts []string
}

func (f *fakeSkillEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.lastTexts = append([]string(nil), texts...)
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		v, ok := f.vecs[txt]
		if !ok {
			return nil, fmt.Errorf("fakeSkillEmbedder: no vector for %q", txt)
		}
		out[i] = v
	}
	return out, nil
}

func (f *fakeSkillEmbedder) Model() string { return "fake-embed" }
func (f *fakeSkillEmbedder) Dims() int     { return 3 }

func rankFixture() (*fakeSkillEmbedder, []skills.Skill) {
	e := &fakeSkillEmbedder{vecs: map[string][]float32{
		"fix the flaky test":                    {1, 0, 0},
		"tdd: test-driven development workflow": {0.9, 0.1, 0}, // cos ≈ 0.994
		"lint: bulk lint fixing":                {0.6, 0.8, 0}, // cos = 0.6
		"cooking: recipes for dinner":           {0, 1, 0},     // cos = 0
	}}
	candidates := []skills.Skill{
		{Name: "tdd", Description: "test-driven development workflow"},
		{Name: "cooking", Description: "recipes for dinner"},
		{Name: "lint", Description: "bulk lint fixing"},
	}
	return e, candidates
}

func TestSkillRankerSelectsAboveThresholdBestFirst(t *testing.T) {
	e, candidates := rankFixture()
	got := newSkillRanker().rank(context.Background(), e, candidates, "fix the flaky test")
	if len(got) != 2 || got[0] != "tdd" || got[1] != "lint" {
		t.Fatalf("rank = %v, want [tdd lint]", got)
	}
}

func TestSkillRankerCapsAtMaxK(t *testing.T) {
	e := &fakeSkillEmbedder{vecs: map[string][]float32{
		"goal":     {1, 0, 0},
		"a: alpha": {0.99, 0.1, 0},  // cos ≈ 0.995
		"b: beta":  {0.97, 0.24, 0}, // cos ≈ 0.971
		"c: gamma": {0.95, 0.31, 0}, // cos ≈ 0.951
	}}
	candidates := []skills.Skill{
		{Name: "a", Description: "alpha"},
		{Name: "b", Description: "beta"},
		{Name: "c", Description: "gamma"},
	}
	got := newSkillRanker().rank(context.Background(), e, candidates, "goal")
	if len(got) != skillHintMaxK || got[0] != "a" || got[1] != "b" {
		t.Fatalf("rank = %v, want top %d [a b]", got, skillHintMaxK)
	}
}

func TestSkillRankerBelowThresholdReturnsNil(t *testing.T) {
	e := &fakeSkillEmbedder{vecs: map[string][]float32{
		"goal":                  {1, 0, 0},
		"cooking: recipes":      {0, 1, 0},
		"gardening: composting": {0, 0.9, 0.1},
	}}
	candidates := []skills.Skill{
		{Name: "cooking", Description: "recipes"},
		{Name: "gardening", Description: "composting"},
	}
	if got := newSkillRanker().rank(context.Background(), e, candidates, "goal"); got != nil {
		t.Fatalf("rank = %v, want nil (all below threshold)", got)
	}
}

func TestSkillRankerCachesSkillVectors(t *testing.T) {
	e, candidates := rankFixture()
	r := newSkillRanker()
	r.rank(context.Background(), e, candidates, "fix the flaky test")
	r.rank(context.Background(), e, candidates, "fix the flaky test")
	if e.calls != 2 {
		t.Fatalf("Embed calls = %d, want 2 (goal is re-embedded per call)", e.calls)
	}
	if len(e.lastTexts) != 1 || e.lastTexts[0] != "fix the flaky test" {
		t.Fatalf("second call embedded %v, want only the goal (skill vectors cached)", e.lastTexts)
	}
}

func TestSkillRankerEmptyInputs(t *testing.T) {
	e, candidates := rankFixture()
	r := newSkillRanker()
	if got := r.rank(context.Background(), e, candidates, "  "); got != nil {
		t.Fatalf("blank goal: rank = %v, want nil", got)
	}
	if got := r.rank(context.Background(), nil, candidates, "fix the flaky test"); got != nil {
		t.Fatalf("nil embedder: rank = %v, want nil", got)
	}
	if got := r.rank(context.Background(), e, nil, "fix the flaky test"); got != nil {
		t.Fatalf("no candidates: rank = %v, want nil", got)
	}
	if e.calls != 0 {
		t.Fatalf("Embed calls = %d, want 0 for empty inputs", e.calls)
	}
}
