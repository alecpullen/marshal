package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/policy"
)

// A runtime interaction patch is a session-scoped authority change: it must not
// save a config file, reload the runtime, or replace the managers a running job
// depends on, and the committed tuple is what the runner then reports.
func TestRuntimeInteractionPatchDoesNotSaveReloadOrReplaceManagers(t *testing.T) {
	cfg := nativeToolAgentConfig("interaction-patch")
	rt, state := newAccessTestRuntime(t, cfg)

	// A save-visible fixture: bytes of a config file the runtime could
	// plausibly rewrite. A patch must leave them byte-identical.
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agent]\nmode = \"act\"\n"), 0644); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}

	var reloads atomic.Int64
	rt.ConfigReloader = func(config.Config) error {
		reloads.Add(1)
		return nil
	}
	runner, jobMgr := rt.Runner, rt.JobManager

	mode := interaction.ModeAct
	auto := true
	st, err := rt.SetInteraction(interaction.Patch{Mode: &mode, Auto: &auto})
	if err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}
	if st.Mode != interaction.ModeAct || !st.Auto {
		t.Fatalf("committed tuple = %+v, want act/auto", st)
	}
	if got := state.InteractionState(); got != st {
		t.Fatalf("session tuple = %+v, want the committed %+v", got, st)
	}
	if n := reloads.Load(); n != 0 {
		t.Fatalf("a patch triggered %d config reloads, want 0", n)
	}
	if rt.Runner != runner {
		t.Fatal("a patch replaced the runner")
	}
	if rt.JobManager != jobMgr {
		t.Fatal("a patch replaced the job manager")
	}
	// The configured baseline is untouched: a patch is not a config edit.
	if rt.Config.Agent.Mode != cfg.Agent.Mode || rt.Config.Agent.Auto != cfg.Agent.Auto {
		t.Fatalf("Runtime.Config tuple = %q/%v, want the configured %q/%v",
			rt.Config.Agent.Mode, rt.Config.Agent.Auto, cfg.Agent.Mode, cfg.Agent.Auto)
	}
	// The engine's compatibility projection follows the committed tuple.
	if runner.Policy == nil {
		t.Fatal("the runner has no policy engine")
	}
	if got := runner.Policy.ApprovalMode(); got != policy.ModeAuto {
		t.Fatalf("engine approval mode = %q, want %q", got, policy.ModeAuto)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-read fixture config: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a patch rewrote the config file:\nbefore %q\nafter  %q", before, after)
	}
}

// A rejected patch changes nothing: the tuple, the revision and the engine stay
// exactly as they were.
func TestRuntimeInteractionPatchFailureIsAtomic(t *testing.T) {
	cfg := nativeToolAgentConfig("interaction-patch-failure")
	rt, state := newAccessTestRuntime(t, cfg)
	runner := rt.Runner

	before := state.InteractionState()
	beforeMode := runner.Policy.ApprovalMode()
	bad := interaction.Mode("bogus")
	if _, err := rt.SetInteraction(interaction.Patch{Mode: &bad}); err == nil {
		t.Fatal("SetInteraction accepted an invalid mode")
	}
	if got := state.InteractionState(); got != before {
		t.Fatalf("session tuple after a rejected patch = %+v, want %+v", got, before)
	}
	if rt.Runner != runner {
		t.Fatal("a rejected patch replaced the runner")
	}
	if got := runner.Policy.ApprovalMode(); got != beforeMode {
		t.Fatalf("engine mode after a rejected patch = %q, want %q", got, beforeMode)
	}
}

// A patch applies to this conversation only: the configured starting tuple, and
// therefore the next conversation, keeps its configured value.
func TestRuntimeInteractionConfigStaysStartingDefaults(t *testing.T) {
	cfg := nativeToolAgentConfig("interaction-config-defaults")
	rt, state := newAccessTestRuntime(t, cfg)
	want, err := cfg.InteractionDefaults()
	if err != nil {
		t.Fatalf("InteractionDefaults: %v", err)
	}

	mode := interaction.ModeAsk
	if _, err := rt.SetInteraction(interaction.Patch{Mode: &mode}); err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}
	if got := state.InteractionState(); got.Mode != interaction.ModeAsk {
		t.Fatalf("session mode = %q, want ask", got.Mode)
	}
	if _, err := rt.Config.InteractionDefaults(); err != nil {
		t.Fatalf("Runtime.Config no longer validates after a patch: %v", err)
	}
	if rt.Config.Agent.Mode != cfg.Agent.Mode || rt.Config.Agent.Auto != cfg.Agent.Auto {
		t.Fatalf("Runtime.Config tuple = %q/%v, want the configured %q/%v",
			rt.Config.Agent.Mode, rt.Config.Agent.Auto, cfg.Agent.Mode, cfg.Agent.Auto)
	}
	// A conversation started after the patch still begins at the configured
	// tuple, so the patch never became a saved default.
	fresh := session.New(rt.Config, t.TempDir(), time.Unix(0, 0), session.Persistence{})
	if got := fresh.InteractionState(); got.Mode != want.Mode || got.Auto != want.Auto {
		t.Fatalf("a fresh conversation started at %+v, want the configured %+v", got, want)
	}
}

