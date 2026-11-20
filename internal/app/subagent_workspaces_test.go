package app

import (
	"context"
	"testing"

	"marshal/internal/sandbox"
	"marshal/internal/tools/native"
)

// plainRunner is a command runner that states nothing about linked worktrees.
type plainRunner struct{}

func (plainRunner) Run(context.Context, native.CommandRequest) (native.CommandResult, error) {
	return native.CommandResult{}, nil
}

// TestIsolatedWorktreeSupportIsDetectedFromTheBackend pins where the isolated
// dispatch capability comes from: the execution backend's own statement, never a
// model argument. Restricted and passthrough execute on the host and can host a
// linked worktree; a runner that states nothing reports no support, because an
// absent capability is not a yes. The container backend's own refusal is pinned
// in internal/sandbox, whose fixture needs no running container.
func TestIsolatedWorktreeSupportIsDetectedFromTheBackend(t *testing.T) {
	restricted, err := sandbox.New(sandbox.Config{Backend: "restricted"}, nil)
	if err != nil {
		t.Fatalf("sandbox.New(restricted): %v", err)
	}
	if !native.SupportsLinkedWorktrees(restricted) {
		t.Fatal("restricted executes on the host and must permit a linked git worktree")
	}

	passthrough, err := sandbox.New(sandbox.Config{Backend: "passthrough", UnsafePassthrough: true}, nil)
	if err != nil {
		t.Fatalf("sandbox.New(passthrough): %v", err)
	}
	if !native.SupportsLinkedWorktrees(passthrough) {
		t.Fatal("passthrough executes on the host and must permit a linked git worktree")
	}

	// A runner that never states the capability supports nothing: isolated
	// dispatch must refuse rather than assume a host filesystem.
	if native.SupportsLinkedWorktrees(plainRunner{}) {
		t.Fatal("a runner without the capability was reported as supporting linked worktrees")
	}
}
