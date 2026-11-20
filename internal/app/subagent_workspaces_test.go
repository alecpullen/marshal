package app

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/llm/pricing"
	"marshal/internal/sandbox"
	"marshal/internal/snapshot"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// plainRunner is a command runner that states nothing about linked worktrees.
type plainRunner struct{}

func (plainRunner) Run(context.Context, native.CommandRequest) (native.CommandResult, error) {
	return native.CommandResult{}, nil
}

// TestIsolatedWorktreeSupportIsDetectedFromTheBackend pins where the isolated
// dispatch capability comes from: the execution backend's own statement, never a
// model argument. Restricted and passthrough execute on the host and can host a
// linked worktree; a runner that states nothing reports no support, because an
// absent capability is not a yes. The container backend's own refusal is pinned
// in internal/sandbox, whose fixture needs no running container.
func TestIsolatedWorktreeSupportIsDetectedFromTheBackend(t *testing.T) {
	restricted, err := sandbox.New(sandbox.Config{Backend: "restricted"}, nil)
	if err != nil {
		t.Fatalf("sandbox.New(restricted): %v", err)
	}
	if !native.SupportsLinkedWorktrees(restricted) {
		t.Fatal("restricted executes on the host and must permit a linked git worktree")
	}

	passthrough, err := sandbox.New(sandbox.Config{Backend: "passthrough", UnsafePassthrough: true}, nil)
	if err != nil {
		t.Fatalf("sandbox.New(passthrough): %v", err)
	}
	if !native.SupportsLinkedWorktrees(passthrough) {
		t.Fatal("passthrough executes on the host and must permit a linked git worktree")
	}

	// A runner that never states the capability supports nothing: isolated
	// dispatch must refuse rather than assume a host filesystem.
	if native.SupportsLinkedWorktrees(plainRunner{}) {
		t.Fatal("a runner without the capability was reported as supporting linked worktrees")
	}
}

// workspaceFixture is one conversation with a real parent tool registry and the
// production child factory, so a test exercises the wiring the runtime uses
// rather than a hand-rolled native option set. Every directory is a real
// temporary tree: nothing here reads the developer's checkout.
type workspaceFixture struct {
	cfg       config.Config
	parent    *session.State
	parentReg *registry.Registry
	factory   agent.SubagentRunnerFactory
	database  *db.DB
	fileTrack native.FileTracker
}

// newWorkspaceFixture builds the fixture over root, letting a caller adjust the
// config and the parent's native options before either is registered. A nil
// mutate keeps the defaults.
func newWorkspaceFixture(t *testing.T, root string, mutate func(*config.Config, *native.Options)) *workspaceFixture {
	t.Helper()
	cfg := config.Default()
	cfg.Project.Name = "subagent-workspace-binding"
	cfg.Privacy.RemoteProvidersAllowed = false

	database, err := db.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	parent := session.New(cfg, root, time.Unix(100, 0), session.Persistence{SessionID: "parent-session"})
	opts := native.Options{
		WorkspaceRoot: root,
		SessionState:  parent,
		Config:        cfg,
		DB:            database,
		ProjectID:     0,
		FileTracker:   filetrack.New(database.SQLDB(), "parent-session"),
		AgentID:       "main",
		AgentLabel:    "the main agent",
	}
	if mutate != nil {
		mutate(&cfg, &opts)
	}
	// The state was built from the pre-mutation config; carry the caller's
	// adjustments into the state the tools read.
	parent.Config = cfg

	parentReg := registry.New()
	if err := native.RegisterAll(parentReg, opts); err != nil {
		t.Fatalf("register parent registry: %v", err)
	}
	pol := policy.NewEngine(&cfg, nil)
	pol.WithRegistry(parentReg)

	factory, _ := buildSubagentFactoryWithLock(cfg, parent, nil, parentReg, pol, "m", nil, nil, database, 0, pricing.ModelPricing{}, &swarm.WriteLock{}, opts, nil, nil)
	return &workspaceFixture{
		cfg:       cfg,
		parent:    parent,
		parentReg: parentReg,
		factory:   factory,
		database:  database,
		fileTrack: opts.FileTracker,
	}
}

