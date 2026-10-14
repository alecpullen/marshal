// internal/tools/native/system_access_readtools_test.go — read-tool widening.
package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// TestReadToolsSystemAccessAbsolute pins the read-side widening: repo.search
// accepts out-of-root absolute paths under system access, matching the prompt
// directive's promise, and keeps the prior containment rejection when the
// flag is off. csv.inspect and json.query were retired in the context-diet
// Tier 2a cut, so only the surviving read tool is covered here.
func TestReadToolsSystemAccessAbsolute(t *testing.T) {
	root := t.TempDir()
	// Resolve first: on macOS /var is a symlink to /private/var and the
	// resolvers return the symlink-resolved path.
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(outside, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	write("notes.txt", "needle\n")

	register := func(t *testing.T, system bool) *registry.Registry {
		t.Helper()
		state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
		state.SetSystemAccess(system)
		reg := registry.New()
		if err := RegisterAll(reg, Options{
			WorkspaceRoot: root,
			CommandRunner: &fakeRunner{},
			SessionState:  state,
			Config:        config.Default(),
		}); err != nil {
			t.Fatalf("RegisterAll: %v", err)
		}
		return reg
	}

	t.Run("repo.search absolute with system", func(t *testing.T) {
		res, err := invokeTool(t, register(t, true), "repo.search",
			`{"query":"needle","path":`+jsonString(outside)+`}`)
		if err != nil {
			t.Fatalf("repo.search: %v", err)
		}
		if !strings.Contains(res.Content, "needle") {
			t.Fatalf("repo.search content = %q, want the out-of-root match", res.Content)
		}
	})

	t.Run("repo.search absolute without system", func(t *testing.T) {
		_, err := invokeTool(t, register(t, false), "repo.search",
			`{"query":"needle","path":`+jsonString(outside)+`}`)
		if err == nil {
			t.Fatal("repo.search out-of-root succeeded without system access, want containment rejection")
		}
	})

}

// jsonString quotes s for embedding in a hand-built JSON tool argument.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
