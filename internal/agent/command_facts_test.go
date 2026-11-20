package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pathutil"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// factsRunner is a command runner that also reports the backend environment it
// would launch with, which is what a preparation requires. It records every
// request so a test can compare what policy judged with what actually runs.
type factsRunner struct {
	mu       sync.Mutex
	requests []native.CommandRequest
}

func (r *factsRunner) Run(_ context.Context, req native.CommandRequest) (native.CommandResult, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	if req.OnStart != nil {
		req.OnStart(4242)
	}
	return native.CommandResult{ExitCode: 0, Meta: registry.SandboxMeta{Backend: "fixture", Enabled: true}}, nil
}

func (r *factsRunner) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	return []string{"PATH=/fixture/bin", "PWD=" + dir}, map[string]string{"PATH": "/fixture/bin", "PWD": dir}, nil
}

func (r *factsRunner) lastRequest(t *testing.T) native.CommandRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("runner was never called")
	}
	return r.requests[len(r.requests)-1]
}

func (r *factsRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// factsHarness wires one real dispatch boundary: a native registry whose
// shell.run prepares real facts, the session's live authority behind a real
// policy authorizer, and the conversation's one command service.
type factsHarness struct {
	root       string
	state      *session.State
	runner     *factsRunner
	jobs       *native.JobManager
	reg        *registry.Registry
	pol        *policy.PolicyEngine
	authorizer *policy.Authorizer
	runner2    *Runner
	mu         sync.Mutex
	attempts   []policy.CommandAttempt
}

func newFactsHarness(t *testing.T, cfg config.Config, root string, configure ...func(*native.Options)) *factsHarness {
	t.Helper()
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	runner := &factsRunner{}
	jobs := native.NewCommandService(context.Background(), native.LaunchPolicy{
		Runner:         runner,
		Dir:            root,
		MaxOutputBytes: 1 << 20,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jobs.Shutdown(ctx)
	})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	pol.WithRegistry(reg)
	authorizer := policy.NewAuthorizer(pol, state.InteractionAuthority())
	h := &factsHarness{root: root, state: state, runner: runner, jobs: jobs, reg: reg, pol: pol, authorizer: authorizer}
	opts := native.Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		JobManager:    jobs,
		SessionState:  state,
		Config:        cfg,
		CommandAdmission: func(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error) {
			h.mu.Lock()
			h.attempts = append(h.attempts, attempt)
			h.mu.Unlock()
			return authorizer.BeginCommand(ctx, attempt)
		},
		SetupAuthorizer: authorizer,
	}
	for _, apply := range configure {
		apply(&opts)
	}
	if err := native.RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	h.runner2 = NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")
	return h
}

func (h *factsHarness) prepared(t *testing.T, command string) registry.CommandFacts {
	t.Helper()
	tool, ok := h.reg.Lookup("shell.run")
	if !ok || tool.PrepareCommand == nil {
		t.Fatal("shell.run is not registered with a preparation")
	}
	facts, err := tool.PrepareCommand(context.Background(), registry.ToolCall{
		ID: "prep", Name: "shell.run", Args: []byte(`{"command":"` + command + `"}`),
	})
	if err != nil {
		t.Fatalf("PrepareCommand: %v", err)
	}
	return facts
}

func (h *factsHarness) accepted() []policy.CommandAttempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]policy.CommandAttempt(nil), h.attempts...)
}