// child builds one child through the production factory with an isolated
// binding to root: the child owns that tree.
func (f *workspaceFixture) child(t *testing.T, taskKey, root string) (*agent.Runner, *session.State) {
	t.Helper()
	return f.childWithBinding(t, agent.SubagentRequest{
		TaskKey:    taskKey,
		ActorID:    "actor-" + taskKey,
		Binding:    subagentwork.Binding{Mode: subagentwork.ModeIsolated, Root: root, ProjectRoot: f.parent.WorkingDir},
		Target:     subagentwork.Target{Root: f.parent.WorkingDir},
		ActorLabel: "a subagent",
	})
}

// childWithBinding builds one child through the production factory, returning
// the runner and its own session state.
func (f *workspaceFixture) childWithBinding(t *testing.T, req agent.SubagentRequest) (*agent.Runner, *session.State) {
	t.Helper()
	child, childState, err := f.factory(req)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child == nil || childState == nil {
		t.Fatal("factory returned no child")
	}
	return child, childState
}

// sharedBindingFor states the explicit shared binding a fixture supplies when it
// is not exercising dispatch's isolated allocation. A fixture that wants a
// shared child must say so; there is no implicit fallback.
func sharedBindingFor(parent *session.State) (subagentwork.Binding, subagentwork.Target) {
	ws := parent.Workspace()
	binding := subagentwork.Binding{
		Mode:        subagentwork.ModeShared,
		Root:        ws.ActiveRoot,
		ProjectRoot: ws.ProjectRoot,
		Branch:      ws.Branch,
		BaseSHA:     ws.BaseSha,
	}
	target := subagentwork.Target{Root: ws.ProjectRoot, Branch: ws.TargetBranch, BaseSHA: ws.BaseSha}
	return binding, target
}

// mustSharedBinding and mustSharedTarget are the one-value forms of
// sharedBindingFor for fixtures that only need one of the two.
func mustSharedBinding(parent *session.State) subagentwork.Binding {
	binding, _ := sharedBindingFor(parent)
	return binding
}

func mustSharedTarget(parent *session.State) subagentwork.Target {
	_, target := sharedBindingFor(parent)
	return target
}

// subagentRequestForSharing builds the request a fixture uses when it wants a
// child that shares the conversation's own checkout. Dispatch always states its
// binding; a fixture must do the same rather than rely on an implicit fallback.
func subagentRequestForSharing(parent *session.State) agent.SubagentRequest {
	binding, target := sharedBindingFor(parent)
	return agent.SubagentRequest{ActorLabel: "a subagent", Binding: binding, Target: target}
}

// callTool dispatches one tool by name with JSON args, the way the runner does.
func callTool(t *testing.T, reg *registry.Registry, name, args string) (registry.ToolResult, error) {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("registry has no %s", name)
	}
	return tool.Handler(context.Background(), registry.ToolCall{Name: name, Args: []byte(args)})
}

// canonicalDir resolves a temp directory the way the tools resolve roots, so a
// platform whose temp root is a symlink compares equal.
func canonicalDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return resolved
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s exists but must not", path)
	}
}

