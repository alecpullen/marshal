package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// Owned-workspace dispatch: one agent.run call gets a durable task identity
// before any workspace exists, allocates (or re-observes) the workspace that
// identity owns, binds its child to it, and records how the child's run ended.
//
// Everything here is instance-scoped. The service is wired by the runtime
// through WithSubagentWorkspaceService; a tool built without one (an embedded
// fixture, a package test) keeps the pre-cutover binding of the captured parent
// checkout and owns no record. There is no package-global factory and no second
// source of lifecycle truth: the record is the store's, and the live card joins
// it by WorkKey.

// subagentWorkspaceConfig is the owned-workspace half of agent.run's wiring.
type subagentWorkspaceConfig struct {
	// service allocates, observes and completes the owned workspace. Nil means
	// this build owns no workspaces: dispatch keeps the pre-cutover binding of
	// the parent's captured checkout.
	service *subagentwork.Service
	// isolatedSupported is the execution backend's own statement that it can
	// host a linked git worktree. It is trusted wiring, never a model argument,
	// and false refuses isolated dispatch instead of falling back to shared.
	isolatedSupported bool
}

// wired reports whether this build can allocate an owned workspace.
func (c subagentWorkspaceConfig) wired() bool { return c.service != nil }

// WithSubagentWorkspaceService configures agent.run with the conversation's
// owned-workspace service. It is instance-scoped: the runtime creates one
// service per conversation and hands the same instance to every build, so a
// config reload reuses the records, the child and the service rather than
// starting a second one.
func WithSubagentWorkspaceService(service *subagentwork.Service) SubagentOption {
	return func(cfg *subagentToolConfig) { cfg.workspaces.service = service }
}

// WithIsolatedWorktreeSupport supplies the execution backend's capability to
// agent.run. Production derives it from the backend itself
// (native.SupportsLinkedWorktrees); a build that never states it supports
// nothing, because an absent capability is not a yes.
func WithIsolatedWorktreeSupport(supported bool) SubagentOption {
	return func(cfg *subagentToolConfig) { cfg.workspaces.isolatedSupported = supported }
}

// WithAwaitWorkspaces configures agent.await's retained task-key lookup: the
// owner-scoped records a conversation keeps after its children finished, so a
// parent (or the same parent after a restart) can inspect a retained report and
// candidate without reviving a child.
func WithAwaitWorkspaces(service *subagentwork.Service) AwaitOption {
	return func(o *awaitOptions) { o.workspaces = service }
}

// resolveSubagentWorkspaceMode applies the dispatch contract to the model's
// workspace argument: omitted means isolated, an explicit empty or unknown
// value is refused, and a base commit is only meaningful for an isolated
// workspace. Every one of these is decided before anything is allocated, so a
// refused call costs no worktree, no slot and no provider request.
func resolveSubagentWorkspaceMode(raw *string, baseCommit string) (subagentwork.Mode, error) {
	mode := subagentwork.ModeIsolated
	if raw != nil {
		switch strings.TrimSpace(*raw) {
		case string(subagentwork.ModeIsolated):
			mode = subagentwork.ModeIsolated
		case string(subagentwork.ModeShared):
			mode = subagentwork.ModeShared
		case "":
			return "", errors.New(`agent.run: "workspace" must be "isolated" or "shared"; omit it for the default (isolated)`)
		default:
			return "", fmt.Errorf(`agent.run: unknown workspace %q; use "isolated" or "shared"`, *raw)
		}
	}
	if mode == subagentwork.ModeShared && strings.TrimSpace(baseCommit) != "" {
		return "", errors.New(`agent.run: "base_commit" is only meaningful for an isolated workspace; a shared child works in the captured checkout as it is`)
	}
	return mode, nil
}

// subagentTaskIdentity derives the stable identity of one logical dispatch.
//
// The identity comes from the execution facts the runner stamped on the call —
// the turn, the step and the tool call — never from the prompt text and never
// from a model call id alone, so a retried call that reaches the handler twice
// derives the same key while two genuinely different calls in one step keep
// theirs. The key is opaque generated safe text because it becomes a branch
// component and a directory name.
func subagentTaskIdentity(ctx context.Context, owner string) (taskKey, invocationID string) {
	origin, ok := session.ExecutionOriginFromContext(ctx)
	if !ok || (origin.TurnID == "" && origin.StepID == "" && origin.ToolCallID == "") {
		// No logical invocation is known (a direct caller, a fixture), so this
		// dispatch is its own: a random key deduplicates nothing and silently
		// merges no two calls.
		key := "t" + rand.Text()
		return key, key
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		owner, origin.TurnID, origin.StepID, origin.ToolCallID,
	}, "\x00")))
	key := "t" + hex.EncodeToString(sum[:16])
	return key, key
}

