package bridge

import (
	"context"
	"fmt"
	"strings"
)

// FormatPatch emits the agent's commits since base as a mailbox-format
// patch series.
//
// An empty base is refused rather than defaulted: format-patch with no
// range emits every commit in history, which is not what anyone wants
// and is confusing to receive.
func (g *gitRunner) FormatPatch(dir, base string) ([]byte, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("bridge: patch export requires a base ref")
	}
	out, err := g.run(dir, Credential{Kind: "none"},
		"format-patch", base+"..HEAD", "--stdout")
	if err != nil {
		return nil, fmt.Errorf("format patch: %w", err)
	}
	return out, nil
}

// Patch returns an agent's work as a patch series. It is the exit for a
// read-only agent, and an escape hatch for a writable one whose gate
// failure the operator does not want to override — work is never
// trapped.
func (f *Fleet) Patch(ctx context.Context, agentID string) ([]byte, error) {
	a, ok := f.ws.Agent(agentID)
	if !ok {
		return nil, fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	if a.SourceKind != "git" {
		return nil, fmt.Errorf("bridge: patch export applies to git-sourced agents only")
	}
	return f.git.FormatPatch(a.Project, a.TargetBranch)
}