// TestChildCannotRetargetParentOrSibling pins the split between the parent and
// its children: each of them writes only into the tree the dispatcher recorded
// for it, and no child can reach a rebinding or integration tool that would
// move it somewhere else.
func TestChildCannotRetargetParentOrSibling(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())
	rootB := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)

	childA, stateA := f.child(t, "task-a", rootA)
	childB, stateB := f.child(t, "task-b", rootB)

	if got := stateA.Workspace().ActiveRoot; got != rootA {
		t.Errorf("child A active root = %q, want %q", got, rootA)
	}
	if got := stateB.Workspace().ActiveRoot; got != rootB {
		t.Errorf("child B active root = %q, want %q", got, rootB)
	}
	if got := f.parent.Workspace().ActiveRoot; got != parentRoot {
		t.Errorf("parent active root = %q, want %q (a child must not move it)", got, parentRoot)
	}

	// A child cannot rebind its own checkout or integrate into the parent's.
	for _, name := range []string{"workspace.worktree", "workspace.finish"} {
		if _, ok := childA.Registry.Lookup(name); ok {
			t.Fatalf("child A registry still holds %s", name)
		}
		if _, ok := childB.Registry.Lookup(name); ok {
			t.Fatalf("child B registry still holds %s", name)
		}
	}

	if _, err := callTool(t, childA.Registry, "file.write", `{"path":"only-a.txt","content":"a"}`); err != nil {
		t.Fatalf("child A file.write: %v", err)
	}
	if _, err := callTool(t, childB.Registry, "file.write", `{"path":"only-b.txt","content":"b"}`); err != nil {
		t.Fatalf("child B file.write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootA, "only-a.txt")); err != nil {
		t.Fatalf("child A's write did not land in its own root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "only-b.txt")); err != nil {
		t.Fatalf("child B's write did not land in its own root: %v", err)
	}
	mustNotExist(t, filepath.Join(rootA, "only-b.txt"))
	mustNotExist(t, filepath.Join(rootB, "only-a.txt"))
	mustNotExist(t, filepath.Join(parentRoot, "only-a.txt"))
	mustNotExist(t, filepath.Join(parentRoot, "only-b.txt"))
}

// TestSharedChildRetainsRootAfterParentSwitch pins the rule that a shared child
// shares files, not a mutable pointer: the parent moving to another checkout
// must not redirect the child's files, commands, authority or previews.
func TestSharedChildRetainsRootAfterParentSwitch(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	elsewhere := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)

	binding, target := sharedBindingFor(f.parent)
	child, childState := f.childWithBinding(t, agent.SubagentRequest{
		TaskKey: "task-shared", ActorID: "actor-shared", ActorLabel: "a subagent",
		Binding: binding, Target: target,
	})

	// The parent leaves for another checkout. Everything below must still be
	// the root the dispatch captured.
	f.parent.SetWorkspace(session.Workspace{ProjectRoot: parentRoot, ActiveRoot: elsewhere})
	if got := f.parent.Workspace().ActiveRoot; got != elsewhere {
		t.Fatalf("parent active root = %q, want the switched root %q", got, elsewhere)
	}

	if _, err := callTool(t, child.Registry, "file.write", `{"path":"shared.txt","content":"x"}`); err != nil {
		t.Fatalf("shared child file.write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parentRoot, "shared.txt")); err != nil {
		t.Fatalf("shared child wrote outside its captured root: %v", err)
	}
	mustNotExist(t, filepath.Join(elsewhere, "shared.txt"))

	// Commands capture the bound root at admission, not the parent's new one.
	shell, ok := child.Registry.Lookup("shell.run")
	if !ok || shell.PrepareCommand == nil {
		t.Fatal("child shell.run has no command preparation")
	}
	facts, err := shell.PrepareCommand(context.Background(), registry.ToolCall{Name: "shell.run", Args: []byte(`{"command":"true"}`)})
	if err != nil {
		t.Fatalf("prepare child shell.run: %v", err)
	}
	if facts.Dir != parentRoot {
		t.Errorf("child command dir = %q, want the captured root %q", facts.Dir, parentRoot)
	}

	// The child's authority still reports the root it is bound to.
	if got := childState.InteractionAuthority().Snapshot().WorkspaceRoot; got != parentRoot {
		t.Errorf("child authority workspace root = %q, want %q", got, parentRoot)
	}

	// Previews resolve against the bound root too.
	writer, ok := child.Registry.Lookup("file.write")
	if !ok || writer.ResolvePath == nil {
		t.Fatal("child file.write has no preview resolver")
	}
	preview, err := writer.ResolvePath(context.Background(), "shared.txt", true)
	if err != nil {
		t.Fatalf("preview shared.txt: %v", err)
	}
	if want := filepath.Join(parentRoot, "shared.txt"); preview != want {
		t.Errorf("preview = %q, want %q", preview, want)
	}
}

// TestChildUndoAndSnapshotsUseBoundWorkspace pins undo and snapshot ownership:
// an isolated child's rollback evidence and snapshots stay in its own tree, and
// a shared child's backup stays visible to the conversation's /undo with the
// path it recorded, even after the parent rebinds.
func TestChildUndoAndSnapshotsUseBoundWorkspace(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())
	elsewhere := canonicalDir(t, t.TempDir())

	var rooted *snapshot.Rooted
	f := newWorkspaceFixture(t, parentRoot, func(_ *config.Config, opts *native.Options) {
		rooted = snapshot.NewRooted(t.TempDir(), parentRoot, func() string { return parentRoot }, 1<<20, nil, slog.New(slog.DiscardHandler))
		opts.SessionState.SetSnapshotter(rooted)
	})

	// Isolated: undo evidence and snapshots belong to the child's tree.
	child, childState := f.child(t, "task-isolated", rootA)
	if childState.HasBackup() {
		t.Fatal("a child started with rollback evidence it never committed")
	}
	if _, err := callTool(t, child.Registry, "file.write", `{"path":"probe.txt","content":"v2"}`); err != nil {
		t.Fatalf("isolated child file.write: %v", err)
	}
	if !childState.HasBackup() {
		t.Fatal("isolated child's write stored no rollback evidence on its own state")
	}
	if f.parent.HasBackup() {
		t.Fatal("an isolated child's write leaked rollback evidence into the conversation's /undo")
	}

	if _, err := exec.LookPath("git"); err == nil {
		if child.Snapshotter == nil {
			t.Fatal("isolated child has no snapshotter")
		}
		if _, same := child.Snapshotter.(*snapshot.Rooted); !same {
			t.Fatal("isolated child's snapshotter is not a rooted snapshot service")
		}
		ctx := context.Background()
		mustWrite(t, filepath.Join(parentRoot, "parent.txt"), "parent")
		if err := os.WriteFile(filepath.Join(rootA, "snap.txt"), []byte("before"), 0o644); err != nil {
			t.Fatalf("write snap: %v", err)
		}
		hash, err := child.Snapshotter.Track(ctx)
		if err != nil {
			t.Fatalf("child Track: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rootA, "snap.txt"), []byte("after"), 0o644); err != nil {
			t.Fatalf("rewrite snap: %v", err)
		}
		if err := child.Snapshotter.Restore(ctx, hash); err != nil {
			t.Fatalf("child Restore: %v", err)
		}
		if data, _ := os.ReadFile(filepath.Join(rootA, "snap.txt")); string(data) != "before" {
			t.Errorf("child root snap.txt = %q, want before (the child's own restore)", data)
		}
		if data, _ := os.ReadFile(filepath.Join(parentRoot, "parent.txt")); string(data) != "parent" {
			t.Errorf("parent.txt = %q, want untouched by the child's restore", data)
		}
	}

	// Shared: the backup stays parent-visible, and the recorded path is fixed.
	binding, target := sharedBindingFor(f.parent)
	sharedChild, _ := f.childWithBinding(t, agent.SubagentRequest{
		TaskKey: "task-shared-undo", ActorID: "actor-shared-undo", ActorLabel: "a subagent",
		Binding: binding, Target: target,
	})
	if _, err := callTool(t, sharedChild.Registry, "file.write", `{"path":"shared-probe.txt","content":"x"}`); err != nil {
		t.Fatalf("shared child file.write: %v", err)
	}
	if !f.parent.HasBackup() {
		t.Fatal("a shared child's write was not visible to the conversation's /undo")
	}
	backup := f.parent.Backup()
	if len(backup) == 0 || backup[0].Path != "shared-probe.txt" {
		t.Fatalf("conversation backup = %+v, want the shared child's recorded path shared-probe.txt", backup)
	}
	f.parent.SetWorkspace(session.Workspace{ProjectRoot: parentRoot, ActiveRoot: elsewhere})
	backup = f.parent.Backup()
	if len(backup) == 0 || backup[0].Path != "shared-probe.txt" {
		t.Fatalf("after the parent rebind, backup = %+v, want the recorded path unchanged", backup)
	}
}

// TestChildLedgerDoesNotBorrowSiblingRead pins ledger ownership: a read in one
// isolated child's copy must not authorize an edit of a sibling's different
// copy, while shared children keep the conversation's common ledger.
func TestChildLedgerDoesNotBorrowSiblingRead(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())
	rootB := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)

	// The same root-relative name in two different trees.
	mustWrite(t, filepath.Join(rootA, "file.go"), "package a\n")
	mustWrite(t, filepath.Join(rootB, "file.go"), "package b\n")
	mustWrite(t, filepath.Join(parentRoot, "shared.go"), "package shared\n")

	childA, _ := f.child(t, "task-ledger-a", rootA)
	childB, _ := f.child(t, "task-ledger-b", rootB)

	// A reads its own copy; that read is A's business alone.
	if _, err := callTool(t, childA.Registry, "file.read", `{"path":"file.go"}`); err != nil {
		t.Fatalf("child A file.read: %v", err)
	}
	// B's different copy was never read in B's ledger, so editing it must be
	// refused rather than borrowing A's read.
	_, err := callTool(t, childB.Registry, "file.edit", `{"path":"file.go","old_string":"package b","new_string":"package bb"}`)
	if err == nil {
		t.Fatal("child B edited a copy it never read, borrowing a sibling's ledger entry")
	}
	if !strings.Contains(err.Error(), "never read") {
		t.Fatalf("child B edit error = %v, want a read-before-edit refusal", err)
	}
	if data, _ := os.ReadFile(filepath.Join(rootB, "file.go")); string(data) != "package b\n" {
		t.Errorf("child B's file.go = %q, want it unchanged", data)
	}
	// A's own read still licenses A's edit: the ledger works normally.
	if _, err := callTool(t, childA.Registry, "file.edit", `{"path":"file.go","old_string":"package a","new_string":"package aa"}`); err != nil {
		t.Fatalf("child A file.edit after its own read: %v", err)
	}

	// Shared children share the conversation's ledger: one read, one working
	// tree, one conversation.
	binding, target := sharedBindingFor(f.parent)
	sharedReader, _ := f.childWithBinding(t, agent.SubagentRequest{
		TaskKey: "task-shared-read", ActorID: "actor-shared-read", ActorLabel: "a subagent",
		Binding: binding, Target: target,
	})
	sharedWriter, _ := f.childWithBinding(t, agent.SubagentRequest{
		TaskKey: "task-shared-write", ActorID: "actor-shared-write", ActorLabel: "a subagent",
		Binding: binding, Target: target,
	})
	if _, err := callTool(t, sharedReader.Registry, "file.read", `{"path":"shared.go"}`); err != nil {
		t.Fatalf("shared reader file.read: %v", err)
	}
	if _, err := callTool(t, sharedWriter.Registry, "file.edit", `{"path":"shared.go","old_string":"package shared","new_string":"package shared2"}`); err != nil {
		t.Fatalf("shared writer file.edit after a sibling's read: %v", err)
	}
}

