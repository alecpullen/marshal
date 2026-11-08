package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/skills"
)

// skillFixture renders a skill file whose frontmatter name is deliberately
// independent of its filename, as the skills panel's fixture does.
func skillFixture(name, description string) string {
	return fmt.Sprintf("+++\nname = %q\ndescription = %q\n+++\n\n# %s\n", name, description, name)
}

// writeSkillEntry writes a top-level .md skill into dir and returns its path.
func writeSkillEntry(t *testing.T, dir, file, name, description string) string {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(skillFixture(name, description)), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", path, err)
	}
	return path
}

// skillStores creates the two skill stores a session reads: the user config's
// skills directory and the project's .marshal/skills.
func skillStores(t *testing.T, home, work string) (global, project string) {
	t.Helper()
	global = filepath.Join(config.UserDir(home), "skills")
	project = filepath.Join(work, ".marshal", "skills")
	for _, dir := range []string{global, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create skill store %s: %v", dir, err)
		}
	}
	return global, project
}

// skillSession builds a session over home and work whose skill index is idx. A
// nil idx leaves the session with no index, as a runtime that built none does.
func skillSession(t *testing.T, home, work string, idx *skills.Index) *Session {
	t.Helper()
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	deps := Deps{State: state, HomeDir: home}
	if idx != nil {
		deps.SkillIndex = func() *skills.Index { return idx }
	}
	_, sess := New(deps)
	return sess
}

// parseSkill reads and parses a skill file the way discovery does.
func parseSkill(t *testing.T, path string) skills.Skill {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill %s: %v", path, err)
	}
	sk, err := skills.Parse(string(raw))
	if err != nil {
		t.Fatalf("parse skill %s: %v", path, err)
	}
	return sk
}

// entryFor returns the listed entry of scope and source, failing when the
// listing holds none.
func entryFor(t *testing.T, entries []acpwire.SkillEntry, scope, source string) acpwire.SkillEntry {
	t.Helper()
	for _, e := range entries {
		if e.Scope == scope && e.Source == source {
			return e
		}
	}
	t.Fatalf("no %s entry %q in %+v", scope, source, entries)
	return acpwire.SkillEntry{}
}

// awaitChanged fails unless the session fires Changed within a short window.
func awaitChanged(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not fire")
	}
}
