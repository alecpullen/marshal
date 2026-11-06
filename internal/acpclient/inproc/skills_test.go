package inproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

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

// TestSkillsListsBothStoresWithLoadReasons pins the listing and its load
// reasons: an entry the session's index holds and has not loaded is loadable,
// while one the index lacks, and every entry of a session with no index, is
// not.
func TestSkillsListsBothStoresWithLoadReasons(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	global, project := skillStores(t, home, work)
	writeSkillEntry(t, global, "alpha.md", "alpha", "global fixture")
	writeSkillEntry(t, project, "beta.md", "beta", "project fixture")

	idx := skills.NewIndex()
	idx.Set("alpha", parseSkill(t, filepath.Join(global, "alpha.md")))
	sess := skillSession(t, home, work, idx)

	entries, err := sess.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Skills = %+v, want the global and the project entry", entries)
	}
	if blocked := entryFor(t, entries, "global", "alpha.md").LoadBlocked; blocked != "" {
		t.Errorf("global alpha LoadBlocked = %q, want \"\" (the index holds it)", blocked)
	}
	wantBeta := "the current session has no beta — restart Marshal to pick it up"
	if blocked := entryFor(t, entries, "project", "beta.md").LoadBlocked; blocked != wantBeta {
		t.Errorf("project beta LoadBlocked = %q, want %q", blocked, wantBeta)
	}

	// A session with no index at all blocks every entry, whatever it holds.
	bare := skillSession(t, home, work, nil)
	entries, err = bare.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills without an index: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Skills without an index = %+v, want both entries", entries)
	}
	wantNoIndex := "this session has no skill index — restart Marshal to build one"
	for _, e := range entries {
		if e.LoadBlocked != wantNoIndex {
			t.Errorf("%s/%s LoadBlocked = %q, want %q", e.Scope, e.Source, e.LoadBlocked, wantNoIndex)
		}
	}
}

// TestLoadSkillChecksTheStoreAgainstTheListing pins LoadSkill's disk and index
// checks: a file rewritten after the listing is refused with the
// changed-on-disk reason, a fresh listing loads it, and a second load of an
// active skill is refused rather than silently re-injected.
func TestLoadSkillChecksTheStoreAgainstTheListing(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	global, _ := skillStores(t, home, work)
	path := writeSkillEntry(t, global, "alpha.md", "alpha", "original body")

	idx := skills.NewIndex()
	idx.Set("alpha", parseSkill(t, path))
	sess := skillSession(t, home, work, idx)
	ctx := context.Background()

	if _, err := sess.Skills(ctx); err != nil {
		t.Fatalf("Skills: %v", err)
	}
	// Rewrite the file behind the listing's back. The same file is kept, so
	// what the seam notices is the content, not the identity.
	if err := os.WriteFile(path, []byte(skillFixture("alpha", "rewritten body")), 0o644); err != nil {
		t.Fatalf("rewrite skill: %v", err)
	}
	wantChanged := "alpha.md changed on disk — reopen the panel to pick up the new content"
	if _, err := sess.LoadSkill(ctx, "global", "alpha.md"); err == nil || err.Error() != wantChanged {
		t.Fatalf("LoadSkill after a rewrite = %v, want %q", err, wantChanged)
	}

	// A fresh listing sees the new content, and the index is brought up to it
	// as a restart would.
	idx.Set("alpha", parseSkill(t, path))
	if _, err := sess.Skills(ctx); err != nil {
		t.Fatalf("Skills after the rewrite: %v", err)
	}
	name, err := sess.LoadSkill(ctx, "global", "alpha.md")
	if err != nil {
		t.Fatalf("LoadSkill after a fresh listing: %v", err)
	}
	if name != "alpha" {
		t.Fatalf("LoadSkill name = %q, want alpha", name)
	}
	if !sess.state.HasActiveSkill("alpha") {
		t.Fatal("the loaded skill is not active in the session")
	}

	wantActive := "alpha is already active — its current body is kept until unloaded; unload it before loading another version"
	if _, err := sess.LoadSkill(ctx, "global", "alpha.md"); err == nil || err.Error() != wantActive {
		t.Fatalf("second LoadSkill = %v, want %q", err, wantActive)
	}
}

// TestRemoveSkillUsesTheListedSource pins removal by discovered source: two
// entries in one store share a frontmatter name, and removing one by its source
// leaves the other's file alone.
func TestRemoveSkillUsesTheListedSource(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	global, _ := skillStores(t, home, work)
	one := writeSkillEntry(t, global, "one.md", "shared", "first fixture")
	two := writeSkillEntry(t, global, "two.md", "shared", "second fixture")

	sess := skillSession(t, home, work, nil)
	ctx := context.Background()
	entries, err := sess.Skills(ctx)
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Skills = %+v, want both entries sharing a name", entries)
	}

	if err := sess.RemoveSkill(ctx, "global", "one.md"); err != nil {
		t.Fatalf("RemoveSkill: %v", err)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Errorf("removed entry's file still exists: %v", err)
	}
	if _, err := os.Stat(two); err != nil {
		t.Errorf("the sibling's file is gone: %v", err)
	}
}