// fakeLSPSync answers every edit with a cached diagnostic, standing in for a
// language server the parent selected for its own root.
type fakeLSPSync struct{ diagnostic string }

func (f fakeLSPSync) BeginEdit([]string) (func(context.Context) string, bool) {
	return func(context.Context) string { return f.diagnostic }, true
}
func (fakeLSPSync) Rescan() {}

// TestChildDiagnosticsDoNotUseOtherRootLSP pins diagnostics ownership: a child
// bound to its own root runs the configured root-local checker there, and the
// parent's cached language-server verdict is not reported as the child's.
func TestChildDiagnosticsDoNotUseOtherRootLSP(t *testing.T) {
	const parentCached = "parent-cached-language-server-error"
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())

	f := newWorkspaceFixture(t, parentRoot, func(cfg *config.Config, opts *native.Options) {
		// A placeholder-free checker template runs once, in the directory the
		// toolset resolves — which for a child must be the child's root.
		cfg.Diagnostics.Commands = map[string]string{"go": "pwd"}
		opts.Config = *cfg
		opts.LSPSync = fakeLSPSync{diagnostic: parentCached}
	})

	// The parent itself still uses the borrowed language server.
	if _, err := callTool(t, f.parentReg, "file.write", `{"path":"parent.go","content":"package p\n"}`); err != nil {
		t.Fatalf("parent file.write: %v", err)
	}
	parentResult, err := callTool(t, f.parentReg, "file.write", `{"path":"parent2.go","content":"package p\n"}`)
	if err != nil {
		t.Fatalf("parent file.write: %v", err)
	}
	if !strings.Contains(parentResult.Content, parentCached) {
		t.Fatalf("parent result does not carry its language-server verdict: %q", parentResult.Content)
	}

	child, _ := f.child(t, "task-diagnostics", rootA)
	result, err := callTool(t, child.Registry, "file.write", `{"path":"child.go","content":"package c\n"}`)
	if err != nil {
		t.Fatalf("child file.write: %v", err)
	}
	if strings.Contains(result.Content, parentCached) {
		t.Fatalf("child result reported the parent's cached diagnostic as its own: %q", result.Content)
	}
	if !strings.Contains(result.Content, rootA) {
		t.Fatalf("child diagnostics did not run in the child root %q: %q", rootA, result.Content)
	}
}

