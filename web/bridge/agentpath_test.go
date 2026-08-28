package bridge

import (
	"errors"
	"strings"
	"testing"
)

func rtFor(root string, containerized bool) *agentRuntime {
	return &agentRuntime{id: "a1", root: root, containerized: containerized}
}

func TestAgentPathTranslatesAContainerizedWorkspace(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	got, err := rt.agentPath("/state/work/a1/cmd/marshal/main.go")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/work/cmd/marshal/main.go") {
		t.Fatalf("got %q, want /work/cmd/marshal/main.go", got)
	}
}

func TestAgentPathMapsTheWorkspaceRootItself(t *testing.T) {
	rt := rtFor("/host-projects/marshal", true)
	got, err := rt.agentPath("/host-projects/marshal")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/work") {
		t.Fatalf("got %q, want /work", got)
	}
}

func TestAgentPathIsIdentityForAHostProcessAgent(t *testing.T) {
	rt := rtFor("/home/me/code", false)
	got, err := rt.agentPath("/home/me/code/pkg/x.go")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/home/me/code/pkg/x.go") {
		t.Fatalf("got %q, want the path unchanged", got)
	}
}

func TestAgentPathRefusesAPathOutsideTheWorkspace(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	_, err := rt.agentPath("/state/work/a2/secret")
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("got %v, want ErrOutsideWorkspace", err)
	}
	if !strings.Contains(err.Error(), "/state/work/a2/secret") ||
		!strings.Contains(err.Error(), "/state/work/a1") {
		t.Errorf("error names only one view: %v", err)
	}
}

func TestAgentPathRespectsSegmentBoundaries(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	if _, err := rt.agentPath("/state/work/a1-evil/x"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("matched a sibling sharing a prefix: %v", err)
	}
}

func TestAgentPathRefusesWhenTheWorkspaceRootIsUnset(t *testing.T) {
	rt := rtFor("", true)
	if _, err := rt.agentPath("/anything"); err == nil {
		t.Fatal("a containerized runtime with no workspace root accepted a path")
	}
}
