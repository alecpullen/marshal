package pipeline

import (
	"context"
	"fmt"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

// Dispatcher builds and runs one subagent per call. Every subagent is
// fresh: it inherits no conversation history, only the prompt it is given.
type Dispatcher struct {
	Factory swarm.RunnerFactory
	// State, when non-nil, is the parent session that receives a subagent
	// summary card per dispatched role runner. The card is registered only
	// when the factory-built runner runs on its own child session (runner.
	// State != State); a runner sharing the parent session logs to the
	// parent transcript directly and needs no card.
	State *session.State
	// OnTokens, when non-nil, receives each subagent's total token usage.
	OnTokens func(int)

	// ExecCtx, when non-zero, carries the worktree and artifact root that
	// child registries must be bound to. When zero, the dispatcher falls
	// back to the parent session's workspace (legacy behavior, pre-isolation).
	ExecCtx ExecutionContext

	// RegistryFactory, when non-nil, builds a fresh registry bound to the
	// execution context for each dispatch. When nil, the factory-built
	// runner uses the parent registry (legacy).
	RegistryFactory RegistryFactory

	// exec is the seam tests replace. When nil, runExec is used.
	exec func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error)
}

func (d Dispatcher) run(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, label, prompt string) (string, error) {
	if d.exec != nil {
		return d.exec(ctx, role, scope, prompt)
	}
	return d.runExec(ctx, role, scope, label, prompt)
}

// runExec builds a role-bound runner and runs one task on it.
func (d Dispatcher) runExec(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, label, prompt string) (string, error) {
	if d.Factory == nil {
		return "", fmt.Errorf("pipeline dispatch: no runner factory")
	}
	runner, err := d.Factory(role, scope)
	if err != nil {
		return "", fmt.Errorf("pipeline dispatch: build %s runner: %w", role, err)
	}
	// When an execution context and registry factory are wired, bind the
	// runner to a fresh registry scoped to the worktree and artifact root
	// rather than the parent session's project root. Otherwise fall back to
	// the factory-built runner's parent registry (legacy, pre-isolation).
	if d.RegistryFactory != nil && d.ExecCtx.WorkspaceRoot != "" {
		regScope := pipelineScope(scope)
		reg, err := d.RegistryFactory(d.ExecCtx, regScope)
		if err != nil {
			return "", fmt.Errorf("pipeline dispatch: build %s registry: %w", role, err)
		}
		runner.Registry = reg
	}
	if d.OnTokens != nil {
		runner.UsageObserver = func(u schema.TokenUsage) { d.OnTokens(u.TotalTokens) }
	}
	// Register a summary card on the parent session only when the runner
	// streams into its own child session; a shared-session runner already
	// logs to the parent transcript directly.
	registered := d.State != nil && runner.State != nil && runner.State != d.State
	var view session.SubagentView
	if registered {
		if label == "" {
			label = truncateForError(prompt)
		}
		meta := session.SubagentMeta{
			Role: role,
		}
		if runner.Model != "" {
			meta.Model = runner.Model
		}
		if runner.Provider != nil {
			meta.Provider = runner.Provider.Name()
		}
		view = d.State.RegisterSubagentWithMeta(fmt.Sprintf("%s · %s", role, label), runner.State, meta)
	}
	task, err := runner.RunTask(ctx, prompt)
	if err != nil {
		if registered {
			d.State.FinishSubagent(view.ID, "", err)
		}
		return "", fmt.Errorf("pipeline dispatch: %s run: %w", role, err)
	}
	if task == nil {
		err := fmt.Errorf("pipeline dispatch: %s returned no task", role)
		if registered {
			d.State.FinishSubagent(view.ID, "", err)
		}
		return "", err
	}
	if registered {
		d.State.FinishSubagent(view.ID, task.Summary, nil)
	}
	return task.Summary, nil
}

// Implement dispatches an implementer or fixer and parses its report.
// label is used for the parent-session subagent card; when empty the
// prompt's first 240 characters are used.
func (d Dispatcher) Implement(ctx context.Context, role agent.AgentRole, label, prompt string) (ImplementerReport, error) {
	out, err := d.run(ctx, role, swarm.ScopeFull, label, prompt)
	if err != nil {
		return ImplementerReport{}, err
	}
	rep, err := ParseImplementerReport(out)
	if err != nil {
		return ImplementerReport{Raw: out}, fmt.Errorf("%w (output was: %q)", err, truncateForError(out))
	}
	return rep, nil
}

// Review dispatches a reviewer and parses its verdicts.
// label is used for the parent-session subagent card; when empty the
// prompt's first 240 characters are used.
func (d Dispatcher) Review(ctx context.Context, role agent.AgentRole, label, prompt string) (ReviewReport, error) {
	out, err := d.run(ctx, role, swarm.ScopeArtifactWriter, label, prompt)
	if err != nil {
		return ReviewReport{}, err
	}
	rev, err := ParseReviewReport(out)
	if err != nil {
		return ReviewReport{Raw: out}, fmt.Errorf("%w (output was: %q)", err, truncateForError(out))
	}
	return rev, nil
}

// truncateForError keeps an unparseable report readable in an error string.
func truncateForError(s string) string {
	const max = 240
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// pipelineScope maps a swarm registry scope to the pipeline's own scope.
// The pipeline only distinguishes full, read-only, and artifact-writer; the
// swarm tester scope is treated as full so the child registry keeps its
// command tools.
func pipelineScope(scope swarm.RegistryScope) RegistryScope {
	if scope == swarm.ScopeReadOnly {
		return ScopeReadOnly
	}
	if scope == swarm.ScopeArtifactWriter {
		return ScopeArtifactWriter
	}
	return ScopeFull
}
