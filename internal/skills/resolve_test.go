package skills

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The built-in these fixtures collide with: a shared copy of the same declared
// name is what `prefer_shared` is allowed to swap.
const sharedBuiltInName = "brainstorming"

func mustResolve(t *testing.T, stores Stores, preferShared []string) *Resolved {
	t.Helper()
	resolved, err := Resolve(stores, preferShared)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolved
}

// sources lists a name's candidates' sources in precedence order.
func sources(res Resolution) []string {
	out := make([]string, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		out = append(out, c.Source)
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestResolveDefaultPrecedence covers all four sources at once: each copy is
// discovered, keeps its own identity, and a name held by every filesystem
// source resolves project-first.
func TestResolveDefaultPrecedence(t *testing.T) {
	globalDir, projectDir, sharedDir := t.TempDir(), t.TempDir(), t.TempDir()

	writeSkillFile(t, globalDir, "global-only.md", skillContent("global-only", "A global skill"))
	writeSkillFile(t, projectDir, "project-only.md", skillContent("project-only", "A project skill"))
	writeBundleSkill(t, sharedDir, "shared-only", skillContent("shared-only", "A shared skill"))
	writeSkillFile(t, globalDir, "layered.md", skillContent("layered", "Global version"))
	writeSkillFile(t, projectDir, "layered.md", skillContent("layered", "Project version"))
	writeBundleSkill(t, sharedDir, "layered", skillContent("layered", "Shared version"))

	resolved := mustResolve(t, Stores{GlobalDir: globalDir, ProjectDir: projectDir, SharedDir: sharedDir}, nil)

	for name, want := range map[string]string{
		"global-only":     SourceGlobal,
		"project-only":    SourceProject,
		"shared-only":     SourceShared,
		sharedBuiltInName: SourceBuiltin,
	} {
		res, ok := resolved.ByName[name]
		if !ok {
			t.Fatalf("%s missing from the resolution", name)
		}
		if res.Winner.Source != want {
			t.Errorf("%s winner source = %q, want %q", name, res.Winner.Source, want)
		}
		if len(res.Candidates) != 1 {
			t.Errorf("%s candidates = %d, want 1", name, len(res.Candidates))
		}
		if res.PreferShared {
			t.Errorf("%s unexpectedly preferred", name)
		}
	}

	res, ok := resolved.ByName["layered"]
	if !ok {
		t.Fatal("layered missing from the resolution")
	}
	if want := []string{SourceProject, SourceGlobal, SourceShared}; !sameStrings(sources(res), want) {
		t.Fatalf("layered precedence = %v, want %v", sources(res), want)
	}
	if res.Winner.Skill.Description != "Project version" {
		t.Errorf("layered winner = %q, want the project copy", res.Winner.Skill.Description)
	}
	if got, _ := resolved.Index.Load("layered"); got.Description != "Project version" {
		t.Errorf("index layered = %q, want the project copy", got.Description)
	}
	if len(resolved.Names) == 0 {
		t.Fatal("Resolved.Names is empty")
	}
}

// TestResolveNoPreferenceMatchesLoadSkills pins the default decision: a shared
// copy never displaces a built-in without a preference.
func TestResolveNoPreferenceMatchesLoadSkills(t *testing.T) {
	sharedDir := t.TempDir()
	writeBundleSkill(t, sharedDir, "brainstorming", skillContent(sharedBuiltInName, "Shared version"))

	resolved := mustResolve(t, Stores{SharedDir: sharedDir}, nil)
	res := resolved.ByName[sharedBuiltInName]
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %d, want built-in and shared", len(res.Candidates))
	}
	if want := []string{SourceBuiltin, SourceShared}; !sameStrings(sources(res), want) {
		t.Fatalf("precedence = %v, want %v", sources(res), want)
	}
	if res.Winner.Source != SourceBuiltin {
		t.Fatalf("winner = %q, want the built-in copy", res.Winner.Source)
	}
	if got, _ := resolved.Index.Load(sharedBuiltInName); got.Description == "Shared version" {
		t.Fatal("the shared copy entered the index without a preference")
	}

	// The name promises equivalence with the two-store loader the rest of the
	// runtime still calls, so check it: a global copy of the same name must
	// win in both.
	globalDir := t.TempDir()
	writeSkillFile(t, globalDir, "brainstorming.md", skillContent(sharedBuiltInName, "Global version"))
	viaResolve := mustResolve(t, Stores{GlobalDir: globalDir}, nil)
	viaLoad, err := LoadSkills(globalDir, "")
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	fromResolve, ok := viaResolve.Index.Load(sharedBuiltInName)
	if !ok {
		t.Fatal("Resolve dropped a global copy of a built-in name")
	}
	fromLoad, ok := viaLoad.Load(sharedBuiltInName)
	if !ok {
		t.Fatal("LoadSkills dropped a global copy of a built-in name")
	}
	if fromResolve.Description != fromLoad.Description {
		t.Fatalf("Resolve chose %q, LoadSkills chose %q", fromResolve.Description, fromLoad.Description)
	}
}

// TestResolvePreferSharedSwapsOnlyBuiltInAndShared is the whole preference
// contract: it flips built-in against shared for the named skill and nothing
// else, so a global or project copy still wins over both.
func TestResolvePreferSharedSwapsOnlyBuiltInAndShared(t *testing.T) {
	globalDir, projectDir, sharedDir := t.TempDir(), t.TempDir(), t.TempDir()

	writeBundleSkill(t, sharedDir, "brainstorming", skillContent(sharedBuiltInName, "Shared version"))
	writeSkillFile(t, globalDir, "test-driven-development.md", skillContent("test-driven-development", "Global version"))
	writeBundleSkill(t, sharedDir, "test-driven-development", skillContent("test-driven-development", "Shared version"))
	writeSkillFile(t, projectDir, "work-decomposition.md", skillContent("work-decomposition", "Project version"))
	writeBundleSkill(t, sharedDir, "work-decomposition", skillContent("work-decomposition", "Shared version"))

	prefer := []string{sharedBuiltInName, "test-driven-development", "work-decomposition", "  "}
	resolved := mustResolve(t, Stores{GlobalDir: globalDir, ProjectDir: projectDir, SharedDir: sharedDir}, prefer)

	res := resolved.ByName[sharedBuiltInName]
	if !res.PreferShared {
		t.Error("brainstorming preference not reported")
	}
	if want := []string{SourceShared, SourceBuiltin}; !sameStrings(sources(res), want) {
		t.Fatalf("brainstorming precedence = %v, want %v", sources(res), want)
	}
	if res.Winner.Skill.Description != "Shared version" {
		t.Fatalf("brainstorming winner = %q, want the shared copy", res.Winner.Skill.Description)
	}

	res = resolved.ByName["test-driven-development"]
	if want := []string{SourceGlobal, SourceShared, SourceBuiltin}; !sameStrings(sources(res), want) {
		t.Fatalf("test-driven-development precedence = %v, want %v", sources(res), want)
	}
	if res.Winner.Source != SourceGlobal {
		t.Fatalf("preference promoted a shared copy over the global one: %q", res.Winner.Source)
	}

	res = resolved.ByName["work-decomposition"]
	if want := []string{SourceProject, SourceShared, SourceBuiltin}; !sameStrings(sources(res), want) {
		t.Fatalf("work-decomposition precedence = %v, want %v", sources(res), want)
	}
	if res.Winner.Source != SourceProject {
		t.Fatalf("preference promoted a shared copy over the project one: %q", res.Winner.Source)
	}

	if got, _ := resolved.Index.Load(sharedBuiltInName); got.Description != "Shared version" {
		t.Errorf("index brainstorming = %q, want the shared copy", got.Description)
	}
}

// TestResolveMaskedCandidateKeepsPreferenceState: a copy a higher source masks
// is still reported, so the panel can say so.
func TestResolveMaskedCandidateKeepsPreferenceState(t *testing.T) {
	globalDir, sharedDir := t.TempDir(), t.TempDir()
	writeSkillFile(t, globalDir, "brainstorming.md", skillContent(sharedBuiltInName, "Global version"))
	writeBundleSkill(t, sharedDir, "brainstorming", skillContent(sharedBuiltInName, "Shared version"))

	resolved := mustResolve(t, Stores{GlobalDir: globalDir, SharedDir: sharedDir}, []string{sharedBuiltInName})
	res := resolved.ByName[sharedBuiltInName]
	if len(res.Candidates) != 3 {
		t.Fatalf("candidates = %d, want global, shared and built-in", len(res.Candidates))
	}
	if res.Winner.Source != SourceGlobal {
		t.Fatalf("winner = %q, want the global copy", res.Winner.Source)
	}
	if !res.PreferShared {
		t.Error("masked name lost its preference state")
	}
	if want := []string{SourceGlobal, SourceShared, SourceBuiltin}; !sameStrings(sources(res), want) {
		t.Fatalf("precedence = %v, want %v", sources(res), want)
	}
}

// TestResolveLinkedSharedBundle: other agents install shared bundles as
// symlinks, and ReadDir does not report those as directories.
func TestResolveLinkedSharedBundle(t *testing.T) {
	installDir := t.TempDir()
	writeBundleSkill(t, installDir, "superpowers", skillContent("superpowers", "Linked bundle"))

	sharedDir := t.TempDir()
	if err := os.Symlink(filepath.Join(installDir, "superpowers"), filepath.Join(sharedDir, "superpowers")); err != nil {
		t.Fatalf("symlink bundle: %v", err)
	}
	writeSkillFile(t, installDir, "flat.md", skillContent("flat-linked", "Linked file"))
	if err := os.Symlink(filepath.Join(installDir, "flat.md"), filepath.Join(sharedDir, "flat.md")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}

	resolved := mustResolve(t, Stores{SharedDir: sharedDir}, nil)
	for name, sourceName := range map[string]string{
		"superpowers": "superpowers",
		"flat-linked": "flat.md",
	} {
		res, ok := resolved.ByName[name]
		if !ok {
			t.Fatalf("%s not discovered through its symlink", name)
		}
		if res.Winner.Source != SourceShared {
			t.Errorf("%s source = %q, want shared", name, res.Winner.Source)
		}
		if res.Winner.SourceName != sourceName {
			t.Errorf("%s source name = %q, want %q", name, res.Winner.SourceName, sourceName)
		}
		if res.Winner.SourceInfo != nil {
			t.Errorf("%s carries a removal snapshot; shared stores are read-only", name)
		}
	}
}

// TestResolveMissingSharedRoot: no shared source is empty, never fatal.
func TestResolveMissingSharedRoot(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "mine.md", skillContent("mine", "Project skill"))

	resolved := mustResolve(t, Stores{ProjectDir: projectDir, SharedDir: filepath.Join(t.TempDir(), "absent")}, nil)
	if _, ok := resolved.Index.Load("mine"); !ok {
		t.Fatal("project skill lost when the shared root is missing")
	}
	if _, ok := resolved.Index.Load(sharedBuiltInName); !ok {
		t.Fatal("built-ins lost when the shared root is missing")
	}
}

