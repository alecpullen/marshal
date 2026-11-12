package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// discoveryWriteFile writes one fixture file, creating its parents.
func discoveryWriteFile(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// discoveryFactValues returns a section's fact values, sorted.
func discoveryFactValues(facts []DiscoveryFact) []string {
	values := make([]string, 0, len(facts))
	for _, fact := range facts {
		values = append(values, fact.Value)
	}
	sort.Strings(values)
	return values
}

func discoveryRequireFactValues(t *testing.T, label string, facts []DiscoveryFact, want ...string) {
	t.Helper()
	got := discoveryFactValues(facts)
	sorted := append([]string{}, want...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(got, sorted) {
		t.Fatalf("%s facts = %v, want %v", label, got, sorted)
	}
}

func discoveryFindFact(t *testing.T, facts []DiscoveryFact, value string) DiscoveryFact {
	t.Helper()
	for _, fact := range facts {
		if fact.Value == value {
			return fact
		}
	}
	t.Fatalf("no fact with value %q in %v", value, discoveryFactValues(facts))
	return DiscoveryFact{}
}

func TestInspectCheckoutFindsKnownSetup(t *testing.T) {
	root := t.TempDir()
	discoveryWriteFile(t, root, "AGENTS.md", []byte("# agents\n"))
	discoveryWriteFile(t, root, "CLAUDE.md", []byte("# claude\n"))
	discoveryWriteFile(t, root, "README.md", []byte("# readme\n"))
	discoveryWriteFile(t, root, "README.dev.md", []byte("dev notes\n"))
	discoveryWriteFile(t, root, ".cursorrules", []byte("rules\n"))
	discoveryWriteFile(t, root, "docs/README.md", []byte("# docs\n"))
	discoveryWriteFile(t, root, "go.mod", []byte("module example.com/x\n\ngo 1.22\n"))
	discoveryWriteFile(t, root, "package.json", []byte(`{"name":"x"}`))
	discoveryWriteFile(t, root, "tsconfig.json", []byte(`{}`))
	discoveryWriteFile(t, root, "Dockerfile", []byte("FROM scratch\n"))
	discoveryWriteFile(t, root, "compose.yaml", []byte("services: {}\n"))
	discoveryWriteFile(t, root, ".devcontainer/devcontainer.json", []byte(`{"image":"x"}`))
	discoveryWriteFile(t, root, ".github/workflows/ci.yml", []byte("on: push\n"))
	discoveryWriteFile(t, root, ".github/workflows/release.yaml", []byte("on: tag\n"))
	discoveryWriteFile(t, root, ".github/workflows/notes.txt", []byte("not a workflow\n"))

	got, err := inspectCheckout(root)
	if err != nil {
		t.Fatalf("inspectCheckout: %v", err)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none", got.Gaps)
	}

	discoveryRequireFactValues(t, "instructions", got.Instructions,
		".cursorrules", "AGENTS.md", "CLAUDE.md", "README.dev.md", "README.md")
	discoveryRequireFactValues(t, "docs", got.Docs, "docs/README.md")
	discoveryRequireFactValues(t, "tooling", got.Tooling, "go.mod", "package.json", "tsconfig.json")
	discoveryRequireFactValues(t, "environment", got.Environment,
		".devcontainer/", "Dockerfile", "compose.yaml")
	discoveryRequireFactValues(t, "ci", got.CI,
		".github/workflows/ci.yml", ".github/workflows/release.yaml")

	// Every fact carries its checkout-relative source and a section that
	// matches its kind, so the UI can group them without guessing.
	for _, fact := range got.facts() {
		if fact.Origin != "detected" {
			t.Fatalf("%s origin = %q, want detected", fact.Value, fact.Origin)
		}
		if fact.Section != fact.Kind {
			t.Fatalf("%s section = %q, kind = %q", fact.Value, fact.Section, fact.Kind)
		}
		if !strings.HasPrefix(fact.Source, "checkout:") {
			t.Fatalf("%s source = %q, want a checkout: prefix", fact.Value, fact.Source)
		}
		if fact.Value == "" || strings.Contains(fact.Value, root) {
			t.Fatalf("%s value = %q, want a relative path", fact.Value, fact.Value)
		}
	}

	if agents := discoveryFindFact(t, got.Instructions, "AGENTS.md"); agents.Source != "checkout:AGENTS.md" {
		t.Fatalf("AGENTS.md source = %q", agents.Source)
	} else if agents.Size == 0 {
		t.Fatalf("AGENTS.md size = 0, want the inspected size")
	}
	if docs := discoveryFindFact(t, got.Docs, "docs/README.md"); docs.Source != "checkout:docs/README.md" {
		t.Fatalf("docs source = %q", docs.Source)
	}
	if ci := discoveryFindFact(t, got.CI, ".github/workflows/ci.yml"); ci.Source != "checkout:.github/workflows/ci.yml" {
		t.Fatalf("ci source = %q", ci.Source)
	}
	if env := discoveryFindFact(t, got.Environment, ".devcontainer/"); env.Source != "checkout:.devcontainer" {
		t.Fatalf(".devcontainer source = %q", env.Source)
	}

	// Tooling facts identify the language without returning file content.
	for value, want := range map[string]string{
		"go.mod":        "Go",
		"package.json":  "TypeScript",
		"tsconfig.json": "TypeScript",
	} {
		if fact := discoveryFindFact(t, got.Tooling, value); fact.Detail != want {
			t.Fatalf("%s detail = %q, want %q", value, fact.Detail, want)
		}
	}
}

func TestInspectCheckoutBoundsEveryFileRead(t *testing.T) {
	root := t.TempDir()
	atLimit := bytes.Repeat([]byte("a"), discoveryReadLimit)
	overLimit := bytes.Repeat([]byte("b"), discoveryReadLimit+1)
	discoveryWriteFile(t, root, "CLAUDE.md", atLimit)
	discoveryWriteFile(t, root, "AGENTS.md", overLimit)
	discoveryWriteFile(t, root, "docs/README.md", overLimit)
	discoveryWriteFile(t, root, "go.mod", overLimit)

	got, err := inspectCheckout(root)
	if err != nil {
		t.Fatalf("inspectCheckout: %v", err)
	}

	// Exactly at the limit is readable: the bound is inclusive.
	discoveryRequireFactValues(t, "instructions", got.Instructions, "CLAUDE.md")
	// One byte past the limit is a gap, never a fact: nothing is decoded.
	discoveryRequireFactValues(t, "docs", got.Docs)
	discoveryRequireFactValues(t, "tooling", got.Tooling)
	for _, facts := range [][]DiscoveryFact{got.Instructions, got.Docs, got.Tooling} {
		for _, fact := range facts {
			if fact.Value == "absent" {
				t.Fatalf("an oversized target was reported as absent: %+v", fact)
			}
		}
	}

	want := map[string]string{
		"checkout_instructions": "AGENTS.md",
		"checkout_docs":         "docs/README.md",
		"checkout_tooling":      "go.mod",
	}
	if len(got.Gaps) != len(want) {
		t.Fatalf("gaps = %+v, want one per oversized target", got.Gaps)
	}
	for _, gap := range got.Gaps {
		path, ok := want[gap.Key]
		if !ok {
			t.Fatalf("unexpected gap key %q (%s)", gap.Key, gap.Detail)
		}
		if !strings.Contains(gap.Detail, path) {
			t.Fatalf("%s gap detail = %q, want it to name %s", gap.Key, gap.Detail, path)
		}
		if !strings.Contains(gap.Detail, "1 MiB") {
			t.Fatalf("%s gap detail = %q, want the inspection limit", gap.Key, gap.Detail)
		}
	}
}

func TestInspectCheckoutDistinguishesAbsentFromUnreadable(t *testing.T) {
	// An empty checkout is a completed search: every section reports absent.
	empty := t.TempDir()
	got, err := inspectCheckout(empty)
	if err != nil {
		t.Fatalf("inspectCheckout: %v", err)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none for an empty checkout", got.Gaps)
	}
	for _, section := range []struct {
		name  string
		facts []DiscoveryFact
	}{
		{"instructions", got.Instructions},
		{"docs", got.Docs},
		{"tooling", got.Tooling},
		{"environment", got.Environment},
		{"ci", got.CI},
	} {
		if len(section.facts) != 1 {
			t.Fatalf("%s facts = %v, want exactly one absence fact", section.name, discoveryFactValues(section.facts))
		}
		fact := section.facts[0]
		if fact.Value != "absent" {
			t.Fatalf("%s value = %q, want absent", section.name, fact.Value)
		}
		if fact.Section != section.name {
			t.Fatalf("%s section = %q", section.name, fact.Section)
		}
		if !strings.Contains(fact.Detail, "searched") || !strings.Contains(fact.Detail, "checkout") {
			t.Fatalf("%s absence detail = %q, want it to name what was searched", section.name, fact.Detail)
		}
	}

	// A directory where a known instruction file belongs cannot be read: it
	// is a gap, and the section must not claim absence.
	blocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blocked, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = inspectCheckout(blocked)
	if err != nil {
		t.Fatalf("inspectCheckout: %v", err)
	}
	if len(got.Instructions) != 0 {
		t.Fatalf("instructions facts = %v, want none", discoveryFactValues(got.Instructions))
	}
	var gap *DiscoveryGap
	for i := range got.Gaps {
		if got.Gaps[i].Key == "checkout_instructions" {
			gap = &got.Gaps[i]
		}
	}
	if gap == nil {
		t.Fatalf("gaps = %+v, want one for checkout_instructions", got.Gaps)
	}
	if gap.Section != "instructions" {
		t.Fatalf("gap section = %q", gap.Section)
	}
	if gap.State != DiscoveryCapabilityUnsupported {
		t.Fatalf("gap state = %q, want %q", gap.State, DiscoveryCapabilityUnsupported)
	}
	if !strings.Contains(gap.Detail, "AGENTS.md") {
		t.Fatalf("gap detail = %q, want it to name the target", gap.Detail)
	}
	// The two outcomes are textually distinct: a gap never reads as absence.
	if gap.Detail == "absent" || strings.Contains(gap.Detail, "searched") {
		t.Fatalf("gap detail = %q, want it to read as a failed inspection", gap.Detail)
	}
	// One unreadable target does not poison the other sections.
	discoveryRequireFactValues(t, "docs", got.Docs, "absent")
	discoveryRequireFactValues(t, "tooling", got.Tooling, "absent")
	discoveryRequireFactValues(t, "environment", got.Environment, "absent")
	discoveryRequireFactValues(t, "ci", got.CI, "absent")

	// An unreadable file is a gap too. A root-owned test process can read
	// anything, so probe rather than assume the mode took effect.
	unreadable := t.TempDir()
	discoveryWriteFile(t, unreadable, "AGENTS.md", []byte("# agents\n"))
	locked := filepath.Join(unreadable, "AGENTS.md")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
	if f, err := os.Open(locked); err == nil {
		f.Close()
		t.Log("process can read a 0o000 file (running as root); skipping that case")
	} else {
		got, err = inspectCheckout(unreadable)
		if err != nil {
			t.Fatalf("inspectCheckout: %v", err)
		}
		if len(got.Instructions) != 0 {
			t.Fatalf("instructions facts = %v, want none", discoveryFactValues(got.Instructions))
		}
		found := false
		for _, gap := range got.Gaps {
			if strings.Contains(gap.Detail, "AGENTS.md") {
				found = true
			}
		}
		if !found {
			t.Fatalf("gaps = %+v, want one naming the unreadable AGENTS.md", got.Gaps)
		}
	}
}

func TestInspectCheckoutDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	discoveryWriteFile(t, outside, "AGENTS.md", []byte("OUTSIDE INSTRUCTIONS\n"))
	discoveryWriteFile(t, outside, "docs/README.md", []byte("OUTSIDE DOCS\n"))
	discoveryWriteFile(t, outside, "Dockerfile", []byte("FROM outside\n"))

	root := t.TempDir()
	for _, rel := range []string{"AGENTS.md", "docs", "Dockerfile"} {
		target := filepath.Join(outside, filepath.FromSlash(rel))
		link := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	got, err := inspectCheckout(root)
	if err != nil {
		t.Fatalf("inspectCheckout: %v", err)
	}

	// Nothing behind a symlink is read or reported as a source.
	for _, fact := range got.facts() {
		if fact.Value == "absent" {
			continue
		}
		t.Fatalf("symlinked target reported as %q (%s)", fact.Value, fact.Source)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none: a symlink is skipped, not failed", got.Gaps)
	}
	// The searches completed, so each section says what it looked for.
	discoveryRequireFactValues(t, "instructions", got.Instructions, "absent")
	discoveryRequireFactValues(t, "docs", got.Docs, "absent")
	discoveryRequireFactValues(t, "environment", got.Environment, "absent")
	for _, fact := range got.facts() {
		if strings.Contains(fact.Detail, outside) || strings.Contains(fact.Source, outside) {
			t.Fatalf("fact leaked the symlink target: %+v", fact)
		}
	}
}

func TestInspectCheckoutDoesNotExecuteSetup(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "setup-ran")
	command := "touch '" + filepath.ToSlash(marker) + "'"

	discoveryWriteFile(t, root, "Makefile", []byte("all:\n\t"+command+"\n"))
	discoveryWriteFile(t, root, "package.json",
		[]byte(`{"name":"x","scripts":{"postinstall":"`+command+`"}}`))
	discoveryWriteFile(t, root, ".devcontainer/devcontainer.json",
		[]byte(`{"image":"x","postCreateCommand":"`+command+`"}`))
	discoveryWriteFile(t, root, ".github/workflows/ci.yml", []byte("jobs:\n  b:\n    steps:\n      - run: "+command+"\n"))

	got, err := inspectCheckout(root)
	if err != nil {
		t.Fatalf("inspectCheckout: %v", err)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none", got.Gaps)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("setup ran during inspection: marker %s exists (err = %v)", marker, err)
	}
	// The manifests are still detected; only their commands stay untouched.
	discoveryRequireFactValues(t, "tooling", got.Tooling, "package.json")
	discoveryRequireFactValues(t, "environment", got.Environment, ".devcontainer/")
	discoveryRequireFactValues(t, "ci", got.CI, ".github/workflows/ci.yml")
}

// discoveryTreeEntry is one working-tree file's mode and content hash.
type discoveryTreeEntry struct {
	mode string
	sum  string
}

// discoverySnapshot captures what a checkout looked like from outside Git:
// HEAD, porcelain status, and every working-tree file outside .git/.
func discoverySnapshot(t *testing.T, dir string) (head, status string, tree map[string]discoveryTreeEntry) {
	t.Helper()
	head = strings.TrimSpace(discoveryGitOutput(t, dir, "rev-parse", "HEAD"))
	status = discoveryGitOutput(t, dir, "status", "--porcelain", "--untracked-files=all")
	tree = map[string]discoveryTreeEntry{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		tree[rel] = discoveryTreeEntry{mode: info.Mode().String(), sum: hex.EncodeToString(sum[:])}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return head, status, tree
}

func discoveryGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// discoveryGitRepo creates a disposable repository with one commit.
func discoveryGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "--initial-branch=main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	discoveryWriteFile(t, dir, "README.md", []byte("seed\n"))
	discoveryWriteFile(t, dir, "src/main.go", []byte("package main\n"))
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "seed")
	return dir
}

