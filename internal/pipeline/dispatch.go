package pipeline

import (
	"context"
	"fmt"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/llm/schema"
)

// Dispatcher builds and runs one subagent per call. Every subagent is
// fresh: it inherits no conversation history, only the prompt it is given.
type Dispatcher struct {
	Factory swarm.RunnerFactory
	// OnTokens, when non-nil, receives each subagent's total token usage.
	OnTokens func(int)

	// exec is the seam tests replace. When nil, runExec is used.
	exec func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error)
}

func (d Dispatcher) run(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
	if d.exec != nil {
		return d.exec(ctx, role, scope, prompt)
	}
	return d.runExec(ctx, role, scope, prompt)
}

// runExec builds a role-bound runner and runs one task on it.
func (d Dispatcher) runExec(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
	if d.Factory == nil {
		return "", fmt.Errorf("pipeline dispatch: no runner factory")
	}
	runner, err := d.Factory(role, scope)
	if err != nil {
		return "", fmt.Errorf("pipeline dispatch: build %s runner: %w", role, err)
	}
	if d.OnTokens != nil {
		runner.UsageObserver = func(u schema.TokenUsage) { d.OnTokens(u.TotalTokens) }
	}
	task, err := runner.RunTask(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("pipeline dispatch: %s run: %w", role, err)
	}
	if task == nil {
		return "", fmt.Errorf("pipeline dispatch: %s returned no task", role)
	}
	return task.Summary, nil
}

// Implement dispatches an implementer or fixer and parses its report.
func (d Dispatcher) Implement(ctx context.Context, role agent.AgentRole, prompt string) (ImplementerReport, error) {
	out, err := d.run(ctx, role, swarm.ScopeFull, prompt)
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
func (d Dispatcher) Review(ctx context.Context, role agent.AgentRole, prompt string) (ReviewReport, error) {
	out, err := d.run(ctx, role, swarm.ScopeReadOnly, prompt)
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
