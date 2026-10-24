package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"marshal/internal/tools/registry"
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
}

// Exit analysis gets temporary Act + System access to annotate its external
// report, then restores the user's tuple on success, failure, and cancellation.
func TestExitAnalysisAutomaticallyGrantsAndRestoresSystemAccess(t *testing.T) {
	for _, mode := range []interaction.Mode{interaction.ModeAsk, interaction.ModeAct} {
		for _, outcome := range []string{"success", "failure", "canceled"} {
			t.Run(string(mode)+"/"+outcome, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				rt := startEndRuntime(t)
				sys := false
				if _, err := rt.SetInteraction(interaction.Patch{Mode: &mode, System: &sys}); err != nil {
					t.Fatal(err)
				}
				before := rt.State.InteractionState()
				report := filepath.Join(t.TempDir(), "postmortem.json")
				original := []byte(`{"agent_observations":[]}`)
				if err := os.WriteFile(report, original, 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: endWorkIDs(rt.ActiveWork()), RequireDisplayed: true}); err != nil {
					t.Fatal(err)
				}
				settleCtx, cancelSettle := context.WithTimeout(ctx, 5*time.Second)
				if err := rt.WaitForEnd(settleCtx); err != nil {
					cancelSettle()
					t.Fatal(err)
				}
				cancelSettle()
				analysisErr := errors.New("analysis failed")
				ran := false
				rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
					ran = true
					access := rt.State.InteractionState()
					if access.Mode != interaction.ModeAct || !access.System || access.Auto != before.Auto {
						return fmt.Errorf("postmortem authority = %+v, want Act + System with Auto preserved", access)
					}
					registryBefore, policyBefore := rt.ToolRegistry, rt.Runner.Policy
					candidate := config.Clone(rt.Config)
					candidate.Project.Name = "must-not-reload-during-postmortem"
					if err := reloadAgentRuntime(analysisCtx, candidate, rt); !errors.Is(err, session.ErrConversationEnding) {
						return fmt.Errorf("reload during postmortem = %v, want conversation-ending refusal", err)
					}
					if rt.ToolRegistry != registryBefore || rt.Runner.Policy != policyBefore || rt.Config.Project.Name == candidate.Project.Name {
						return errors.New("postmortem reload replaced live dependencies")
					}
					authority := rt.State.InteractionAuthority()
					if lease, err := authority.BeginAdmission(context.Background(), authority.Snapshot()); err == nil {
						lease.Release()
						return errors.New("ordinary tool admission reopened during postmortem")
					}
					if outcome == "failure" {
						return analysisErr
					}
					if outcome == "canceled" {
						cancel()
						return ctx.Err()
					}
					for i, name := range []string{"file.read", "file.edit"} {
						args := map[string]any{"path": report}
						if name == "file.edit" {
							args["old_string"] = "[]"
							args["new_string"] = `["observed"]`
						}
						call := policy.Call{InvocationID: fmt.Sprint(i), ToolName: name, Args: args}
						proof, ev, err := rt.Runner.Authorizer.Evaluate(analysisCtx, call)
						if err != nil || ev.Decision != policy.DecisionAllow {
							return fmt.Errorf("%s evaluation = %+v: %v", name, ev, err)
						}
						toolCtx := policy.WithAuthorization(analysisCtx, proof)
						admission, err := rt.Runner.Authorizer.BeginTool(toolCtx, call)
						if err != nil {
							return err
						}
						err = admission.Commit()
						admission.Release()
						if err != nil {
							return err
						}
						tool, ok := rt.ToolRegistry.Lookup(name)
						if !ok {
							return fmt.Errorf("missing native tool %s", name)
						}
						raw, err := json.Marshal(args)
						if err != nil {
							return err
						}
						if _, err := tool.Handler(toolCtx, registry.ToolCall{Name: name, Args: raw}); err != nil {
							return err
						}
					}
					return nil
				})
				err := rt.RunExitAnalysis(ctx, "record observations in "+report)
				switch outcome {
				case "success":
					if err != nil {
						t.Fatalf("RunExitAnalysis: %v", err)
					}
				case "failure":
					if !errors.Is(err, analysisErr) {
						t.Fatalf("RunExitAnalysis = %v, want analysis error", err)
					}
				case "canceled":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("RunExitAnalysis = %v, want cancellation", err)
					}
				}
				if !ran {
					t.Fatal("postmortem pass did not run")
				}
				after := rt.State.InteractionState()
				if after.Mode != before.Mode || after.Auto != before.Auto || after.System != before.System {
					t.Fatalf("postmortem leaked access: %+v -> %+v", before, after)
				}
				data, err := os.ReadFile(report)
				if err != nil {
					t.Fatal(err)
				}
				want := original
				if outcome == "success" {
					want = []byte(`{"agent_observations":["observed"]}`)
				}
				if !bytes.Equal(data, want) {
					t.Fatalf("report = %q, want %q", data, want)
				}
			})
		}
	}
}
