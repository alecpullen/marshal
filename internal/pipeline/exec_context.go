package pipeline

import (
	"path/filepath"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// ExecutionContext is the per-dispatch isolation context: where workers
// operate, where artifacts live, and how artifact paths are expressed in
// prompts. It is created by the controller after acquiring the worktree
// and passed to every dispatch.
type ExecutionContext struct {
	RepoRoot      string
	WorkspaceRoot string
	ArtifactRoot  string
	ArtifactAlias string
}

// ArtifactPath returns the filesystem path for an artifact relative to the
// artifact root.
func (c ExecutionContext) ArtifactPath(rel string) string {
	return filepath.Join(c.ArtifactRoot, rel)
}

// NamedRoots returns the alias-to-path map for native tool construction.
func (c ExecutionContext) NamedRoots() map[string]string {
	if c.ArtifactAlias == "" {
		return nil
	}
	return map[string]string{c.ArtifactAlias: c.ArtifactRoot}
}

// RegistryFactory builds a fresh tool registry bound to the given execution
// context, session scope, and the runner's own session state. Each dispatch
// creates its own registry so native tool handlers close over the correct
// workspace root and artifact aliases rather than the parent session's
// project root; stateful tools (todo.write, scratchpad.*) land on the
// runner's session, which is what the drill-down card renders.
type RegistryFactory func(ctx ExecutionContext, scope RegistryScope, state *session.State) (*registry.Registry, error)

// RegistryScope controls which tools are available to the built registry,
// mirroring swarm.RegistryScope for the pipeline's simpler read-only /
// full distinction.
type RegistryScope int

const (
	ScopeFull RegistryScope = iota
	ScopeReadOnly
	ScopeArtifactWriter // read-only source + artifact-only writes (reviewers)
	// ScopeFallback restricts the child registry's file-write tools to
	// the declared marshal.agent allowed files. The shell channel stays
	// enabled so the agent can run arbitrary commands (sandbox/policy
	// governs those at a higher layer), but file.write_patch is
	// narrowed to the scope so the fallback cannot modify unrelated
	// parts of the worktree.
	ScopeFallback
)
