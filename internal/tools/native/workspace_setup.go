package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// maxHookWarningChars caps the output tail quoted in a hook-failure warning.
const maxHookWarningChars = 500

// RunSetupPlan executes plan.Hooks inside the workspace the bound registry's
// shell.run preparation names, in order.
//
// A setup hook is a command like any other, so it is prepared, evaluated and
// launched exactly as shell.run would be: the hook's own prepared cwd and
// captured environment are what policy judges and what the process receives,
// and only an Allow (or an exact approval that is already valid) admits the
// launch. A Deny, a missing capability or a command that needs confirmation
// this path cannot obtain is returned as a refusal and launches nothing — this
// function never fabricates the approval it does not have.
//
// A hook's timeout_seconds keeps its meaning as an execution deadline: a hook
// still running when it expires is stopped and joined here, instead of being
// handed to the background the way an ordinary foreground wait hands off a long
// command. A failed hook is a warning and the plan continues; a refusal stops
// the plan because every later hook may depend on the one that did not run.
func RunSetupPlan(ctx context.Context, plan worktree.SetupPlan, reg *registry.Registry, authorizer *policy.Authorizer, jobs *JobManager) ([]string, error) {
	if len(plan.Hooks) == 0 {
		return nil, nil
	}
	if reg == nil {
		return nil, fmt.Errorf("worktree setup hooks are unavailable: no tool registry is bound")
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok || tool.PrepareCommand == nil {
		return nil, fmt.Errorf("worktree setup hooks are unavailable: this registry cannot prepare shell.run")
	}
	if authorizer == nil {
		return nil, fmt.Errorf("worktree setup hooks require a policy authorizer")
	}
	if jobs == nil {
		return nil, fmt.Errorf("worktree setup hooks require the conversation's command service")
	}

	var warnings []string
	for index, hook := range plan.Hooks {
		command := strings.TrimSpace(hook.Command)
		if command == "" {
			continue
		}
		invocationID := fmt.Sprintf("setup-hook-%d", index+1)
		args, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			return warnings, fmt.Errorf("setup hook %q: %w", command, err)
		}
		facts, err := tool.PrepareCommand(ctx, registry.ToolCall{ID: invocationID, Name: "shell.run", Args: args})
		if err != nil {
			return warnings, fmt.Errorf("setup hook %q cannot be prepared: %w", command, err)
		}
		if strings.TrimSpace(facts.Command) != command {
			return warnings, fmt.Errorf("setup hook %q prepared different bytes (%q)", command, facts.Command)
		}
		attempt := policy.CommandAttempt{InvocationID: invocationID, ToolName: "shell.run", Input: facts}
		proof, evaluation, err := authorizer.Evaluate(ctx, policy.Call{
			InvocationID: invocationID,
			ToolName:     "shell.run",
			Shell:        &attempt.Input,
		})
		if err != nil {
			return warnings, fmt.Errorf("setup hook %q: %w", command, err)
		}
		if evaluation.Decision != policy.DecisionAllow {
			return warnings, fmt.Errorf("setup hook %q was not run: %s", command, setupRefusalReason(evaluation))
		}

		timeout := clampTimeout(hook.TimeoutSeconds, defaultShellTimeout, maxShellTimeout)
		info, err := jobs.Launch(ctx, LaunchSpec{
			Command:     facts.Command,
			Dir:         facts.Dir,
			Environment: facts.Environment,
			Wait:        timeout,
			Admit: func(admitCtx context.Context) (*policy.Admission, error) {
				return authorizer.BeginCommand(policy.WithAuthorization(admitCtx, proof), attempt)
			},
		})
		if err != nil {
			return warnings, fmt.Errorf("setup hook %q could not start: %w", command, err)
		}
		res, err := jobs.WaitForeground(ctx, info.ID, timeout)
		if err != nil {
			return warnings, fmt.Errorf("setup hook %q: %w", command, err)
		}
		if !res.Terminal {
			// The hook's own deadline, not an ordinary foreground wait: stop and
			// join THIS hook so setup cannot leave a command running that the
			// plan believes it finished.
			if killErr := jobs.Kill(info.ID); killErr != nil {
				return warnings, fmt.Errorf("setup hook %q exceeded its %s deadline and could not be stopped: %w", command, timeout, killErr)
			}
			warnings = append(warnings, fmt.Sprintf("setup hook %q exceeded its %s deadline and was stopped", command, timeout))
			continue
		}
		if warning, failed := setupHookFailure(command, res); failed {
			warnings = append(warnings, warning)
		}
	}
	return warnings, nil
}

// setupHookFailure renders a failed hook as a warning, quoting the output tail
// so a long build log does not swamp the transcript.
func setupHookFailure(command string, res ForegroundResult) (string, bool) {
	if res.RunError == nil && res.Info.ExitCode != nil && *res.Info.ExitCode == 0 {
		return "", false
	}
	output := res.Result.Stderr
	if output == "" {
		output = res.Result.Stdout
	}
	if output == "" {
		output = res.Output
	}
	output = strings.TrimSpace(output)
	if res.RunError != nil {
		if output == "" {
			output = res.RunError.Error()
		} else {
			output = output + ": " + res.RunError.Error()
		}
	}
	if len(output) > maxHookWarningChars {
		output = output[len(output)-maxHookWarningChars:]
	}
	return fmt.Sprintf("setup hook %q failed: %s", command, output), true
}

// setupRefusalReason renders the reasons one hook was refused for, so the
// refusal says which capability or confirmation setup cannot provide.
func setupRefusalReason(evaluation policy.Evaluation) string {
	parts := make([]string, 0, len(evaluation.Reasons))
	for _, reason := range evaluation.Reasons {
		message := strings.TrimSpace(reason.Message)
		if message == "" {
			message = string(reason.Code)
		}
		if message != "" {
			parts = append(parts, message)
		}
	}
	if len(parts) == 0 {
		return "it requires approval that worktree setup cannot obtain"
	}
	return strings.Join(parts, "; ")
}

// runSetupHooks executes a fresh worktree's pending hooks through the toolset's
// bound registry, its setup authorizer and the conversation's command service.
//
// Every failure is returned as a warning, never an error: a failed hook leaves
// the worktree usable, and interactive setup must never block isolation. The
// directory comes from the prepared facts — the hook runs in the workspace its
// approval bound — because the session was already moved into the worktree this
// plan was created for.
func (t *toolSet) runSetupHooks(ctx context.Context, dir string, plan worktree.SetupPlan) []string {
	if len(plan.Hooks) == 0 {
		return nil
	}
	// Every hook runs in the fresh worktree, so the plan as a whole holds one
	// shared root-use lease for it: a lifecycle mutation at that root refuses
	// while setup is still establishing the tree, and the lease is released when
	// the last hook has settled. A registration with no lease callback (a test
	// or non-git fixture) acquires nothing.
	release, err := t.holdRootUse(ctx, dir, worktree.UseKindTool)
	if err != nil {
		return []string{"setup hooks: " + err.Error()}
	}
	defer release()
	warnings, err := RunSetupPlan(ctx, plan, t.registry, t.setupAuthorizer, t.jobManager)
	if err != nil {
		warnings = append(warnings, "setup hooks: "+err.Error())
	}
	return warnings
}
