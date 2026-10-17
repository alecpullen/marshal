package app

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"marshal/internal/trust"
)

// docsOnlyTools names tools that docs/tools/ documents but that a unit-test
// runtime cannot register. Every entry needs a reason. On Linux/macOS the map
// is empty in practice — shell.sudo registers there — but the entry keeps the
// guard honest on Windows, where sudo_register_windows.go returns no tools.
var docsOnlyTools = map[string]string{
	"shell.sudo": "platform-gated: registered only on non-Windows (sudo_register.go build tag)",
}

// toolsDocIndexRe matches the machine-readable index line at the top of each
// docs/tools family file: <!-- tools: a, b, c -->
var toolsDocIndexRe = regexp.MustCompile(`^<!--\s*tools:\s*(.*?)\s*-->$`)

// TestToolsDocumentationDrift keeps docs/tools/ and the live tool registry in
// step. Three assertions, all driven by the index line at the top of each
// family doc:
//
//  1. every registered, non-internal tool is documented somewhere;
//  2. every documented name is registered (or in docsOnlyTools);
//  3. no retired tool name appears anywhere in the docs.
//
// Only the index line is parsed. Prose in these docs necessarily names source
// files (prompts.go, native.go) and error strings, and a name-shaped match
// over prose would flag those as unregistered tools.
func TestToolsDocumentationDrift(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	// The fixture enables what it can (embeddings, web search) so the registry
	// is as complete as a unit test allows: codebase_search needs a resolvable
	// embedding route and web.search needs a search URL.
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
[lsp]
enabled = false
[indexing]
embedding_preset = "mock/m"
[web]
enabled = true
search_url = "http://localhost:9999/search"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	registered := map[string]bool{}
	for _, tl := range rt.Runner.Registry.List() {
		registered[tl.Name] = true
	}
	if len(registered) == 0 {
		t.Fatal("no tools registered; the drift check below would be vacuous")
	}

	dir := filepath.Join("..", "..", "docs", "tools")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	indexed := map[string]string{} // tool name -> file that indexes it
	var docFiles []string
	docText := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		docFiles = append(docFiles, e.Name())
		docText[e.Name()] = string(raw)

		// README.md documents how the surface is assembled, not a family of
		// tools, so it carries no index line. It is still scanned for retired
		// names below.
		if e.Name() == "README.md" {
			continue
		}

		first := ""
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				first = strings.TrimSpace(line)
				break
			}
		}
		m := toolsDocIndexRe.FindStringSubmatch(first)
		if m == nil {
			t.Errorf("%s: first non-blank line must be a <!-- tools: ... --> index, got %q", e.Name(), first)
			continue
		}
		for _, name := range strings.Split(m[1], ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if prev, dup := indexed[name]; dup {
				t.Errorf("tool %s is indexed by both %s and %s", name, prev, e.Name())
				continue
			}
			indexed[name] = e.Name()
		}
	}
	if len(indexed) == 0 {
		t.Fatal("no tool index lines found under docs/tools; the drift check would be vacuous")
	}

	// 1. Registered -> documented.
	var undocumented []string
	for name := range registered {
		tl, _ := rt.Runner.Registry.Lookup(name)
		if tl.Internal {
			continue
		}
		if _, ok := indexed[name]; !ok {
			undocumented = append(undocumented, name)
		}
	}
	sort.Strings(undocumented)
	for _, name := range undocumented {
		t.Errorf("tool %s is registered and model-facing but not indexed by any docs/tools file", name)
	}

	// 2. Indexed -> registered.
	var stale []string
	for name, file := range indexed {
		if registered[name] {
			continue
		}
		if _, ok := docsOnlyTools[name]; ok {
			continue
		}
		stale = append(stale, name+" ("+file+")")
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("docs/tools indexes %s, which is not registered and not in docsOnlyTools", s)
	}

	// 3. Retired names absent. A name that is still registered is not retired
	// in the registry sense and may be documented: file.write_patch is
	// Internal, not removed, and editing.md documents it deliberately.
	for _, name := range retiredToolNames {
		if registered[name] {
			continue
		}
		for _, file := range docFiles {
			if strings.Contains(docText[file], name) {
				t.Errorf("docs/tools/%s mentions retired tool %s", file, name)
			}
		}
	}
}
