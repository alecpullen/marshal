package app

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/skills"
)

func autoloadTestState(t *testing.T, cfg config.Config) *session.State {
	t.Helper()
	return session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The entry-point skill of a suite has to reach context without the model
// choosing to load it — otherwise nothing ever tells the model that skills
// are worth reaching for in the first place.
func TestAutoloadSkillsInjectsConfiguredSkills(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("using-superpowers", skills.Skill{
		Name: "using-superpowers", Description: "how to use skills", Body: "ALWAYS check for a skill first.",
	})
	cfg := config.Default()
	cfg.Skills.Autoload = []string{"using-superpowers"}
	state := autoloadTestState(t, cfg)

	autoloadSkills(cfg, idx, state, discardLogger())

	if !state.HasActiveSkill("using-superpowers") {
		t.Fatal("configured skill should be active before the first turn")
	}
	var found bool
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "ALWAYS check for a skill first.") {
			found = true
		}
	}
	if !found {
		t.Fatal("skill body should be injected into the transcript")
	}
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSkill {
			t.Fatalf("autoload should not post ContentTypeSkill tag, got %q", m.Content)
		}
	}
	bodyFound := false
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSkillBody {
			bodyFound = true
		}
	}
	if !bodyFound {
		t.Fatal("autoload should still inject the skill body")
	}
}

func TestAutoloadSkillsQuietButExplicitSkillLoadPostsTag(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("using-superpowers", skills.Skill{
		Name: "using-superpowers", Description: "how to use skills", Body: "ALWAYS check for a skill first.",
	})
	cfg := config.Default()
	cfg.Skills.Autoload = []string{"using-superpowers"}
	state := autoloadTestState(t, cfg)

	autoloadSkills(cfg, idx, state, discardLogger())
	if err := skills.LoadSkillIntoSession(idx, state, "using-superpowers"); err == nil {
		t.Fatal("explicit skill.load after autoload should fail because already active")
	}

	// Reset and load explicitly to verify the tag path.
	state = autoloadTestState(t, cfg)
	if err := skills.LoadSkillIntoSession(idx, state, "using-superpowers"); err != nil {
		t.Fatalf("explicit skill.load: %v", err)
	}
	var foundTag bool
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSkill && m.Content == "using-superpowers" {
			foundTag = true
		}
	}
	if !foundTag {
		t.Fatal("explicit skill.load should post ContentTypeSkill tag")
	}
}

// A config entry that outlives its skill must not stop the session starting.
func TestAutoloadSkillsSkipsUnknownAndEmptyNames(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("real", skills.Skill{Name: "real", Description: "d", Body: "b"})
	cfg := config.Default()
	cfg.Skills.Autoload = []string{"uninstalled", "  ", "real"}
	state := autoloadTestState(t, cfg)

	autoloadSkills(cfg, idx, state, discardLogger())

	if !state.HasActiveSkill("real") {
		t.Fatal("a bad entry must not prevent later entries from loading")
	}
	if state.HasActiveSkill("uninstalled") {
		t.Fatal("unknown skill should not be marked active")
	}
}

func TestAutoloadSkillsNoopWhenUnconfigured(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("real", skills.Skill{Name: "real", Description: "d", Body: "b"})
	state := autoloadTestState(t, config.Default())

	autoloadSkills(config.Default(), idx, state, discardLogger())

	if state.HasActiveSkill("real") {
		t.Fatal("nothing should autoload without config")
	}
}
