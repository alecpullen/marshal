package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/snapshot"
	"marshal/internal/trust"
)

// This file covers the runtime half of the snapshot-warning work: the
// startup/close maintenance path uses the SAME message wording and the same
// suppression policy as the agent's in-turn capture hooks, and an expected
// shutdown is not a "recovery required" event.

// maintenanceSnapshot is a SnapshotCloser + SnapshotStatusCloser double. It
// implements the optional status extension, which is how Close is told the
// store needs attention.
//
// CloseSnapshots performs no storage work at all — that is the contract — so
// the double only records that it was called and can inject a lifecycle
// failure.
type maintenanceSnapshot struct {
	statusErr error
	closeErr  error
	closes    int
}

func (s *maintenanceSnapshot) CloseSnapshots(_ context.Context) error {
	s.closes++
	return s.closeErr
}

func (s *maintenanceSnapshot) Status(_ context.Context) error { return s.statusErr }

// closeOnlySnapshot implements ONLY SnapshotCloser: the runtime must not
// require the status extension.
type closeOnlySnapshot struct {
	closeErr error
	closes   int
}

func (s *closeOnlySnapshot) CloseSnapshots(_ context.Context) error {
	s.closes++
	return s.closeErr
}

// snapshotWarningMessages returns the plain system messages carrying a
// snapshot warning — the transcript projection the TUI and ACP both render.
func snapshotWarningMessages(st *session.State) []string {
	var out []string
	for _, m := range st.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "snapshot") {
			out = append(out, m.Content)
		}
	}
	return out
}

// newCloseTestRuntime builds a Runtime shaped like the ones the existing Close
// tests use: enough wiring for Quiesce plus the snapshot stage.
func newCloseTestRuntime(t *testing.T, st *session.State, snap SnapshotCloser, warnings *agent.SnapshotWarningState, logw io.Writer) *Runtime {
	t.Helper()
	if logw == nil {
		logw = io.Discard
	}
	workCtx, workCancel := context.WithCancel(context.Background())
	return &Runtime{
		Config:           config.Default(),
		State:            st,
		workCtx:          workCtx,
		workCancel:       workCancel,
		Snapshot:         snap,
		SnapshotWarnings: warnings,
		Logger:           slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// TestRuntimeCloseSurfacesSnapshotMaintenanceFailure pins that a store that
// needs attention at close reaches the transcript with the full message
// contract: workspace, reason, recovery command, and the explicit statement
// that rollback protection is missing.
func TestRuntimeCloseSurfacesSnapshotMaintenanceFailure(t *testing.T) {
	var logBuf bytes.Buffer
	// The warning goes to the SESSION logger (that is what State.Logger
	// returns), which is also what carries the structured fields.
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{Logger: logger})
	snap := &maintenanceSnapshot{statusErr: errors.New("legacy_recovery_required: old store present")}
	rt := newCloseTestRuntime(t, st, snap, agent.NewSnapshotWarningState(), &logBuf)

	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	warnings := snapshotWarningMessages(st)
	if len(warnings) != 1 {
		t.Fatalf("close produced %d snapshot warnings, want 1: %v", len(warnings), warnings)
	}
	msg := warnings[0]
	for _, want := range []string{
		"Snapshot maintenance unavailable in " + st.WorkingDir,
		"legacy_recovery_required",
		"WITHOUT a rollback snapshot",
		"Recover: marshal snapshots status, then marshal snapshots migrate --workspace <workspace-id>.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("close warning missing %q:\n%s", want, msg)
		}
	}
	// The lifecycle must still have run: the status probe supplements it, it
	// does not replace it.
	if snap.closes != 1 {
		t.Fatalf("CloseSnapshots called %d times, want 1 (status must not replace the lifecycle)", snap.closes)
	}
	if !strings.Contains(logBuf.String(), "snapshot capture unavailable") {
		t.Errorf("maintenance failure was not logged:\n%s", logBuf.String())
	}
}