func newDiscoveryFleet(t *testing.T) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	f := NewFleet(ws, "unused", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(f.Close)
	return f
}

func newDiscoveryGitFleet(t *testing.T) *Fleet {
	t.Helper()
	f := newDiscoveryFleet(t)
	f.git = testGitRunner(t)
	return f
}

func TestDiscoveryGitStateIsReadOnly(t *testing.T) {
	dir := discoveryGitRepo(t)
	f := newDiscoveryGitFleet(t)

	headBefore, statusBefore, treeBefore := discoverySnapshot(t, dir)

	head, branch, dirty, files, detail, err := f.discoveryGitState(context.Background(), dir)
	if err != nil {
		t.Fatalf("discoveryGitState: %v", err)
	}
	if head != headBefore {
		t.Fatalf("head = %q, want %q", head, headBefore)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	if dirty == nil || *dirty {
		t.Fatalf("dirty = %v, want a determined clean checkout", dirty)
	}
	if len(files) != 0 {
		t.Fatalf("dirtyFiles = %v, want none", files)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty for a clean checkout", detail)
	}

	headAfter, statusAfter, treeAfter := discoverySnapshot(t, dir)
	if headAfter != headBefore {
		t.Fatalf("HEAD moved: %q -> %q", headBefore, headAfter)
	}
	if statusAfter != statusBefore {
		t.Fatalf("status changed:\n before %q\n after  %q", statusBefore, statusAfter)
	}
	if !reflect.DeepEqual(treeBefore, treeAfter) {
		t.Fatalf("working tree changed:\n before %v\n after  %v", treeBefore, treeAfter)
	}
}

// discoveryGitCall records one hardened git invocation.
type discoveryGitCall struct {
	args []string
	env  []string
}

type discoveryRecordingFleet struct {
	fleet *Fleet
	calls []discoveryGitCall
}

// discoveryGitVerb returns the subcommand of a hardened git invocation,
// skipping the global options hardenedGitArgs and the caller prefix.
func discoveryGitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "-C", "--git-dir", "--work-tree":
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			return args[i]
		}
	}
	return ""
}

func discoveryHasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func newDiscoveryRecordingFleet(t *testing.T, responses map[string]string) *discoveryRecordingFleet {
	t.Helper()
	f := newDiscoveryFleet(t)
	if f.git == nil {
		f.git = &gitRunner{}
	}
	rec := &discoveryRecordingFleet{fleet: f}
	f.git.exec = func(dir string, env []string, args ...string) ([]byte, error) {
		rec.calls = append(rec.calls, discoveryGitCall{
			args: append([]string(nil), args...),
			env:  append([]string(nil), env...),
		})
		if out, ok := responses[discoveryGitVerb(args)]; ok {
			return []byte(out), nil
		}
		return nil, fmt.Errorf("unexpected git invocation %v", args)
	}
	return rec
}

func TestDiscoveryGitStateReportsDirtyWithoutRepair(t *testing.T) {
	dir := discoveryGitRepo(t)
	discoveryWriteFile(t, dir, "README.md", []byte("modified\n"))
	discoveryWriteFile(t, dir, "notes.txt", []byte("untracked\n"))
	f := newDiscoveryGitFleet(t)

	headBefore, statusBefore, treeBefore := discoverySnapshot(t, dir)

	head, _, dirty, files, detail, err := f.discoveryGitState(context.Background(), dir)
	if err != nil {
		t.Fatalf("discoveryGitState: %v", err)
	}
	if head != headBefore {
		t.Fatalf("head = %q, want %q", head, headBefore)
	}
	if dirty == nil || !*dirty {
		t.Fatalf("dirty = %v, want true", dirty)
	}
	sort.Strings(files)
	if !reflect.DeepEqual(files, []string{"README.md", "notes.txt"}) {
		t.Fatalf("dirtyFiles = %v, want the modified and untracked files", files)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty when the list is not capped", detail)
	}

	// Reporting dirtiness repairs nothing: the operator's bytes and status
	// are exactly what they were.
	_, statusAfter, treeAfter := discoverySnapshot(t, dir)
	if statusAfter != statusBefore {
		t.Fatalf("status changed:\n before %q\n after  %q", statusBefore, statusAfter)
	}
	if !strings.Contains(statusAfter, "?? notes.txt") {
		t.Fatalf("untracked file was removed or committed: %q", statusAfter)
	}
	if !reflect.DeepEqual(treeBefore, treeAfter) {
		t.Fatalf("working tree changed:\n before %v\n after  %v", treeBefore, treeAfter)
	}

	// Local reads go through the hardened runner with an empty Credential,
	// and only the four allowlisted verbs are ever issued.
	rec := newDiscoveryRecordingFleet(t, map[string]string{
		"rev-parse":    "abc123\n",
		"symbolic-ref": "main\n",
		"status":       " M README.md\n?? notes.txt\n",
		"for-each-ref": "main\n",
	})
	if _, _, _, _, _, err := rec.fleet.discoveryGitState(context.Background(), dir); err != nil {
		t.Fatalf("discoveryGitState with a recording runner: %v", err)
	}
	allowed := map[string]bool{
		"rev-parse":    true,
		"symbolic-ref": true,
		"status":       true,
		"for-each-ref": true,
	}
	forbidden := []string{
		"fetch", "pull", "push", "clone", "checkout", "switch", "reset", "clean",
		"add", "commit", "merge", "rebase", "stash", "gc", "prune", "update-ref",
		"config", "remote", "worktree", "submodule",
	}
	seen := map[string]bool{}
	for _, call := range rec.calls {
		verb := discoveryGitVerb(call.args)
		seen[verb] = true
		if !allowed[verb] {
			t.Fatalf("git verb %q is not allowlisted (args %v)", verb, call.args)
		}
		for _, bad := range forbidden {
			if discoveryHasArg(call.args, bad) {
				t.Fatalf("local read used %q (args %v)", bad, call.args)
			}
		}
		if verb == "status" && !discoveryHasArg(call.args, "--no-optional-locks") {
			t.Fatalf("status ran without --no-optional-locks: %v", call.args)
		}
		for _, kv := range call.env {
			if strings.HasPrefix(kv, "MARSHAL_ASKPASS") || strings.HasPrefix(kv, "GIT_ASKPASS") {
				t.Fatalf("local read carried credential environment %q", kv)
			}
		}
	}
	for verb := range allowed {
		if !seen[verb] {
			t.Fatalf("git %q was never exercised; the allowlist assertion would be vacuous", verb)
		}
	}
}

