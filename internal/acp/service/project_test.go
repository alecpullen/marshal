package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// newProjectTestSession builds a session over a fresh state in dir, with deps
// applied on top.
func newProjectTestSession(t *testing.T, dir string, deps Deps) *Session {
	t.Helper()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	deps.State = state
	_, sess := New(deps)
	return sess
}

// writeProjectTestPlan writes a plan file into dir's plans directory.
func writeProjectTestPlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	plansDir := filepath.Join(dir, ".marshal", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	path := filepath.Join(plansDir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}
