package sdd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceEnsureCreatesDir(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	dir, err := ws.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	expected := filepath.Join(root, ".marshal", "sdd")
	if dir != expected {
		t.Errorf("dir = %q, want %q", dir, expected)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf(".gitignore not created: %v", err)
	}
	ign, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.TrimSpace(string(ign)) != "*" {
		t.Errorf(".gitignore = %q, want %q", strings.TrimSpace(string(ign)), "*")
	}
}

func TestWorkspaceTaskBriefPath(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if got := ws.BriefPath(3); got != filepath.Join(root, ".marshal", "sdd", "task-3-brief.md") {
		t.Errorf("BriefPath(3) = %q", got)
	}
}

func TestWorkspaceReportPath(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if got := ws.ReportPath(3); got != filepath.Join(root, ".marshal", "sdd", "task-3-report.md") {
		t.Errorf("ReportPath(3) = %q", got)
	}
}

func TestWorkspaceWriteTaskBrief(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	path, err := ws.WriteTaskBrief(1, "task body here")
	if err != nil {
		t.Fatalf("WriteTaskBrief: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if string(data) != "task body here" {
		t.Errorf("brief content = %q, want %q", string(data), "task body here")
	}
}

func TestWorkspaceReportsDir(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if got := ws.ReportsDir(); got != filepath.Join(root, ".marshal", "sdd") {
		t.Errorf("ReportsDir = %q, want %q", got, filepath.Join(root, ".marshal", "sdd"))
	}
}
