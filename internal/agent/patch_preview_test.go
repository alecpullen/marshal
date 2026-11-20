package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestPreviewPatchDiffGeneratesUnifiedDiff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	patchText := "File: main.go\n<<<<<<< SEARCH\nfunc main() {}\n=======\nfunc main() {\n\tprintln(\"hi\")\n}\n>>>>>>> REPLACE\n"

	diff, err := PreviewPatchDiff(dir, patchText, false)
	if err != nil {
		t.Fatalf("PreviewPatchDiff returned error: %v", err)
	}
	if !strings.Contains(diff, "--- a/main.go") || !strings.Contains(diff, "+++ b/main.go") {
		t.Fatalf("diff missing unified diff headers: %s", diff)
	}
	if !strings.Contains(diff, `println("hi")`) {
		t.Fatalf("diff missing added content: %s", diff)
	}
}

func TestPreviewPatchDiffRejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	patchText := "File: /etc/passwd\n<<<<<<< SEARCH\nroot\n=======\nroot2\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(dir, patchText, false); err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
}

func TestPreviewPatchDiffRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	patchText := "File: ../outside.go\n<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(dir, patchText, false); err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestPreviewPatchDiffRejectsSearchBlockNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	patchText := "File: main.go\n<<<<<<< SEARCH\nnot present\n=======\nreplacement\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(dir, patchText, false); err == nil {
		t.Fatal("expected validation error, got nil")
	}
}

func TestPreviewPatchDiffRejectsEmptyPatch(t *testing.T) {
	dir := t.TempDir()
	if _, err := PreviewPatchDiff(dir, "", false); err == nil {
		t.Fatal("expected error for empty patch, got nil")
	}
}

// boundTargetFixture builds the fixture the resolver tests share: a project
// checkout and a worktree that both contain target.txt with different bytes,
// and a session bound to the worktree. A reader that resolves the wrong copy
// is therefore visible in the bytes it reports.
func boundTargetFixture(t *testing.T) (root, worktree, target string) {
	t.Helper()
	root = t.TempDir()
	worktree = filepath.Join(root, ".marshal", "worktrees", "bound")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("root copy\nfunc target() { ok() }\n"), 0644); err != nil {
		t.Fatalf("write root copy: %v", err)
	}
	// The worktree copy holds the same failing line far enough down that a
	// line number alone identifies which copy was read.
	var wt strings.Builder
	for i := 0; i < 11; i++ {
		wt.WriteString("// filler line " + strconv.Itoa(i) + "\n")
	}
	wt.WriteString("worktree copy\nfunc target() { ok() }\n")
	if err := os.WriteFile(filepath.Join(worktree, "target.txt"), []byte(wt.String()), 0644); err != nil {
		t.Fatalf("write worktree copy: %v", err)
	}
	return root, worktree, "target.txt"
}

// TestPreviewAndRetryHintUseBoundTarget pins review focus 5: the approval
// preview and the retry hint must read the same copy the write tool would
// edit. Both used to resolve against the launch directory, so the diff a user
// approved and the line numbers a model was told to re-read could describe a
// different file than the one the edit changed.
func TestPreviewAndRetryHintUseBoundTarget(t *testing.T) {
	projectRoot, worktree, _ := boundTargetFixture(t)
	state := session.New(config.Default(), projectRoot, time.Unix(100, 0), session.Persistence{})
	state.SetWorkspace(session.Workspace{ProjectRoot: projectRoot, ActiveRoot: worktree, Branch: "feat/bound"})

	// The resolver stands in for the native toolset's: relative paths resolve
	// from the worktree the tool is bound to, not the launch directory.
	resolve := func(_ context.Context, p string, _ bool) (string, error) {
		return filepath.Join(worktree, p), nil
	}
	reg := registry.New()
	tool := registry.Tool{
		Name:        "file.write_patch",
		Description: "patch",
		Risk:        registry.RiskWorkspaceWrite,
		ResolvePath: resolve,
		// The test drives the approval/preview and retry-hint paths directly;
		// the handler only satisfies registration.
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "patched"}, nil
		},
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register tool: %v", err)
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	patchText := "File: target.txt\n<<<<<<< SEARCH\nfunc target() { ok() }\n=======\nfunc target() { ok(); more() }\n>>>>>>> REPLACE\n"
	argsMap := map[string]interface{}{"patch": patchText}
	args, err := json.Marshal(argsMap)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	// The approval diff is built while the user is deciding, so read it off the
	// pending call the way the TUI does.
	pending := make(chan string, 1)
	go func() {
		_, _, _, _ = r.requestApproval(context.Background(), tool, "file.write_patch", args, argsMap, "patch target.txt", false)
		pending <- ""
	}()
	deadline := time.After(2 * time.Second)
	var diff string
	for {
		if pc := state.PendingApproval(); pc != nil {
			diff = pc.Diff
			pc.Respond(session.UserApprovalDecision{Approved: false})
			break
		}
		select {
		case <-deadline:
			t.Fatal("approval preview never appeared")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	<-pending

	if !strings.Contains(diff, "worktree copy") {
		t.Fatalf("preview did not read the bound worktree copy:\n%s", diff)
	}
	if strings.Contains(diff, "root copy") {
		t.Fatalf("preview read the launch-directory copy instead of the bound target:\n%s", diff)
	}

	// The retry hint names the file and line window of the same copy. The
	// SEARCH block matches the root copy exactly, so a reader resolving the
	// wrong root would find it and produce no hint at all.
	failedText := "File: target.txt\n<<<<<<< SEARCH\nfunc target() { ok(); more() }\n=======\nfunc target() { ok(); more(); done() }\n>>>>>>> REPLACE\n"
	failedArgs, err := json.Marshal(map[string]interface{}{"patch": failedText})
	if err != nil {
		t.Fatalf("marshal failed args: %v", err)
	}
	execErr := errors.New("file.write_patch: " + patch.FailureSearchNotFound + " in target.txt")
	hint := failedPatchTargetHint(r, "file.write_patch", failedArgs, execErr)
	if hint == "" {
		t.Fatal("expected a retry hint for the failing SEARCH block")
	}
	if !strings.Contains(hint, "file.read target.txt") {
		t.Fatalf("hint does not name the bound copy relatively:\n%s", hint)
	}
	if strings.Contains(hint, "root copy") {
		t.Fatalf("hint describes the launch-directory copy:\n%s", hint)
	}
	// The advertised window is in the worktree copy's layout (the failing line
	// sits at line 13 there), not the root copy's (line 2).
	start := hintStartLine(t, hint)
	if start < 8 {
		t.Fatalf("hint advertises the wrong copy: start_line=%d in\n%s", start, hint)
	}
}

// hintStartLine extracts the start_line the retry hint advertises.
func hintStartLine(t *testing.T, hint string) int {
	t.Helper()
	const marker = "start_line="
	i := strings.Index(hint, marker)
	if i < 0 {
		t.Fatalf("hint has no start_line:\n%s", hint)
	}
	rest := hint[i+len(marker):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("parse start_line from %q: %v", hint, err)
	}
	return n
}
