package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/subagentwork"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// The parent-only lifecycle tool names. A child never sees either: the subtask
// scope view excludes them, because integration and disposal are the parent's
// decisions about work it owns.
const (
	subagentIntegrateTool = "agent.integrate"
	subagentDiscardTool   = "agent.discard"
)

// lifecycleArgumentSchema is the exact argument surface both tools accept. It
// deliberately contains no path, branch or owner: the mutation target comes from
// the durable record, so neither a model nor a hook can steer it at another
// checkout.
const lifecycleArgumentSchema = `{"type":"object","properties":{` +
	`"task_key":{"type":"string","description":"The stable task key agent.run returned for the owned child."},` +
	`"expected_head":{"type":"string","description":"The exact candidate commit the parent inspected and reviewed."}` +
	`},"required":["task_key","expected_head"],"additionalProperties":false}`

// lifecycleArgs is the decoded form of that schema.
type lifecycleArgs struct {
	TaskKey      string `json:"task_key"`
	ExpectedHead string `json:"expected_head"`
}

// lifecycleTool is the implementation both parent-only tools share: the same
// argument binding, the same admission shape and the same receipt, differing
// only in the service call and the risk the policy engine judges.
type lifecycleTool struct {
	name   string
	owner  string
	svc    *subagentwork.Service
	deps   subagentwork.MutationDeps
	auth   *policy.Authorizer
	mutate func(context.Context, subagentwork.Mutation, subagentwork.MutationDeps) (subagentwork.Record, error)
}

// NewSubagentIntegrateTool builds the parent-only tool that integrates one owned
// child's exact candidate into the checkout the record captured.
//
// The tool declares WorkspaceEffectManaged and OwnsAdmission: dispatch attaches
// the evaluated proof to the handler's context and does not spend it, and the
// handler admits the exact effect at the mutation boundary — the one place the
// record's source and target facts are known and locked. Its risks are ordinary
// ones, so the existing approval rules, the explicit-deny rules and the
// conversation end gate all still apply.
func NewSubagentIntegrateTool(owner string, svc *subagentwork.Service, deps subagentwork.MutationDeps, authorizer *policy.Authorizer) registry.Tool {
	return newSubagentLifecycleTool(subagentIntegrateTool, registry.RiskWorkspaceWrite, owner, svc, deps, authorizer,
		func(ctx context.Context, m subagentwork.Mutation, d subagentwork.MutationDeps) (subagentwork.Record, error) {
			return svc.Integrate(ctx, m, d)
		})
}

// NewSubagentDiscardTool builds the parent-only tool that discards one owned
// child the parent deliberately abandons. It is destructive, so the existing
// destructive-operation approval rules decide it.
func NewSubagentDiscardTool(owner string, svc *subagentwork.Service, deps subagentwork.MutationDeps, authorizer *policy.Authorizer) registry.Tool {
	return newSubagentLifecycleTool(subagentDiscardTool, registry.RiskDestructive, owner, svc, deps, authorizer,
		func(ctx context.Context, m subagentwork.Mutation, d subagentwork.MutationDeps) (subagentwork.Record, error) {
			return svc.Discard(ctx, m, d)
		})
}