// TestRuntimeCloseMaintenanceWarningIsSuppressed pins the anti-spam policy at
// the runtime layer: two runtimes sharing one suppression state report the
// same failure once.
func TestRuntimeCloseMaintenanceWarningIsSuppressed(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	warnings := agent.NewSnapshotWarningState()
	failure := errors.New("budget_exhausted: workspace over budget")

	rt1 := newCloseTestRuntime(t, st, &maintenanceSnapshot{statusErr: failure}, warnings, nil)
	if err := rt1.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	rt2 := newCloseTestRuntime(t, st, &maintenanceSnapshot{statusErr: failure}, warnings, nil)
	if err := rt2.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := len(snapshotWarningMessages(st)); got != 1 {
		t.Fatalf("two identical maintenance failures produced %d warnings, want 1", got)
	}

	// A different reason is a meaningful status change and warns again.
	rt3 := newCloseTestRuntime(t, st,
		&maintenanceSnapshot{statusErr: errors.New("insufficient_free_space: no room")}, warnings, nil)
	if err := rt3.Close(context.Background()); err != nil {
		t.Fatalf("third Close: %v", err)
	}
	got := snapshotWarningMessages(st)
	if len(got) != 2 {
		t.Fatalf("a new maintenance reason produced %d total warnings, want 2", len(got))
	}
	if !strings.Contains(got[1], "insufficient_free_space") {
		t.Errorf("new-reason maintenance warning lacks its reason:\n%s", got[1])
	}
}

// TestRuntimeCloseHealthyStatusResetsSuppression: a store that reports healthy
// at close clears the suppression, so the next failure is visible again.
func TestRuntimeCloseHealthyStatusResetsSuppression(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	warnings := agent.NewSnapshotWarningState()
	failure := errors.New("budget_exhausted: over budget")

	rt1 := newCloseTestRuntime(t, st, &maintenanceSnapshot{statusErr: failure}, warnings, nil)
	if err := rt1.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if got := len(snapshotWarningMessages(st)); got != 1 {
		t.Fatalf("first failure produced %d warnings, want 1", got)
	}

	// Healthy close.
	rt2 := newCloseTestRuntime(t, st, &maintenanceSnapshot{}, warnings, nil)
	if err := rt2.Close(context.Background()); err != nil {
		t.Fatalf("healthy Close: %v", err)
	}

	// Relapse of the same failure must be visible again.
	rt3 := newCloseTestRuntime(t, st, &maintenanceSnapshot{statusErr: failure}, warnings, nil)
	if err := rt3.Close(context.Background()); err != nil {
		t.Fatalf("relapse Close: %v", err)
	}
	if got := len(snapshotWarningMessages(st)); got != 2 {
		t.Fatalf("relapse after a healthy close produced %d total warnings, want 2", got)
	}
}

// TestRuntimeCloseCancelledStatusProbeIsNotRecoverySpam pins the shutdown
// policy at the runtime layer: an expected shutdown is logged, never shown as a
// recovery-required event.
//
// The old form of this test drove a CANCELLED PRUNE; the prune is gone from
// shutdown entirely (that is the point of this task), so the same policy is now
// pinned on the one storage query shutdown still makes — the read-only status
// probe. A probe that reports cancellation is logged at Debug and never reaches
// the transcript.
func TestRuntimeCloseCancelledStatusProbeIsNotRecoverySpam(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{Logger: logger})
	rt := newCloseTestRuntime(t, st,
		&maintenanceSnapshot{statusErr: context.Canceled}, agent.NewSnapshotWarningState(), &logBuf)

	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(snapshotWarningMessages(st)); got != 0 {
		t.Fatalf("a cancelled status probe produced %d visible warnings, want 0: %v",
			got, snapshotWarningMessages(st))
	}
	if !strings.Contains(logBuf.String(), "snapshot") {
		t.Errorf("cancelled status probe was not logged at all:\n%s", logBuf.String())
	}
}

