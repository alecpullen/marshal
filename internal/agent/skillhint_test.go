package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
)

func hintRunner(t *testing.T, e *fakeSkillEmbedder, idx *skills.Index) *Runner {
	t.Helper()
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	return &Runner{
		SkillIndex:    idx,
		SkillEmbedder: e,
		State:         state,
	}
}

// The ranker must never activate a skill: gating on cosine similarity is
// what this change removes. Measured separation between true positives and
// true negatives is negative, so a hint is the only honest output.
func TestComputeSkillHintsDoesNotActivateSkills(t *testing.T) {
	e, candidates := rankFixture()
	idx := skills.NewIndex()
	for _, c := range candidates {
		idx.Set(c.Name, c)
	}
	r := hintRunner(t, e, idx)

	r.computeSkillHints(context.Background(), "fix the flaky test")

	if got := r.State.ActiveSkills(); len(got) != 0 {
		t.Fatalf("computeSkillHints activated %v, want no activations", got)
	}
	if len(r.skillHints) == 0 {
		t.Fatal("computeSkillHints recorded no hints")
	}
	if r.skillHints[0] != "tdd" {
		t.Errorf("top hint = %q, want %q", r.skillHints[0], "tdd")
	}
}

func TestComputeSkillHintsCapsAtMaxK(t *testing.T) {
	e := &fakeSkillEmbedder{vecs: map[string][]float32{
		"goal":     {1, 0, 0},
		"a: alpha": {0.99, 0.1, 0},
		"b: beta":  {0.97, 0.24, 0},
		"c: gamma": {0.95, 0.31, 0},
		"d: delta": {0.93, 0.37, 0},
	}}
	idx := skills.NewIndex()
	for _, s := range []skills.Skill{
		{Name: "a", Description: "alpha"},
		{Name: "b", Description: "beta"},
		{Name: "c", Description: "gamma"},
		{Name: "d", Description: "delta"},
	} {
		idx.Set(s.Name, s)
	}
	r := hintRunner(t, e, idx)

	r.computeSkillHints(context.Background(), "goal")

	if len(r.skillHints) != skillHintMaxK {
		t.Fatalf("hints = %v, want %d", r.skillHints, skillHintMaxK)
	}
}

// An unconfigured embedder must be a silent no-op, never a turn failure.
func TestComputeSkillHintsNoEmbedderIsNoOp(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("a", skills.Skill{Name: "a", Description: "alpha"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "goal")

	if len(r.skillHints) != 0 {
		t.Fatalf("hints = %v, want none without an embedder", r.skillHints)
	}
}

// Already-active skills are not worth hinting — the model has the body.
func TestComputeSkillHintsSkipsActiveSkills(t *testing.T) {
	e, candidates := rankFixture()
	idx := skills.NewIndex()
	for _, c := range candidates {
		idx.Set(c.Name, c)
	}
	r := hintRunner(t, e, idx)
	r.State.ActivateSkill("tdd")

	r.computeSkillHints(context.Background(), "fix the flaky test")

	for _, h := range r.skillHints {
		if h == "tdd" {
			t.Fatal("hinted an already-active skill")
		}
	}
}

func TestBuildSkillHintMessageListsNamesAndDescriptions(t *testing.T) {
	msg, ok := BuildSkillHintMessage([]skills.Skill{
		{Name: "tdd", Description: "tests before implementation"},
		{Name: "debug", Description: "root-cause before fixes"},
	})
	if !ok {
		t.Fatal("BuildSkillHintMessage returned ok=false for two hints")
	}
	for _, want := range []string{"tdd", "tests before implementation", "debug", "root-cause before fixes"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("hint message missing %q\ngot: %s", want, msg.Content)
		}
	}
	if msg.Role != schema.RoleSystem {
		t.Errorf("hint role = %v, want system", msg.Role)
	}
}

func TestBuildSkillHintMessageEmptyIsNotOK(t *testing.T) {
	if _, ok := BuildSkillHintMessage(nil); ok {
		t.Fatal("empty hints must return ok=false so no message is inserted")
	}
}

// The hint varies per turn. It must never land in messages[0], which is the
// provider's cache prefix — see the ActiveSkills ordering comment in
// internal/app/session/session.go.
func TestAppendSkillHintNeverTouchesSystemPrompt(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "tests first"})
	r := &Runner{SkillIndex: idx, State: session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})}
	r.skillHints = []string{"tdd"}

	system := schema.ChatMessage{Role: schema.RoleSystem, Content: "SYSTEM PROMPT"}
	got := r.appendSkillHint([]schema.ChatMessage{system})

	if got[0].Content != "SYSTEM PROMPT" {
		t.Fatalf("messages[0] was modified: %q", got[0].Content)
	}
	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(got))
	}
	if !strings.Contains(got[1].Content, "tdd") {
		t.Errorf("hint message missing the hinted skill: %q", got[1].Content)
	}
}

// A hint naming a skill that vanished from the index must not panic or
// produce an empty bullet.
func TestAppendSkillHintSkipsUnknownSkills(t *testing.T) {
	idx := skills.NewIndex()
	r := &Runner{SkillIndex: idx, State: session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})}
	r.skillHints = []string{"ghost"}

	system := schema.ChatMessage{Role: schema.RoleSystem, Content: "SYSTEM PROMPT"}
	got := r.appendSkillHint([]schema.ChatMessage{system})

	if len(got) != 1 {
		t.Fatalf("len(messages) = %d, want 1 (no hint message for unknown skills)", len(got))
	}
}
