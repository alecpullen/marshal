package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestEffectiveAdditionalRootsInjectsProjectRootInWorktree(t *testing.T) {
	projectRoot := t.TempDir()
	worktreePath := t.TempDir()

	// Create a file in the project root that is NOT in the worktree.
	docPath := filepath.Join(projectRoot, "docs", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(docPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("architecture"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	st := session.New(config.Config{}, projectRoot, time.Now(), session.Persistence{})
	st.SetWorkspace(session.Workspace{
		ProjectRoot: projectRoot,
		ActiveRoot:  worktreePath,
		Branch:      "feat-x",
	})

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: worktreePath,
		CommandRunner: &fakeRunner{},
		Guardrail:     func(string) error { return nil },
		SessionState:  st,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Read a file from the project root using a path relative to the worktree.
	// The path ../<basename-of-projectRoot>/docs/architecture.md should resolve.
	result, err := invokeTool(t, reg, "file.read", `{"path":"../`+filepath.Base(projectRoot)+`/docs/architecture.md"}`)
	if err != nil {
		t.Fatalf("file.read from project root while in worktree: %v", err)
	}
	if !strings.Contains(result.Content, "architecture") {
		t.Fatalf("Content should contain the file body, got: %q", result.Content)
	}
}

func TestEffectiveAdditionalRootsNoInjectionWhenNotInWorktree(t *testing.T) {
	projectRoot := t.TempDir()
	outside := t.TempDir()

	// A file outside the project root.
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	st := session.New(config.Config{}, projectRoot, time.Now(), session.Persistence{})
	// No SetWorkspace: ActiveRoot is empty, so no injection.

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: projectRoot,
		CommandRunner: &fakeRunner{},
		Guardrail:     func(string) error { return nil },
		SessionState:  st,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Reading a file outside the root must still fail.
	_, err := invokeTool(t, reg, "file.read", `{"path":"../`+filepath.Base(outside)+`/secret.txt"}`)
	if err == nil {
		t.Fatal("reading outside the project root without a worktree must fail")
	}
}
