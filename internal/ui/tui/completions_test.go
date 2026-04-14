package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

func newCompletionTestModel(repoRoot string) *model {
	m := &model{
		repoRoot:  repoRoot,
		streaming: &strings.Builder{},
	}
	m.input = textarea.New()
	m.input.SetHeight(inputHeight)
	return m
}

func TestUpdateCommandCompletions(t *testing.T) {
	m := newCompletionTestModel(t.TempDir())
	m.input.SetValue("/sh")
	m.updateCompletions()

	if !m.hasCompletions() {
		t.Fatal("expected completions to be active")
	}

	if m.completionState.type_ != completionCommand {
		t.Errorf("expected completion type to be completionCommand, got %d", m.completionState.type_)
	}

	// Check that /ship is in suggestions
	found := false
	for _, s := range m.completionState.suggestions {
		if s == "/ship" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected /ship in suggestions, got %v", m.completionState.suggestions)
	}
}

func TestClearCompletions(t *testing.T) {
	m := newCompletionTestModel(t.TempDir())
	m.input.SetValue("/ship")
	m.updateCompletions()

	if !m.hasCompletions() {
		t.Fatal("expected completions to be active")
	}

	m.clearCompletions()

	if m.hasCompletions() {
		t.Error("expected completions to be cleared")
	}
}

func TestCompletionNavigation(t *testing.T) {
	m := newCompletionTestModel(t.TempDir())
	m.input.SetValue("/s")
	m.updateCompletions()

	if !m.hasCompletions() {
		t.Fatal("expected completions to be active")
	}

	initialIdx := m.completionState.selectedIdx

	// Test next
	m.selectNextCompletion()
	if m.completionState.selectedIdx == initialIdx && len(m.completionState.suggestions) > 1 {
		t.Error("expected selection to move to next")
	}

	// Test prev
	m.selectPrevCompletion()
	if m.completionState.selectedIdx != initialIdx {
		t.Error("expected selection to return to initial")
	}
}

func TestAcceptCompletion(t *testing.T) {
	m := newCompletionTestModel(t.TempDir())
	m.input.SetValue("/shi")
	m.updateCompletions()

	if !m.hasCompletions() {
		t.Fatal("expected completions to be active")
	}

	// Find /ship in suggestions and select it
	for i, s := range m.completionState.suggestions {
		if s == "/ship" {
			m.completionState.selectedIdx = i
			break
		}
	}

	accepted := m.acceptCompletion()
	if !accepted {
		t.Error("expected completion to be accepted")
	}

	// Check input was updated with trailing space
	if !strings.HasPrefix(m.input.Value(), "/ship") {
		t.Errorf("expected input to start with /ship, got %s", m.input.Value())
	}
}

func TestFileCompletions(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()
	testFiles := []string{"main.go", "helper.go", "README.md"}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	m := newCompletionTestModel(tmpDir)
	m.input.SetValue("/add ")
	m.updateCompletions()

	if !m.hasCompletions() {
		t.Fatal("expected file completions to be active")
	}

	if m.completionState.type_ != completionFilePath {
		t.Errorf("expected completion type to be completionFilePath, got %d", m.completionState.type_)
	}

	// Check that test files are in suggestions
	found := false
	for _, s := range m.completionState.suggestions {
		if strings.HasPrefix(s, "main") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find main* in file suggestions, got %v", m.completionState.suggestions)
	}
}

func TestFileCompletionsWithPrefix(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()
	testFiles := []string{"main.go", "helper.go", "manifest.json"}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	m := newCompletionTestModel(tmpDir)
	m.input.SetValue("/add ma")
	m.updateCompletions()

	if !m.hasCompletions() {
		t.Fatal("expected file completions to be active")
	}

	// Should only suggest main.go and manifest.json (starting with "ma")
	for _, s := range m.completionState.suggestions {
		if !strings.HasPrefix(strings.ToLower(s), "ma") {
			t.Errorf("expected all suggestions to start with 'ma', got %s", s)
		}
	}
}

func TestNoCompletionsForNonCommand(t *testing.T) {
	m := newCompletionTestModel(t.TempDir())
	m.input.SetValue("hello world")
	m.updateCompletions()

	if m.hasCompletions() {
		t.Error("expected no completions for non-command input")
	}
}

func TestShouldIgnoreFile(t *testing.T) {
	testCases := []struct {
		name     string
		filename string
		expected bool
	}{
		{"node_modules", "node_modules", true},
		{"vendor", "vendor", true},
		{"main.go", "main.go", false},
		{"README.md", "README.md", false},
		{"dist", "dist", true},
		{"build", "build", true},
		{"__pycache__", "__pycache__", true},
		// Note: .git and .DS_Store are filtered earlier by hidden file check
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldIgnoreFile(tc.filename)
			if result != tc.expected {
				t.Errorf("shouldIgnoreFile(%q) = %v, expected %v", tc.filename, result, tc.expected)
			}
		})
	}
}

func TestGetCompletionPreview(t *testing.T) {
	m := newCompletionTestModel(t.TempDir())
	m.completionState = &completionState{
		type_:       completionCommand,
		suggestions: []string{"/ship", "/save", "/session"},
		selectedIdx: 1,
	}

	preview := m.getCompletionPreview()
	if preview == "" {
		t.Error("expected non-empty preview")
	}

	// Should contain the selected item indicator
	if !strings.Contains(preview, ">/save") {
		t.Errorf("expected preview to highlight selected item with '>/save', got: %s", preview)
	}
}

func TestAllCommandsList(t *testing.T) {
	// Ensure all commands have the "/" prefix
	for _, cmd := range allCommands {
		if !strings.HasPrefix(cmd, "/") {
			t.Errorf("command %q should start with /", cmd)
		}
	}

	// Check that common commands are in the list
	requiredCommands := []string{"/ship", "/add", "/help", "/quit", "/init"}
	for _, required := range requiredCommands {
		found := false
		for _, cmd := range allCommands {
			if cmd == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("required command %q not found in allCommands", required)
		}
	}
}
