package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/trust"
)

func TestSkillsPreferSharedDecodesFromUserConfig(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml",
		"[skills]\nprefer_shared = ['brainstorming', 'test-driven-development']\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"brainstorming", "test-driven-development"}
	if !reflect.DeepEqual(l.Merged.Skills.PreferShared, want) {
		t.Fatalf("PreferShared = %v, want %v", l.Merged.Skills.PreferShared, want)
	}
	if !reflect.DeepEqual(l.User.Skills.PreferShared, want) {
		t.Fatalf("user layer PreferShared = %v, want %v", l.User.Skills.PreferShared, want)
	}
}

// prefer_shared is user-global only: the project layer is stripped before
// merge, the user's list stands, and the ignore is reported.
func TestSkillsPreferSharedProjectLayerIgnored(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[skills]\nprefer_shared = ['brainstorming']\n")
	writeFile(t, work+"/.marshal/config.toml", "[skills]\nprefer_shared = ['postmortem']\n")
	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"brainstorming"}; !reflect.DeepEqual(l.Merged.Skills.PreferShared, want) {
		t.Fatalf("project preference applied: %v", l.Merged.Skills.PreferShared)
	}
	if !l.ProjectPreferSharedIgnored {
		t.Fatal("ProjectPreferSharedIgnored = false")
	}
	if !hasPath(Diagnose(l.Merged, l), "skills.prefer_shared") {
		t.Fatal("no diagnostic for the ignored project preference")
	}
}

// A project-only preference changes nothing at all, rather than silently
// winning because the user never set one.
func TestSkillsPreferSharedProjectOnlyIgnored(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", "[skills]\nprefer_shared = ['brainstorming']\n")
	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Merged.Skills.PreferShared) != 0 {
		t.Fatalf("project-only preference applied: %v", l.Merged.Skills.PreferShared)
	}
	if !l.ProjectPreferSharedIgnored {
		t.Fatal("ProjectPreferSharedIgnored = false")
	}
}

// The panel's toggle writes through SaveUserConfigValue, so the dotted path
// has to reach the []string field and survive a reload.
func TestSkillsPreferSharedSaveUserConfigValue(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	path := UserConfigPath(home)
	if err := SaveUserConfigValue(path, "skills.prefer_shared", []string{"brainstorming"}); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"brainstorming"}; !reflect.DeepEqual(l.Merged.Skills.PreferShared, want) {
		t.Fatalf("PreferShared = %v, want %v", l.Merged.Skills.PreferShared, want)
	}

	if err := SaveUserConfigValue(path, "skills.prefer_shared", []string{}); err != nil {
		t.Fatal(err)
	}
	l, err = LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Merged.Skills.PreferShared) != 0 {
		t.Fatalf("cleared preference came back as %v", l.Merged.Skills.PreferShared)
	}
}

// A project save must never carry the preference: it is the user's install
// choice, so writing the merged config (which holds it) leaves no trace in the
// committed file and no "ignored" diagnostic on the next load.
func TestSkillsPreferSharedProjectSaveOmitsPreference(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	if err := SaveUserConfigValue(UserConfigPath(home), "skills.prefer_shared", []string{"brainstorming"}); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Skills.PreferShared = []string{"brainstorming"}
	// A project-relevant delta, so writeSections emits [skills] at all and the
	// omission comes from the strip rather than from an absent section.
	cfg.Skills.MaxActive = 4

	path := filepath.Join(work, ".marshal", "config.toml")
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "prefer_shared") {
		t.Fatalf("project save wrote the user's preference:\n%s", data)
	}
	if !strings.Contains(string(data), "max_active") {
		t.Fatalf("project-relevant skill setting was dropped:\n%s", data)
	}

	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.ProjectPreferSharedIgnored {
		t.Fatal("project save produced a file the loader reports as setting the preference")
	}
	if want := []string{"brainstorming"}; !reflect.DeepEqual(l.Merged.Skills.PreferShared, want) {
		t.Fatalf("PreferShared = %v, want %v", l.Merged.Skills.PreferShared, want)
	}
}

// When the preference is the only skill setting, the save drops [skills]
// entirely rather than pinning today's defaults into the project file.
func TestSkillsPreferSharedOnlyProjectSaveWritesNoSkillsSection(t *testing.T) {
	work := t.TempDir()
	cfg := Default()
	cfg.Skills.PreferShared = []string{"brainstorming"}

	path := filepath.Join(work, ".marshal", "config.toml")
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[skills]") {
		t.Fatalf("project save pinned a default-valued [skills] table:\n%s", data)
	}
}

// A whole-config save must not drop the preference either.
func TestSkillsPreferSharedSectionSaveRoundTrip(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	path := UserConfigPath(home)
	cfg := Default()
	cfg.Skills.PreferShared = []string{"brainstorming"}
	if err := SaveUserConfigSection(path, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"brainstorming"}; !reflect.DeepEqual(l.Merged.Skills.PreferShared, want) {
		t.Fatalf("PreferShared = %v, want %v", l.Merged.Skills.PreferShared, want)
	}
}

// A save that cannot reach the disk returns its error: the caller has to be
// able to report a failure rather than a preference that was never written.
func TestSkillsPreferSharedWriteFailureIsReturned(t *testing.T) {
	home := t.TempDir()
	// A regular file where a directory would have to be, so neither the load
	// nor the write can succeed — for any user, root included.
	blocked := filepath.Join(home, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if err := SaveUserConfigValue(filepath.Join(blocked, "config.toml"), "skills.prefer_shared", []string{"brainstorming"}); err == nil {
		t.Fatal("SaveUserConfigValue below a regular file returned nil")
	}
}

// The ignored project value stays on disk: "ignored" is not "deleted", so the
// diagnostic keeps describing a value its author can still see.
func TestSkillsPreferSharedProjectValueStaysOnDisk(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	projectPath := filepath.Join(work, ".marshal", "config.toml")
	writeFile(t, projectPath, "[skills]\nprefer_shared = ['postmortem']\n")

	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !l.ProjectPreferSharedIgnored {
		t.Fatal("ProjectPreferSharedIgnored = false")
	}
	if len(l.Merged.Skills.PreferShared) != 0 {
		t.Fatalf("project preference applied: %v", l.Merged.Skills.PreferShared)
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "prefer_shared") {
		t.Fatalf("the ignored project preference was erased from disk:\n%s", data)
	}
}

// A [providers] hoist rewrites the project file from the file it read. The
// preference that load merely ignores has to survive that rewrite rather than
// disappearing with the sections that really did move.
func TestSkillsPreferSharedSurvivesAProviderHoist(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	projectPath := filepath.Join(work, ".marshal", "config.toml")
	writeFile(t, projectPath, `[providers.local]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"

[skills]
prefer_shared = ['postmortem']
`)

	if _, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[providers.local]") {
		t.Fatalf("the hoistable provider stayed in the project file:\n%s", data)
	}
	if !strings.Contains(string(data), "prefer_shared") {
		t.Fatalf("the hoist erased the ignored preference:\n%s", data)
	}
}

// An untrusted project applies nothing at all: the whole layer is skipped, so
// the preference is neither merged, stripped nor reported.
func TestSkillsPreferSharedUntrustedProjectIgnored(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", "[skills]\nprefer_shared = ['postmortem']\n")

	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionDontTrust},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Merged.Skills.PreferShared) != 0 {
		t.Fatalf("untrusted project preference applied: %v", l.Merged.Skills.PreferShared)
	}
}
