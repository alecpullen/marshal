package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
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

// WithoutNetwork returns a new Registry containing src's tools minus every
// RiskNetwork tool. Unattended children get this view: the SDD pipeline's
// role runners are built with childSession: true and therefore evaluate
// under an auto-approving policy clone, so a network tool's Confirm would be
// silently downgraded to Allow with no user watching. It also keeps
// web.fetch — an in-process HTTP client — from bypassing the shell sandbox's
// network isolation entirely.
func WithoutNetwork(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		if tool.Risk == RiskNetwork {
			continue
		}
		_ = view.Register(tool)
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
// tools plus sdd.* tools plus agent.run, dropping the file-write tools
// (file.write_patch, file.write, file.edit) and the command tools
// (shell.run, test.run). The SDD orchestrator must never edit source
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
	// Preparation must strip the same override execution does, or the tester's
	// evaluated command would differ from the one it actually runs. The facts
	// are copied so a consumer cannot mutate the value the next call sees.
	if prepare := tool.PrepareCommand; prepare != nil {
		testerTool.PrepareCommand = func(ctx context.Context, call ToolCall) (CommandFacts, error) {
			call.Args = []byte(`{}`)
			facts, err := prepare(ctx, call)
			if err != nil {
				return CommandFacts{}, err
			}
			return cloneCommandFacts(facts), nil
		}
	}
	testerTool.Internal = false
	return testerTool
}

// FallbackWriterView returns a new Registry containing src's tools with
// the file-write tools (file.write_patch, file.write, file.edit) narrowed
// to exactly the listed allowed paths (or their descendants). The
// marshal.agent fallback agent's shell channel
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
			_ = view.Register(fallbackWriterPatchTool(tool, allowed, allowedSet))
		case tool.Name == "file.write" || tool.Name == "file.edit":
			_ = view.Register(pathScopedTool(tool, func(ctx context.Context, cleaned, raw string) error {
				if scopedTargetOutside(ctx, tool, raw, allowed, false, pathInAllowlist(cleaned, allowedSet)) {
					return fmt.Errorf("%s in fallback scope may only write under the declared scope (path %q is outside)", tool.Name, raw)
				}
				return nil
			}))
		case tool.Risk == RiskNetwork:
			// Network egress: the fallback child runs unattended under an
			// auto-approving policy, so it must not reach the network. Keyed
			// on the risk level rather than the tool name so a future
			// network-capable tool cannot slip through the default case now
			// that web tools are enabled by default.
		default:
			_ = view.Register(tool)
		}
	}
	return view
}

// pathScopedTool narrows a path-addressed write tool (file.write, file.edit)
// to paths check accepts. check receives the cleaned path and returns a
// non-nil error to reject it. Without this the whole-file write tools would
// bypass a scoped view's path guard entirely.
func pathScopedTool(tool Tool, check func(ctx context.Context, cleaned, raw string) error) Tool {
	original := tool.Handler
	filtered := tool
	filtered.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("%s could not parse arguments: %w", tool.Name, err)
		}
		if err := check(ctx, cleanScopePath(args.Path), args.Path); err != nil {
			return ToolResult{}, err
		}
		return original(ctx, call)
	}
	return filtered
}

// scopedTargetEscapes reports whether raw, resolved through the tool's OWN
// resolver, lands outside every declared scope entry. A scope entry is a
// declared alias ("@run") or a declared path; both are resolved by the tool so
// the comparison is between canonical targets, not between spellings.
//
// It returns judged=false when the tool carries no resolver or when no
// declared entry resolves under it. The caller then keeps its lexical rule,
// which is all a scope over a hand-built Tool can do. When the tool refuses
// the path itself, the tool's own handler reports it and the view adds
// nothing.
func scopedTargetEscapes(ctx context.Context, tool Tool, raw string, entries []string, exact bool) (escapes bool, judged bool) {
	if tool.ResolvePath == nil || raw == "" {
		return false, false
	}
	target, err := tool.ResolvePath(ctx, raw, true)
	if err != nil {
		return false, false
	}
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		resolved, rerr := tool.ResolvePath(ctx, entry, true)
		if rerr != nil {
			continue
		}
		roots = append(roots, resolved)
	}
	if len(roots) == 0 {
		return false, false
	}
	for _, root := range roots {
		if target == root {
			return false, true
		}
		if !exact && pathIsWithin(root, target) {
			return false, true
		}
	}
	return true, true
}