// TestFinalChildRegistryCannotReloadExcludedTools pins the scope of the FINAL
// child registry: every tool a child must not hold is absent after native
// registration and session inheritance, so no retained handler can load one
// back.
func TestFinalChildRegistryCannotReloadExcludedTools(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)

	// The runtime registers the background-work family separately from
	// native.RegisterAll, so add them to the parent here: the child inherits
	// from this registry, and the test must prove inheritance cannot carry
	// them in.
	for _, name := range []string{"agent.run", "agent.await", "agent.kill", "agent.steer"} {
		if err := f.parentReg.Register(registry.Tool{
			Name: name, Schema: []byte(`{"type":"object"}`), Risk: registry.RiskReadOnly,
			Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
				return registry.ToolResult{}, nil
			},
		}); err != nil {
			t.Fatalf("register parent %s: %v", name, err)
		}
	}

	// The parent really does hold them all — otherwise this test would prove
	// nothing about the child's filtering.
	parentNames := map[string]bool{}
	for _, tool := range f.parentReg.List() {
		parentNames[tool.Name] = true
	}

	child, _ := f.child(t, "task-exclusions", rootA)
	for _, name := range []string{
		"agent.run", "agent.await", "agent.kill", "agent.steer",
		"question.ask", "mode.request", "tools.select",
		"workspace.worktree", "workspace.finish",
		"shell.sudo", "config.read",
	} {
		if !parentNames[name] {
			t.Fatalf("parent registry has no %s, so the child's exclusion proves nothing", name)
		}
		if _, ok := child.Registry.Lookup(name); ok {
			t.Errorf("child registry holds %s; the final scope view must exclude it", name)
		}
	}
	// The tools a child needs for real work survive.
	for _, name := range []string{"file.read", "file.write", "file.edit", "shell.run", "todo.write"} {
		if _, ok := child.Registry.Lookup(name); !ok {
			t.Errorf("child registry lost %s, which an implementation task needs", name)
		}
	}
}