// lookupSubagentWorkspace reports whether this logical invocation already owns
// a retained task, so a duplicate call observes a definite existing record
// instead of starting a second child. A found record is returned as it stands;
// the caller decides what its state means.
func (c subagentWorkspaceConfig) lookup(ctx context.Context, state *session.State) (taskKey, invocationID string, existing *subagentwork.Record, err error) {
	taskKey, invocationID = subagentTaskIdentity(ctx, state.SessionID())
	if !c.wired() {
		return taskKey, invocationID, nil, nil
	}
	rec, inspectErr := c.service.Inspect(ctx, state.SessionID(), taskKey)
	switch {
	case inspectErr == nil:
		return taskKey, invocationID, &rec, nil
	case errors.Is(inspectErr, subagentwork.ErrRecordNotFound):
		return taskKey, invocationID, nil, nil
	default:
		return taskKey, invocationID, nil, fmt.Errorf("agent.run: inspect owned workspace: %w", inspectErr)
	}
}

// prepare allocates (or re-observes) the workspace this dispatch owns. It is
// called after the concurrency slot and the consent gate, so a refused caller
// never leaves a worktree behind, and before the child factory, so the child is
// built against the recorded binding rather than a guess.
func (c subagentWorkspaceConfig) prepare(ctx context.Context, state *session.State, args agentRunArgs, mode subagentwork.Mode, taskKey, invocationID string) (subagentwork.Record, error) {
	ws := state.Workspace()
	rec, err := c.service.Prepare(ctx, subagentwork.PrepareRequest{
		OwnerSessionID:    state.SessionID(),
		TaskKey:           taskKey,
		InvocationID:      invocationID,
		ProjectRoot:       ws.ProjectRoot,
		ParentRoot:        ws.ActiveRoot,
		BaseCommit:        args.BaseCommit,
		Mode:              mode,
		Setup:             worktree.WorktreeSetup{Config: state.Config.Worktree},
		IsolatedSupported: c.isolatedSupported,
	})
	if err != nil {
		return rec, err
	}
	state.ApplySubagentWorkspace(rec)
	return rec, nil
}

// observe re-reads the owned workspace as it is now and retains what it found.
// It is the evidence a completion report quotes, and it is taken before the
// report is appended so the report names the same candidate the record settles
// on.
func (c subagentWorkspaceConfig) observe(ctx context.Context, state *session.State, rec subagentwork.Record) subagentwork.Record {
	if !c.wired() || rec.TaskKey == "" {
		return rec
	}
	observed, err := c.service.Inspect(context.WithoutCancel(ctx), rec.OwnerSessionID, rec.TaskKey)
	if err != nil {
		state.Logger().Warn("observe owned subagent workspace failed", "error", err, "subagent_task", rec.TaskKey)
		return rec
	}
	state.ApplySubagentWorkspace(observed)
	return observed
}

// complete records how the child's run ended. The outcome is exactly what the
// caller reports: a failed, stopped or partial run is never promoted to success
// because its workspace happens to contain commits. It runs with a
// non-cancellable context because the child's own context is cancelled on every
// terminal path, and losing this write is losing the evidence.
func (c subagentWorkspaceConfig) complete(ctx context.Context, state *session.State, rec subagentwork.Record, outcome string, reportMessageID int64) {
	if !c.wired() || rec.TaskKey == "" {
		return
	}
	written, err := c.service.Complete(context.WithoutCancel(ctx), rec.OwnerSessionID, rec.TaskKey, subagentwork.Completion{
		Outcome:         outcome,
		ReportMessageID: reportMessageID,
	})
	if err != nil {
		state.Logger().Error("record owned subagent workspace completion failed", "error", err, "subagent_task", rec.TaskKey)
		return
	}
	state.ApplySubagentWorkspace(written)
}

// runSetup executes the setup hooks captured at allocation, inside the child's
// own workspace and under its bounded actor, before any model call.
//
// This is P1's setup path: each hook is prepared by the child registry's
// shell.run (so its cwd and environment are the child's), admitted by the
// child's own authorizer — never a fabricated approval — and launched through
// the conversation's one command service. A refusal and a failed hook both fail
// the dispatch: required setup that did not run leaves a child whose build state
// is unknown, and starting a model there spends tokens on a workspace the parent
// cannot trust. The worktree is retained either way.
func (c subagentWorkspaceConfig) runSetup(ctx context.Context, state *session.State, rec subagentwork.Record, child *Runner) error {
	if len(rec.SetupPlan.Hooks) == 0 {
		return nil
	}
	if child == nil || child.Registry == nil {
		return errors.New("owned workspace setup is unavailable: the child has no tool registry")
	}
	jobs, _ := state.GetTurnStopService().(*native.JobManager)
	if jobs == nil {
		return errors.New("owned workspace setup is unavailable: the conversation's command service is not installed")
	}
	warnings, err := native.RunSetupPlan(ctx, rec.SetupPlan, child.Registry, child.Authorizer, jobs)
	if err != nil {
		return fmt.Errorf("required workspace setup did not run: %w", err)
	}
	if len(warnings) > 0 {
		return fmt.Errorf("required workspace setup failed: %s", strings.Join(warnings, "; "))
	}
	return nil
}