// An unrelated reload keeps the session's patched tuple; an explicit change to
// the configured tuple replaces it. System access survives both.
func TestRuntimeInteractionDefaultTupleRebase(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("interaction-rebase")
	rt, state := newAccessTestRuntime(t, cfg)

	ask := interaction.ModeAsk
	sys := true
	if _, err := rt.SetInteraction(interaction.Patch{Mode: &ask, System: &sys}); err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}
	patched := state.InteractionState()

	// An unrelated setting changes; the patched tuple and System survive.
	unrelated := cfg
	unrelated.Agent.MaxToolIterations = cfg.Agent.MaxToolIterations + 7
	if err := reloadAgentRuntime(ctx, unrelated, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	kept := state.InteractionState()
	if kept.Mode != interaction.ModeAsk || !kept.System {
		t.Fatalf("tuple after an unrelated reload = %+v, want ask with system access kept", kept)
	}
	if kept.Revision <= patched.Revision {
		t.Fatalf("revision after a successful install = %d, want it above %d", kept.Revision, patched.Revision)
	}

	// An explicit change to the configured tuple clears the patch; System is
	// still preserved.
	explicit := cfg
	explicit.Agent.Mode = interaction.ModeAct
	explicit.Agent.Auto = true
	if err := reloadAgentRuntime(ctx, explicit, rt); err != nil {
		t.Fatalf("reloadAgentRuntime (explicit): %v", err)
	}
	got := state.InteractionState()
	if got.Mode != interaction.ModeAct || !got.Auto {
		t.Fatalf("tuple after an explicit configured change = %+v, want act/auto", got)
	}
	if !got.System {
		t.Fatal("an explicit configured change revoked system access")
	}
	if got.Revision <= kept.Revision {
		t.Fatalf("revision after the explicit install = %d, want it above %d", got.Revision, kept.Revision)
	}
	if rt.Config.Agent.Mode != interaction.ModeAct || !rt.Config.Agent.Auto {
		t.Fatalf("Runtime.Config tuple = %q/%v, want the explicit %q/true", rt.Config.Agent.Mode, rt.Config.Agent.Auto, interaction.ModeAct)
	}
}

// A reload that cannot build changes no tuple, source or revision.
func TestRuntimeInteractionFailedReloadPreservesRevision(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("interaction-failed-reload")
	rt, state := newAccessTestRuntime(t, cfg)
	runner := rt.Runner

	mode := interaction.ModeAct
	auto := true
	if _, err := rt.SetInteraction(interaction.Patch{Mode: &mode, Auto: &auto}); err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}
	before := state.InteractionState()

	// A config that cannot build: the preset names a provider that is not
	// configured, so resolution fails before any credential is touched.
	bad := config.Default()
	bad.Privacy.RemoteProvidersAllowed = true
	bad.Profile.Default = "p"
	bad.Models.Presets = map[string]routing.ModelPreset{
		"missing/m": {Provider: "missing", Model: "m", LocalOnly: true},
	}
	bad.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "missing/m"},
		}},
	}
	if err := reloadAgentRuntime(ctx, bad, rt); err == nil {
		t.Fatal("expected the reload to fail")
	}
	if rt.Runner != runner {
		t.Fatal("a failed reload replaced the runner")
	}
	if got := state.InteractionState(); got != before {
		t.Fatalf("tuple after a failed reload = %+v, want %+v", got, before)
	}
	if got := rt.Config.Agent.Mode; got != cfg.Agent.Mode {
		t.Fatalf("Runtime.Config mode after a failed reload = %q, want %q", got, cfg.Agent.Mode)
	}
}