// TestChildBoundRootRemovalFailsClosed pins the dispatcher-bound failure mode:
// removing the assigned root makes the child's tools fail, and never falls back
// to writing the file into the original project root.
func TestChildBoundRootRemovalFailsClosed(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)

	child, childState := f.child(t, "task-removal", rootA)
	if err := os.RemoveAll(rootA); err != nil {
		t.Fatalf("RemoveAll(%s): %v", rootA, err)
	}

	_, err := callTool(t, child.Registry, "file.write", `{"path":"escaped.txt","content":"x"}`)
	if err == nil {
		t.Fatal("a write to a removed assigned root succeeded")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("write error = %v, want the bound-root unavailability failure", err)
	}
	for _, path := range []string{
		filepath.Join(parentRoot, "escaped.txt"),
		filepath.Join(childState.Workspace().ProjectRoot, "escaped.txt"),
	} {
		mustNotExist(t, path)
	}
	// The binding did not move: the child still names the root it lost.
	if got := childState.Workspace().ActiveRoot; got != rootA {
		t.Errorf("child active root = %q, want the recorded %q", got, rootA)
	}
}

// TestSubagentWorkspaceMetadataIsOwnerScopedAndDeepCopied pins the retained
// metadata a live view joins by WorkKey: only this conversation's records are
// kept, only newer revisions replace older ones, reads are deep copies ordered
// by revision, and registration adopts the dispatcher's already-allocated key.
func TestSubagentWorkspaceMetadataIsOwnerScopedAndDeepCopied(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)
	owner := f.parent

	record := func(revision uint64, ownerID, taskKey string, commits ...string) subagentwork.Record {
		return subagentwork.Record{
			Version:        subagentwork.RecordVersion,
			OwnerSessionID: ownerID,
			TaskKey:        taskKey,
			Revision:       revision,
			Binding:        subagentwork.Binding{Mode: subagentwork.ModeIsolated, Root: parentRoot},
			Commits:        commits,
		}
	}

	// Another conversation's record is never adopted.
	owner.ApplySubagentWorkspace(record(9, "some-other-session", "foreign", "deadbeef"))
	if got := owner.SubagentWorkspaces(); len(got) != 0 {
		t.Fatalf("retained %d records owned by another session, want 0", len(got))
	}

	owner.ApplySubagentWorkspace(record(1, owner.SessionID(), "task-one", "aaa"))
	owner.ApplySubagentWorkspace(record(2, owner.SessionID(), "task-two", "bbb"))
	// A stale revision must not overwrite the newer one.
	owner.ApplySubagentWorkspace(record(0, owner.SessionID(), "task-one", "stale"))

	got := owner.SubagentWorkspaces()
	if len(got) != 2 {
		t.Fatalf("retained %d records, want 2", len(got))
	}
	// Revision-ordered.
	if got[0].Revision != 1 || got[1].Revision != 2 {
		t.Fatalf("records = [%d, %d], want revision order [1, 2]", got[0].Revision, got[1].Revision)
	}
	if len(got[0].Commits) != 1 || got[0].Commits[0] != "aaa" {
		t.Fatalf("task-one commits = %v, want the revision-1 evidence [aaa]", got[0].Commits)
	}

	// Deep copy: mutating what the reader got must not touch what was kept.
	got[0].Commits[0] = "tampered"
	again := owner.SubagentWorkspaces()
	if len(again) != 2 || again[0].Commits[0] != "aaa" {
		t.Fatalf("stored record was mutated through a returned slice: %v", again[0].Commits)
	}

	// Registration adopts the dispatcher's key, so the live card, the record
	// and the report name one identity.
	binding, target := sharedBindingFor(owner)
	child, childState := f.childWithBinding(t, agent.SubagentRequest{
		TaskKey: "task-work-key", ActorID: "actor-work-key", ActorLabel: "a subagent",
		Binding: binding, Target: target,
	})
	_ = child
	view := owner.RegisterSubagentWithMetaAndWork("", "delegate", childState, session.SubagentMeta{WorkKey: "task-work-key"})
	if view.WorkKey != "task-work-key" {
		t.Fatalf("registered view WorkKey = %q, want the dispatcher's task-work-key", view.WorkKey)
	}
	// A registration with no allocated key still mints one.
	plain := owner.RegisterSubagent("plain", childState)
	if plain.WorkKey == "" {
		t.Fatal("a registration without an allocated key minted none")
	}
}