// pathIsWithin reports whether the already canonical target lies inside the
// already canonical root.
func pathIsWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// scopedTargetOutside reports whether a scoped view must reject raw. When the
// tool carries its own resolver, the resolver decides: the request and every
// declared scope entry are canonicalized through it, so an absolute spelling
// of an in-scope target is accepted while a symlink pivot, an alias traversal
// ("@alias/../app.go") or a traversal out of an allowlist that the tool would
// otherwise permit is refused. A hand-built Tool without a resolver — every
// scope over a tool no native registration produced — keeps the lexical rule.
func scopedTargetOutside(ctx context.Context, tool Tool, raw string, entries []string, exact bool, lexical bool) bool {
	if escapes, judged := scopedTargetEscapes(ctx, tool, raw, entries, exact); judged {
		return escapes
	}
	return !lexical
}

func fallbackWriterPatchTool(tool Tool, allowed []string, allowedSet map[string]bool) Tool {
	original := tool.Handler
	filtered := tool
	filtered.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var args struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			slog.Warn("failed to parse file.write_patch args", "tool", "file.write_patch", "error", err)
		}
		res, err := patch.ParseRepairing(args.Patch)
		if err != nil || len(res.Patches) == 0 {
			return ToolResult{}, fmt.Errorf("file.write_patch in fallback scope requires a non-empty patch inside the declared allowlist")
		}
		for _, fp := range res.Patches {
			cleaned := cleanScopePath(fp.Path)
			if scopedTargetOutside(ctx, tool, fp.Path, allowed, false, pathInAllowlist(cleaned, allowedSet)) {
				return ToolResult{}, fmt.Errorf("file.write_patch in fallback scope may only write under the declared scope (path %q is outside)", fp.Path)
			}
		}
		return original(ctx, call)
	}
	return filtered
}

// allowedEntries lists an exact-match allowlist's entries, so the tool's own
// resolver can canonicalize each one for the scope comparison.
func allowedEntries(allowed map[string]bool) []string {
	entries := make([]string, 0, len(allowed))
	for entry := range allowed {
		entries = append(entries, entry)
	}
	sort.Strings(entries)
	return entries
}

// pathInAllowlist reports whether path equals one of the allowed entries
// or sits beneath one as a descendant. Empty allowlist denies everything.
// Both path and allowed entries are expected to be clean (no ".." segments).
func pathInAllowlist(path string, allowed map[string]bool) bool {
	if len(allowed) == 0 {
		return false
	}
	// Clean the path to normalize "//" → "/" (handles root="/" edge case).
	cleaned := filepath.Clean(path)
	if allowed[cleaned] {
		return true
	}
	for root := range allowed {
		// Preserve the separator boundary: root "/" must match "/foo" but
		// not a bare sibling prefix. Trimming the trailing slash then
		// re-appending "/" keeps the descendant check exact while turning
		// the root="/" prefix "//" into "/".
		root = strings.TrimSuffix(root, "/")
		if cleaned == root {
			return true
		}
		if strings.HasPrefix(cleaned, root+"/") {
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
		case tool.Name == "file.write" || tool.Name == "file.edit":
			_ = view.Register(pathScopedTool(tool, func(ctx context.Context, cleaned, raw string) error {
				lexical := strings.HasPrefix(cleaned, artifactAlias+"/")
				if scopedTargetOutside(ctx, tool, raw, []string{artifactAlias}, false, lexical) {
					return fmt.Errorf("%s in artifact-writer scope may only write under %s/ (path %q is outside)", tool.Name, artifactAlias, raw)
				}
				return nil
			}))
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
			lexical := strings.HasPrefix(cleaned, alias+"/")
			if scopedTargetOutside(ctx, tool, fp.Path, []string{alias}, false, lexical) {
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
			case "agent.run", "question.ask", "mode.request":
				// Nested-agent, question, and mode tools are excluded: the
				// orphaned child must not spawn agents or ask the user.
			default:
				_ = view.Register(tool)
			}
		case tool.Name == "file.write_patch":
			_ = view.Register(planWriterPatchTool(tool, alias, allowedSet))
		case tool.Name == "file.write" || tool.Name == "file.edit":
			_ = view.Register(pathScopedTool(tool, func(ctx context.Context, cleaned, raw string) error {
				if scopedTargetOutside(ctx, tool, raw, allowed, true, allowedSet[cleaned]) {
					return fmt.Errorf("%s in plan-writer scope may only write the candidate plan (path %q is not allowed)", tool.Name, raw)
				}
				return nil
			}))
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
			if scopedTargetOutside(ctx, tool, fp.Path, allowedEntries(allowed), true, allowed[cleaned]) {
				return ToolResult{}, fmt.Errorf("file.write_patch in plan-writer scope may only write the candidate plan (path %q is not allowed)", fp.Path)
			}
		}
		return original(ctx, call)
	}
	return filtered
}