// TestRuntimeCloseLifecycleFailureIsLogOnly pins the deliberate asymmetry: a
// failure to CANCEL/JOIN snapshot work at Close is logged but NOT surfaced.
// Close runs for every session, including ones that never captured a snapshot,
// so a bare lifecycle error would put a rollback warning in front of a user who
// never had a snapshot to lose. Only the store's own structured status — which
// distinguishes "nothing to do" from "needs recovery" — warrants a transcript
// entry.
//
// It replaces TestRuntimeClosePruneFailureIsLogOnly: that test drove a RAW
// PRUNE FAILURE, and shutdown no longer prunes at all (see
// TestRuntimeCloseRunsNoGitGC, which pins the stronger property).
func TestRuntimeCloseLifecycleFailureIsLogOnly(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{Logger: logger})

	// Status healthy, snapshot lifecycle fails with a plain subprocess error.
	rt := newCloseTestRuntime(t, st,
		&maintenanceSnapshot{closeErr: errors.New("snapshot refs: not a git repository")},
		agent.NewSnapshotWarningState(), &logBuf)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(snapshotWarningMessages(st)); got != 0 {
		t.Fatalf("raw snapshot close failure produced %d visible warnings, want 0: %v",
			got, snapshotWarningMessages(st))
	}
	if !strings.Contains(logBuf.String(), "snapshot close failed") {
		t.Errorf("raw snapshot close failure was not logged:\n%s", logBuf.String())
	}
}

// TestRuntimeCloseIsSafeWithoutWarningsState makes sure a Runtime built
// without the shared state (every existing test literal) still closes cleanly
// when maintenance reports a fault.
func TestRuntimeCloseIsSafeWithoutWarningsState(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := &Runtime{
		Config:   config.Default(),
		State:    st,
		Snapshot: &maintenanceSnapshot{statusErr: errors.New("internal_error: boom")},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestRuntimeCloseAcceptsCloseOnlySnapshotCloser pins that the status
// extension is optional: a plain SnapshotCloser still closes, and reports
// nothing it cannot describe.
func TestRuntimeCloseAcceptsCloseOnlySnapshotCloser(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	snap := &closeOnlySnapshot{}
	rt := newCloseTestRuntime(t, st, snap, agent.NewSnapshotWarningState(), nil)

	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if snap.closes != 1 {
		t.Fatalf("CloseSnapshots called %d times, want 1", snap.closes)
	}
	if got := len(snapshotWarningMessages(st)); got != 0 {
		t.Fatalf("close-only closer produced %d warnings, want 0", got)
	}
}

// TestRuntimeCloseMaintenanceMessageMatchesAgentWording pins the cross-layer
// contract: the runtime's maintenance warning is literally the same rendering
// as the agent's capture warning, limits included.
func TestRuntimeCloseMaintenanceMessageMatchesAgentWording(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := newCloseTestRuntime(t, st,
		&maintenanceSnapshot{statusErr: errors.New("budget_exhausted: over budget")},
		agent.NewSnapshotWarningState(), nil)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	warnings := snapshotWarningMessages(st)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(warnings))
	}
	for _, want := range []string{
		"Snapshot maintenance unavailable in " + st.WorkingDir,
		"budget_exhausted",
		"WITHOUT a rollback snapshot",
		// The default budgets, as the settings UI would render them.
		"Limit: workspace budget 2.0 GB, global budget 10.0 GB",
		"Recover: marshal snapshots status, then marshal snapshots cleanup.",
	} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("runtime warning missing %q:\n%s", want, warnings[0])
		}
	}
}

