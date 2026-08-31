package agent

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
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

	r.computeSkillHints(context.Background(), "fix the flaky test", Classify("fix the flaky test"))

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

	r.computeSkillHints(context.Background(), "goal", ClassQuestion)

	if len(r.skillHints) != skillHintMaxK {
		t.Fatalf("hints = %v, want %d", r.skillHints, skillHintMaxK)
	}
}

// An unconfigured embedder must be a silent no-op for the ranked path —
// but class defaults still fire, so a question-class goal with no
// investigation shape still hints nothing.
func TestComputeSkillHintsNoEmbedderIsNoOp(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("a", skills.Skill{Name: "a", Description: "alpha"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "goal", ClassQuestion)

	if len(r.skillHints) != 0 {
		t.Fatalf("hints = %v, want none without an embedder", r.skillHints)
	}
}

// The embeddings-free fallback: an edit-class turn always sees the tdd and
// verification suggestions, no embedder required.
func TestComputeSkillHintsEditClassDefaultsWithoutEmbedder(t *testing.T) {
	idx := skills.NewIndex()
	for _, name := range []string{"test-driven-development", "verification-before-completion"} {
		idx.Set(name, skills.Skill{Name: name, Description: "d"})
	}
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "fix the flaky test", ClassEdit)

	want := []string{"test-driven-development", "verification-before-completion"}
	if !reflect.DeepEqual(r.skillHints, want) {
		t.Fatalf("hints = %v, want %v", r.skillHints, want)
	}
}