// TestResolveInvalidSharedCopySkipped: a shared copy that will not parse falls
// back to the built-in instead of erroring.
func TestResolveInvalidSharedCopySkipped(t *testing.T) {
	sharedDir := t.TempDir()
	writeBundleSkill(t, sharedDir, "brainstorming", "# No frontmatter here\n")
	writeBundleSkill(t, sharedDir, "broken", "+++\ndescription = \"no name\"\n+++\n")
	writeBundleSkill(t, sharedDir, "fine", skillContent("fine", "A valid shared skill"))

	resolved := mustResolve(t, Stores{SharedDir: sharedDir}, []string{sharedBuiltInName})
	if _, ok := resolved.Index.Load("broken"); ok {
		t.Error("an invalid shared copy entered the index")
	}
	if _, ok := resolved.Index.Load("fine"); !ok {
		t.Error("a valid shared copy was lost alongside invalid ones")
	}
	res := resolved.ByName[sharedBuiltInName]
	if len(res.Candidates) != 1 || res.Winner.Source != SourceBuiltin {
		t.Fatalf("brainstorming candidates = %v, want the built-in alone", sources(res))
	}
}

// TestResolveBrokenSharedSymlinkSkipped: a dangling link costs only itself.
func TestResolveBrokenSharedSymlinkSkipped(t *testing.T) {
	sharedDir := t.TempDir()
	writeBundleSkill(t, sharedDir, "fine", skillContent("fine", "A valid shared skill"))
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(sharedDir, "gone")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	resolved := mustResolve(t, Stores{SharedDir: sharedDir}, nil)
	if _, ok := resolved.Index.Load("fine"); !ok {
		t.Fatal("a broken link cost the other shared skills")
	}
	// The link must leave nothing behind either: a copy fabricated from the
	// dangling name would be invisible to the assertion above.
	for _, res := range resolved.ByName {
		for _, candidate := range res.Candidates {
			if candidate.SourceName == "gone" {
				t.Fatalf("%s was fabricated from the dangling link", res.Name)
			}
		}
	}
}

