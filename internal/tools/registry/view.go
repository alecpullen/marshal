package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"marshal/internal/tools/patch"
)

// ReadOnlyView returns a new Registry containing only src's read-only
// tools. Swarm roles that must not modify the workspace (planner, repo
// scout, reviewer) are given this view: write tools disappear from their
// system-prompt tool list and Lookup fails for them, so read-only access
// is enforced structurally rather than by prompt instructions.
func ReadOnlyView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		if tool.Risk == RiskReadOnly {
			// Tools were valid when registered with src; re-registering
			// the same Tool value cannot fail.
			_ = view.Register(tool)
		}
	}
	return view
}

// TesterView returns a new Registry containing src's read-only tools plus a
// constrained test.run. The swarm tester needs to inspect and run the
// configured test command, but must not get arbitrary shell execution or
// write/network tools. This keeps "does not modify source" structural rather
// than prompt-only.
func TesterView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		switch {
		case tool.Risk == RiskReadOnly:
			_ = view.Register(tool)
		case tool.Name == "test.run":
			_ = view.Register(testerTestRunTool(tool))
		}
	}
	return view
}

// OrchestratorView returns a new Registry containing src's read-only
// tools plus sdd.* tools plus agent.run, but dropping file.write_patch,
// shell.run, and test.run. The SDD orchestrator must never edit source
// or run arbitrary shell — it writes only via sdd.* tools to .marshal/sdd/
// and dispatches workers via agent.run. This is the preventive edit
// guard (spec §8, UNKNOWN 4): structural enforcement, not prompt-only.
func OrchestratorView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		switch {
		case tool.Risk == RiskReadOnly:
			_ = view.Register(tool)
		case tool.Risk == RiskWorkspaceWrite && strings.HasPrefix(tool.Name, "sdd."):
			_ = view.Register(tool)
		}
	}
	return view
}

func testerTestRunTool(tool Tool) Tool {
	testerTool := tool
	testerTool.Description = "Run the configured test command in the workspace."
	testerTool.Schema = []byte(`{"type":"object","properties":{}}`)
	original := tool.Handler
	testerTool.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		call.Args = []byte(`{}`)
		return original(ctx, call)
	}
	return testerTool
}

// FallbackWriterView returns a new Registry containing src's tools with
// file.write_patch narrowed to exactly the listed allowed paths (or
// their descendants). The marshal.agent fallback agent's shell channel
// remains available because command execution is governed by
// sandbox/policy at a higher layer; this view protects only the
// file-write surface. Allowed paths are interpreted as directory
// prefixes: a patch is permitted when its target path equals an allowed
// entry or sits beneath one.
func FallbackWriterView(src *Registry, allowed []string) *Registry {
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	view := New()
	for _, tool := range src.List() {
		switch {
		case tool.Deferred:
			// Deferred tools are never exposed to the fallback child.
		case tool.Name == "file.write_patch":
			_ = view.Register(fallbackWriterPatchTool(tool, allowedSet))
		default:
			_ = view.Register(tool)
		}
	}
	return view
}

func fallbackWriterPatchTool(tool Tool, allowed map[string]bool) Tool {
	original := tool.Handler
	filtered := tool
	filtered.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var args struct {
			Patch string `json:"patch"`
		}
		_ = json.Unmarshal(call.Args, &args)
		res, err := patch.ParseRepairing(args.Patch)
		if err != nil || len(res.Patches) == 0 {
			return ToolResult{}, fmt.Errorf("file.write_patch in fallback scope requires a non-empty patch inside the declared allowlist")
		}
		for _, fp := range res.Patches {
			cleaned := cleanScopePath(fp.Path)
			if !pathInAllowlist(cleaned, allowed) {
				return ToolResult{}, fmt.Errorf("file.write_patch in fallback scope may only write under the declared scope (path %q is outside)", fp.Path)
			}
		}
		return original(ctx, call)
	}
	return filtered
}