// TestSubagentWorkspaceMetadataRecordsTheChildBinding joins the metadata the
// factory reports back to the record key, which is what a live view needs to
// show where its child is actually working.
func TestSubagentWorkspaceMetadataRecordsTheChildBinding(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	rootA := canonicalDir(t, t.TempDir())
	f := newWorkspaceFixture(t, parentRoot, nil)

	_, childState := f.child(t, "task-binding", rootA)
	f.parent.ApplySubagentWorkspace(subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: f.parent.SessionID(),
		TaskKey:        "task-binding",
		Revision:       1,
		Binding:        subagentwork.Binding{Mode: subagentwork.ModeIsolated, Root: rootA},
	})

	records := f.parent.SubagentWorkspaces()
	if len(records) != 1 {
		t.Fatalf("retained %d records, want 1", len(records))
	}
	if records[0].Binding.Root != rootA {
		t.Fatalf("record binding root = %q, want the child's %q", records[0].Binding.Root, rootA)
	}
	// The child state's own workspace names the same root, so the card and the
	// record cannot disagree about where the child is working.
	if got := childState.Workspace().ActiveRoot; got != records[0].Binding.Root {
		t.Fatalf("child active root = %q, record binding root = %q", got, records[0].Binding.Root)
	}
}

// TestChildAuthorityStillTracksOwnerRevocation pins that binding a child to its
// own root does not freeze its authority: the owner revoking system access
// still takes effect under the child's fixed root.
func TestChildAuthorityStillTracksOwnerRevocation(t *testing.T) {
	parentRoot := canonicalDir(t, t.TempDir())
	outside := filepath.Join(canonicalDir(t, t.TempDir()), "outside.txt")
	f := newWorkspaceFixture(t, parentRoot, nil)

	f.parent.SetSystemAccess(true)

	binding, target := sharedBindingFor(f.parent)
	child, childState := f.childWithBinding(t, agent.SubagentRequest{
		TaskKey: "task-authority", ActorID: "actor-authority", ActorLabel: "a subagent",
		System: true, Binding: binding, Target: target,
	})
	if !childState.SystemAccess() {
		t.Fatal("child granted system access while the owner still holds it")
	}
	if _, err := callTool(t, child.Registry, "file.write", `{"path":"`+outside+`","content":"x"}`); err != nil {
		t.Fatalf("child write outside its root with system access: %v", err)
	}

	// The owner revokes. The child's fixed root must not preserve the grant.
	f.parent.SetSystemAccess(false)
	if childState.SystemAccess() {
		t.Fatal("child still reports system access after the owner revoked it")
	}
	if _, err := callTool(t, child.Registry, "file.write", `{"path":"`+filepath.Join(canonicalDir(t, t.TempDir()), "denied.txt")+`","content":"x"}`); err == nil {
		t.Fatal("child wrote outside its root after the owner revoked system access")
	}
	// Inside its own root the child keeps working normally.
	if _, err := callTool(t, child.Registry, "file.write", `{"path":"inside.txt","content":"x"}`); err != nil {
		t.Fatalf("child write inside its root after revocation: %v", err)
	}
}