// TestStartRuntimeSharesSnapshotWarningStateAcrossLayers is the integration
// assertion: the runtime's maintenance warning and the runner's in-turn
// capture warning about the same workspace and reason are ONE user-visible
// event, and a new reason is visible again. That only holds if both layers
// share the suppression state, so the behaviour pins the wiring.
func TestStartRuntimeSharesSnapshotWarningStateAcrossLayers(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[project]
name = "snapshot-warning-test"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := StartRuntime(ctx,
		WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.SnapshotWarnings == nil {
		t.Fatal("rt.SnapshotWarnings = nil, want a shared warning state")
	}
	if rt.Runner == nil {
		t.Fatal("rt.Runner = nil")
	}

	// A maintenance failure with the budget reason: visible once.
	maintenanceErr := errors.New("budget_exhausted: workspace over budget")
	rt.SnapshotWarnings.ReportMaintenance(rt.State, agent.SnapshotPhaseMaintenance, maintenanceErr, false)
	if got := len(snapshotWarningMessages(rt.State)); got != 1 {
		t.Fatalf("maintenance warning count = %d, want 1", got)
	}

	// Drive an in-turn capture with a scripted provider so the turn reaches
	// the turn-start hook without a network call. The turn's own outcome is
	// irrelevant here: the capture hook runs before the model call, and the
	// only thing under test is whose suppression state it consults.
	scripted := &agenttest.ScriptedProvider{ToolCalls: [][]schema.ToolCall{
		{{ID: "tc1", Name: "file.read", Args: json.RawMessage(`{"path":"go.mod"}`)}},
		nil,
	}, Responses: []string{"", "done"}}
	route, _, err := rt.Runner.RouteResolver.Resolve("question")
	if err != nil {
		t.Fatalf("resolve route: %v", err)
	}
	route.Preset.Model = "test-model"
	rt.Runner.Provider = scripted
	rt.Runner.Model = "test-model"
	rt.Runner.RouteResolver = &staticRouteResolver{route: route, provider: scripted}
	rt.Runner.NativeTools = true
	rt.Runner.SetForceClass(string(agent.ClassQuestion))
	rt.Runner.SnapshotRecorder = nil

	// An in-turn capture failure with the SAME workspace and reason. Under the
	// shared state it is suppressed; a runner-private state would add a second
	// warning here.
	rt.Runner.Snapshotter = &failingSnapshotter{err: maintenanceErr}
	_, _ = rt.Runner.RunTask(context.Background(), "turn one")
	if got := len(snapshotWarningMessages(rt.State)); got != 1 {
		t.Fatalf("in-turn failure under a shared state produced %d total warnings, want 1: %v",
			got, snapshotWarningMessages(rt.State))
	}

	// A different reason in-turn IS a status change and must be visible.
	rt.Runner.Snapshotter = &failingSnapshotter{err: errors.New("insufficient_free_space: no room")}
	_, _ = rt.Runner.RunTask(context.Background(), "turn two")
	got := snapshotWarningMessages(rt.State)
	if len(got) != 2 {
		t.Fatalf("a new in-turn reason produced %d total warnings, want 2: %v", len(got), got)
	}
	if !strings.Contains(got[1], "Turn-start snapshot") ||
		!strings.Contains(got[1], "insufficient_free_space") {
		t.Errorf("in-turn warning lacks its phase or reason:\n%s", got[1])
	}
}

// TestRuntimeCloseRunsNoGitGC is the strongest form of the shutdown assertion:
// it drives a REAL snapshot.Rooted through a real capture and a real Close, with
// a recording Git wrapper installed, and proves that shutdown runs NO Git
// command at all — in particular no `gc`, `repack`, `reflog expire`, or
// `prune`.
//
// That is the original defect: the old Close ran a filesystem prune and then
// `git gc --prune=now` under a 30 s deadline, ignoring the error; snapshots were
// reachable from HEAD so the gc freed nothing, and an interrupted repack left
// 171 GiB of tmp_pack_* garbage behind.
func TestRuntimeCloseRunsNoGitGC(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dataDir := t.TempDir()
	workDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "git.log")
	recorder := recordingGitWrapper(t, logPath)

	snap := snapshot.NewRooted(dataDir, workDir,
		func() string { return workDir },
		int64(config.Default().Snapshots.MaxFileBytes), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		snapshot.WithRootedGitBinary(recorder))
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := snap.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if hash == "" {
		t.Fatal("Track returned no hash")
	}
	commandsBefore := strings.Count(readFileOrEmpty(t, logPath), "\n")

	st := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	rt := &Runtime{
		Config:           config.Default(),
		State:            st,
		Snapshot:         snap,
		SnapshotWarnings: agent.NewSnapshotWarningState(),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		workCtx:          context.Background(),
	}
	rt.workCtx, rt.workCancel = context.WithCancel(context.Background())

	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recorded := readFileOrEmpty(t, logPath)
	after := strings.Split(strings.TrimRight(recorded, "\n"), "\n")
	if len(after) > commandsBefore {
		// The shutdown path's only permitted Git use is the read-only status
		// probe (ref listings). Anything that could WRITE — gc, repack, reflog
		// expiry, ref pruning, an object write — is the defect.
		for _, c := range after[commandsBefore:] {
			sub := recordedGitSubcommand(c)
			if banned := heavyGitSubcommand(sub, c); banned != "" {
				t.Errorf("Close ran a heavy storage command %q (matched %q)", c, banned)
				continue
			}
			switch sub {
			case "for-each-ref", "rev-list", "cat-file", "rev-parse", "symbolic-ref", "ls-tree":
				// Read-only inspection; permitted.
			default:
				t.Errorf("Close ran a non-read-only Git command %q (subcommand %q)", c, sub)
			}
		}
	}
}

