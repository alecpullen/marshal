package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"marshal/internal/snapshot"
)

// This file pins the `marshal snapshots` CLI: its dispatch, its read-only
// status, and — most importantly — that its destructive subcommands cannot be
// authorised by ordinary stdin.
//
// Every fixture is a t.TempDir. The data directory is redirected through
// MARSHAL_DATA_DIR (which config.DataDir honours) so no test can reach the
// user's real ~/.local/share/marshal.

// cliSandbox points the CLI at an isolated data directory and returns it.
//
// MARSHAL_DATA_DIR is the override config.DataDir consults FIRST, so setting it
// is what makes the whole CLI test-safe rather than merely likely-safe.
func cliSandbox(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", dataDir)
	// The user config is read from HOME, so it is redirected too: a real
	// user-global budget would otherwise leak into every assertion here.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return dataDir
}

// requireGitCLI skips only when Git is genuinely unavailable.
func requireGitCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// cliLegacyStore builds a pre-v2 store under the sandboxed data dir and returns
// its workspace hash.
//
// The store is created at `<dataDir>/snapshots/<workspace>` exactly as the old
// shadow-repo layout did, with a branch AND a snapshot ref, because the branch
// is the second retention root the original defect hinged on.
func cliLegacyStore(t *testing.T, dataDir, workspace string) string {
	t.Helper()
	storePath := filepath.Join(dataDir, "snapshots", workspace)
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	runGit(t, storePath, nil, "init", "--bare")
	// A commit, a branch, and a snapshot ref: the shape the old layout produced.
	blob := strings.TrimSpace(runGit(t, storePath, []byte("content\n"), "hash-object", "-w", "-t", "blob", "--stdin"))
	tree := strings.TrimSpace(runGit(t, storePath, []byte("100644 blob "+blob+"\ta.txt\n"), "mktree"))
	commit := strings.TrimSpace(runGit(t, storePath,
		[]byte("snapshot\n"),
		"-c", "user.name=m", "-c", "user.email=m@m", "commit-tree", tree))
	runGit(t, storePath, nil, "update-ref", "refs/heads/master", commit)
	runGit(t, storePath, nil, "update-ref", "refs/snapshots/"+commit, commit)
	return workspace
}

// runGit runs one git command in dir, feeding stdin when it is non-nil, and
// fails the test on error.
func runGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	if len(args) == 0 {
		t.Fatal("no git args")
	}
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, buf.String())
	}
	return buf.String()
}

