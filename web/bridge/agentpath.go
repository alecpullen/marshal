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
// Its sanctioned constructor is (*agentRuntime).agentPath. A raw
// AgentPath("...") conversion is allowed for paths that were never the
// bridge's (e.g. the non-fleet path, or a repo-relative diff path), but
// using it for a bridge path is a review flag, not a build error. The
// type turns a missed call site that passes a typed sessionParams into
// a build failure; map[string]any call sites are not protected and must
// be checked by review.
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

// bridgePath converts a path as the agent sees it back into the bridge's
// view — the inverse of agentPath. A non-containerized agent shares the
// bridge's filesystem, so the translation is the identity. A containerized
// agent sees its workspace at containerWorkDir; a path under that is
// mapped back onto rt.root. A path outside the agent's workspace is
// returned unchanged: it may be an error message rather than a path.
func (rt *agentRuntime) bridgePath(agentPath string) (string, error) {
	if !rt.containerized {
		return agentPath, nil
	}
	if agentPath == containerWorkDir {
		return rt.root, nil
	}
	if strings.HasPrefix(agentPath, containerWorkDir+"/") {
		return filepath.Join(rt.root, strings.TrimPrefix(agentPath, containerWorkDir+"/")), nil
	}
	return agentPath, nil
}