// recordedGitSubcommand extracts the git subcommand from a recorded argument
// list, skipping the `-c name=value` pins and `--option` arguments.
func recordedGitSubcommand(c string) string {
	fields := strings.Fields(c)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-c":
			i++
		case strings.HasPrefix(f, "-c"), strings.HasPrefix(f, "--"):
			continue
		default:
			return f
		}
	}
	return ""
}

// heavyGitSubcommand reports the banned behaviour a recorded command carries,
// or "" when it carries none.
func heavyGitSubcommand(sub, full string) string {
	switch sub {
	case "gc", "repack", "prune", "prune-packed":
		return sub
	case "reflog":
		if strings.Contains(full, "expire") {
			return "reflog expire"
		}
	}
	return ""
}

// recordingGitWrapper writes a git shim that logs each invocation and delegates
// to the real binary.
func recordingGitWrapper(t *testing.T, logPath string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write recording git: %v", err)
	}
	return script
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces pins the
// startup contract: a session whose workspace has no store performs no
// maintenance and creates nothing, while a session whose workspace HAS a store
// runs the sweep and surfaces a problem through the shared warning helper.
func TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmp := t.TempDir()
	cfg := nativeToolAgentConfig("startup-maintenance")
	cfg.Skills.Autoload = nil

	// The data directory derives from the injected home directory, exactly as
	// production derives it.
	homeDir := t.TempDir()
	dataDir := config.DataDir(homeDir)
	cfgSnapshots := cfg.Snapshots
	cfgSnapshots.RetentionDays = 7

	// 1. No store at all: StartRuntime must not create one.
	rt, err := StartRuntime(context.Background(),
		WithWorkingDir(tmp),
		WithHomeDir(homeDir),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "snapshots")); err == nil && len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("startup created store metadata for a workspace that never captured: %v", names)
	}
	if got := len(snapshotWarningMessages(rt.State)); got != 0 {
		rt.Close(context.Background())
		t.Fatalf("a storeless startup produced %d snapshot warnings, want 0: %v", got, snapshotWarningMessages(rt.State))
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 2. With a store: the sweep runs, and an over-budget store reaches the
	// transcript through the same helper the in-turn captures use.
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := snapshot.New(dataDir, tmp, int64(cfgSnapshots.MaxFileBytes), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.Track(context.Background()); err != nil {
		t.Fatalf("seed Track: %v", err)
	}

	// A workspace ceiling of one byte makes the store need attention on the
	// next sweep, which is exactly the condition the warning must surface.
	cfg.Snapshots = cfgSnapshots
	cfg.Snapshots.WorkspaceMaxBytes = 1
	cfg.Snapshots.GlobalMaxBytes = 1

	rt2, err := StartRuntime(context.Background(),
		WithWorkingDir(tmp),
		WithHomeDir(homeDir),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
	)
	if err != nil {
		t.Fatalf("StartRuntime (with store): %v", err)
	}
	defer rt2.Close(context.Background())
	warnings := snapshotWarningMessages(rt2.State)
	if len(warnings) == 0 {
		t.Fatal("startup maintenance over an over-budget store produced no visible warning")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "Snapshot maintenance unavailable") ||
		!strings.Contains(joined, "WITHOUT a rollback snapshot") {
		t.Errorf("startup maintenance warning lacks the shared wording:\n%s", joined)
	}
}

// failingSnapshotter is the agent.Snapshotter double for the wiring test.
type failingSnapshotter struct{ err error }

func (f *failingSnapshotter) Track(context.Context) (string, error) { return "", f.err }

func (f *failingSnapshotter) Diff(context.Context, string) (string, error) { return "", nil }

func (f *failingSnapshotter) Restore(context.Context, string) error { return nil }
