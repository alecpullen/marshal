// internal/agent/postmortem_system_test.go — the motivating case for system
// access, proven at the runner seam: a session with the flag on patches a
// report JSON at an absolute path outside the workspace.
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"

	"marshal/internal/agent/agenttest"
)

// TestPostmortemSystemAccessWritePath runs a real turn whose only write is a
// file.write_patch against an out-of-root absolute path. With system access
// on the patch applies and the write is file-tracked; the same turn without
// the flag is rejected by the resolver (pinned separately in the native
// package).
func TestPostmortemSystemAccessWritePath(t *testing.T) {
	root := t.TempDir()

	// Resolve the temp dir first: on macOS /var is a symlink to /private/var
	// and the resolver returns the symlink-resolved path.
	reportDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	report := filepath.Join(reportDir, "report.json")
	if err := os.WriteFile(report, []byte("{\n  \"agent_observations\": []\n}\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "filetrack.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)

	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeCommandRunner{},
		FileTracker:   ft,
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetApprovalMode(policy.ModeAuto)
	pol.WithRegistry(reg)

	patchText := "File: " + report + "\n<<<<<<< SEARCH\n  \"agent_observations\": []\n=======\n  \"agent_observations\": [\"observed\"]\n>>>>>>> REPLACE"
	p := &agenttest.ScriptedProvider{Responses: []string{
		fmt.Sprintf(`{"rationale":"read the report","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, report),
		fmt.Sprintf(`{"rationale":"annotate","action":{"type":"tool_call","tool":"file.write_patch","args":{"patch":%q}}}`, patchText),
		`{"rationale":"done","action":{"type":"final","content":"Annotated the report."}}`,
	}}

	r := NewRunner(p, reg, pol, state, "test-model")
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	if _, err := r.RunTask(context.Background(), "annotate the postmortem report"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(data), `"agent_observations": ["observed"]`) {
		t.Fatalf("report was not patched; content = %q", string(data))
	}

	// The write must be file-tracked under the resolved absolute path.
	if _, hasRead, err := ft.LastReadTime(report); err != nil || !hasRead {
		t.Fatalf("filetrack LastReadTime(%q) = (_, %v, %v), want the write recorded", report, hasRead, err)
	}
}