// subagentRecordOutcome maps a finished child to the record's execution
// outcome. A cancelled child was stopped, not failed, and a salvaged report is
// partial: the record must not claim more than the run actually did.
func subagentRecordOutcome(runErr error, salvagedReason string) string {
	switch {
	case runErr == nil && salvagedReason != "":
		return subagentwork.OutcomePartial
	case runErr == nil:
		return subagentwork.OutcomeDone
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return subagentwork.OutcomeStopped
	default:
		return subagentwork.OutcomeFailed
	}
}

// capturedCheckoutBinding is the pre-cutover binding: the parent's own
// checkout, captured at dispatch, in shared mode. A build with no workspace
// service (an embedded fixture) binds its children this way, and the record's
// own binding replaces it whenever the service is wired.
func capturedCheckoutBinding(state *session.State) (subagentwork.Binding, subagentwork.Target) {
	ws := state.Workspace()
	return subagentwork.Binding{
			Mode:        subagentwork.ModeShared,
			Root:        ws.ActiveRoot,
			ProjectRoot: ws.ProjectRoot,
			Branch:      ws.Branch,
			BaseSHA:     ws.BaseSha,
		}, subagentwork.Target{
			Root:    ws.ActiveRoot,
			Branch:  ws.Branch,
			BaseSHA: ws.BaseSha,
		}
}

// liveViewByWorkKey finds the live card that already owns key, so a duplicate
// dispatch returns the receipt of the child it started rather than a second one.
func liveViewByWorkKey(state *session.State, key string) (session.SubagentView, bool) {
	if key == "" {
		return session.SubagentView{}, false
	}
	for _, v := range state.Subagents() {
		if v.WorkKey == key {
			return v, true
		}
	}
	return session.SubagentView{}, false
}

// duplicateDispatchReceipt builds the result a repeated logical invocation
// returns. It never starts a child: a definite existing task returns the
// receipt of the work it already owns, and a prior start whose state cannot be
// established returns unknown rather than a second run.
func duplicateDispatchReceipt(state *session.State, rec subagentwork.Record, args agentRunArgs) registry.ToolResult {
	label := args.Description
	if label == "" {
		label = rec.TaskKey
	}
	switch rec.Outcome {
	case subagentwork.OutcomePreparing, subagentwork.OutcomeUnknown:
		// An allocation intent exists but never reached a definite prepared
		// state. Another run would race that attempt; the honest answer is
		// unknown, with the retained artifacts named.
		return registry.ToolResult{
			Summary: fmt.Sprintf("task %s: prior start is unknown — no second child was started", rec.TaskKey),
			Content: fmt.Sprintf("An earlier dispatch of this same call began task %s and its allocation never reported a definite result. No second child was started, and no model call was made. Inspect the retained workspace with agent.await {\"task_key\": %q} before deciding what to do; the branch and any worktree it names are retained, not deleted.\n\n%s",
				rec.TaskKey, rec.TaskKey, subagentWorkspaceBlock(rec)),
		}
	}
	if v, ok := liveViewByWorkKey(state, rec.TaskKey); ok {
		return registry.ToolResult{
			Summary: fmt.Sprintf("started as subagent %d — %s", v.ID, label),
			Content: fmt.Sprintf("Subagent %d (%s) already owns task %s for this call, so no second child was started and no model call was made. Its report will arrive as a [subagent %d finished] message; call agent.await with \"id\": %d to wait for it, or with \"task_key\": %q to inspect the retained workspace.\n\n%s",
				v.ID, label, rec.TaskKey, v.ID, v.ID, rec.TaskKey, subagentWorkspaceBlock(rec)),
			Notice: subagentWorkNotice(v),
		}
	}
	return registry.ToolResult{
		Summary: fmt.Sprintf("task %s is already %s — no second child was started", rec.TaskKey, subagentOutcomeWord(rec.Outcome)),
		Content: fmt.Sprintf("This call already dispatched task %s and it is %s. No second child was started and no model call was made. Inspect the retained report and candidate with agent.await {\"task_key\": %q}.\n\n%s",
			rec.TaskKey, subagentOutcomeWord(rec.Outcome), rec.TaskKey, subagentWorkspaceBlock(rec)),
	}
}

