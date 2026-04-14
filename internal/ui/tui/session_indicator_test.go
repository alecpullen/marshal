package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

func TestRenderSessionIndicator_Uninitialized(t *testing.T) {
	// Create temp dir without .marshal
	tmpDir := t.TempDir()
	m := &model{
		repoRoot:  tmpDir,
		streaming: &strings.Builder{},
	}
	m.input = textarea.New()

	indicator := m.renderSessionIndicator()
	if !strings.Contains(indicator, "/init") {
		t.Errorf("expected 'run /init' suggestion, got: %s", indicator)
	}
}

func TestRenderSessionIndicator_Initialized(t *testing.T) {
	// Create temp dir with .marshal and session_context.md
	tmpDir := t.TempDir()
	marshalDir := filepath.Join(tmpDir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(marshalDir, "session_context.md")
	if err := os.WriteFile(contextPath, []byte("# Session"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &model{
		repoRoot:  tmpDir,
		sessionID: "abc12345",
		streaming: &strings.Builder{},
	}
	m.input = textarea.New()

	indicator := m.renderSessionIndicator()
	if !strings.Contains(indicator, "session") {
		t.Errorf("expected session info, got: %s", indicator)
	}
}

func TestRenderSessionIndicator_NoSessionID(t *testing.T) {
	// Create temp dir with .marshal and session_context.md but no sessionID
	tmpDir := t.TempDir()
	marshalDir := filepath.Join(tmpDir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(marshalDir, "session_context.md")
	if err := os.WriteFile(contextPath, []byte("# Session"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &model{
		repoRoot:  tmpDir,
		sessionID: "",
		streaming: &strings.Builder{},
	}
	m.input = textarea.New()

	indicator := m.renderSessionIndicator()
	if !strings.Contains(indicator, "initialized") {
		t.Errorf("expected 'initialized', got: %s", indicator)
	}
}

func TestRenderSessionIndicator_Partial(t *testing.T) {
	// Create temp dir with .marshal but no session_context.md
	tmpDir := t.TempDir()
	marshalDir := filepath.Join(tmpDir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create only sessions.db
	dbPath := filepath.Join(marshalDir, "sessions.db")
	if err := os.WriteFile(dbPath, []byte("fake db"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &model{
		repoRoot:  tmpDir,
		streaming: &strings.Builder{},
	}
	m.input = textarea.New()

	indicator := m.renderSessionIndicator()
	if !strings.Contains(indicator, "partial") {
		t.Errorf("expected 'partial', got: %s", indicator)
	}
}

func TestRenderSessionIndicator_EmptyRepoRoot(t *testing.T) {
	m := &model{
		repoRoot:  "",
		streaming: &strings.Builder{},
	}
	m.input = textarea.New()

	indicator := m.renderSessionIndicator()
	if indicator != "" {
		t.Errorf("expected empty indicator for empty repoRoot, got: %s", indicator)
	}
}
