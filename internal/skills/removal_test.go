package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
}

// scopedByName returns the discovered entry for a scope and direct child name.
func scopedByName(t *testing.T, list []ScopedSkill, scope, source string) ScopedSkill {
	t.Helper()
	for _, s := range list {
		if s.Scope == scope && s.SourceName == source {
			return s
		}
	}
	t.Fatalf("no discovered skill scope=%q source=%q in %+v", scope, source, list)
	return ScopedSkill{}
}

// mustSurvive asserts path still exists. Lstat is used so a surviving symlink
// counts as present.
func mustSurvive(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to survive: %v", path, err)
	}
}

func mustBeGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, Lstat error = %v", path, err)
	}
}

func TestListScopesRecordsRemovalSource(t *testing.T) {
	base := t.TempDir()
	globalDir := filepath.Join(base, "global")
	projectDir := filepath.Join(base, "project")
	mustMkdir(t, globalDir)
	mustMkdir(t, projectDir)

	// Frontmatter names deliberately differ from the filesystem names: the
	// recorded source must come from the filesystem, not from the skill.
	writeSkillFile(t, globalDir, "actual.md", skillContent("renamed-global", "global skill"))
	writeBundleSkill(t, projectDir, "bundle-dir", skillContent("renamed-project", "project bundle"))

	list, err := ListScopes(globalDir, projectDir)
	if err != nil {
		t.Fatalf("ListScopes: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListScopes returned %d entries, want 2: %+v", len(list), list)
	}

	file := scopedByName(t, list, scopeGlobal, "actual.md")
	if file.Skill.Name != "renamed-global" {
		t.Errorf("global skill name = %q, want renamed-global", file.Skill.Name)
	}
	if file.SourceInfo == nil {
		t.Fatal("global source snapshot is nil")
	}
	if !file.SourceInfo.Mode().IsRegular() {
		t.Errorf("global source mode = %v, want a regular file", file.SourceInfo.Mode())
	}

	bundle := scopedByName(t, list, scopeProject, "bundle-dir")
	if bundle.Skill.Name != "renamed-project" {
		t.Errorf("project skill name = %q, want renamed-project", bundle.Skill.Name)
	}
	if bundle.SourceInfo == nil {
		t.Fatal("project source snapshot is nil")
	}
	if !bundle.SourceInfo.IsDir() {
		t.Errorf("project source mode = %v, want a directory", bundle.SourceInfo.Mode())
	}

	// A bundle's source is its directory, not its SKILL.md.
	info, err := os.Lstat(filepath.Join(projectDir, "bundle-dir"))
	if err != nil {
		t.Fatalf("Lstat bundle-dir: %v", err)
	}
	if !os.SameFile(info, bundle.SourceInfo) {
		t.Error("project source snapshot does not describe the bundle directory")
	}
}

func TestRemoveScopedSkillExactSource(t *testing.T) {
	base := t.TempDir()
	globalDir := filepath.Join(base, "global")
	projectDir := filepath.Join(base, "project")
	for _, dir := range []string{globalDir, projectDir} {
		mustMkdir(t, dir)
		writeSkillFile(t, dir, "actual.md", skillContent("renamed-"+filepath.Base(dir), "top-level skill"))
		writeBundleSkill(t, dir, "bundle-dir", skillContent("bundle-"+filepath.Base(dir), "bundled skill"))
		writeSkillFile(t, dir, "sibling.md", skillContent("sibling-"+filepath.Base(dir), "untouched skill"))
	}

	list, err := ListScopes(globalDir, projectDir)
	if err != nil {
		t.Fatalf("ListScopes: %v", err)
	}

	for _, op := range []struct {
		label string
		root  string
		skill ScopedSkill
	}{
		{"global file", globalDir, scopedByName(t, list, scopeGlobal, "actual.md")},
		{"global bundle", globalDir, scopedByName(t, list, scopeGlobal, "bundle-dir")},
		{"project file", projectDir, scopedByName(t, list, scopeProject, "actual.md")},
		{"project bundle", projectDir, scopedByName(t, list, scopeProject, "bundle-dir")},
	} {
		if err := RemoveScopedSkill(op.root, op.skill); err != nil {
			t.Fatalf("%s: RemoveScopedSkill: %v", op.label, err)
		}
		mustBeGone(t, filepath.Join(op.root, op.skill.SourceName))
		mustSurvive(t, filepath.Join(op.root, "sibling.md"))
	}
	for _, dir := range []string{globalDir, projectDir} {
		mustSurvive(t, dir)
	}
}