// subagentOutcomeWord renders a recorded outcome for a tool result.
func subagentOutcomeWord(outcome string) string {
	switch outcome {
	case subagentwork.OutcomePrepared, subagentwork.OutcomeRunning:
		return "running"
	case subagentwork.OutcomeDone:
		return "finished"
	case subagentwork.OutcomePartial:
		return "finished with a partial report"
	case subagentwork.OutcomeFailed:
		return "failed"
	case subagentwork.OutcomeStopped:
		return "stopped"
	case subagentwork.OutcomeInterrupted:
		return "interrupted"
	case subagentwork.OutcomeUnknown, "":
		return "of unknown state"
	default:
		return outcome
	}
}

// subagentWorkNotice is the machine-readable reference to a registered child,
// so a consumer can pair a receipt with the report it later binds. Empty for a
// card with no work identity.
func subagentWorkNotice(v session.SubagentView) *registry.ToolNotice {
	childSessionID := ""
	if v.Child != nil {
		childSessionID = v.Child.SessionID()
	}
	return &registry.ToolNotice{
		Kind: registry.NoticeSubagentWork,
		Data: map[string]any{
			registry.NoticeSubagentWork: registry.SubagentWorkReference{
				TaskKey:        v.WorkKey,
				RuntimeEpoch:   v.RuntimeEpoch,
				AgentID:        v.ID,
				ChildSessionID: childSessionID,
			},
		},
	}
}

// workspaceReceiptSuffix appends the workspace identity to a dispatch receipt.
// It is empty for a build that owns no workspaces, so the pre-cutover receipt
// text is unchanged there.
func workspaceReceiptSuffix(rec subagentwork.Record) string {
	block := subagentWorkspaceBlock(rec)
	if block == "" {
		return ""
	}
	return "\n\n" + block
}

// subagentWorkspaceBlock renders the workspace identity a receipt, a report and
// the retained record all name: the task key, the mode, the canonical worktree
// path, the branch, the starting commit and the captured integration target,
// plus whatever evidence has been observed so far. A model must never have to
// reconstruct these from a natural-language brief.
func subagentWorkspaceBlock(rec subagentwork.Record) string {
	if rec.TaskKey == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "workspace: %s\n", workspaceModeWord(rec.Binding.Mode))
	fmt.Fprintf(&b, "task: %s\n", rec.TaskKey)
	if rec.Binding.Root != "" {
		fmt.Fprintf(&b, "worktree: %s\n", rec.Binding.Root)
	}
	if rec.Binding.Branch != "" {
		fmt.Fprintf(&b, "branch: %s\n", rec.Binding.Branch)
	}
	if rec.Binding.BaseSHA != "" {
		fmt.Fprintf(&b, "base: %s\n", rec.Binding.BaseSHA)
	}
	if target := workspaceTargetLine(rec.Target); target != "" {
		fmt.Fprintf(&b, "target: %s\n", target)
	}
	if rec.Candidate != "" {
		fmt.Fprintf(&b, "candidate: %s\n", rec.Candidate)
	}
	if len(rec.Commits) > 0 {
		fmt.Fprintf(&b, "commits: %s\n", strings.Join(rec.Commits, ", "))
	}
	if len(rec.ChangedPaths) > 0 {
		fmt.Fprintf(&b, "changed paths: %s\n", strings.Join(rec.ChangedPaths, ", "))
	}
	if rec.Dirty != nil {
		fmt.Fprintf(&b, "uncommitted changes: %s\n", yesNo(*rec.Dirty))
	} else if rec.Binding.Mode == subagentwork.ModeIsolated {
		b.WriteString("uncommitted changes: unknown\n")
	}
	if rec.SourceError != "" {
		fmt.Fprintf(&b, "workspace error: %s\n", rec.SourceError)
	}
	return strings.TrimRight(b.String(), "\n")
}

// workspaceModeWord names the mode in the words a model reads.
func workspaceModeWord(mode subagentwork.Mode) string {
	switch mode {
	case subagentwork.ModeShared:
		return "shared (the captured parent checkout; this child leaves commits to you)"
	case subagentwork.ModeIsolated:
		return "isolated (this child's own worktree and branch; it commits its own work)"
	default:
		return string(mode)
	}
}

// workspaceTargetLine renders the captured integration target: where this
// child's work is destined to land.
func workspaceTargetLine(target subagentwork.Target) string {
	if target.Root == "" {
		return ""
	}
	if target.Branch != "" {
		return target.Root + " on " + target.Branch
	}
	return target.Root
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