// TestResolveDuplicateSourceFilenames: identity is (source, source name), so
// the same file name in two stores yields two distinct candidates, and the
// same declared name under it still resolves to one winner.
func TestResolveDuplicateSourceFilenames(t *testing.T) {
	projectDir, sharedDir := t.TempDir(), t.TempDir()
	writeSkillFile(t, projectDir, "note.md", skillContent("project-note", "Project skill"))
	writeSkillFile(t, sharedDir, "note.md", skillContent("shared-note", "Shared skill"))
	writeSkillFile(t, projectDir, "dup.md", skillContent("dup", "Project version"))
	writeSkillFile(t, sharedDir, "dup.md", skillContent("dup", "Shared version"))

	resolved := mustResolve(t, Stores{ProjectDir: projectDir, SharedDir: sharedDir}, nil)

	for name, wantSource := range map[string]string{
		"project-note": SourceProject,
		"shared-note":  SourceShared,
	} {
		res := resolved.ByName[name]
		if len(res.Candidates) != 1 {
			t.Fatalf("%s candidates = %d, want 1", name, len(res.Candidates))
		}
		if res.Winner.Source != wantSource || res.Winner.SourceName != "note.md" {
			t.Errorf("%s identity = (%q, %q), want (%q, note.md)",
				name, res.Winner.Source, res.Winner.SourceName, wantSource)
		}
	}

	res := resolved.ByName["dup"]
	if len(res.Candidates) != 2 {
		t.Fatalf("dup candidates = %d, want 2", len(res.Candidates))
	}
	if res.Candidates[0].Source == res.Candidates[1].Source {
		t.Fatalf("dup candidates share a source: %v", sources(res))
	}
	for _, c := range res.Candidates {
		if c.SourceName != "dup.md" {
			t.Errorf("dup candidate source name = %q, want dup.md", c.SourceName)
		}
	}
	if res.Winner.Source != SourceProject {
		t.Fatalf("dup winner = %q, want the project copy", res.Winner.Source)
	}
}