// TestApprovalAndLaunchFactsMatch pins the whole boundary: the facts the real
// authorizer evaluated, the attempt admitted at the launch, the CommandRequest
// the runner recorded and the managed execution's own evidence all describe
// one command in one directory with one environment — the prepared one.
func TestApprovalAndLaunchFactsMatch(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Confirm.Commands = []string{"echo facts"}
	h := newFactsHarness(t, cfg, t.TempDir())
	prepared := h.prepared(t, "echo facts")

	go answerApproval(t, h.state, session.UserApprovalDecision{Approved: true})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msgs, err := h.runner2.executeToolCall(ctx, ModelAction{
		Type:       ActionToolCall,
		Tool:       "shell.run",
		Args:       json.RawMessage(`{"command":"echo facts"}`),
		ToolCallID: "c1",
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %v, want the one tool result", msgs)
	}
	if strings.Contains(msgs[0].Content, "failed") || strings.Contains(msgs[0].Content, "not started") {
		t.Fatalf("tool result = %q, want the command to have run", msgs[0].Content)
	}

	attempts := h.accepted()
	if len(attempts) != 1 {
		t.Fatalf("accepted attempts = %d, want exactly 1", len(attempts))
	}
	evaluated := attempts[0].Input
	if evaluated.Command != prepared.Command || evaluated.Dir != prepared.Dir {
		t.Fatalf("evaluated facts = %q in %q, want the prepared %q in %q",
			evaluated.Command, evaluated.Dir, prepared.Command, prepared.Dir)
	}
	if !reflect.DeepEqual(evaluated.Env, prepared.Env) {
		t.Fatalf("evaluated Env = %v, want the analyzed %v", evaluated.Env, prepared.Env)
	}

	req := h.runner.lastRequest(t)
	if strings.TrimSpace(req.Command) != strings.TrimSpace(prepared.Command) {
		t.Fatalf("launched command = %q, want the prepared %q", req.Command, prepared.Command)
	}
	if req.Dir != prepared.Dir {
		t.Fatalf("launched dir = %q, want the prepared %q", req.Dir, prepared.Dir)
	}
	if !reflect.DeepEqual(req.Environment, prepared.Environment) {
		t.Fatalf("launched environment = %v, want the prepared %v", req.Environment, prepared.Environment)
	}

	evidence := h.jobs.List()
	if len(evidence) != 1 {
		t.Fatalf("managed executions = %d, want exactly 1", len(evidence))
	}
	if evidence[0].Command != prepared.Command || evidence[0].Dir != prepared.Dir {
		t.Fatalf("execution evidence = %q in %q, want %q in %q",
			evidence[0].Command, evidence[0].Dir, prepared.Command, prepared.Dir)
	}
}

// TestParentRebindDuringApprovalRejectsStaleProof pins that a proof bound to the
// old parent root cannot launch after the root moved mid-approval, and that the
// retry's reevaluation prepares the rebound root.
func TestParentRebindDuringApprovalRejectsStaleProof(t *testing.T) {
	root := t.TempDir()
	rebound := t.TempDir()
	cfg := config.Default()
	cfg.Tools.Shell.Confirm.Commands = []string{"echo facts"}
	h := newFactsHarness(t, cfg, root)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan []string, 1)
	go func() {
		msgs, _ := h.runner2.executeToolCall(ctx, ModelAction{
			Type:       ActionToolCall,
			Tool:       "shell.run",
			Args:       json.RawMessage(`{"command":"echo facts"}`),
			ToolCallID: "c1",
		})
		var texts []string
		for _, m := range msgs {
			texts = append(texts, m.Content)
		}
		done <- texts
	}()
	// Rebind the parent root while the approval is still pending, then approve.
	for h.state.PendingApproval() == nil {
		select {
		case <-done:
			t.Fatal("dispatch returned before asking for approval")
		case <-ctx.Done():
			t.Fatal("approval never appeared")
		case <-time.After(time.Millisecond):
		}
	}
	h.state.SetWorkspace(session.Workspace{ProjectRoot: rebound, ActiveRoot: rebound})
	h.state.PendingApproval().Respond(session.UserApprovalDecision{Approved: true})

	var texts []string
	select {
	case texts = <-done:
	case <-ctx.Done():
		t.Fatal("dispatch never returned")
	}
	if h.runner.count() != 0 {
		t.Fatalf("runner runs = %d, want 0: a stale proof must start no process", h.runner.count())
	}
	var refusal string
	for _, text := range texts {
		refusal += text + "\n"
	}
	if !strings.Contains(refusal, "admission context changed") {
		t.Fatalf("refusal = %q, want the stale admission reported", refusal)
	}

	// Reevaluation prepares the rebound root, and the retry launches there.
	prepared := h.prepared(t, "echo facts")
	if prepared.Dir != rebound {
		t.Fatalf("prepared dir = %q, want the rebound root %q", prepared.Dir, rebound)
	}
	go answerApproval(t, h.state, session.UserApprovalDecision{Approved: true})
	if _, err := h.state.RebaseInteractionConfig(cfg); err != nil {
		t.Fatalf("RebaseInteractionConfig: %v", err)
	}
	msgs, err := h.runner2.executeToolCall(ctx, ModelAction{
		Type:       ActionToolCall,
		Tool:       "shell.run",
		Args:       json.RawMessage(`{"command":"echo facts"}`),
		ToolCallID: "c2",
	})
	if err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("retry messages = %v, want the one tool result", msgs)
	}
	if strings.Contains(msgs[0].Content, "failed") || strings.Contains(msgs[0].Content, "not started") {
		t.Fatalf("retry tool result = %q, want the command to have run", msgs[0].Content)
	}
	req := h.runner.lastRequest(t)
	if req.Dir != rebound || req.Command != "echo facts" {
		t.Fatalf("retry ran %q in %q, want %q in %q", req.Command, req.Dir, "echo facts", rebound)
	}
}