// TestInstallSkillRefusals pins the three refusals install makes, in the
// panel's words.
func TestInstallSkillRefusals(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	sess := skillSession(t, home, work, nil)
	ctx := context.Background()

	cases := []struct {
		name    string
		source  string
		scope   string
		wantErr string
	}{
		{"blank source", "   ", "global", "enter a source first"},
		{"project scope on an untrusted session", filepath.Join(work, "source.md"), "project", "project scope needs a trusted folder"},
		{"unknown scope", filepath.Join(work, "source.md"), "elsewhere", `unknown skill scope "elsewhere"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sess.InstallSkill(ctx, tc.source, tc.scope)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("InstallSkill(%q, %q) = %v, want %q", tc.source, tc.scope, err, tc.wantErr)
			}
		})
	}
}

// TestSetSkillAutoloadWritesTheUserConfig pins autoload's persistence: the name
// goes into skills.autoload in the user config and into the live config, and
// turning it off removes it from both.
func TestSetSkillAutoloadWritesTheUserConfig(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	userConfig := filepath.Join(home, "config.toml")
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	_, sess := New(Deps{State: state, HomeDir: home, UserConfigPath: userConfig})
	ctx := context.Background()

	if err := sess.SetSkillAutoload(ctx, "alpha", true); err != nil {
		t.Fatalf("SetSkillAutoload(alpha, true): %v", err)
	}
	if got := userConfigAutoload(t, userConfig); !slices.Contains(got, "alpha") {
		t.Errorf("skills.autoload in the user config = %v, want it to hold alpha", got)
	}
	if got := sess.Config().Skills.Autoload; !slices.Contains(got, "alpha") {
		t.Errorf("Config().Skills.Autoload = %v, want it to hold alpha", got)
	}
	// Writing one name leaves the list's other entries alone.
	for _, name := range config.Default().Skills.Autoload {
		if got := userConfigAutoload(t, userConfig); !slices.Contains(got, name) {
			t.Errorf("skills.autoload in the user config = %v, want it to keep %q", got, name)
		}
	}

	if err := sess.SetSkillAutoload(ctx, "alpha", false); err != nil {
		t.Fatalf("SetSkillAutoload(alpha, false): %v", err)
	}
	if got := userConfigAutoload(t, userConfig); slices.Contains(got, "alpha") {
		t.Errorf("skills.autoload in the user config = %v, want no alpha", got)
	}
	if got := sess.Config().Skills.Autoload; slices.Contains(got, "alpha") {
		t.Errorf("Config().Skills.Autoload = %v, want no alpha", got)
	}
}

// userConfigAutoload reads skills.autoload out of a user config file.
func userConfigAutoload(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	var file struct {
		Skills struct {
			Autoload []string `toml:"autoload"`
		} `toml:"skills"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse user config: %v", err)
	}
	return file.Skills.Autoload
}

// TestSkillGateIsInTheStatus pins the gate's status projection: the switch, the
// sticky decisions with their deny counter, and that each call fires Changed.
func TestSkillGateIsInTheStatus(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	sess := skillSession(t, home, work, nil)
	ctx := context.Background()

	if err := sess.SetSkillGate(ctx, false); err != nil {
		t.Fatalf("SetSkillGate(false): %v", err)
	}
	awaitChanged(t, sess)
	if got := sess.Status().SkillGate; got.Enabled || len(got.Decisions) != 0 {
		t.Fatalf("gate after turning it off = %+v, want disabled with no decisions", got)
	}

	if err := sess.SetSkillGate(ctx, true); err != nil {
		t.Fatalf("SetSkillGate(true): %v", err)
	}
	awaitChanged(t, sess)
	if got := sess.Status().SkillGate; !got.Enabled || len(got.Decisions) != 0 {
		t.Fatalf("gate after turning it on = %+v, want enabled with no decisions", got)
	}

	sess.state.SkillGateRecordDeny("gamma")
	got := sess.Status().SkillGate
	if len(got.Decisions) != 1 {
		t.Fatalf("gate decisions = %+v, want the one deny", got.Decisions)
	}
	if d := got.Decisions[0]; d.Skill != "gamma" || d.Allowed || d.Count != 1 {
		t.Fatalf("denied decision = %+v, want gamma denied once", d)
	}

	if err := sess.ClearSkillGateDecision(ctx, "gamma"); err != nil {
		t.Fatalf("ClearSkillGateDecision: %v", err)
	}
	awaitChanged(t, sess)
	if got := sess.Status().SkillGate; len(got.Decisions) != 0 {
		t.Fatalf("gate decisions after clearing = %+v, want none", got.Decisions)
	}
}