// An investigation-shaped question hints systematic-debugging with no
// embedder — the exact miss from the config-reproducibility investigation.
func TestComputeSkillHintsQuestionInvestigationDefaultsWithoutEmbedder(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("systematic-debugging", skills.Skill{Name: "systematic-debugging", Description: "d"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "why does X behave inconsistently across machines", ClassQuestion)

	want := []string{"systematic-debugging"}
	if !reflect.DeepEqual(r.skillHints, want) {
		t.Fatalf("hints = %v, want %v", r.skillHints, want)
	}
}

// A plain question ("what does this function return") is not an
// investigation and must not hint the debugging skill.
func TestComputeSkillHintsPlainQuestionGetsNoDefaults(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("systematic-debugging", skills.Skill{Name: "systematic-debugging", Description: "d"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "what does this function return", ClassQuestion)

	if len(r.skillHints) != 0 {
		t.Fatalf("hints = %v, want none for a plain question", r.skillHints)
	}
}

// A class default naming an uninstalled skill is dropped, not hinted.
func TestComputeSkillHintsDefaultsRequireInstalledSkill(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("a", skills.Skill{Name: "a", Description: "alpha"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "fix the flaky test", ClassEdit)

	if len(r.skillHints) != 0 {
		t.Fatalf("hints = %v, want none (defaults not installed)", r.skillHints)
	}
}

// With an embedder, ranked hints win the leading slots; class defaults
// fill what remains, capped at skillHintMaxK.
func TestComputeSkillHintsRankedWinsOverDefaults(t *testing.T) {
	_, fixtureCandidates := rankFixture()
	// Own embedder: the fixture's vectors plus a below-threshold vector for
	// the extra skill the test adds to the index (the fake embedder errors
	// on unknown texts, which would silently kill the whole ranked path).
	e := &fakeSkillEmbedder{vecs: map[string][]float32{
		"fix the flaky test":                    {1, 0, 0},
		"tdd: test-driven development workflow": {0.9, 0.1, 0},
		"lint: bulk lint fixing":                {0.6, 0.8, 0},
		"cooking: recipes for dinner":           {0, 1, 0},
		"test-driven-development: d":            {0, 0, 1}, // cos = 0, not ranked
	}}
	idx := skills.NewIndex()
	for _, c := range fixtureCandidates {
		idx.Set(c.Name, c)
	}
	idx.Set("test-driven-development", skills.Skill{Name: "test-driven-development", Description: "d"})
	r := hintRunner(t, e, idx)

	// "fix the flaky test" classifies as edit: ranked [tdd lint] take the
	// first two slots, the tdd default fills the third, and the
	// verification default is dropped by the cap.
	r.computeSkillHints(context.Background(), "fix the flaky test", ClassEdit)

	want := []string{"tdd", "lint", "test-driven-development"}
	if !reflect.DeepEqual(r.skillHints, want) {
		t.Fatalf("hints = %v, want %v", r.skillHints, want)
	}
}

// Hint generation is observable: a turn that produces hints logs one line
// naming them, so misses can be tuned from the session log.
func TestComputeSkillHintsLogsHintShortlist(t *testing.T) {
	var buf bytes.Buffer
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	idx := skills.NewIndex()
	for _, name := range []string{"test-driven-development", "verification-before-completion"} {
		idx.Set(name, skills.Skill{Name: name, Description: "d"})
	}
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "fix the flaky test", ClassEdit)

	logged := buf.String()
	if !strings.Contains(logged, "skill hints") || !strings.Contains(logged, "test-driven-development") {
		t.Fatalf("expected a skill hints log line naming the hint, got:\n%s", logged)
	}
	if !strings.Contains(logged, "source=defaults") {
		t.Fatalf("no-embedder turn should log source=defaults, got:\n%s", logged)
	}
}

// A turn with no hints logs nothing — one line per plain question would be
// noise for zero value.
func TestComputeSkillHintsNoLogWhenNoHints(t *testing.T) {
	var buf bytes.Buffer
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	idx := skills.NewIndex()
	idx.Set("a", skills.Skill{Name: "a", Description: "alpha"})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "what does this function return", ClassQuestion)

	if buf.Len() != 0 {
		t.Fatalf("empty hint set must not log, got:\n%s", buf.String())
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

	r.computeSkillHints(context.Background(), "fix the flaky test", Classify("fix the flaky test"))

	for _, h := range r.skillHints {
		if h == "tdd" {
			t.Fatal("hinted an already-active skill")
		}
	}
}

// Command-class turns (slash commands, /plan, /test) never get class
// defaults — classDefaultHints has no ClassCommand case, and this pins
// that contract rather than relying on the switch's fall-through.
func TestComputeSkillHintsCommandClassGetsNoDefaults(t *testing.T) {
	idx := skills.NewIndex()
	for _, name := range []string{"test-driven-development", "verification-before-completion", "systematic-debugging"} {
		idx.Set(name, skills.Skill{Name: name, Description: "d"})
	}
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}

	r.computeSkillHints(context.Background(), "fix the flaky test", ClassCommand)

	if len(r.skillHints) != 0 {
		t.Fatalf("hints = %v, want none for command class", r.skillHints)
	}
}

// A class default naming an already-active skill is dropped — hinting a
// skill whose body is already on the wire is noise. (The ranked path's
// equivalent is TestComputeSkillHintsSkipsActiveSkills.)
func TestComputeSkillHintsActiveDefaultDropped(t *testing.T) {
	idx := skills.NewIndex()
	for _, name := range []string{"test-driven-development", "verification-before-completion"} {
		idx.Set(name, skills.Skill{Name: name, Description: "d"})
	}
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	r := &Runner{SkillIndex: idx, State: state}
	r.State.ActivateSkill("test-driven-development")

	r.computeSkillHints(context.Background(), "fix the flaky test", ClassEdit)

	want := []string{"verification-before-completion"}
	if !reflect.DeepEqual(r.skillHints, want) {
		t.Fatalf("hints = %v, want %v", r.skillHints, want)
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

func TestAppendSkillBodiesSendsFullBodyWhenYoung(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "d", Body: "FULL BODY TEXT"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.Config.Skills.BodyFullTurns = 3
	state.ActivateSkill("tdd")
	r := &Runner{SkillIndex: idx, State: state}

	got := r.appendSkillBodies(nil)

	if len(got) != 1 || !strings.Contains(got[0].Content, "FULL BODY TEXT") {
		t.Fatalf("young skill must send its full body, got %+v", got)
	}
}

func TestAppendSkillBodiesSendsStubWhenAged(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "d", Body: "FULL BODY TEXT"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.Config.Skills.BodyFullTurns = 2
	state.ActivateSkill("tdd")
	state.TickSkillBodyAges()
	state.TickSkillBodyAges()
	r := &Runner{SkillIndex: idx, State: state}

	got := r.appendSkillBodies(nil)

	if len(got) != 1 {
		t.Fatalf("aged skill must still emit a message, got %d", len(got))
	}
	if strings.Contains(got[0].Content, "FULL BODY TEXT") {
		t.Error("aged skill must not resend its full body")
	}
	if !strings.Contains(got[0].Content, "skill.load") {
		t.Error("stub must tell the model how to get the body back")
	}
	if !strings.Contains(got[0].Content, "tdd") {
		t.Error("stub must name the skill")
	}
}

func TestBodyFullTurnsZeroAlwaysSendsFullBody(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "d", Body: "FULL BODY TEXT"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.Config.Skills.BodyFullTurns = 0
	state.ActivateSkill("tdd")
	for i := 0; i < 20; i++ {
		state.TickSkillBodyAges()
	}
	r := &Runner{SkillIndex: idx, State: state}

	got := r.appendSkillBodies(nil)

	if len(got) != 1 || !strings.Contains(got[0].Content, "FULL BODY TEXT") {
		t.Fatal("BodyFullTurns=0 must always send the full body")
	}
}