// TestResolveSameSourceTieBreak is deterministic: within one store the
// listing order decides, so a name declared twice does not flip between loads.
func TestResolveSameSourceTieBreak(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "a.md", skillContent("twice", "From a"))
	writeSkillFile(t, projectDir, "b.md", skillContent("twice", "From b"))

	resolved := mustResolve(t, Stores{ProjectDir: projectDir}, nil)
	res := resolved.ByName["twice"]
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(res.Candidates))
	}
	if res.Winner.SourceName != "a.md" {
		t.Fatalf("winner source name = %q, want a.md", res.Winner.SourceName)
	}
}

// TestResolveManagedStoresCarryRemovalIdentity: only the writable stores get
// the lstat snapshot a removal verifies against.
func TestResolveManagedStoresCarryRemovalIdentity(t *testing.T) {
	globalDir, projectDir, sharedDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeSkillFile(t, globalDir, "global.md", skillContent("global", "Global"))
	writeBundleSkill(t, projectDir, "project", skillContent("project", "Project"))
	writeBundleSkill(t, sharedDir, "shared", skillContent("shared", "Shared"))

	resolved := mustResolve(t, Stores{GlobalDir: globalDir, ProjectDir: projectDir, SharedDir: sharedDir}, nil)

	for name, pair := range map[string]struct{ dir, sourceName string }{
		"global":  {globalDir, "global.md"},
		"project": {projectDir, "project"},
	} {
		res := resolved.ByName[name]
		if res.Winner.SourceInfo == nil {
			t.Fatalf("%s has no removal snapshot", name)
		}
		onDisk, err := os.Lstat(filepath.Join(pair.dir, pair.sourceName))
		if err != nil {
			t.Fatalf("lstat %s: %v", name, err)
		}
		if !os.SameFile(onDisk, res.Winner.SourceInfo) {
			t.Errorf("%s removal snapshot does not match the store entry", name)
		}
	}

	for name := range map[string]bool{"shared": true, sharedBuiltInName: true} {
		if res := resolved.ByName[name]; res.Winner.SourceInfo != nil {
			t.Errorf("%s carries a removal snapshot; its store is read-only", name)
		}
	}
}

// TestListSourcesFourSources checks the flat discovery pass the panel reads.
func TestListSourcesFourSources(t *testing.T) {
	globalDir, projectDir, sharedDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeSkillFile(t, globalDir, "g.md", skillContent("g", "Global"))
	writeSkillFile(t, projectDir, "p.md", skillContent("p", "Project"))
	writeBundleSkill(t, sharedDir, "s", skillContent("s", "Shared"))

	candidates, err := ListSources(Stores{GlobalDir: globalDir, ProjectDir: projectDir, SharedDir: sharedDir})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.Source] = true
		if c.SourceName == "" {
			t.Errorf("%s candidate has no source name", c.Skill.Name)
		}
	}
	for _, want := range []string{SourceBuiltin, SourceGlobal, SourceProject, SourceShared} {
		if !seen[want] {
			t.Errorf("ListSources missed the %s source", want)
		}
	}
}

