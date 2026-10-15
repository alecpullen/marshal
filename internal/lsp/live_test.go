package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireServer(t *testing.T, bin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("live LSP test skipped under -short")
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not installed", bin)
	}
}

func writeGoModule(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"a/a.go": "package a\n\nfunc Add(x, y int) int { return x + y }\n\ntype T struct{}\n\nfunc (T) Hello() string { return \"hi\" }\n",
		"b/b.go": "package b\n\nimport \"example.com/m/a\"\n\nfunc Use() int { return a.Add(1, 2) }\n",
	}
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644)
	}
}

func startLive(t *testing.T, dir string) (*Manager, *Handle, context.Context) {
	t.Helper()
	servers := map[string]ServerSpec{"go": {Command: "gopls"}}
	m := NewManager(dir, ServersForRoot(servers, dir), nil)
	h := NewHandle(m, servers, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	if !m.WaitReady(ctx, "go", 60*time.Second) {
		t.Fatal("gopls never became ready")
	}
	return m, h, ctx
}

// warm polls until gopls has loaded the module (workspace/symbol answers).
func warm(t *testing.T, h *Handle, ctx context.Context) {
	t.Helper()
	q := NewSymbolQuery(h, nil, 0)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if res, _ := q.Query(ctx, SymbolRequest{Op: "find", Name: "Add"}); strings.Contains(res.Content, "a/a.go") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("gopls never loaded the module")
}

func TestLiveSignatureChangeReportsBrokenCaller(t *testing.T) {
	requireServer(t, "gopls")
	dir := t.TempDir()
	writeGoModule(t, dir)
	m, h, ctx := startLive(t, dir)
	warm(t, h, ctx)
	m.Workspace().maxWait = 15 * time.Second
	// gopls publishes an empty set for the edited file as soon as it sees
	// the write and the error in the depending file about a second later,
	// so the default 300 ms quiet window closes in the gap. Widen it.
	m.Workspace().quiet = 2 * time.Second
	finish, ok := m.Workspace().BeginEdit([]string{"a/a.go"})
	if !ok {
		t.Fatal("BeginEdit not ok")
	}
	os.WriteFile(filepath.Join(dir, "a/a.go"), []byte("package a\n\nfunc Add(x int) int { return x }\n\ntype T struct{}\n\nfunc (T) Hello() string { return \"hi\" }\n"), 0o644)
	out := finish(ctx)
	if !strings.Contains(out, "b/b.go") {
		t.Fatalf("broken caller not reported: %q", out)
	}
}

func TestLiveMethodResolvesByName(t *testing.T) {
	requireServer(t, "gopls")
	dir := t.TempDir()
	writeGoModule(t, dir)
	_, h, ctx := startLive(t, dir)
	warm(t, h, ctx)
	res, err := NewSymbolQuery(h, nil, 0).Query(ctx, SymbolRequest{Op: "definition", Name: "T.Hello"})
	if err != nil || !strings.Contains(res.Content, "a/a.go:7") {
		t.Fatalf("definition = %+v, %v", res, err)
	}
}

func TestLiveReferencesCounted(t *testing.T) {
	requireServer(t, "gopls")
	dir := t.TempDir()
	writeGoModule(t, dir)
	_, h, ctx := startLive(t, dir)
	warm(t, h, ctx)
	res, err := NewSymbolQuery(h, nil, 0).Query(ctx, SymbolRequest{Op: "references", Name: "Add"})
	if err != nil || !strings.Contains(res.Content, "b/b.go:5") || res.Summary != "1 reference(s)" {
		t.Fatalf("references = %+v, %v", res, err)
	}
}

// Review focus 1.
func TestLiveWorktreeDiagnosticsUseWorktreeCopy(t *testing.T) {
	requireServer(t, "gopls")
	project, worktree := t.TempDir(), t.TempDir()
	writeGoModule(t, project)
	writeGoModule(t, worktree)
	_, h, ctx := startLive(t, project)
	newM, old := h.Restart(worktree)
	cctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	_ = old
	go newM.Run(cctx)
	if !newM.WaitReady(cctx, "go", 60*time.Second) {
		t.Fatal("worktree gopls not ready")
	}
	warm(t, h, cctx)
	newM.Workspace().maxWait = 15 * time.Second
	// As above: the worktree server needs more than the default quiet window
	// to publish the error in the depending file.
	newM.Workspace().quiet = 2 * time.Second
	finish, ok := newM.Workspace().BeginEdit([]string{"a/a.go"})
	if !ok {
		t.Fatal("BeginEdit not ok in worktree")
	}
	os.WriteFile(filepath.Join(worktree, "a/a.go"), []byte("package a\n\nfunc Add(x int) int { return x }\n"), 0o644)
	out := finish(cctx)
	if !strings.Contains(out, "b/b.go") {
		t.Fatalf("worktree edit not diagnosed: %q", out)
	}
	if body, _ := os.ReadFile(filepath.Join(project, "a/a.go")); !strings.Contains(string(body), "x, y int") {
		t.Fatal("project copy must be untouched")
	}
}

func TestLiveTypeScriptDefinition(t *testing.T) {
	requireServer(t, "typescript-language-server")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{"compilerOptions":{"strict":true},"include":["*.ts"]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "lib.ts"), []byte("export function greet(n: string): string { return n }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.ts"), []byte("import { greet } from './lib'\ngreet('x')\n"), 0o644)
	servers := map[string]ServerSpec{"typescript": {Command: "typescript-language-server", Args: []string{"--stdio"}}}
	m := NewManager(dir, ServersForRoot(servers, dir), nil)
	h := NewHandle(m, servers, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	if !m.WaitReady(ctx, "typescript", 60*time.Second) {
		t.Fatal("typescript-language-server not ready")
	}
	_ = m.Workspace().DidWrite("main.ts")
	q := NewSymbolQuery(h, nil, 0)
	req := SymbolRequest{Op: "definition", Name: "greet", Path: "main.ts", Line: 2}
	// Before tsserver has loaded the project it answers from the open file
	// alone (main.ts:1, the import specifier); once loaded it answers with
	// the real declaration. Poll until it has loaded, as warm does for gopls.
	var res SymbolResult
	var err error
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		res, err = q.Query(ctx, req)
		if err == nil && strings.Contains(res.Content, "lib.ts:1") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil || !strings.Contains(res.Content, "lib.ts:1") {
		t.Fatalf("ts definition = %+v, %v", res, err)
	}
}
