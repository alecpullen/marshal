package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/agent"
	"marshal/internal/app/session"
	"marshal/internal/jsonextract"
	"marshal/internal/llm/schema"
)

// verifyProposalPrompt is the proposal subagent's goal, unchanged from the TUI.
const verifyProposalPrompt = "Inspect this repository and return exactly one JSON object with \"build\" and \"test\" fields: the shell commands (argv, no pipes) to compile and test it, or empty strings if a step does not apply. Only propose commands that will actually work."

// TurnRunner returns the runtime's current agent runner, or nil while the
// provider is not built. It never returns a typed nil.
func (rt *Runtime) TurnRunner() inproc.Runner {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Runner == nil {
		return nil
	}
	return rt.Runner
}

// RunReview runs a reviewer subagent over the runtime's current session.
func (rt *Runtime) RunReview(ctx context.Context, params acpwire.ReviewParams) error {
	rt.mu.Lock()
	state := rt.State
	factory := rt.CustomAgentFactory
	rt.mu.Unlock()
	if factory == nil {
		// Review refuses as Prompt does: a coded refusal the TUI can branch on.
		return &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "review: no agent runner"}
	}
	reviewRange := params.Range
	if reviewRange == "" && params.Base != "" {
		reviewRange = "base:" + params.Base
	}
	return runReviewSubagent(ctx, state, factory, params.Focus, params.Model, reviewRange)
}

// RunCustomAgent runs the named custom agent as a subagent of the runtime's
// current session.
func (rt *Runtime) RunCustomAgent(ctx context.Context, params acpwire.AgentRunParams) error {
	rt.mu.Lock()
	state := rt.State
	factory := rt.CustomAgentFactory
	rt.mu.Unlock()
	if factory == nil {
		return &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "run agent: no agent runner"}
	}
	return runCustomAgent(ctx, state, factory, params.Agent, params.Goal)
}

// runCustomAgent runs the named custom agent with goal through runSubagentTask.
func runCustomAgent(ctx context.Context, state *session.State, factory agent.SubagentRunnerFactory, name, goal string) error {
	return runSubagentTask(ctx, state, factory, agent.SubagentRequest{Agent: name}, "run agent", "agent · "+name, goal)
}

// SuggestNextReply asks the runner's model for the user's most likely next
// reply to lastMessage, in at most 10 words. "" means none.
func (rt *Runtime) SuggestNextReply(ctx context.Context, lastMessage string) (string, error) {
	rt.mu.Lock()
	runner := rt.Runner
	rt.mu.Unlock()
	if runner == nil {
		return "", nil
	}
	res, err := runner.Chat(ctx, []schema.ChatMessage{
		{Role: schema.RoleUser, Content: lastMessage},
		{Role: schema.RoleSystem, Content: "Suggest the user's most likely next reply in at most 10 words, or NONE."},
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(res.Content)
	if strings.EqualFold(text, "NONE") || text == "" {
		return "", nil
	}
	return text, nil
}

// ProposeVerifyCommands runs a one-shot subagent that proposes build and test
// commands, and parses its reply.
func (rt *Runtime) ProposeVerifyCommands(ctx context.Context) (acpwire.VerifyCommands, error) {
	rt.mu.Lock()
	factory := rt.CustomAgentFactory
	rt.mu.Unlock()
	if factory == nil {
		return acpwire.VerifyCommands{}, errors.New("no subagent factory wired")
	}
	return proposeVerifyCommands(ctx, factory)
}

// proposeVerifyCommands runs the proposal subagent and parses its own
// transcript.
func proposeVerifyCommands(ctx context.Context, factory agent.SubagentRunnerFactory) (acpwire.VerifyCommands, error) {
	child, childState, err := factory(agent.SubagentRequest{})
	if err != nil {
		return acpwire.VerifyCommands{}, fmt.Errorf("build proposal runner: %w", err)
	}
	if child == nil {
		return acpwire.VerifyCommands{}, errors.New("proposal runner is nil")
	}
	if _, err := child.RunTask(ctx, verifyProposalPrompt); err != nil {
		return acpwire.VerifyCommands{}, fmt.Errorf("proposal task failed: %w", err)
	}
	return verifyCommandsFrom(childState.Messages())
}

// verifyCommandsFrom returns the newest build and test object in msgs'
// assistant messages.
func verifyCommandsFrom(msgs []session.Message) (acpwire.VerifyCommands, error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != session.RoleAssistant {
			continue
		}
		raw := msgs[i].Content
		if raw == "" {
			continue
		}
		text, err := jsonextract.Extract(raw)
		if err != nil {
			continue
		}
		var p acpwire.VerifyCommands
		if json.Unmarshal([]byte(text), &p) != nil {
			continue
		}
		return p, nil
	}
	return acpwire.VerifyCommands{}, errors.New("proposal task returned no parseable JSON")
}

// bindTurnDeps points d's runner, its run calls and its pipeline calls at rt.
// Run uses it, and so do the tests that build Deps the way Run does.
func bindTurnDeps(d *inproc.Deps, rt *Runtime) {
	d.Runner = rt.TurnRunner
	d.Review = rt.RunReview
	d.RunAgent = rt.RunCustomAgent
	d.Suggest = rt.SuggestNextReply
	d.VerifyProposal = rt.ProposeVerifyCommands
	d.Swarm = rt.RunSwarm
	d.NewPlanRun = rt.NewPlanRun
	d.PlanPreflight = rt.PlanPreflight
	d.AuthorPlan = rt.AuthorPlan
}