// pathInAllowlist reports whether path equals one of the allowed entries
// or sits beneath one as a descendant. Empty allowlist denies everything.
// Both path and allowed entries are expected to be clean (no ".." segments).
func pathInAllowlist(path string, allowed map[string]bool) bool {
	if len(allowed) == 0 {
		return false
	}
	if allowed[path] {
		return true
	}
	for root := range allowed {
		if strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// cleanScopePath returns a cleaned, slash-separated path suitable for
// scope checks. It collapses ".." segments and strips leading "./" so
// that traversal attempts such as "internal/foo/../bar" cannot bypass a
// prefix check. Paths that escape the repository (e.g. "../outside") are
// returned as-is and will then fail the allowlist check.
func cleanScopePath(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "./")
	return path
}

// ArtifactWriterView returns a new Registry containing src's read-only
// tools plus file.write_patch restricted to paths under the named artifact
// root. Reviewers need to write their verdict under @run but must not
// modify source or run shell commands.
func ArtifactWriterView(src *Registry, artifactAlias string) *Registry {
	view := New()
	for _, tool := range src.List() {
		switch {
		case tool.Risk == RiskReadOnly:
			_ = view.Register(tool)
		case tool.Name == "file.write_patch":
			_ = view.Register(artifactWriterPatchTool(tool, artifactAlias))
		}
	}
	return view
}

func artifactWriterPatchTool(tool Tool, alias string) Tool {
	original := tool.Handler
	filtered := tool
	filtered.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var args struct {
			Patch string `json:"patch"`
		}
		_ = json.Unmarshal(call.Args, &args)
		// Only allow patches whose target paths all start with the alias
		// prefix. Parse the real file.write_patch format (File: <path>
		// headers) rather than substring-matching the whole blob, which a
		// source patch could trivially satisfy by containing the alias text
		// anywhere.
		res, err := patch.ParseRepairing(args.Patch)
		if err != nil || len(res.Patches) == 0 {
			return ToolResult{}, fmt.Errorf("file.write_patch in artifact-writer scope may only write under %s/", alias)
		}
		for _, fp := range res.Patches {
			cleaned := cleanScopePath(fp.Path)
			if !strings.HasPrefix(cleaned, alias+"/") {
				return ToolResult{}, fmt.Errorf("file.write_patch in artifact-writer scope may only write under %s/ (path %q is outside)", alias, fp.Path)
			}
		}
		return original(ctx, call)
	}
	return filtered
}

// PlanWriterView returns a new Registry for the SDD plan-authoring child. It
// retains read-only tools (except nested-agent, question, mode, and
// side-effectful native tools that could mutate state outside the
// candidate), plus file.write_patch restricted to exactly the allowed plan
// artifact paths. The child may inspect the repository and write one plan
// artifact, but must not modify source, run commands, or reach the
// network.
func PlanWriterView(src *Registry, alias string, allowed []string) *Registry {
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	view := New()
	for _, tool := range src.List() {
		switch {
		case tool.Deferred:
			// Deferred tools are never exposed to the authoring child.
		case tool.Risk == RiskReadOnly:
			switch tool.Name {
			case "agent.run", "question.ask", "ask_user", "mode.request":
				// Nested-agent, question, and mode tools are excluded: the
				// orphaned child must not spawn agents or ask the user.
			case "diagnostics.check", "recall_history":
				// Side-effectful despite the RiskReadOnly label:
				// diagnostics.check runs configured per-language shell
				// commands and recall_history dereferences the session DB
				// (panics when nil). Stripped so the orphan cannot run
				// arbitrary checkers or touch archived-turn storage.
			default:
				_ = view.Register(tool)
			}
		case tool.Name == "file.write_patch":
			_ = view.Register(planWriterPatchTool(tool, alias, allowedSet))
		}
	}
	return view
}

func planWriterPatchTool(tool Tool, alias string, allowed map[string]bool) Tool {
	original := tool.Handler
	filtered := tool
	filtered.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var args struct {
			Patch string `json:"patch"`
		}
		_ = json.Unmarshal(call.Args, &args)
		res, err := patch.ParseRepairing(args.Patch)
		if err != nil || len(res.Patches) == 0 {
			return ToolResult{}, fmt.Errorf("file.write_patch in plan-writer scope may only write the candidate plan under %s/", alias)
		}
		for _, fp := range res.Patches {
			cleaned := cleanScopePath(fp.Path)
			if !allowed[cleaned] {
				return ToolResult{}, fmt.Errorf("file.write_patch in plan-writer scope may only write the candidate plan (path %q is not allowed)", fp.Path)
			}
		}
		return original(ctx, call)
	}
	return filtered
}