// newSubagentLifecycleTool assembles one parent-only tool. Its schema names no
// free path, its preparation reads the record, and its handler builds the
// admission from the proof dispatch attached.
func newSubagentLifecycleTool(name string, risk registry.RiskLevel, owner string, svc *subagentwork.Service, deps subagentwork.MutationDeps, authorizer *policy.Authorizer, mutate func(context.Context, subagentwork.Mutation, subagentwork.MutationDeps) (subagentwork.Record, error)) registry.Tool {
	impl := &lifecycleTool{
		name:   name,
		owner:  owner,
		svc:    svc,
		deps:   deps,
		auth:   authorizer,
		mutate: mutate,
	}
	tool := registry.Tool{
		Name:            name,
		Description:     lifecycleDescription(name),
		Schema:          json.RawMessage(lifecycleArgumentSchema),
		Risk:            risk,
		WorkspaceEffect: registry.WorkspaceEffectManaged,
		OwnsAdmission:   true,
	}
	// Read-only preparation: this is what the policy engine binds before it
	// decides, and the service recomputes it under the lease at the mutation.
	tool.PrepareEffect = func(ctx context.Context, call registry.ToolCall) (string, error) {
		args, _, err := decodeLifecycleArgs(call.Args)
		if err != nil {
			return "", err
		}
		if svc == nil {
			return "", fmt.Errorf("%s: this session holds no owned-workspace service, so there is no candidate to act on", name)
		}
		prepared, err := svc.PrepareMutation(ctx, impl.mutation(args.TaskKey, args.ExpectedHead))
		if err != nil {
			return "", err
		}
		return prepared.EffectKey(), nil
	}
	tool.Handler = impl.handler
	return tool
}

// handler is the managed mutation. The proof dispatch attached is spent here —
// once, at the boundary the service reports — and never earlier.
func (t *lifecycleTool) handler(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
	args, argsMap, err := decodeLifecycleArgs(call.Args)
	if err != nil {
		return registry.ToolResult{}, err
	}
	mutation := t.mutation(args.TaskKey, args.ExpectedHead)
	if t.svc == nil {
		return lifecycleResult(t.name, mutation, subagentwork.Record{}),
			fmt.Errorf("%s: this session holds no owned-workspace service, so there is no candidate to act on", t.name)
	}
	if t.mutate == nil {
		return lifecycleResult(t.name, mutation, subagentwork.Record{}),
			fmt.Errorf("%s is not wired in this build", t.name)
	}

	// The per-call dependency set: everything dispatch prepared, plus the one
	// admission this call owns. The closure reads the opaque proof from the
	// handler's context, so the decision is the one made for this invocation.
	callDeps := t.deps
	callDeps.Admit = func(admitCtx context.Context, effectKey string) (subagentwork.Admission, error) {
		return t.admit(admitCtx, argsMap, effectKey)
	}

	rec, mutateErr := t.mutate(ctx, mutation, callDeps)
	result := lifecycleResult(t.name, mutation, rec)
	if mutateErr != nil {
		return result, mutateErr
	}
	return result, nil
}

// admit revalidates the attached proof against the live authority and the
// recomputed effect key, then hands back the lease the service spends. A
// refusal here is a refusal, never an implicit approval: a revoked mode, a
// changed rule set, a spent proof or a moved target all end the mutation.
func (t *lifecycleTool) admit(ctx context.Context, args map[string]any, effectKey string) (subagentwork.Admission, error) {
	if t.auth == nil {
		return nil, errors.New("no live policy authorizer is wired, so the mutation cannot be admitted")
	}
	admission, err := t.auth.BeginTool(ctx, policy.Call{
		InvocationID: policy.InvocationID(ctx),
		ToolName:     t.name,
		Args:         args,
		EffectKey:    effectKey,
	})
	if err != nil {
		return nil, err
	}
	if admission == nil {
		return nil, policy.ErrApprovalRequired
	}
	return admission, nil
}

// mutation is the request this call names: the owner is the tool's own, so a
// model cannot address another session's work.
func (t *lifecycleTool) mutation(taskKey, expectedHead string) subagentwork.Mutation {
	return subagentwork.Mutation{
		OwnerSessionID: t.owner,
		TaskKey:        taskKey,
		ExpectedHead:   expectedHead,
	}
}

