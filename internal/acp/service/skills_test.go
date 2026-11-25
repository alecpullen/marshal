package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// sharedSkillStore creates and returns the shared store under home, the one
// another agent installs into and Marshal only reads.
func sharedSkillStore(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the shared skill store %s: %v", dir, err)
	}
	return dir
}

// newSkillSession builds a session over home and work with deps's extras, and
// returns the state it serves. A state the caller supplied is served as it is.
func newSkillSession(t *testing.T, home, work string, deps Deps) (*Session, *session.State) {
	t.Helper()
	state := deps.State
	if state == nil {
		state = session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	}
	deps.State = state
	deps.HomeDir = home
	_, sess := New(deps)
	return sess, state
}

// preferSkills installs names as the session's saved skills.prefer_shared, as
// a config load carrying the user's list would.
func preferSkills(t *testing.T, sess *Session, names ...string) {
	t.Helper()
	cfg := sess.state.ConfigSnapshot()
	cfg.Skills.PreferShared = names
	if _, err := sess.state.RebaseInteractionConfig(cfg); err != nil {
		t.Fatalf("install the live preference: %v", err)
	}
}

// hasSkillName reports whether list carries name.
func hasSkillName(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// userConfigPreferShared reads skills.prefer_shared out of a user config file
// as TOML, so a stray substring elsewhere in the file cannot stand in.
func userConfigPreferShared(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the user config: %v", err)
	}
	var file struct {
		Skills struct {
			PreferShared []string `toml:"prefer_shared"`
		} `toml:"skills"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse the user config: %v", err)
	}
	return file.Skills.PreferShared
}

// TestTheListingNamesEverySourceWithItsWinnerAndThePreference pins the listing
// across the four sources: a built-in and a shared copy of one declared name
// are both listed under their own scopes, the effective copy is the one the
// resolution holds, and the saved preference marks every copy of the name.
func TestTheListingNamesEverySourceWithItsWinnerAndThePreference(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	global, _ := skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	writeSkillEntry(t, global, "brainstorming.md", "brainstorming", "the global copy")
	writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared copy")
	writeSkillEntry(t, shared, "solo.md", "solo", "the shared copy of a name nothing else declares")

	sess, _ := newSkillSession(t, home, work, Deps{})
	preferSkills(t, sess, "brainstorming")
	entries, err := sess.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}

	// The preference swaps only the last two sources: the user's own install
	// still wins, and exactly one copy of the name is effective.
	for _, want := range []struct {
		scope, source string
		effective     bool
		preferred     bool
	}{
		{scope: "global", source: "brainstorming.md", effective: true, preferred: true},
		{scope: "builtin", source: "brainstorming", preferred: true},
		{scope: "shared", source: "brainstorming.md", preferred: true},
	} {
		entry := entryFor(t, entries, want.scope, want.source)
		if entry.Name != "brainstorming" {
			t.Errorf("%s/%s name = %q, want brainstorming", want.scope, want.source, entry.Name)
		}
		if entry.Effective != want.effective {
			t.Errorf("%s/%s Effective = %v, want %v", want.scope, want.source, entry.Effective, want.effective)
		}
		if entry.Preferred != want.preferred {
			t.Errorf("%s/%s Preferred = %v, want %v", want.scope, want.source, entry.Preferred, want.preferred)
		}
	}

	// A name the preference does not carry: its only copy is effective and
	// nothing about it is preferred.
	solo := entryFor(t, entries, "shared", "solo.md")
	if !solo.Effective || solo.Preferred {
		t.Errorf("shared solo.md = %+v, want it effective and not preferred", solo)
	}

	// One effective entry per declared name, never two.
	counts := map[string]int{}
	for _, e := range entries {
		if e.Effective {
			counts[e.Name]++
		}
	}
	for name, n := range counts {
		if n != 1 {
			t.Errorf("%s has %d effective entries, want exactly one", name, n)
		}
	}
	if counts["brainstorming"] != 1 {
		t.Errorf("brainstorming has %d effective entries, want exactly one", counts["brainstorming"])
	}
}

// TestAnUnpreferredSharedCopyLosesToTheBuiltIn pins the default order: with no
// saved preference the built-in wins, and the shared copy only loses.
func TestAnUnpreferredSharedCopyLosesToTheBuiltIn(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared copy")

	sess, _ := newSkillSession(t, home, work, Deps{})
	entries, err := sess.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if builtin := entryFor(t, entries, "builtin", "brainstorming"); !builtin.Effective {
		t.Errorf("builtin brainstorming = %+v, want it effective", builtin)
	}
	if sharedEntry := entryFor(t, entries, "shared", "brainstorming.md"); sharedEntry.Effective {
		t.Errorf("shared brainstorming.md = %+v, want it not effective", sharedEntry)
	}
}

// TestThePreferenceIsPersistedToTheUserConfigAlone pins the write: the op
// changes skills.prefer_shared in the user config, leaves the project config
// alone, publishes the new resolution, and turning it off restores the
// built-in.
func TestThePreferenceIsPersistedToTheUserConfigAlone(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared copy")
	userConfig := filepath.Join(home, "config.toml")

	sess, state := newSkillSession(t, home, work, Deps{
		UserConfigPath: userConfig,
		ReloadLayers: func() (config.Layers, bool) {
			layers, err := config.LoadLayers(config.LoadOptions{HomeDir: home, WorkingDir: work, SkipProjectConfig: true})
			return layers, err == nil
		},
	})
	ctx := context.Background()

	if err := sess.SetSkillPreferShared(ctx, "brainstorming", true); err != nil {
		t.Fatalf("SetSkillPreferShared(brainstorming, true): %v", err)
	}
	if got := userConfigPreferShared(t, userConfig); !hasSkillName(got, "brainstorming") {
		t.Errorf("skills.prefer_shared in the user config = %v, want it to hold brainstorming", got)
	}
	if _, err := os.Stat(config.ProjectConfigPath(work)); !os.IsNotExist(err) {
		t.Errorf("the project config exists after a preference write: %v", err)
	}
	if got := state.ConfigSnapshot().Skills.PreferShared; !hasSkillName(got, "brainstorming") {
		t.Errorf("the live skills.prefer_shared = %v, want it to hold brainstorming", got)
	}

	entries, err := sess.Skills(ctx)
	if err != nil {
		t.Fatalf("Skills after the preference: %v", err)
	}
	if sharedEntry := entryFor(t, entries, "shared", "brainstorming.md"); !sharedEntry.Effective || !sharedEntry.Preferred {
		t.Errorf("shared brainstorming.md = %+v, want it effective and preferred", sharedEntry)
	}
	if builtin := entryFor(t, entries, "builtin", "brainstorming"); builtin.Effective {
		t.Errorf("builtin brainstorming = %+v, want it no longer effective", builtin)
	}

	if err := sess.SetSkillPreferShared(ctx, "brainstorming", false); err != nil {
		t.Fatalf("SetSkillPreferShared(brainstorming, false): %v", err)
	}
	if got := userConfigPreferShared(t, userConfig); hasSkillName(got, "brainstorming") {
		t.Errorf("skills.prefer_shared in the user config = %v, want no brainstorming", got)
	}
	entries, err = sess.Skills(ctx)
	if err != nil {
		t.Fatalf("Skills after turning the preference off: %v", err)
	}
	if builtin := entryFor(t, entries, "builtin", "brainstorming"); !builtin.Effective {
		t.Errorf("builtin brainstorming = %+v, want it effective again", builtin)
	}
}

// TestAPreferenceThatCannotBePersistedChangesNothing pins the failure: a write
// error is returned unchanged and the live config keeps the list it had.
func TestAPreferenceThatCannotBePersistedChangesNothing(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared copy")

	// A user config path that is a directory cannot be read or written.
	unwritable := filepath.Join(home, "config.toml")
	if err := os.MkdirAll(unwritable, 0o755); err != nil {
		t.Fatalf("create the unwritable config path: %v", err)
	}
	sess, state := newSkillSession(t, home, work, Deps{UserConfigPath: unwritable})

	err := sess.SetSkillPreferShared(context.Background(), "brainstorming", true)
	if err == nil {
		t.Fatal("SetSkillPreferShared over an unwritable path = nil, want the write's error")
	}
	if got := state.ConfigSnapshot().Skills.PreferShared; len(got) != 0 {
		t.Fatalf("the live skills.prefer_shared = %v after a failed write, want it unchanged", got)
	}
}

// TestThePreferenceSurvivesARestart pins persistence across sessions: a fresh
// session over the same home and working directory, with its config loaded
// from disk, resolves the saved preference.
func TestThePreferenceSurvivesARestart(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared copy")
	userConfig := config.UserConfigPath(home)

	sess, _ := newSkillSession(t, home, work, Deps{UserConfigPath: userConfig})
	if err := sess.SetSkillPreferShared(context.Background(), "brainstorming", true); err != nil {
		t.Fatalf("SetSkillPreferShared: %v", err)
	}

	// A restart reads the user config from disk into a new state. The file it
	// reads is the one a session without an explicit path writes, so the
	// preference has to be where that load looks.
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := cfg.Skills.PreferShared; !hasSkillName(got, "brainstorming") {
		t.Fatalf("the loaded config's skills.prefer_shared = %v, want it to hold brainstorming", got)
	}
	state := session.New(cfg, work, time.Unix(200, 0), session.Persistence{SessionID: "s2"})
	restarted, _ := newSkillSession(t, home, work, Deps{State: state, SkillIndex: func() *skills.Index { return skills.NewIndex() }})

	entries, err := restarted.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills after the restart: %v", err)
	}
	if sharedEntry := entryFor(t, entries, "shared", "brainstorming.md"); !sharedEntry.Effective {
		t.Errorf("shared brainstorming.md = %+v after the restart, want it effective", sharedEntry)
	}
}

// TestAnActiveSkillBlocksTheNextLoadOfItsName pins the future load: once the
// winning copy is active the listing says so for that name, and loading any
// copy of it again is refused while the active body is kept.
func TestAnActiveSkillBlocksTheNextLoadOfItsName(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	sharedPath := writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared body")

	// The preference makes the shared copy the winner, so the session's index
	// holds it — the runtime's index is rebuilt from the same resolution.
	idx := skills.NewIndex()
	idx.Set("brainstorming", parseSkill(t, sharedPath))
	sess, state := newSkillSession(t, home, work, Deps{SkillIndex: func() *skills.Index { return idx }})
	preferSkills(t, sess, "brainstorming")
	ctx := context.Background()

	if _, err := sess.Skills(ctx); err != nil {
		t.Fatalf("Skills: %v", err)
	}
	name, err := sess.LoadSkill(ctx, "shared", "brainstorming.md")
	if err != nil {
		t.Fatalf("LoadSkill(shared, brainstorming.md): %v", err)
	}
	if name != "brainstorming" {
		t.Fatalf("LoadSkill name = %q, want brainstorming", name)
	}
	if !state.HasActiveSkill("brainstorming") {
		t.Fatal("the loaded skill is not active in the session")
	}

	// The next listing reports the name as already active on its winner, and
	// the other copy as a different body the session does not hold.
	entries, err := sess.Skills(ctx)
	if err != nil {
		t.Fatalf("Skills after the load: %v", err)
	}
	if blocked := entryFor(t, entries, "shared", "brainstorming.md").LoadBlocked; !strings.Contains(blocked, "is already active") {
		t.Errorf("shared brainstorming.md LoadBlocked = %q, want the already-active reason", blocked)
	}
	if blocked := entryFor(t, entries, "builtin", "brainstorming").LoadBlocked; blocked == "" {
		t.Errorf("builtin brainstorming LoadBlocked = \"\", want the copy the session does not hold")
	}

	// Loading either copy again is refused while the active body is kept.
	if _, err := sess.LoadSkill(ctx, "shared", "brainstorming.md"); err == nil || !strings.Contains(err.Error(), "is already active") {
		t.Errorf("a second LoadSkill of the shared copy = %v, want the already-active refusal", err)
	}
	if _, err := sess.LoadSkill(ctx, "builtin", "brainstorming"); err == nil {
		t.Error("LoadSkill of the built-in copy = nil, want the session's refusal")
	}
	if !state.HasActiveSkill("brainstorming") {
		t.Error("the active skill was dropped by the refused loads")
	}
}

// TestRemovalRefusesTheReadOnlySources pins the two read-only sources: a
// built-in has no file to remove, and the shared store belongs to another
// tool.
func TestRemovalRefusesTheReadOnlySources(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	sharedPath := writeSkillEntry(t, shared, "brainstorming.md", "brainstorming", "the shared copy")

	sess, _ := newSkillSession(t, home, work, Deps{})
	ctx := context.Background()
	if _, err := sess.Skills(ctx); err != nil {
		t.Fatalf("Skills: %v", err)
	}

	cases := []struct {
		scope, source string
		wantErr       string
	}{
		{"builtin", "brainstorming", "built-in skills cannot be removed"},
		{"shared", "brainstorming.md", "shared skills are read-only through Marshal — remove them where they were installed"},
	}
	for _, tc := range cases {
		err := sess.RemoveSkill(ctx, tc.scope, tc.source)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("RemoveSkill(%q, %q) = %v, want %q", tc.scope, tc.source, err, tc.wantErr)
		}
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Errorf("the shared copy is gone after a refused removal: %v", err)
	}
}

// TestInstallRefusesTheReadOnlySources pins that only the global and project
// stores are writable.
func TestInstallRefusesTheReadOnlySources(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	source := writeSkillEntry(t, work, "source.md", "source", "a fixture to install")

	sess, _ := newSkillSession(t, home, work, Deps{})
	ctx := context.Background()
	for _, scope := range []string{"builtin", "shared"} {
		want := fmt.Sprintf("cannot install into the %s source — only the global and project stores are writable", scope)
		if _, err := sess.InstallSkill(ctx, source, scope); err == nil || err.Error() != want {
			t.Errorf("InstallSkill(source, %q) = %v, want %q", scope, err, want)
		}
	}
}

// TestThePreferenceRefusesAnUnknownOrBlankName pins the two refusals: a blank
// name is the panel's prompt, and a name no source declares is not a
// preference worth saving.
func TestThePreferenceRefusesAnUnknownOrBlankName(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	skillStores(t, home, work)
	shared := sharedSkillStore(t, home)
	writeSkillEntry(t, shared, "solo.md", "solo", "the only copy")

	sess, _ := newSkillSession(t, home, work, Deps{UserConfigPath: filepath.Join(home, "config.toml")})
	ctx := context.Background()

	if err := sess.SetSkillPreferShared(ctx, "   ", true); err == nil || err.Error() != "name a skill first" {
		t.Errorf("SetSkillPreferShared(blank) = %v, want \"name a skill first\"", err)
	}
	if err := sess.SetSkillPreferShared(ctx, "nowhere", true); err == nil || err.Error() != `no skill named "nowhere"` {
		t.Errorf("SetSkillPreferShared(nowhere) = %v, want %q", err, `no skill named "nowhere"`)
	}
	// A source name is not a declared name: the shared copy is solo.md, and
	// only solo names it.
	if err := sess.SetSkillPreferShared(ctx, "solo.md", true); err == nil || err.Error() != `no skill named "solo.md"` {
		t.Errorf("SetSkillPreferShared(solo.md) = %v, want the declared name's refusal", err)
	}
	if err := sess.SetSkillPreferShared(ctx, "solo", true); err != nil {
		t.Errorf("SetSkillPreferShared(solo, true) = %v, want it accepted", err)
	}
}