// TestAbsoluteGrantedWritePolicyAgreesWithTool pins that policy judges a
// non-shell write on the target the tool itself resolved: an approved
// additional write root needs no System, and a read-only reference never
// becomes writable just because its path text is placeable.
func TestAbsoluteGrantedWritePolicyAgreesWithTool(t *testing.T) {
	root := t.TempDir()
	granted := t.TempDir()
	reference := t.TempDir()
	cfg := config.Default()
	h := newFactsHarness(t, cfg, root, func(opts *native.Options) {
		opts.AdditionalRoots = []string{granted}
		opts.PathGrants = []pathutil.RootGrant{{Path: reference, Read: true}}
	})
	ctx := context.Background()

	// The permitted additional root: the tool resolves the write, so policy
	// needs no System for it.
	permitted, err := h.runner2.policyCall(ctx, "inv-permitted", "file.write", map[string]interface{}{
		"path": filepath.Join(granted, "out.txt"), "content": "x",
	})
	if err != nil {
		t.Fatalf("policyCall(permitted): %v", err)
	}
	if len(permitted.Targets) != 1 || !permitted.Targets[0].Validated {
		t.Fatalf("permitted targets = %+v, want one validated target", permitted.Targets)
	}
	if _, ev, err := h.authorizer.Evaluate(ctx, permitted); err != nil {
		t.Fatalf("Evaluate(permitted): %v", err)
	} else if ev.Decision != policy.DecisionAllow || ev.Required.System {
		t.Fatalf("permitted write = %q (System=%v), want allow with no System", ev.Decision, ev.Required.System)
	}

	// The read-only reference: the tool refuses the write, so the target stays
	// unvalidated and policy fails closed onto the System requirement.
	denied, err := h.runner2.policyCall(ctx, "inv-reference", "file.write", map[string]interface{}{
		"path": filepath.Join(reference, "never.txt"), "content": "x",
	})
	if err != nil {
		t.Fatalf("policyCall(reference): %v", err)
	}
	if len(denied.Targets) != 1 || denied.Targets[0].Validated {
		t.Fatalf("reference targets = %+v, want one unvalidated target", denied.Targets)
	}
	if _, ev, err := h.authorizer.Evaluate(ctx, denied); err != nil {
		t.Fatalf("Evaluate(reference): %v", err)
	} else if ev.Decision == policy.DecisionAllow || !ev.Required.System {
		t.Fatalf("reference write = %q (System=%v), want a System requirement", ev.Decision, ev.Required.System)
	}
}
