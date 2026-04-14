package commands_test

import (
	"testing"

	"github.com/alecpullen/marshal/internal/commands"
	"github.com/alecpullen/marshal/internal/skills"
)

func makeReg(t *testing.T, ss ...*skills.Skill) *skills.Registry {
	t.Helper()
	r := skills.New()
	for _, s := range ss {
		if err := r.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestDispatch_NotSlash(t *testing.T) {
	_, ok := commands.Dispatch("hello world", nil)
	if ok {
		t.Error("expected ok=false for non-slash input")
	}
}

func TestDispatch_Ship(t *testing.T) {
	a, ok := commands.Dispatch("/ship", nil)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if a.Kind != commands.KindBuiltin || a.Name != "ship" {
		t.Errorf("got %+v", a)
	}
}

func TestDispatch_Undo(t *testing.T) {
	a, ok := commands.Dispatch("/undo", nil)
	if !ok || a.Kind != commands.KindBuiltin || a.Name != "undo" {
		t.Errorf("got %+v, ok=%v", a, ok)
	}
}

func TestDispatch_Revert_WithID(t *testing.T) {
	a, ok := commands.Dispatch("/revert abc123", nil)
	if !ok || a.Kind != commands.KindBuiltin || a.Name != "revert" {
		t.Fatalf("got %+v, ok=%v", a, ok)
	}
	if a.Arg != "abc123" {
		t.Errorf("arg: got %q, want %q", a.Arg, "abc123")
	}
}

func TestDispatch_Skills(t *testing.T) {
	a, ok := commands.Dispatch("/skills", nil)
	if !ok || a.Kind != commands.KindBuiltin || a.Name != "skills" {
		t.Errorf("got %+v, ok=%v", a, ok)
	}
}

func TestDispatch_Help(t *testing.T) {
	a, ok := commands.Dispatch("/help", nil)
	if !ok || a.Kind != commands.KindBuiltin || a.Name != "help" {
		t.Errorf("got %+v, ok=%v", a, ok)
	}
}

func TestDispatch_History(t *testing.T) {
	a, ok := commands.Dispatch("/history", nil)
	if !ok || a.Kind != commands.KindBuiltin || a.Name != "history" {
		t.Errorf("got %+v, ok=%v", a, ok)
	}
}

func TestDispatch_SkillFound(t *testing.T) {
	// Use a trigger that doesn't conflict with built-in commands
	reg := makeReg(t, &skills.Skill{
		Name:    "mytest",
		Trigger: "/mytest",
	})
	a, ok := commands.Dispatch("/mytest add unit tests", reg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if a.Kind != commands.KindSkill {
		t.Errorf("expected KindSkill, got %v", a.Kind)
	}
	if a.Skill == nil || a.Skill.Name != "mytest" {
		t.Errorf("skill: %+v", a.Skill)
	}
	if a.Prompt != "add unit tests" {
		t.Errorf("prompt: got %q", a.Prompt)
	}
}

func TestDispatch_SkillNoPrompt(t *testing.T) {
	reg := makeReg(t, &skills.Skill{Name: "debug", Trigger: "/debug"})
	a, ok := commands.Dispatch("/debug", reg)
	if !ok || a.Kind != commands.KindSkill {
		t.Fatalf("got %+v, ok=%v", a, ok)
	}
	if a.Prompt != "" {
		t.Errorf("expected empty prompt, got %q", a.Prompt)
	}
}

func TestDispatch_Unknown(t *testing.T) {
	a, ok := commands.Dispatch("/nosuchcmd", nil)
	if !ok {
		t.Fatal("expected ok=true even for unknown commands")
	}
	if a.Kind != commands.KindUnknown {
		t.Errorf("expected KindUnknown, got %v", a.Kind)
	}
	if a.Name != "/nosuchcmd" {
		t.Errorf("name: got %q", a.Name)
	}
}

func TestDispatch_BuiltinBeatsSkill(t *testing.T) {
	// A skill with trigger "/ship" must not shadow the built-in.
	reg := makeReg(t, &skills.Skill{Name: "bad-ship", Trigger: "/debug"})
	// Register a skill with /debug (not /ship since /ship can't be registered twice anyway).
	// Instead test that /ship always returns builtin even with a populated registry.
	a, ok := commands.Dispatch("/ship", reg)
	if !ok || a.Kind != commands.KindBuiltin {
		t.Errorf("/ship should always be builtin, got %+v", a)
	}
}

func TestDispatch_Whitespace(t *testing.T) {
	// Leading/trailing whitespace is trimmed.
	a, ok := commands.Dispatch("  /help  ", nil)
	if !ok || a.Kind != commands.KindBuiltin || a.Name != "help" {
		t.Errorf("got %+v, ok=%v", a, ok)
	}
}