func TestDiscoveryGitStateBoundsDirtyFilesAndRepresentsDetachedHead(t *testing.T) {
	dir := discoveryGitRepo(t)
	f := newDiscoveryGitFleet(t)
	sha := strings.TrimSpace(discoveryGitOutput(t, dir, "rev-parse", "HEAD"))

	// A clean detached checkout: no branch, its SHA in head, and a detail
	// that says why the branch is empty.
	mustGit(t, dir, "update-ref", "--no-deref", "HEAD", sha)
	head, branch, dirty, files, detail, err := f.discoveryGitState(context.Background(), dir)
	if err != nil {
		t.Fatalf("discoveryGitState: %v", err)
	}
	if head != sha {
		t.Fatalf("head = %q, want %q", head, sha)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want empty for a detached HEAD", branch)
	}
	if dirty == nil || *dirty {
		t.Fatalf("dirty = %v, want a determined clean checkout", dirty)
	}
	if len(files) != 0 {
		t.Fatalf("dirtyFiles = %v, want none", files)
	}
	if !strings.Contains(detail, "detached HEAD") {
		t.Fatalf("detail = %q, want it to say detached HEAD", detail)
	}

	// More dirty entries than the cap: the list is bounded and the detail
	// counts what was omitted.
	const extra = 105
	for i := 0; i < extra; i++ {
		discoveryWriteFile(t, dir, fmt.Sprintf("dirty-%03d.txt", i), []byte("x\n"))
	}
	head, branch, dirty, files, detail, err = f.discoveryGitState(context.Background(), dir)
	if err != nil {
		t.Fatalf("discoveryGitState: %v", err)
	}
	if head != sha || branch != "" {
		t.Fatalf("head = %q, branch = %q, want the detached SHA and no branch", head, branch)
	}
	if dirty == nil || !*dirty {
		t.Fatalf("dirty = %v, want true", dirty)
	}
	if len(files) != discoveryDirtyFileLimit {
		t.Fatalf("dirtyFiles = %d, want the %d-entry cap", len(files), discoveryDirtyFileLimit)
	}
	wantOmitted := fmt.Sprintf("%d additional", extra-discoveryDirtyFileLimit)
	if !strings.Contains(detail, "detached HEAD") {
		t.Fatalf("detail = %q, want the detached clause", detail)
	}
	if !strings.Contains(detail, wantOmitted) {
		t.Fatalf("detail = %q, want it to state %q", detail, wantOmitted)
	}

	// The cap is not a detached-only rule: reattach and the truncation
	// clause stands alone.
	mustGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	_, branch, dirty, files, detail, err = f.discoveryGitState(context.Background(), dir)
	if err != nil {
		t.Fatalf("discoveryGitState: %v", err)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main after reattaching", branch)
	}
	if dirty == nil || !*dirty {
		t.Fatalf("dirty = %v, want true", dirty)
	}
	if len(files) != discoveryDirtyFileLimit {
		t.Fatalf("dirtyFiles = %d, want the %d-entry cap", len(files), discoveryDirtyFileLimit)
	}
	if strings.Contains(detail, "detached HEAD") {
		t.Fatalf("detail = %q, want no detached clause on a branch", detail)
	}
	if !strings.Contains(detail, wantOmitted) {
		t.Fatalf("detail = %q, want it to state %q", detail, wantOmitted)
	}
}