// A grant and a reload serialize on the same gate and leave the session tuple
// and the runner agreeing, with the grant's revision committed exactly once.
func TestRuntimeInteractionGrantRevisionSerializesWithReload(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("interaction-grant-revision")
	rt, state := newAccessTestRuntime(t, cfg)

	// Start from Ask so an editing request is genuinely missing; a session that
	// already holds Act can only be asked for System.
	ask := interaction.ModeAsk
	if _, err := rt.SetInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}
	req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
	before := state.InteractionState()

	var wg sync.WaitGroup
	wg.Add(2)
	var grantErr error
	var grantResult session.AccessResult
	go func() {
		defer wg.Done()
		grantResult, grantErr = rt.ResolveAccess(approve(req, "copilot"))
	}()
	go func() {
		defer wg.Done()
		_ = reloadAgentRuntime(ctx, cfg, rt)
	}()
	wg.Wait()
	if grantErr != nil {
		t.Fatalf("ResolveAccess: %v", grantErr)
	}

	got := state.InteractionState()
	// The reload may win the gate. The presented pair is then stale, and the
	// session answers "refresh" rather than granting a capability against a
	// pair that no longer exists. Either outcome is a consistent serialization.
	want := interaction.Defaults{Mode: interaction.ModeAsk}
	wantRevision := before.Revision + 1
	switch grantResult.Status {
	case session.AccessGranted:
		want = interaction.Defaults{Mode: interaction.ModeAct}
		wantRevision = before.Revision + 2
	case session.AccessRefresh:
		// Nothing was granted; the reload's Ask tuple is in force.
	default:
		t.Fatalf("status after a grant racing a reload = %q", grantResult.Status)
	}
	if got.Mode != want.Mode || got.Auto || got.Revision != wantRevision {
		t.Fatalf("tuple after a grant racing a reload = %+v, want %+v at revision %d", got, want, wantRevision)
	}
	if granted := grantResult.Status == session.AccessGranted; granted != got.System {
		t.Fatalf("system access = %v after status %q", got.System, grantResult.Status)
	}
	if rt.Runner == nil || rt.Runner.Policy == nil {
		t.Fatal("the runtime has no policy engine")
	}
	if engine := rt.Runner.Policy.ApprovalMode(); engine != derivedEngineMode(want) {
		t.Fatalf("engine mode = %q, want %q for %+v", engine, derivedEngineMode(want), want)
	}
}

// The optional exit-analysis pass runs under the conversation's current
// authority. In Ask, and in Act without System, the report write it would make
// is not admitted, the pass reports its own non-approved failure, and the
// host-managed extracted report is retained untouched.
func TestExitAnalysisNeverElevatesConversationPermissions(t *testing.T) {
	for _, mode := range []interaction.Mode{interaction.ModeAsk, interaction.ModeAct} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			rt := startEndRuntime(t)
			if rt.Runner == nil || rt.Runner.Policy == nil {
				t.Fatal("the fixture runtime has no runner/engine")
			}

			sys := false
			m := mode
			if _, err := rt.SetInteraction(interaction.Patch{Mode: &m, System: &sys}); err != nil {
				t.Fatalf("SetInteraction: %v", err)
			}
			before := rt.State.InteractionState()
			beforeMode := rt.Runner.Policy.ApprovalMode()

			// The host-managed extraction landed before the optional agent pass
			// starts.
			report := filepath.Join(t.TempDir(), "postmortem.json")
			original := []byte(`{"agent_observations":[]}`)
			if err := os.WriteFile(report, original, 0644); err != nil {
				t.Fatalf("write extracted report: %v", err)
			}

			// Settle an exit so the postmortem phase admits analysis.
			if _, err := rt.AcceptEnd(EndAcceptRequest{
				Intent:           EndIntentExit,
				DisplayedIDs:     endWorkIDs(rt.ActiveWork()),
				RequireDisplayed: true,
			}); err != nil {
				t.Fatalf("AcceptEnd: %v", err)
			}
			settleCtx, cancelSettle := context.WithTimeout(ctx, 5*time.Second)
			if err := rt.WaitForEnd(settleCtx); err != nil {
				cancelSettle()
				t.Fatalf("WaitForEnd: %v", err)
			}
			cancelSettle()

			opRan := false
			writeAdmitted := false
			rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
				opRan = true
				if scope == nil || scope.ID() == "" {
					t.Error("the analysis pass ran without a scope")
				}
				if rt.State.SystemAccess() {
					t.Error("the analysis pass ran with system access it was never granted")
				}
				if got := rt.Runner.Policy.ApprovalMode(); got != beforeMode {
					t.Errorf("the analysis pass moved the engine mode to %q, want %q", got, beforeMode)
				}
				// The optional agent write: evaluate the report write under the
				// conversation's live authority. It must not be preauthorized.
				call := policy.Call{ToolName: "file.write", Args: map[string]any{"path": report}}
				if _, ev, err := rt.Runner.Authorizer.Evaluate(analysisCtx, call); err == nil && ev.Decision == policy.DecisionAllow {
					writeAdmitted = true
				}
				return errors.New("analysis not approved: the pass cannot write the report path")
			})

			if err := rt.RunExitAnalysis(ctx, "record observations in "+report); err == nil {
				t.Fatal("RunExitAnalysis swallowed the pass's non-approved failure")
			}
			if !opRan {
				t.Fatal("the optional agent pass never ran")
			}
			if writeAdmitted {
				t.Fatal("an unapproved optional agent write was admitted")
			}
			if got := rt.State.InteractionState(); got != before {
				t.Fatalf("exit analysis changed conversation permissions: %+v → %+v", before, got)
			}
			after, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("the managed report was removed by a failed analysis: %v", err)
			}
			if !bytes.Equal(after, original) {
				t.Fatalf("the failed analysis rewrote the managed report: %q", after)
			}
		})
	}
}