func TestRemoveScopedSkillRejectsTraversalAndInvalidIdentity(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	mustMkdir(t, store)

	// Sentinel the forged names would reach if the metadata were trusted.
	outside := filepath.Join(base, "victim")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// The metadata name is hostile; the file name is not.
	discoveredPath := writeSkillFile(t, store, "innocent.md", skillContent("../../victim", "hostile name"))
	list, err := ListScopes(store, "")
	if err != nil {
		t.Fatalf("ListScopes: %v", err)
	}
	discovered := scopedByName(t, list, scopeGlobal, "innocent.md")
	if discovered.Skill.Name != "../../victim" {
		t.Fatalf("discovered skill name = %q, want the hostile metadata name", discovered.Skill.Name)
	}

	cases := []struct {
		label string
		skill ScopedSkill
		want  string
	}{
		{
			"forged traversal source",
			ScopedSkill{Skill: discovered.Skill, Scope: scopeGlobal, SourceName: "../victim", SourceInfo: discovered.SourceInfo},
			"invalid source name",
		},
		{
			"dot source",
			ScopedSkill{Skill: discovered.Skill, Scope: scopeGlobal, SourceName: ".", SourceInfo: discovered.SourceInfo},
			"invalid source name",
		},
		{
			"empty source",
			ScopedSkill{Skill: discovered.Skill, Scope: scopeGlobal},
			"empty source name",
		},
		{
			"absolute source",
			ScopedSkill{Skill: discovered.Skill, Scope: scopeGlobal, SourceName: outside, SourceInfo: discovered.SourceInfo},
			"invalid source name",
		},
		{
			"nil snapshot",
			ScopedSkill{Skill: discovered.Skill, Scope: scopeGlobal, SourceName: "innocent.md"},
			"snapshot",
		},
		{
			"unknown scope",
			ScopedSkill{Skill: discovered.Skill, Scope: "user", SourceName: "innocent.md", SourceInfo: discovered.SourceInfo},
			"unknown scope",
		},
	}
	for _, tc := range cases {
		err := RemoveScopedSkill(store, tc.skill)
		if err == nil {
			t.Errorf("%s: expected an error, got nil", tc.label)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.label, err, tc.want)
		}
		mustSurvive(t, outside)
		mustSurvive(t, discoveredPath)
	}

	// The legitimately discovered identity wins over the hostile metadata:
	// only the actual child under the store is removed.
	if err := RemoveScopedSkill(store, discovered); err != nil {
		t.Fatalf("RemoveScopedSkill(discovered): %v", err)
	}
	mustBeGone(t, discoveredPath)
	mustSurvive(t, outside)
}

func TestRemoveScopedSkillRejectsSymlinkAndReplacement(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	mustMkdir(t, store)
	outside := filepath.Join(base, "outside.md")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	t.Run("symlinked child", func(t *testing.T) {
		link := filepath.Join(store, "link.md")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("platform cannot create symlinks: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(link) })

		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("Lstat link: %v", err)
		}
		err = RemoveScopedSkill(store, ScopedSkill{Scope: scopeGlobal, SourceName: "link.md", SourceInfo: info})
		if err == nil {
			t.Fatal("expected an error for a symlinked child, got nil")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("error %q does not mention symlink", err)
		}
		mustSurvive(t, outside)
		mustSurvive(t, link)
	})

	t.Run("missing child", func(t *testing.T) {
		ghost := filepath.Join(store, "ghost.md")
		writeSkillFile(t, store, "ghost.md", skillContent("ghost", "vanishing skill"))
		info, err := os.Lstat(ghost)
		if err != nil {
			t.Fatalf("Lstat ghost: %v", err)
		}
		if err := os.Remove(ghost); err != nil {
			t.Fatalf("remove ghost: %v", err)
		}
		err = RemoveScopedSkill(store, ScopedSkill{Scope: scopeGlobal, SourceName: "ghost.md", SourceInfo: info})
		if err == nil {
			t.Fatal("expected an error for a missing child, got nil")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error %q does not mention missing", err)
		}
		mustSurvive(t, outside)
	})

	t.Run("replaced child", func(t *testing.T) {
		writeSkillFile(t, store, "swap.md", skillContent("swap", "original skill"))
		list, err := ListScopes(store, "")
		if err != nil {
			t.Fatalf("ListScopes: %v", err)
		}
		discovered := scopedByName(t, list, scopeGlobal, "swap.md")

		// Rename a pre-existing file over the discovered child: the inode now
		// behind the name is not the one discovery snapshotted.
		writeSkillFile(t, store, "attacker.md", skillContent("attacker", "replacement"))
		if err := os.Rename(filepath.Join(store, "attacker.md"), filepath.Join(store, "swap.md")); err != nil {
			t.Fatalf("rename replacement over child: %v", err)
		}

		err = RemoveScopedSkill(store, discovered)
		if err == nil {
			t.Fatal("expected an error for a replaced child, got nil")
		}
		if !strings.Contains(err.Error(), "replaced") {
			t.Errorf("error %q does not mention replaced", err)
		}
		raw, readErr := os.ReadFile(filepath.Join(store, "swap.md"))
		if readErr != nil {
			t.Fatalf("read replacement: %v", readErr)
		}
		if !strings.Contains(string(raw), "attacker") {
			t.Errorf("replacement was touched, contents = %q", raw)
		}
		mustSurvive(t, outside)
	})
}

func TestRemoveScopedSkillAllowsSymlinkedStoreRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	mustMkdir(t, real)
	writeSkillFile(t, real, "child.md", skillContent("child", "child skill"))
	writeSkillFile(t, real, "sibling.md", skillContent("sibling", "sibling skill"))

	list, err := ListScopes(real, "")
	if err != nil {
		t.Fatalf("ListScopes: %v", err)
	}
	discovered := scopedByName(t, list, scopeGlobal, "child.md")

	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("platform cannot create symlinks: %v", err)
	}

	if err := RemoveScopedSkill(alias, discovered); err != nil {
		t.Fatalf("RemoveScopedSkill through an aliased store root: %v", err)
	}
	mustBeGone(t, filepath.Join(real, "child.md"))
	mustSurvive(t, filepath.Join(real, "sibling.md"))
	mustSurvive(t, real)
}
