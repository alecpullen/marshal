package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func newTestState() *session.State {
	return session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
}

func TestSkillLoadToolSuccess(t *testing.T) {
	idx := NewIndex()
	idx.skills["debug"] = Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Risk:        "read_only",
		Body:        "# Debug\n\nReproduce, isolate, fix.\n",
	}

	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, ok := reg.Lookup("skill.load")
	if !ok {
		t.Fatal("skill.load tool not registered")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("Risk = %s, want read_only", tool.Risk)
	}
	if tool.Cacheable {
		t.Fatal("skill.load should not be cacheable")
	}

	result, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_1",
		Name: "skill.load",
		Args: []byte(`{"name": "debug"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.Summary == "" {
		t.Fatal("expected summary in result")
	}

	if !state.HasActiveSkill("debug") {
		t.Fatal("expected debug to be active after load")
	}

	msgs := state.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages length = %d, want 2 (hidden body + visible tag)", len(msgs))
	}
	if msgs[0].Role != session.RoleSystem {
		t.Fatalf("Message role = %q, want system", msgs[0].Role)
	}
	if msgs[0].ContentType != session.ContentTypeSkillBody {
		t.Fatalf("Body content type = %q, want skill_body", msgs[0].ContentType)
	}
	if msgs[1].ContentType != session.ContentTypeSkill || msgs[1].Content != "debug" {
		t.Fatalf("Tag message = (%q, %q), want (skill, debug)", msgs[1].ContentType, msgs[1].Content)
	}
	// Content should be wrapped in a procedure marker.
	if !strings.Contains(msgs[0].Content, "procedure to follow") {
		t.Fatalf("Message content missing procedure marker:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "```") {
		t.Fatalf("Message content missing fenced block:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "# Debug\n\nReproduce, isolate, fix.") {
		t.Fatalf("Message content missing skill body:\n%s", msgs[0].Content)
	}
}

func TestSkillLoadToolUnknownName(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_1",
		Name: "skill.load",
		Args: []byte(`{"name": "nonexistent"}`),
	})
	if err == nil {
		t.Fatal("expected error for unknown skill name")
	}
}

func TestSkillLoadToolAlreadyActive(t *testing.T) {
	idx := NewIndex()
	idx.skills["debug"] = Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Body:        "# Debug\n\nBody.\n",
	}

	state := newTestState()
	state.ActivateSkill("debug")

	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	result, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_2",
		Name: "skill.load",
		Args: []byte(`{"name": "debug"}`),
	})
	if err != nil {
		t.Fatalf("repeat load should succeed as a re-fetch, got: %v", err)
	}
	if result.Summary == "" {
		t.Fatal("expected summary in result")
	}
	if got := state.SkillBodyAge("debug"); got != 0 {
		t.Errorf("repeat load must reset body age, got %d", got)
	}
}

func TestSkillLoadToolInvalidArgs(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_4",
		Name: "skill.load",
		Args: []byte(`not json`),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON args")
	}
}

func TestSkillBodyIsWrappedAsReference(t *testing.T) {
	idx := NewIndex()
	idx.skills["test-skill"] = Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Body:        "# Test\n\nThis is the skill body.\n",
	}

	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_ref",
		Name: "skill.load",
		Args: []byte(`{"name": "test-skill"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	msgs := state.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages length = %d, want 2 (hidden body + visible tag)", len(msgs))
	}

	content := msgs[0].Content
	// Must contain a fenced block (triple backticks).
	if !strings.Contains(content, "```") {
		t.Fatalf("expected fenced block (triple backticks) in content:\n%s", content)
	}
	// Must contain a "procedure to follow" marker.
	if !strings.Contains(content, "procedure to follow") {
		t.Fatalf("expected 'procedure to follow' marker in content:\n%s", content)
	}
	// Must contain the skill name.
	if !strings.Contains(content, "test-skill") {
		t.Fatalf("expected skill name in content:\n%s", content)
	}
	// Must contain the original body text.
	if !strings.Contains(content, "# Test\n\nThis is the skill body.") {
		t.Fatalf("expected original skill body in content:\n%s", content)
	}
}

func TestSkillLoadToolMissingNameArg(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_5",
		Name: "skill.load",
		Args: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for missing name arg")
	}
}