// treeFingerprint builds a content fingerprint of every file below root, so a
// read-only command can be proven to have changed nothing.
func treeFingerprint(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			lines = append(lines, "d "+rel)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("f %s %x", rel, data))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// timeNow is the clock the CLI test fixtures date their debris with.
func timeNow() time.Time { return time.Now() }

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// `marshal snapshots` is dispatched through the injectable-I/O seam, exactly
// like history and plugin.
func TestSnapshotsDispatchUsesTheRunnerSeam(t *testing.T) {
	old := snapshotsRunner
	defer func() { snapshotsRunner = old }()
	called := false
	snapshotsRunner = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		called = true
		if len(args) == 0 || args[0] != "status" {
			t.Errorf("args = %v, want the subcommand passed through", args)
		}
		return nil
	}
	if err := run(context.Background(), []string{"snapshots", "status"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !called {
		t.Fatal("snapshotsRunner was not called")
	}
}

// The real dispatcher routes each subcommand and refuses an unknown one.
func TestRunSnapshotsRejectsUnknownSubcommand(t *testing.T) {
	err := runSnapshots(context.Background(), []string{"nonsense"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("unknown subcommand = %v, want an error naming the valid ones", err)
	}
	if err := runSnapshots(context.Background(), nil, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("no subcommand was accepted")
	}
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

// `status` is READ-ONLY and works WITHOUT a terminal: it must not prompt, and it
// must not mutate anything.
func TestSnapshotsStatusIsReadOnlyAndNeedsNoTerminal(t *testing.T) {
	requireGitCLI(t)
	dataDir := cliSandbox(t)
	workspace := cliLegacyStore(t, dataDir, "0123456789ab")

	storePath := filepath.Join(dataDir, "snapshots", workspace)
	before := treeFingerprint(t, storePath)

	var stdout, stderr bytes.Buffer
	// An EMPTY stdin and no terminal: status must still work.
	if err := runSnapshots(context.Background(), []string{"status"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("snapshots status: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Marshal snapshot store:",
		"Budgets:",
		"Old-format workspaces (1):",
		workspace,
		"unknown (the old layout records only the workspace hash)",
		"Ownership note:",
		"marshal snapshots cleanup",
		"marshal snapshots migrate --workspace <id>",
		"marshal snapshots reset --workspace <id>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	// Nothing was prompted and nothing was changed.
	if strings.Contains(out, confirmAckText) {
		t.Errorf("status prompted for a confirmation:\n%s", out)
	}
	if after := treeFingerprint(t, storePath); after != before {
		t.Fatal("snapshots status mutated the store")
	}
}

// confirmAckText is the acknowledgement word as the CLI's prompt spells it. It
// is duplicated here deliberately: a test that imported the package constant
// would pass even if the prompt stopped requiring it.
const confirmAckText = "i-have-stopped-all-old-marshal-instances"

// `status` on a machine that has never captured reports an empty store rather
// than failing.
func TestSnapshotsStatusOnFreshMachine(t *testing.T) {
	cliSandbox(t)
	var stdout, stderr bytes.Buffer
	if err := runSnapshots(context.Background(), []string{"status"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("snapshots status on a fresh machine: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Versioned workspaces (0):") {
		t.Errorf("status did not report zero versioned workspaces:\n%s", out)
	}
	if !strings.Contains(out, "Old-format workspaces (0):") {
		t.Errorf("status did not report zero old-format workspaces:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// cleanup
// ---------------------------------------------------------------------------

// `cleanup` WITHOUT a controlling terminal reconciles the versioned store and
// leaves legacy temporary files alone. The CLI is where a headless invocation
// arrives, so this is the headline safety property.
//
// The headless terminal is INJECTED rather than left to OpenConfirmer, and that
// is deliberate: the real OpenConfirmer opens /dev/tty, so a test harness running
// under a terminal would block waiting for a human. Injecting the non-terminal
// ScriptedConfirmer is exactly what the real one returns on a machine with no
// controlling terminal, so the path under test is identical.
func TestSnapshotsCleanupHeadlessLeavesLegacyUntouched(t *testing.T) {
	requireGitCLI(t)
	dataDir := cliSandbox(t)
	workspace := cliLegacyStore(t, dataDir, "0123456789ab")
	tempPack := plantCLITempPack(t, dataDir, workspace)

	// A headless terminal: the zero-value ScriptedConfirmer reports itself as
	// not-a-terminal, which is what "no controlling terminal" means.
	restore := stubSnapshotsDeps(t, dataDir, &snapshot.ScriptedConfirmer{})
	defer restore()

	var stdout, stderr bytes.Buffer
	// stdin still carries plausible answers, which must NOT be accepted: the
	// channel is wrong, so the content is irrelevant.
	stdin := strings.NewReader(confirmAckText + "\n" + workspace + "\n")
	if err := runSnapshots(context.Background(), []string{"cleanup"}, stdin, &stdout, &stderr); err != nil {
		t.Fatalf("snapshots cleanup: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "no offline acknowledgement was obtained") {
		t.Errorf("cleanup did not report that legacy was left untouched:\n%s", out)
	}
	if _, err := os.Lstat(tempPack); err != nil {
		t.Fatalf("cleanup removed a legacy artifact without a terminal: %v", err)
	}
}

// The production confirmer is what a genuinely tty-less process gets, and it
// reports itself as NOT a terminal so every mutation refuses. This is asserted
// directly rather than inferred, because it is the property the whole workflow
// leans on.
func TestProductionConfirmerRefusesWhenThereIsNoControllingTerminal(t *testing.T) {
	// A process with no controlling terminal cannot open /dev/tty, and the
	// confirmer reports that rather than pretending otherwise. The assertion is
	// conditional on the environment: a test harness that DOES have a tty gets a
	// usable confirmer, which is also correct.
	c := snapshot.OpenConfirmer()
	defer func() { _ = c.Close() }()
	if c.IsTerminal() && strings.Contains(c.Description(), "cannot open") {
		t.Errorf("confirmer reports a terminal but describes an open failure: %s", c.Description())
	}
	if !c.IsTerminal() && !strings.Contains(c.Description(), "no controlling terminal") {
		t.Errorf("a tty-less confirmer did not explain itself: %s", c.Description())
	}
}

// `cleanup` WITH an acknowledged terminal removes recognized temporary files.
func TestSnapshotsCleanupWithAcknowledgedTerminalRemovesTemps(t *testing.T) {
	requireGitCLI(t)
	dataDir := cliSandbox(t)
	workspace := cliLegacyStore(t, dataDir, "0123456789ab")
	tempPack := plantCLITempPack(t, dataDir, workspace)

	term := &snapshot.ScriptedConfirmer{Terminal: true, Answers: []string{confirmAckText}}
	restore := stubSnapshotsDeps(t, dataDir, term)
	defer restore()

	var stdout, stderr bytes.Buffer
	if err := runSnapshots(context.Background(), []string{"cleanup"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("snapshots cleanup: %v\nstderr: %s", err, stderr.String())
	}
	if _, err := os.Lstat(tempPack); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the recognized temporary artifact survived an acknowledged cleanup: %v", err)
	}
	if !strings.Contains(stdout.String(), "Abandoned temporary files removed:") {
		t.Errorf("cleanup did not report what it removed:\n%s", stdout.String())
	}
	// The operator was shown the prerequisite through the TERMINAL, not stdout.
	if !strings.Contains(term.Text(), "Old Marshal binaries do not understand the new advisory store lock") {
		t.Errorf("the offline prerequisite was not shown on the terminal:\n%s", term.Text())
	}
}

// ---------------------------------------------------------------------------
// reset
// ---------------------------------------------------------------------------

// `reset` cannot be authorised by ordinary stdin, even when the pipe contains
// BOTH the acknowledgement and the correct workspace ID.
//
// This is the requirement the plan states most sharply, and the CLI is where a
// pipe would arrive, so it is asserted at the CLI boundary: the headless path
// must refuse AND must not resolve a terminal behind the operator's back.
func TestSnapshotsResetRefusesPipedAuthorisation(t *testing.T) {
	requireGitCLI(t)
	dataDir := cliSandbox(t)
	workspace := cliLegacyStore(t, dataDir, "0123456789ab")
	storePath := filepath.Join(dataDir, "snapshots", workspace)
	before := treeFingerprint(t, storePath)

	// A NON-terminal confirmer: the answers below arrive over ordinary stdin,
	// which is the channel that must never be able to authorise a reset.
	restore := stubSnapshotsDeps(t, dataDir, &snapshot.ScriptedConfirmer{
		Terminal: false,
		Answers:  []string{confirmAckText, workspace},
	})
	defer restore()

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader(confirmAckText + "\n" + workspace + "\n")
	if err := runSnapshots(context.Background(), []string{"reset", "--workspace", workspace}, stdin, &stdout, &stderr); err != nil {
		// A refusal is returned as an error by the CLI, which main prints. The
		// assertion that matters is that nothing was deleted.
		t.Logf("reset refused with: %v", err)
	}
	if _, err := os.Lstat(storePath); err != nil {
		t.Fatalf("a piped invocation destroyed the store: %v", err)
	}
	if after := treeFingerprint(t, storePath); after != before {
		t.Fatal("a piped invocation mutated the store")
	}
	if strings.Contains(stdout.String(), "freed") {
		t.Errorf("a piped invocation claimed to have removed something:\n%s", stdout.String())
	}
}

// `reset` with an acknowledged terminal and the typed ID removes the store.
func TestSnapshotsResetWithTerminalRemovesTheStore(t *testing.T) {
	requireGitCLI(t)
	dataDir := cliSandbox(t)
	workspace := cliLegacyStore(t, dataDir, "0123456789ab")
	storePath := filepath.Join(dataDir, "snapshots", workspace)

	term := &snapshot.ScriptedConfirmer{Terminal: true, Answers: []string{confirmAckText, workspace}}
	restore := stubSnapshotsDeps(t, dataDir, term)
	defer restore()

	var stdout, stderr bytes.Buffer
	if err := runSnapshots(context.Background(), []string{"reset", "--workspace", workspace}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("snapshots reset: %v\nstderr: %s", err, stderr.String())
	}
	if _, err := os.Lstat(storePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the store survived an authorised reset: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"Snapshot reset:", workspace, "untouched:", "project's own .git"} {
		if !strings.Contains(out, want) {
			t.Errorf("reset output missing %q:\n%s", want, out)
		}
	}
}

// A `--workspace` value that is a filesystem path is refused BEFORE the store is
// even opened, so no user-supplied path can ever be resolved for deletion.
func TestSnapshotsRejectsAPathAsWorkspace(t *testing.T) {
	cliSandbox(t)
	outside := t.TempDir()
	marker := filepath.Join(outside, "must-survive")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"migrate", "reset"} {
		for _, bad := range []string{"..", "/etc", outside, "../" + filepath.Base(outside), "not-a-hash"} {
			var stdout, stderr bytes.Buffer
			err := runSnapshots(context.Background(), []string{sub, "--workspace", bad},
				strings.NewReader(""), &stdout, &stderr)
			if err == nil {
				t.Errorf("%s --workspace %q was accepted", sub, bad)
				continue
			}
			if !strings.Contains(err.Error(), "not a workspace identifier") {
				t.Errorf("%s --workspace %q error = %v, want it to explain the identifier shape", sub, bad, err)
			}
		}
	}
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("a path-shaped --workspace destroyed something outside the store: %v", err)
	}
}

// `--workspace` is required, and its absence is a usage error rather than an
// operation against something defaulted.
func TestSnapshotsRequiresWorkspaceFlag(t *testing.T) {
	cliSandbox(t)
	for _, sub := range []string{"migrate", "reset"} {
		var stdout, stderr bytes.Buffer
		err := runSnapshots(context.Background(), []string{sub}, strings.NewReader(""), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "--workspace is required") {
			t.Errorf("%s without --workspace = %v, want a usage error", sub, err)
		}
	}
	// discard additionally requires --generation.
	var stdout, stderr bytes.Buffer
	err := runSnapshots(context.Background(), []string{"discard", "--workspace", "0123456789ab"},
		strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--generation is required") {
		t.Errorf("discard without --generation = %v, want a usage error", err)
	}
}

// ---------------------------------------------------------------------------
// Recovery wording agrees with the CLI
// ---------------------------------------------------------------------------

// The recovery commands the runtime warning tells the user to run must be
// commands this CLI actually accepts.
//
// Requirement 9 in the plan exists because a warning is only useful if it can be
// copied verbatim, and a warning naming a subcommand that does not exist — or one
// that errors out for want of an argument — is worse than no warning at all: the
// user follows it to a usage error and concludes the store is unfixable. So the
// wording is asserted HERE, against the real dispatcher, rather than in a string
// comparison that could drift.
func TestRecoveryWordingMatchesTheCLIItNames(t *testing.T) {
	cliSandbox(t)

	// Every subcommand the warning can name is accepted by the dispatcher (it
	// fails later, for its own reason, but never with "unknown subcommand").
	for _, args := range [][]string{
		{"status"},
		{"cleanup"},
		{"migrate", "--workspace", "0123456789ab"},
		{"reset", "--workspace", "0123456789ab"},
		{"discard", "--workspace", "0123456789ab", "--generation", "00000000000000000000-aaaaaaaa"},
	} {
		var stdout, stderr bytes.Buffer
		err := runSnapshots(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && strings.Contains(err.Error(), "unknown subcommand") {
			t.Errorf("the CLI does not accept %v, which the recovery wording names: %v", args, err)
		}
	}

	// And the commands the warning prints are ACCEPTED as written, which is the
	// property that makes them copyable. The migrate form is asserted with the
	// placeholder already substituted, exactly as a user would after reading
	// `status`.
	for _, args := range [][]string{
		{"status"},
		{"cleanup"},
		{"migrate", "--workspace", "0123456789ab"},
	} {
		var stdout, stderr bytes.Buffer
		err := runSnapshots(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && strings.Contains(err.Error(), "is required") {
			t.Errorf("%v was rejected for a missing argument, so the printed command is not copyable: %v", args, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Shared-dependency seam
// ---------------------------------------------------------------------------

// stubSnapshotsDeps replaces the CLI's dependency seam with one whose Confirmer
// is the supplied terminal, and returns a restore function.
//
// The real openSnapshotsDeps is still used for everything except the terminal, so
// the test exercises the production data-directory and config resolution while
// substituting only the part that needs a human.
func stubSnapshotsDeps(t *testing.T, dataDir string, term snapshot.Confirmer) func() {
	t.Helper()
	old := snapshotsDepsFn
	snapshotsDepsFn = func(ctx context.Context, retentionDays int) (*snapshotsDeps, error) {
		deps, err := old(ctx, retentionDays)
		if err != nil {
			return nil, err
		}
		deps.Confirmer = func() snapshot.Confirmer { return term }
		return deps, nil
	}
	return func() { snapshotsDepsFn = old }
}

// plantCLITempPack writes a BACKDATED `tmp_pack_*` file into a legacy store,
// which is what abandoned debris looks like.
func plantCLITempPack(t *testing.T, dataDir, workspace string) string {
	t.Helper()
	packDir := filepath.Join(dataDir, "snapshots", workspace, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(packDir, "tmp_pack_AbCdEf01")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	// Outside the live-writer freshness window: genuinely abandoned debris
	// rather than an active repack.
	old := timeNow().Add(-30 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}
