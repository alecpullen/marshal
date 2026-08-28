package bridge

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrOutsideWorkspace reports a path with no representation in the
// agent's namespace.
var ErrOutsideWorkspace = errors.New("bridge: path is outside the agent's workspace")

// AgentPath is a path in the agent's namespace, not the bridge's.
//
// Its only constructor is (*agentRuntime).agentPath. That is deliberate:
// the previous approach — a convention of translating at each call site
// — was applied at exactly one of six sites, and the five misses cost a
// verification campaign to diagnose. Making the type unconstructable
// from a bare string turns a missed call site into a build failure.
//
// Paths that were never the bridge's are NOT this type. session/diff's
// path is repo-relative and comes from the agent's own diff output; it
// stays a plain string, and the absence of the type says so.
type AgentPath string

// agentPath converts a path as the bridge sees it into the agent's view.
//
// A non-containerized agent shares the bridge's filesystem, so the
// translation is the identity. A containerized agent sees its workspace
// at containerWorkDir regardless of where the bridge holds it.
func (rt *agentRuntime) agentPath(bridgePath string) (AgentPath, error) {
	if !rt.containerized {
		return AgentPath(bridgePath), nil
	}
	if rt.root == "" {
		return "", fmt.Errorf("bridge: agent %s has no workspace root", rt.id)
	}

	root := filepath.Clean(rt.root)
	clean := filepath.Clean(bridgePath)

	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is not under %s", ErrOutsideWorkspace, clean, root)
	}
	if clean == root {
		return AgentPath(containerWorkDir), nil
	}
	rel, err := filepath.Rel(root, clean)
	if err != nil {
		return "", err
	}
	return AgentPath(filepath.Join(containerWorkDir, rel)), nil
}
