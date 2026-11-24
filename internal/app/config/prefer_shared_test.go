package config

import (
	"reflect"
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