func TestSkillLoadBudgetEvictsOldest(t *testing.T) {
	idx := NewIndex()
	for _, name := range []string{"one", "two", "three"} {
		idx.skills[name] = Skill{Name: name, Description: "d", Body: "body " + name}
	}
	state := newTestState()
	state.Config.Skills.MaxActive = 2

	if err := LoadSkillIntoSession(idx, state, "one"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := LoadSkillIntoSession(idx, state, "two"); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if err := LoadSkillIntoSession(idx, state, "three"); err != nil {
		t.Fatalf("third load should evict oldest, not error: %v", err)
	}
	if state.HasActiveSkill("one") {
		t.Fatal("oldest skill should have been evicted")
	}
	if !state.HasActiveSkill("two") || !state.HasActiveSkill("three") {
		t.Fatal("two and three should still be active")
	}
}

func TestSkillLoadBudgetIgnoresAutoloaded(t *testing.T) {
	idx := NewIndex()
	for _, name := range []string{"auto", "one"} {
		idx.skills[name] = Skill{Name: name, Description: "d", Body: "body " + name}
	}
	state := newTestState()
	state.Config.Skills.MaxActive = 1
	state.Config.Skills.Autoload = []string{"auto"}

	if err := LoadSkillIntoSessionQuiet(idx, state, "auto"); err != nil {
		t.Fatalf("autoload: %v", err)
	}
	// The autoloaded skill must not consume the explicit-load budget.
	if err := LoadSkillIntoSession(idx, state, "one"); err != nil {
		t.Fatalf("explicit load under budget: %v", err)
	}
}

func TestSkillLoadBudgetZeroUnlimited(t *testing.T) {
	idx := NewIndex()
	for _, name := range []string{"a", "b", "c", "d"} {
		idx.skills[name] = Skill{Name: name, Description: "d", Body: "body " + name}
	}
	state := newTestState()
	state.Config.Skills.MaxActive = 0
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := LoadSkillIntoSession(idx, state, name); err != nil {
			t.Fatalf("unlimited load %s: %v", name, err)
		}
	}
}

// A repeat skill.load is the re-fetch path: the body ages out of the wire
// (internal/agent/route.go appendSkillBodies), so asking again must resend
// it rather than error.
func TestRepeatLoadResetsBodyAgeInsteadOfErroring(t *testing.T) {
	state := newTestState()
	idx := NewIndex()
	idx.Set("alpha", Skill{Name: "alpha", Description: "d", Body: "body"})

	sl := NewSkillLoader(idx, state)
	if err := sl.Load("alpha", false); err != nil {
		t.Fatalf("first load: %v", err)
	}
	state.TickSkillBodyAges()
	state.TickSkillBodyAges()

	if err := sl.Load("alpha", false); err != nil {
		t.Fatalf("repeat load must succeed as a re-fetch, got: %v", err)
	}
	if got := state.SkillBodyAge("alpha"); got != 0 {
		t.Errorf("repeat load must reset body age, got %d", got)
	}
}

func TestUnloadDeactivatesSkill(t *testing.T) {
	state := newTestState()
	idx := NewIndex()
	idx.Set("alpha", Skill{Name: "alpha", Description: "d", Body: "body"})
	sl := NewSkillLoader(idx, state)
	if err := sl.Load("alpha", false); err != nil {
		t.Fatalf("load: %v", err)
	}

	if err := sl.Unload("alpha"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if state.HasActiveSkill("alpha") {
		t.Error("alpha still active after unload")
	}
	if err := sl.Unload("alpha"); err == nil {
		t.Error("unloading an inactive skill should error")
	}
}

// Hitting max_active must evict the oldest skill, not refuse the new one.
// The old behaviour returned an error, which stranded the model with a set
// of skills it could not change.
func TestMaxActiveEvictsOldestInsteadOfErroring(t *testing.T) {
	state := newTestState()
	state.Config.Skills.MaxActive = 2
	idx := NewIndex()
	for _, n := range []string{"first", "second", "third"} {
		idx.Set(n, Skill{Name: n, Description: "d", Body: "body"})
	}
	sl := NewSkillLoader(idx, state)

	for _, n := range []string{"first", "second", "third"} {
		if err := sl.Load(n, false); err != nil {
			t.Fatalf("load %s: %v", n, err)
		}
	}

	if state.HasActiveSkill("first") {
		t.Error("oldest skill should have been evicted")
	}
	for _, n := range []string{"second", "third"} {
		if !state.HasActiveSkill(n) {
			t.Errorf("%s should still be active", n)
		}
	}
}

// Auto/quiet loads previously bypassed the cap entirely, so the ranker could
// activate without limit.
func TestQuietLoadsCountAgainstMaxActive(t *testing.T) {
	state := newTestState()
	state.Config.Skills.MaxActive = 1
	idx := NewIndex()
	idx.Set("a", Skill{Name: "a", Description: "d", Body: "body"})
	idx.Set("b", Skill{Name: "b", Description: "d", Body: "body"})
	sl := NewSkillLoader(idx, state)

	if err := sl.Load("a", true); err != nil {
		t.Fatalf("quiet load a: %v", err)
	}
	if err := sl.Load("b", true); err != nil {
		t.Fatalf("quiet load b: %v", err)
	}

	if len(state.ActiveSkills()) != 1 {
		t.Fatalf("active = %v, want exactly 1 under max_active=1", state.ActiveSkills())
	}
}

// Autoloaded skills are user-configured always-on and must survive eviction.
func TestAutoloadedSkillsAreNotEvicted(t *testing.T) {
	state := newTestState()
	state.Config.Skills.MaxActive = 1
	state.Config.Skills.Autoload = []string{"pinned"}
	idx := NewIndex()
	idx.Set("pinned", Skill{Name: "pinned", Description: "d", Body: "body"})
	idx.Set("other", Skill{Name: "other", Description: "d", Body: "body"})
	sl := NewSkillLoader(idx, state)

	if err := sl.Load("pinned", true); err != nil {
		t.Fatalf("load pinned: %v", err)
	}
	if err := sl.Load("other", false); err != nil {
		t.Fatalf("load other: %v", err)
	}

	if !state.HasActiveSkill("pinned") {
		t.Error("autoloaded skill must never be evicted")
	}
}
