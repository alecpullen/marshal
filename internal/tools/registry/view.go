package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
		// prefix. Parse the unified diff's file headers rather than
		// substring-matching the whole blob, which a source patch could
		// trivially satisfy by containing the alias text anywhere.
		paths := patchTargetPaths(args.Patch)
		if len(paths) == 0 {
			return ToolResult{}, fmt.Errorf("file.write_patch in artifact-writer scope may only write under %s/", alias)
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, alias+"/") {
				return ToolResult{}, fmt.Errorf("file.write_patch in artifact-writer scope may only write under %s/ (path %q is outside)", alias, p)
			}
		}
		return original(ctx, call)
	}
	return filtered
}

// patchTargetPaths extracts the target (b-side) file paths from a unified
// diff. It reads `diff --git a/<path> b/<path>` and `+++ b/<path>` headers.
func patchTargetPaths(patch string) []string {
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// diff --git a/<a> b/<b>
			fields := strings.Fields(line)
			if len(fields) >= 4 && strings.HasPrefix(fields[3], "b/") {
				paths = append(paths, strings.TrimPrefix(fields[3], "b/"))
			}
		case strings.HasPrefix(line, "+++ "):
			// +++ b/<path>  (or +++ /dev/null for new files)
			rest := strings.TrimPrefix(line, "+++ ")
			if strings.HasPrefix(rest, "b/") {
				paths = append(paths, strings.TrimPrefix(rest, "b/"))
			}
		}
	}
	return paths
}