// TestSubagentWorkspaceServiceIsConversationScoped pins the lifetime of the
// conversation's owned-workspace service: one instance (and one durable store)
// per conversation, borrowed by every rebuild, and none at all for a build that
// has no database to record ownership in.
func TestSubagentWorkspaceServiceIsConversationScoped(t *testing.T) {
	service := &conversationCommandService{}
	if got := service.subagentWorkspaces(nil); got != nil {
		t.Fatal("a build with no database wired an owned-workspace service")
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	first := service.subagentWorkspaces(database)
	if first == nil {
		t.Fatal("a persistent conversation wired no workspace service")
	}
	if second := service.subagentWorkspaces(database); second != first {
		t.Fatal("a rebuild received a different workspace service instead of the conversation's one")
	}
	if store := service.subagentWorkspaceStore(database); store == nil {
		t.Fatal("the conversation has no durable record store")
	}
}

// TestRunWorkspaceRestartMarksInterruptedRecords pins the restart
// reconciliation: a record a previous process left active becomes interrupted
// (the child was up when the process ended) or unknown (its allocation was never
// confirmed), while a finished record is left exactly as it was.
func TestRunWorkspaceRestartMarksInterruptedRecords(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(t.TempDir(), "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	const owner = "restart-owner"
	if err := database.CreateSession(owner, projectID, "work", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Now(),
		session.Persistence{DB: database, SessionID: owner, Logger: slog.New(slog.DiscardHandler)})
	store := db.NewSubagentWorkspaceStore(database)
	for _, rec := range []subagentwork.Record{
		{Version: subagentwork.RecordVersion, OwnerSessionID: owner, TaskKey: "t-prepared", InvocationID: "inv-prepared", Outcome: subagentwork.OutcomePrepared},
		{Version: subagentwork.RecordVersion, OwnerSessionID: owner, TaskKey: "t-preparing", InvocationID: "inv-preparing", Outcome: subagentwork.OutcomePreparing},
		{Version: subagentwork.RecordVersion, OwnerSessionID: owner, TaskKey: "t-done", InvocationID: "inv-done", Outcome: subagentwork.OutcomeDone},
	} {
		if _, _, err := store.Create(context.Background(), rec); err != nil {
			t.Fatalf("seed %s: %v", rec.TaskKey, err)
		}
	}

	reconcileRestoredSubagentWorkspaces(context.Background(), store, state)

	records, err := store.List(context.Background(), owner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	outcomes := map[string]string{}
	for _, rec := range records {
		outcomes[rec.TaskKey] = rec.Outcome
	}
	if got := outcomes["t-prepared"]; got != subagentwork.OutcomeInterrupted {
		t.Fatalf("a prepared record left by a previous process = %q, want interrupted", got)
	}
	if got := outcomes["t-preparing"]; got != subagentwork.OutcomeUnknown {
		t.Fatalf("a preparing record left by a previous process = %q, want unknown", got)
	}
	if got := outcomes["t-done"]; got != subagentwork.OutcomeDone {
		t.Fatalf("a finished record = %q, want it left as done", got)
	}
	// The conversation retains what it reconciled, so a card can join by key.
	if retained := state.SubagentWorkspaces(); len(retained) != 2 {
		t.Fatalf("retained %d reconciled records, want the two that changed", len(retained))
	}
}