// decodeLifecycleArgs decodes the argument object while reproducing exactly the
// map dispatch evaluated, so the admission at the mutation boundary fingerprints
// the same facts. Both required fields are checked here as well as by the
// schema: a handler reached directly (a test, a role view) must not mutate on an
// empty task key or an empty candidate.
func decodeLifecycleArgs(raw json.RawMessage) (lifecycleArgs, map[string]any, error) {
	argsMap, _, err := parseToolArgs(raw)
	if err != nil {
		return lifecycleArgs{}, nil, err
	}
	var args lifecycleArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return lifecycleArgs{}, nil, fmt.Errorf("arguments are not a valid JSON object")
		}
	}
	if strings.TrimSpace(args.TaskKey) == "" {
		return lifecycleArgs{}, nil, errors.New("task_key is required: name the owned child's stable task key")
	}
	if strings.TrimSpace(args.ExpectedHead) == "" {
		return lifecycleArgs{}, nil, errors.New("expected_head is required: name the exact candidate commit this task reviewed")
	}
	return args, argsMap, nil
}

// lifecycleResult renders one mutation's receipt. It always carries the task key
// and the candidate, so every outcome — an integrated merge, a refusal, a
// conflict, an owned cleanup that could not finish — can be paired with the work
// it belongs to without parsing prose. A receipt states only what the record
// holds: a capture that could not publish records no snapshot hash rather than
// implying rollback coverage.
func lifecycleResult(name string, m subagentwork.Mutation, rec subagentwork.Record) registry.ToolResult {
	var b strings.Builder
	fmt.Fprintf(&b, "task_key: %s\n", m.TaskKey)
	fmt.Fprintf(&b, "candidate: %s\n", m.ExpectedHead)
	state := ""
	if rec.Integration != nil {
		state = rec.Integration.State
	}
	if rec.Outcome != "" {
		fmt.Fprintf(&b, "execution: %s\n", rec.Outcome)
	}
	if state != "" {
		fmt.Fprintf(&b, "integration: %s\n", state)
	}
	if rec.Integration != nil {
		if commit := rec.Integration.ResultCommit; commit != "" {
			fmt.Fprintf(&b, "result_commit: %s\n", commit)
		}
		if hash := rec.Integration.SnapshotHash; hash != "" {
			fmt.Fprintf(&b, "snapshot: %s\n", hash)
		}
		if reason := strings.TrimSpace(rec.Integration.Reason); reason != "" {
			fmt.Fprintf(&b, "reason: %s\n", reason)
		}
	}
	if rec.CleanupState != "" {
		fmt.Fprintf(&b, "cleanup: %s\n", rec.CleanupState)
	}
	if rec.Target.Branch != "" {
		fmt.Fprintf(&b, "target_branch: %s\n", rec.Target.Branch)
	}
	return registry.ToolResult{Summary: lifecycleSummary(name, m.TaskKey, rec, state), Content: b.String()}
}

// lifecycleSummary is the one-line outcome a transcript row can show.
func lifecycleSummary(name, taskKey string, rec subagentwork.Record, state string) string {
	action := "integration"
	if name == subagentDiscardTool {
		action = "discard"
	}
	if state == "integrated" && rec.Integration != nil && rec.Integration.ResultCommit != "" {
		target := rec.Target.Branch
		if target == "" {
			target = "the recorded target"
		}
		return fmt.Sprintf("%s %s into %s at %s", action, taskKey, target, shortSHA(rec.Integration.ResultCommit))
	}
	return fmt.Sprintf("%s %s: %s", action, taskKey, state)
}

// shortSHA abbreviates a commit for a summary line; it never shortens what the
// receipt's Content reports.
func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// lifecycleDescription is the model-facing description of one lifecycle tool. It
// states the workflow the tool does not perform for the parent: inspecting the
// candidate and the required review happen before the call.
func lifecycleDescription(name string) string {
	switch name {
	case subagentIntegrateTool:
		return "Integrate one owned child's reviewed candidate into the checkout and branch it was dispatched for. Address it by the stable task key and the exact candidate commit already inspected and reviewed; the target comes from the recorded work, never from an argument. Review and any required checks happen before this call. A conflict aborts only this merge and keeps the child's branch for resolution."
	case subagentDiscardTool:
		return "Discard one owned child's work this parent deliberately abandons, addressed by the stable task key and its exact candidate commit. Refused while the child is busy, unknown, dirty, at another commit, or not provably owned; dirty work stays available for salvage."
	default:
		return name
	}
}