// TestResolveSharedBundleNamedLikeAFile: a bundle directory whose name ends in
// ".md" is a bundle, not a skill file. Deciding by the name before the entry
// type handed the directory to the parser as a file, so it vanished from every
// listing without saying why.
func TestResolveSharedBundleNamedLikeAFile(t *testing.T) {
	sharedDir := t.TempDir()
	writeBundleSkill(t, sharedDir, "oddly.md", skillContent("oddly-named", "A bundle named like a file"))

	resolved := mustResolve(t, Stores{SharedDir: sharedDir}, nil)
	res, ok := resolved.ByName["oddly-named"]
	if !ok {
		t.Fatal("a bundle directory named *.md was not discovered")
	}
	if res.Winner.Source != SourceShared {
		t.Fatalf("source = %q, want shared", res.Winner.Source)
	}
	if res.Winner.SourceName != "oddly.md" {
		t.Fatalf("source name = %q, want oddly.md", res.Winner.SourceName)
	}
}

// TestResolveSharedRootIsAFile: a shared path that names a regular file is an
// empty source, not an error — the same rule as a missing root.
func TestResolveSharedRootIsAFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	if err := os.WriteFile(root, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write shared root: %v", err)
	}
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "mine.md", skillContent("mine", "Project skill"))

	resolved := mustResolve(t, Stores{ProjectDir: projectDir, SharedDir: root}, nil)
	if _, ok := resolved.Index.Load("mine"); !ok {
		t.Fatal("a shared root that is not a directory cost the other sources")
	}
	if _, ok := resolved.Index.Load(sharedBuiltInName); !ok {
		t.Fatal("built-ins lost when the shared root is not a directory")
	}
}

// TestResolvePreferredSharedCopyMissing: a preference naming a skill with no
// shared copy is inert rather than an error, and the built-in still wins.
func TestResolvePreferredSharedCopyMissing(t *testing.T) {
	resolved := mustResolve(t, Stores{}, []string{sharedBuiltInName})
	res := resolved.ByName[sharedBuiltInName]
	if !res.PreferShared {
		t.Error("the preference was dropped for a name with no shared copy")
	}
	if res.Winner.Source != SourceBuiltin {
		t.Fatalf("winner = %q, want the built-in copy", res.Winner.Source)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %d, want the built-in alone", len(res.Candidates))
	}
}

// TestResolveGlobalOutranksBuiltIn: a user install beats a built-in of the same
// name at the resolver level too, not only through LoadSkills.
func TestResolveGlobalOutranksBuiltIn(t *testing.T) {
	globalDir := t.TempDir()
	writeSkillFile(t, globalDir, "brainstorming.md", skillContent(sharedBuiltInName, "Global version"))

	resolved := mustResolve(t, Stores{GlobalDir: globalDir}, nil)
	res := resolved.ByName[sharedBuiltInName]
	if want := []string{SourceGlobal, SourceBuiltin}; !sameStrings(sources(res), want) {
		t.Fatalf("precedence = %v, want %v", sources(res), want)
	}
	if res.Winner.Skill.Description != "Global version" {
		t.Fatalf("winner = %q, want the global copy", res.Winner.Skill.Description)
	}
}

// TestResolveNamesMatchTheIndex: Names is sorted and names exactly what ByName
// and the index hold — the order the panel lists them in.
func TestResolveNamesMatchTheIndex(t *testing.T) {
	projectDir := t.TempDir()
	for _, name := range []string{"zebra", "alpha", "middle"} {
		writeSkillFile(t, projectDir, name+".md", skillContent(name, "A skill"))
	}

	resolved := mustResolve(t, Stores{ProjectDir: projectDir}, nil)
	if !sort.StringsAreSorted(resolved.Names) {
		t.Fatalf("Names is not sorted: %v", resolved.Names)
	}
	if len(resolved.Names) != len(resolved.ByName) {
		t.Fatalf("Names has %d entries, ByName has %d", len(resolved.Names), len(resolved.ByName))
	}
	for _, name := range resolved.Names {
		if _, ok := resolved.ByName[name]; !ok {
			t.Errorf("Names holds %q, which ByName does not", name)
		}
		if _, ok := resolved.Index.Load(name); !ok {
			t.Errorf("Names holds %q, which the index does not", name)
		}
	}
}
