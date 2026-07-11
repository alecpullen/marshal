# Full Config Settings TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat single-form settings overlay with a two-pane settings TUI (sidebar of 15 config sections + per-section editor pane) that can view and edit every section of `.marshal/config.toml`, saved atomically via an extended `SaveProjectConfig`.

**Architecture:** The settings package (`internal/app/tui/settings/`) is rebuilt in place around a `sectionPane` interface: scalar sections wrap a `huh.Form`, collection sections use a generic list+sub-form editor, plus a list-of-strings widget and a map editor. All panes bind to a single working copy `state.cfg`; `Ctrl+S` saves everything through `config.SaveProjectConfig`, which is extended to persist the full editable surface while preserving unmanaged sections.

**Tech Stack:** Go, Bubble Tea v2 / huh v2 / lipgloss v2 (`charm.land` imports), `github.com/pelletier/go-toml/v2`.

**Spec:** `docs/superpowers/specs/2026-07-10-full-config-settings-tui-design.md`

## Global Constraints

- Public settings API is unchanged: `New(cfg config.Config, workingDir, projectCfgPath string) Model`, `Model.Update(tea.Msg) (Model, tea.Cmd)`, `Model.View() string`, `Model.SetSize(w, h int)`, `Model.Init() tea.Cmd`, `Model.FocusedFieldTitle() string`, `Model.Footer() string`, `Model.BoolValue(title string) bool`, `SavedMsg{Cfg config.Config}`, `CancelledMsg{}`. Parent TUI files `internal/app/tui/model.go` / `view.go` must need **zero edits**.
- Imports use `charm.land/bubbletea/v2`, `charm.land/huh/v2`, `charm.land/bubbles/v2/key`, `charm.land/lipgloss/v2`. Theme: `huhtheme.WarmSunset()`.
- Build needs `CGO_ENABLED=1`. Verify with `go build ./cmd/marshal`, `go test ./...`, `gofmt -w .`, `go vet ./...`.
- Only the project file (`.marshal/config.toml`) is written. Never the user config.
- Secrets (`providers.<n>.api_key`, `web.search_key`) render masked as `••••<last4>`; the real value is what gets saved.
- Save writes a newly-covered section only if it differs from `config.Default()` **or** the section already exists in the file (so a user resetting a value back to default still gets it written, and pristine files don't bloat).
- Esc semantics: first Esc closes an open sub-form/inline edit; Esc at a section's top level (or sidebar) emits `CancelledMsg`.
- TDD: every task writes the failing test first. Commit at the end of every task.
- Tests that assert on rendered views must strip ANSI first (lipgloss v2 always emits SGR codes) — reuse the `stripANSI` helper pattern from existing TUI tests.

## File Structure

```
internal/app/config/
  config.go              — MODIFY: configFile anonymous structs → named file* structs
  save.go                — MODIFY: SaveProjectConfig extended to full surface
  save_test.go           — MODIFY: round-trip tests for every editable section

internal/app/tui/settings/
  messages.go            — unchanged (SavedMsg, CancelledMsg)
  model.go               — REWRITE: two-pane Model, sidebar, focus, Esc levels, dirty, save
  state.go               — CREATE: state struct + cloneConfig
  sections.go            — CREATE: section registry (ordered ids/titles/pane factories)
  pane.go                — CREATE: sectionPane interface
  scalar.go              — CREATE: scalarPane (huh.Form wrapper) + settingsKeyMap
  liststrings.go         — CREATE: inline list-of-strings widget
  mixed.go               — CREATE: mixedPane (scalar form + N string lists)
  collection.go          — CREATE: generic list + sub-form editor
  mapeditor.go           — CREATE: key/value map editor pane
  masked.go              — CREATE: maskKey helper
  validation.go          — CREATE: soft-warning rules
  section_agent.go       — CREATE: agent + profile select (scalarPane)
  section_privacy.go     — CREATE (scalarPane)
  section_snapshots.go   — CREATE (scalarPane)
  section_web.go         — CREATE (scalarPane, masked search key)
  section_indexing.go    — CREATE (mixedPane)
  section_commands.go    — CREATE (mixedPane)
  section_shell.go       — CREATE (mixedPane)
  section_sandbox.go     — CREATE (mixedPane)
  section_providers.go   — CREATE (collectionPane, masked api key)
  section_presets.go     — CREATE (collectionPane)
  section_mcp.go         — CREATE (collectionPane + threshold)
  section_hooks.go       — CREATE (collectionPane + fail_closed)
  section_permissions.go — CREATE (collectionPane)
  section_swarm.go       — CREATE (mapeditor + scalars)
  section_diagnostics.go — CREATE (mapeditor)
  model_test.go          — MODIFY: update flat-form tests to two-pane reality
  *_test.go              — CREATE: per-widget/section tests
```

---

### Task 1: Name the configFile section structs

Pure refactor — the anonymous structs inside `configFile` (and duplicated in `save.go`) become named types so Task 2 can construct them without repeating tag blocks. No behavior change; the existing test suites are the safety net.

**Files:**
- Modify: `internal/app/config/config.go` (the `configFile` declaration, ~lines 278–384)
- Modify: `internal/app/config/save.go` (replace anonymous-struct literals with the named types)

**Interfaces:**
- Produces (package-private, used by Task 2): `fileProject`, `fileCommands`, `fileProfile`, `fileAgent`, `filePrivacy`, `fileIndexing`, `fileShell`, `fileTools`, `fileWeb`, `fileSwarmBudget`, `fileSwarm`, `fileMCPServer`, `fileMCP`, `fileSnapshots`, `filePermissions`, `fileDiagnostics`, `fileHookEntry`, `fileHooks`, `fileModels`, `fileAgentEntry` — each mirroring the current anonymous struct field-for-field, tags unchanged. `configFile` fields become pointers to these types (same nullability as today). `sandboxFile` already exists and is kept.

- [ ] **Step 1: Run the existing tests to establish green baseline**

Run: `go test ./internal/app/config/...`
Expected: PASS (baseline before refactor)

- [ ] **Step 2: Introduce the named types and rewrite `configFile`**

In `config.go`, above `configFile`, add (tags copied verbatim from the current anonymous structs):

```go
type fileProject struct {
	Name      *string  `toml:"name"`
	Languages []string `toml:"languages"`
}

type fileCommands struct {
	Test   *string `toml:"test"`
	Format *string `toml:"format"`
	Vet    *string `toml:"vet"`
}

type fileProfile struct {
	Default *string `toml:"default"`
}

type fileAgent struct {
	Provider                 *string `toml:"provider"`
	Model                    *string `toml:"model"`
	MaxToolIterations        *int    `toml:"max_tool_iterations"`
	MaxRetries               *int    `toml:"max_retries"`
	MaxTurnContextTokens     *int    `toml:"max_turn_context_tokens"`
	MaxStructuredOutputChars *int    `toml:"max_structured_output_chars"`
	PlanFirst                *bool   `toml:"plan_first"`
	SubtaskIterations        *int    `toml:"subtask_iterations"`
}

type filePrivacy struct {
	RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
	RedactSecrets          *bool `toml:"redact_secrets"`
	IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
}

type fileIndexing struct {
	UseTreesitter  *bool    `toml:"use_treesitter"`
	UseEmbeddings  *bool    `toml:"use_embeddings"`
	SummariseFiles *bool    `toml:"summarise_files"`
	Ignore         []string `toml:"ignore"`
}

type fileShell struct {
	DefaultTimeoutSeconds *int          `toml:"default_timeout_seconds"`
	MaxOutputBytes        *int          `toml:"max_output_bytes"`
	MaxBackgroundJobs     *int          `toml:"max_background_jobs"`
	BackgroundRetention   *string       `toml:"background_retention"`
	AllowNetwork          *bool         `toml:"allow_network"`
	AllowSudo             *bool         `toml:"allow_sudo"`
	AllowDestructive      *bool         `toml:"allow_destructive"`
	AutoApprove           *bool         `toml:"auto_approve"`
	GuardrailDynamicArgv0 *string       `toml:"guardrail_dynamic_argv0"`
	Allow                 *CommandRules `toml:"allow"`
	Confirm               *CommandRules `toml:"confirm"`
	Deny                  *PatternRules `toml:"deny"`
	Sandbox               *sandboxFile  `toml:"sandbox"`
}

type fileTools struct {
	Shell *fileShell `toml:"shell"`
}

type fileWeb struct {
	Enabled        *bool   `toml:"enabled"`
	FetchTimeout   *string `toml:"fetch_timeout"`
	SearchProvider *string `toml:"search_provider"`
	SearchURL      *string `toml:"search_url"`
	SearchKey      *string `toml:"search_key"`
}

type fileSwarmBudget struct {
	MaxFixRounds   *int           `toml:"max_fix_rounds"`
	MaxTotalTokens *int           `toml:"max_total_tokens"`
	ToolIters      map[string]int `toml:"tool_iters"`
}

type fileSwarm struct {
	Budget *fileSwarmBudget `toml:"budget"`
}

type fileMCPServer struct {
	Command *string           `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type fileMCP struct {
	Servers                  map[string]fileMCPServer `toml:"servers"`
	Policies                 map[string]string        `toml:"policies"`
	DisclosureThresholdTools *int                     `toml:"disclosure_threshold_tools"`
}

type fileSnapshots struct {
	Enabled       *bool `toml:"enabled"`
	RetentionDays *int  `toml:"retention_days"`
	MaxFileBytes  *int  `toml:"max_file_bytes"`
}

type filePermissions struct {
	Rules []PermissionRule `toml:"rules"`
}

type fileDiagnostics struct {
	Commands map[string]string `toml:"commands"`
}

type fileHookEntry struct {
	Event     *string `toml:"event"`
	Matcher   *string `toml:"matcher"`
	Command   *string `toml:"command"`
	TimeoutMS *int    `toml:"timeout_ms"`
}

type fileHooks struct {
	FailClosed *bool           `toml:"fail_closed"`
	Entries    []fileHookEntry `toml:"entries"`
}

type fileModels struct {
	Presets map[string]modelPresetConfig `toml:"presets"`
}

type fileAgentEntry struct {
	Context contextBudgetConfig `toml:"context"`
}
```

Then shrink `configFile` to:

```go
type configFile struct {
	Project     *fileProject     `toml:"project"`
	Commands    *fileCommands    `toml:"commands"`
	Profile     *fileProfile     `toml:"profile"`
	Agent       *fileAgent       `toml:"agent"`
	Privacy     *filePrivacy     `toml:"privacy"`
	Indexing    *fileIndexing    `toml:"indexing"`
	Tools       *fileTools       `toml:"tools"`
	Web         *fileWeb         `toml:"web"`
	Swarm       *fileSwarm       `toml:"swarm"`
	MCP         *fileMCP         `toml:"mcp"`
	Snapshots   *fileSnapshots   `toml:"snapshots"`
	Permissions *filePermissions `toml:"permissions"`
	Diagnostics *fileDiagnostics `toml:"diagnostics"`
	Hooks       *fileHooks       `toml:"hooks"`
	// Providers stays a plain map: nil already distinguishes absent/present.
	Providers     map[string]ProviderConfig               `toml:"providers"`
	Models        *fileModels                             `toml:"models"`
	AgentProfiles map[string]agentProfileConfig           `toml:"agent_profiles"`
	Agents        map[routing.AgentRole]fileAgentEntry    `toml:"agents"`
}
```

The `merge` function body keeps working as-is (field names and shapes are identical) except the `file.MCP.Servers` iteration now ranges over `fileMCPServer` values and the `file.Hooks.Entries` loop ranges over `fileHookEntry` — the field accesses are unchanged, only the type names in any explicit declarations need updating.

- [ ] **Step 3: Update `save.go` to use the named types**

Replace every anonymous-struct literal with the named type. The three sites: `file.Profile = &fileProfile{Default: &defaultProfile}`; both `file.Agent = &struct{...}` branches become `file.Agent = &fileAgent{...}` with the same field values; `file.Privacy` init becomes `&filePrivacy{}`; `file.Models` init becomes `&fileModels{Presets: map[string]modelPresetConfig{}}`; `file.Tools` / `file.Tools.Shell` inits become `&fileTools{}` / `&fileShell{}`; in `SaveUserConfigRule`, `file.Permissions` init becomes `&filePermissions{}`.

- [ ] **Step 4: Verify no behavior change**

Run: `go build ./... && go test ./internal/app/config/... && go vet ./internal/app/config/...`
Expected: build OK, all tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/config/config.go internal/app/config/save.go
git commit -m "refactor(config): name configFile section structs"
```

---

### Task 2: Extend SaveProjectConfig to the full editable surface

**Files:**
- Modify: `internal/app/config/save.go`
- Test: `internal/app/config/save_test.go`

**Interfaces:**
- Consumes: the named `file*` structs from Task 1.
- Produces: `SaveProjectConfig(path string, cfg Config) error` now persists, in addition to today's sections: `project`, `commands`, `indexing`, `web`, `swarm`, `mcp`, `snapshots`, `permissions`, `diagnostics`, `hooks`, `providers`, and **all** `models.presets` (not just the active one). Newly-covered sections are written only if they differ from `Default()` or already exist in the file. Existing always-written sections (profile/agent/privacy/shell/sandbox) keep their current behavior.

- [ ] **Step 1: Write the failing round-trip test**

Append to `internal/app/config/save_test.go`:

```go
func fullEditedConfig() Config {
	cfg := Default()
	cfg.Project = ProjectConfig{Name: "acme", Languages: []string{"go", "python"}}
	cfg.Commands = CommandsConfig{Test: "make test", Format: "make fmt", Vet: "make vet"}
	cfg.Indexing = IndexingConfig{UseTreesitter: true, UseEmbeddings: true, SummariseFiles: true, Ignore: []string{"build/**"}}
	cfg.Web = WebConfig{Enabled: true, FetchTimeout: 45 * time.Second, SearchProvider: "searx", SearchURL: "http://localhost:8888", SearchKey: "sk-live-1234"}
	cfg.Swarm.Budget = SwarmBudgetConfig{MaxFixRounds: 5, MaxTotalTokens: 99000, ToolIters: map[string]int{"tester": 9}}
	cfg.MCP = MCPConfig{
		Servers:                  map[string]MCPServerConfig{"fs": {Command: "mcp-fs", Args: []string{"--root", "."}, Env: map[string]string{"A": "1"}}},
		Policies:                 map[string]string{"fs": "confirm"},
		DisclosureThresholdTools: 25,
	}
	cfg.Snapshots = SnapshotsConfig{Enabled: false, RetentionDays: 14, MaxFileBytes: 1000}
	cfg.Permissions.Rules = []PermissionRule{{Permission: "shell", Pattern: "go *", Action: "allow"}}
	cfg.Diagnostics.Commands = map[string]string{"go": "go vet ./...", "py": "ruff check"}
	cfg.Hooks = HooksConfig{FailClosed: true, Entries: []HookConfig{{Event: "pre_tool", Matcher: "shell.*", Command: "echo hi", TimeoutMS: 500}}}
	cfg.Providers = map[string]ProviderConfig{"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "real-key", APIKeyEnv: "OLLAMA_KEY", ToolCalling: true}}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Name: "fast", Provider: "ollama", Model: "qwen3", ContextWindow: 32768, MaxOutputTokens: 4096, Temperature: 0.2, TopP: 0.9, ToolCalling: "native", ReasoningEffort: "low", LocalOnly: true},
	}
	return cfg
}

func TestSaveProjectConfigFullSurfaceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	cfg := fullEditedConfig()

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: filepath.Join(dir, "no-home"), WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !reflect.DeepEqual(loaded.Project, cfg.Project) {
		t.Errorf("project: got %+v want %+v", loaded.Project, cfg.Project)
	}
	if loaded.Commands != cfg.Commands {
		t.Errorf("commands: got %+v want %+v", loaded.Commands, cfg.Commands)
	}
	if !reflect.DeepEqual(loaded.Indexing, cfg.Indexing) {
		t.Errorf("indexing: got %+v want %+v", loaded.Indexing, cfg.Indexing)
	}
	if loaded.Web != cfg.Web {
		t.Errorf("web: got %+v want %+v", loaded.Web, cfg.Web)
	}
	if !reflect.DeepEqual(loaded.Swarm, cfg.Swarm) {
		t.Errorf("swarm: got %+v want %+v", loaded.Swarm, cfg.Swarm)
	}
	if !reflect.DeepEqual(loaded.MCP, cfg.MCP) {
		t.Errorf("mcp: got %+v want %+v", loaded.MCP, cfg.MCP)
	}
	if loaded.Snapshots != cfg.Snapshots {
		t.Errorf("snapshots: got %+v want %+v", loaded.Snapshots, cfg.Snapshots)
	}
	if !reflect.DeepEqual(loaded.Permissions, cfg.Permissions) {
		t.Errorf("permissions: got %+v want %+v", loaded.Permissions, cfg.Permissions)
	}
	if !reflect.DeepEqual(loaded.Diagnostics.Commands, cfg.Diagnostics.Commands) {
		t.Errorf("diagnostics: got %+v want %+v", loaded.Diagnostics.Commands, cfg.Diagnostics.Commands)
	}
	if !reflect.DeepEqual(loaded.Hooks, cfg.Hooks) {
		t.Errorf("hooks: got %+v want %+v", loaded.Hooks, cfg.Hooks)
	}
	if !reflect.DeepEqual(loaded.Providers, cfg.Providers) {
		t.Errorf("providers: got %+v want %+v", loaded.Providers, cfg.Providers)
	}
	if !reflect.DeepEqual(loaded.Models.Presets["fast"], cfg.Models.Presets["fast"]) {
		t.Errorf("preset fast: got %+v want %+v", loaded.Models.Presets["fast"], cfg.Models.Presets["fast"])
	}
}

func TestSaveProjectConfigOmitsDefaultNewSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	if err := SaveProjectConfig(path, Default()); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, section := range []string{"[web]", "[mcp]", "[hooks]", "[permissions]", "[snapshots]", "[diagnostics]", "[project]", "[commands]", "[indexing]", "[swarm.budget]", "[providers"} {
		if strings.Contains(string(data), section) {
			t.Errorf("default-valued section %s should be omitted from a pristine file:\n%s", section, data)
		}
	}
}

func TestSaveProjectConfigPreservesAgentProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	seed := "[agent_profiles.local_balanced]\nimplementer = \"fast\"\n"
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SaveProjectConfig(path, Default()); err != nil {
		t.Fatalf("save: %v", err)
	}
	loadedFile, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if got := loadedFile.AgentProfiles["local_balanced"].Implementer; got != "fast" {
		t.Errorf("agent_profiles dropped by save: implementer=%q want %q", got, "fast")
	}
}
```

Add `"os"`, `"reflect"`, `"strings"`, `"time"`, and `"marshal/internal/llm/routing"` to the test file imports if missing.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/config/ -run 'TestSaveProjectConfig(FullSurface|Omits|Preserves)' -v`
Expected: FAIL — the new sections come back as defaults after round-trip (save currently drops them).

- [ ] **Step 3: Implement the extension**

In `save.go`, add a generic pointer helper and append the new section writers at the end of `SaveProjectConfig`, just before `toml.Marshal`:

```go
func ptr[T any](v T) *T { return &v }
```

```go
	def := Default()

	if file.Project != nil || !reflect.DeepEqual(cfg.Project, def.Project) {
		file.Project = &fileProject{Name: ptr(cfg.Project.Name), Languages: cfg.Project.Languages}
	}
	if file.Commands != nil || cfg.Commands != def.Commands {
		file.Commands = &fileCommands{Test: ptr(cfg.Commands.Test), Format: ptr(cfg.Commands.Format), Vet: ptr(cfg.Commands.Vet)}
	}
	if file.Indexing != nil || !reflect.DeepEqual(cfg.Indexing, def.Indexing) {
		file.Indexing = &fileIndexing{
			UseTreesitter:  ptr(cfg.Indexing.UseTreesitter),
			UseEmbeddings:  ptr(cfg.Indexing.UseEmbeddings),
			SummariseFiles: ptr(cfg.Indexing.SummariseFiles),
			Ignore:         cfg.Indexing.Ignore,
		}
	}
	if file.Web != nil || cfg.Web != def.Web {
		file.Web = &fileWeb{
			Enabled:        ptr(cfg.Web.Enabled),
			FetchTimeout:   ptr(cfg.Web.FetchTimeout.String()),
			SearchProvider: ptr(cfg.Web.SearchProvider),
			SearchURL:      ptr(cfg.Web.SearchURL),
			SearchKey:      ptr(cfg.Web.SearchKey),
		}
	}
	if file.Swarm != nil || !reflect.DeepEqual(cfg.Swarm, def.Swarm) {
		file.Swarm = &fileSwarm{Budget: &fileSwarmBudget{
			MaxFixRounds:   ptr(cfg.Swarm.Budget.MaxFixRounds),
			MaxTotalTokens: ptr(cfg.Swarm.Budget.MaxTotalTokens),
			ToolIters:      cfg.Swarm.Budget.ToolIters,
		}}
	}
	if file.MCP != nil || !reflect.DeepEqual(cfg.MCP, def.MCP) {
		servers := map[string]fileMCPServer{}
		for name, srv := range cfg.MCP.Servers {
			servers[name] = fileMCPServer{Command: ptr(srv.Command), Args: srv.Args, Env: srv.Env}
		}
		file.MCP = &fileMCP{
			Servers:                  servers,
			Policies:                 cfg.MCP.Policies,
			DisclosureThresholdTools: ptr(cfg.MCP.DisclosureThresholdTools),
		}
	}
	if file.Snapshots != nil || cfg.Snapshots != def.Snapshots {
		file.Snapshots = &fileSnapshots{
			Enabled:       ptr(cfg.Snapshots.Enabled),
			RetentionDays: ptr(cfg.Snapshots.RetentionDays),
			MaxFileBytes:  ptr(cfg.Snapshots.MaxFileBytes),
		}
	}
	if file.Permissions != nil || len(cfg.Permissions.Rules) > 0 {
		file.Permissions = &filePermissions{Rules: cfg.Permissions.Rules}
	}
	if file.Diagnostics != nil || !reflect.DeepEqual(cfg.Diagnostics, def.Diagnostics) {
		file.Diagnostics = &fileDiagnostics{Commands: cfg.Diagnostics.Commands}
	}
	if file.Hooks != nil || !reflect.DeepEqual(cfg.Hooks, def.Hooks) {
		entries := make([]fileHookEntry, 0, len(cfg.Hooks.Entries))
		for _, h := range cfg.Hooks.Entries {
			entries = append(entries, fileHookEntry{Event: ptr(h.Event), Matcher: ptr(h.Matcher), Command: ptr(h.Command), TimeoutMS: ptr(h.TimeoutMS)})
		}
		file.Hooks = &fileHooks{FailClosed: ptr(cfg.Hooks.FailClosed), Entries: entries}
	}
	if file.Providers != nil || len(cfg.Providers) > 0 {
		file.Providers = cfg.Providers
	}
	if file.Models != nil || len(cfg.Models.Presets) > 0 {
		if file.Models == nil {
			file.Models = &fileModels{}
		}
		file.Models.Presets = map[string]modelPresetConfig{}
		for name, p := range cfg.Models.Presets {
			file.Models.Presets[name] = modelPresetConfig{
				Provider: p.Provider, Model: p.Model, ContextWindow: p.ContextWindow,
				MaxOutputTokens: p.MaxOutputTokens, Temperature: p.Temperature, TopP: p.TopP,
				ToolCalling: p.ToolCalling, ReasoningEffort: p.ReasoningEffort, LocalOnly: p.LocalOnly,
			}
		}
	}
```

This block supersedes the old "write only the active preset" logic — delete the `activePresetName`-guarded `file.Models` block from the existing function (the full-presets write above covers it). Keep the `activePresetName` helper: the `file.Agent` provider/model suppression still uses it. Add `"reflect"` to imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/config/...`
Expected: PASS, including all pre-existing save tests.

- [ ] **Step 5: Commit**

```bash
git add internal/app/config/save.go internal/app/config/save_test.go
git commit -m "feat(config): SaveProjectConfig persists the full editable surface"
```

---

### Task 3: Two-pane Model skeleton (state, pane interface, sections registry, sidebar)

Rebuilds `settings.Model` around the sidebar + pane layout. Every section initially uses a `staticPane` (renders "coming in a later task") so the model is fully navigable and testable before any real editor exists. Real panes replace the factories task by task.

**Files:**
- Create: `internal/app/tui/settings/state.go`
- Create: `internal/app/tui/settings/pane.go`
- Create: `internal/app/tui/settings/sections.go`
- Rewrite: `internal/app/tui/settings/model.go`
- Test: `internal/app/tui/settings/skeleton_test.go`
- Modify: `internal/app/tui/settings/model_test.go` (temporarily skip flat-form tests; Task 4+ re-enable/rewrite them)

**Interfaces:**
- Produces:
  - `type state struct { cfg config.Config; snapshot config.Config }` and `func cloneConfig(config.Config) config.Config` (deep copy of all maps/slices).
  - `type sectionPane interface { Init() tea.Cmd; Update(tea.Msg) (sectionPane, tea.Cmd); View(width int) string; SetWidth(int); HasInnerFocus() bool; CloseInner(); FocusedFieldTitle() string }`
  - `type section struct { id, title string; build func(s *state) sectionPane }` and `func sectionList() []section` returning the 15 sections in spec order: agent, providers, presets, privacy, shell, sandbox, indexing, web, swarm, mcp, snapshots, hooks, permissions, diagnostics, commands.
  - `Model` keeps the whole public API from Global Constraints. New internals: `sections []section`, `panes []sectionPane` (index-aligned), `cursor int`, `paneFocused bool`, `helpOpen bool`.
  - `func (m Model) dirty() bool` — `!reflect.DeepEqual(m.state.cfg, m.state.snapshot)`.
- Consumes: `config.SaveProjectConfig` (Task 2), `huhtheme.WarmSunset()`.

- [ ] **Step 1: Write the failing skeleton tests**

Create `internal/app/tui/settings/skeleton_test.go`:

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func keyPress(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.Msg
		switch k {
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		m, _ = m.Update(msg)
	}
	return m
}

func TestSidebarListsAllSections(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	view := stripANSI(m.View())
	for _, title := range []string{"Agent", "Providers", "Model Presets", "Privacy", "Shell", "Sandbox", "Indexing", "Web", "Swarm", "MCP", "Snapshots", "Hooks", "Permissions", "Diagnostics", "Commands"} {
		if !strings.Contains(view, title) {
			t.Errorf("sidebar missing section %q", title)
		}
	}
}

func TestSidebarCursorMovesAndClamps(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m = keyPress(m, "up")
	if m.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.cursor)
	}
	m = keyPress(m, "j", "j")
	if m.cursor != 2 {
		t.Errorf("cursor after jj = %d, want 2", m.cursor)
	}
	m = keyPress(m, "G")
	if m.cursor != len(m.sections)-1 {
		t.Errorf("G should jump to last section, got %d", m.cursor)
	}
	m = keyPress(m, "g")
	if m.cursor != 0 {
		t.Errorf("g should jump to first section, got %d", m.cursor)
	}
}

func TestTabEntersAndLeavesPane(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m = keyPress(m, "tab")
	if !m.paneFocused {
		t.Fatal("tab should focus the pane")
	}
	m = keyPress(m, "shift+tab")
	if m.paneFocused {
		t.Fatal("shift+tab should return to sidebar")
	}
}

func TestEscAtTopLevelCancels(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatal("esc at top level should emit CancelledMsg")
	}
}

func TestDirtyReflectsWorkingCopyChanges(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	if m.dirty() {
		t.Fatal("fresh model must not be dirty")
	}
	m.state.cfg.Privacy.RemoteProvidersAllowed = true
	if !m.dirty() {
		t.Fatal("mutating the working copy must set dirty")
	}
}

func TestCloneConfigIsDeep(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{"a": {BaseURL: "x"}}
	clone := cloneConfig(cfg)
	clone.Providers["a"] = config.ProviderConfig{BaseURL: "changed"}
	clone.Tools.Shell.Allow.Commands[0] = "changed"
	clone.Indexing.Ignore[0] = "changed"
	if cfg.Providers["a"].BaseURL != "x" || cfg.Tools.Shell.Allow.Commands[0] == "changed" || cfg.Indexing.Ignore[0] == "changed" {
		t.Fatal("cloneConfig must deep-copy maps and slices")
	}
}
```

`stripANSI` — if not already present in this package's tests, add to `skeleton_test.go`:

```go
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }
```

Also, in `model_test.go`, mark each existing flat-form test body with `t.Skip("flat form replaced by two-pane model; re-enabled in Task 4")` as the first line (they reference `m.form` internals that no longer exist — delete those references as needed to keep compiling).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestSidebar|TestTabEnters|TestEscAtTop|TestDirty|TestClone' -v`
Expected: FAIL (compile errors: `cursor`, `sections`, `cloneConfig` undefined)

- [ ] **Step 3: Implement state.go, pane.go, sections.go, and rewrite model.go**

`state.go`:

```go
package settings

import (
	"maps"
	"slices"

	"marshal/internal/app/config"
)

// state holds the single mutable working copy of the config that every
// section pane binds to by pointer, plus an immutable snapshot used for
// dirty detection. It is heap-allocated (Model stores *state) so pointer
// bindings survive Model value copies.
type state struct {
	cfg      config.Config
	snapshot config.Config
}

func newState(cfg config.Config) *state {
	working := cloneConfig(cfg)
	return &state{cfg: working, snapshot: cloneConfig(working)}
}

// cloneConfig deep-copies every map and slice reachable from cfg that the
// settings panes can mutate, so edits to the working copy never leak into
// the snapshot (or the caller's config).
func cloneConfig(cfg config.Config) config.Config {
	out := cfg
	out.Project.Languages = slices.Clone(cfg.Project.Languages)
	out.Indexing.Ignore = slices.Clone(cfg.Indexing.Ignore)
	out.Providers = maps.Clone(cfg.Providers)
	out.Models.Presets = maps.Clone(cfg.Models.Presets)
	out.AgentProfiles = maps.Clone(cfg.AgentProfiles)
	out.Agents = maps.Clone(cfg.Agents)
	out.Tools.Shell.Allow.Commands = slices.Clone(cfg.Tools.Shell.Allow.Commands)
	out.Tools.Shell.Confirm.Commands = slices.Clone(cfg.Tools.Shell.Confirm.Commands)
	out.Tools.Shell.Deny.Patterns = slices.Clone(cfg.Tools.Shell.Deny.Patterns)
	out.Tools.Shell.Sandbox.EnvAllowlist = slices.Clone(cfg.Tools.Shell.Sandbox.EnvAllowlist)
	out.Tools.Shell.Sandbox.EnvDenylist = slices.Clone(cfg.Tools.Shell.Sandbox.EnvDenylist)
	out.Swarm.Budget.ToolIters = maps.Clone(cfg.Swarm.Budget.ToolIters)
	if cfg.MCP.Servers != nil {
		out.MCP.Servers = make(map[string]config.MCPServerConfig, len(cfg.MCP.Servers))
		for name, srv := range cfg.MCP.Servers {
			srv.Args = slices.Clone(srv.Args)
			srv.Env = maps.Clone(srv.Env)
			out.MCP.Servers[name] = srv
		}
	}
	out.MCP.Policies = maps.Clone(cfg.MCP.Policies)
	out.Permissions.Rules = slices.Clone(cfg.Permissions.Rules)
	out.Diagnostics.Commands = maps.Clone(cfg.Diagnostics.Commands)
	out.Hooks.Entries = slices.Clone(cfg.Hooks.Entries)
	return out
}
```

`pane.go`:

```go
package settings

import tea "charm.land/bubbletea/v2"

// sectionPane renders and edits one config section in the right pane.
type sectionPane interface {
	Init() tea.Cmd
	Update(tea.Msg) (sectionPane, tea.Cmd)
	View(width int) string
	SetWidth(int)
	// HasInnerFocus reports whether the pane has an open sub-form or
	// inline edit that Esc should close instead of closing the overlay.
	HasInnerFocus() bool
	// CloseInner discards the deepest open sub-form or inline edit.
	CloseInner()
	FocusedFieldTitle() string
}

// staticPane is a read-only placeholder pane (also used for hints).
type staticPane struct{ text string }

func (p *staticPane) Init() tea.Cmd                          { return nil }
func (p *staticPane) Update(tea.Msg) (sectionPane, tea.Cmd)  { return p, nil }
func (p *staticPane) View(width int) string                  { return p.text }
func (p *staticPane) SetWidth(int)                           {}
func (p *staticPane) HasInnerFocus() bool                    { return false }
func (p *staticPane) CloseInner()                            {}
func (p *staticPane) FocusedFieldTitle() string              { return "" }
```

`sections.go`:

```go
package settings

type section struct {
	id    string
	title string
	build func(s *state) sectionPane
}

// sectionList is the ordered sidebar registry. Later tasks replace the
// staticPane factories with real editors section by section.
func sectionList() []section {
	placeholder := func(s *state) sectionPane { return &staticPane{text: "Editor coming soon — edit .marshal/config.toml directly."} }
	return []section{
		{id: "agent", title: "Agent", build: placeholder},
		{id: "providers", title: "Providers", build: placeholder},
		{id: "presets", title: "Model Presets", build: placeholder},
		{id: "privacy", title: "Privacy", build: placeholder},
		{id: "shell", title: "Shell", build: placeholder},
		{id: "sandbox", title: "Sandbox", build: placeholder},
		{id: "indexing", title: "Indexing", build: placeholder},
		{id: "web", title: "Web", build: placeholder},
		{id: "swarm", title: "Swarm", build: placeholder},
		{id: "mcp", title: "MCP", build: placeholder},
		{id: "snapshots", title: "Snapshots", build: placeholder},
		{id: "hooks", title: "Hooks", build: placeholder},
		{id: "permissions", title: "Permissions", build: placeholder},
		{id: "diagnostics", title: "Diagnostics", build: placeholder},
		{id: "commands", title: "Commands", build: placeholder},
	}
}
```

`model.go` (full rewrite; keep the package doc tone of the old file):

```go
package settings

import (
	"fmt"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
)

const sidebarWidth = 18

type Model struct {
	state          *state
	sections       []section
	panes          []sectionPane
	cursor         int
	paneFocused    bool
	helpOpen       bool
	workingDir     string
	projectCfgPath string
	footer         string
	width          int
	height         int
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	st := newState(cfg)
	secs := sectionList()
	panes := make([]sectionPane, len(secs))
	for i, sec := range secs {
		panes[i] = sec.build(st)
		if c := panes[i].Init(); c != nil {
			_ = c()
		}
	}
	return Model{
		state:          st,
		sections:       secs,
		panes:          panes,
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}
}

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	pw := width - sidebarWidth - 6
	if pw < 30 {
		pw = 30
	}
	for _, p := range m.panes {
		p.SetWidth(pw)
	}
}

func (m Model) dirty() bool {
	return !reflect.DeepEqual(m.state.cfg, m.state.snapshot)
}

func (m *Model) activePane() sectionPane { return m.panes[m.cursor] }

func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			if m.activePane().HasInnerFocus() {
				m.activePane().CloseInner()
				return *m, nil
			}
			return *m, func() tea.Msg { return CancelledMsg{} }
		case "ctrl+s":
			return *m, m.saveCmd()
		case "?":
			if !m.activePane().HasInnerFocus() {
				m.helpOpen = !m.helpOpen
				return *m, nil
			}
		}

		if !m.paneFocused {
			switch k.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
				return *m, nil
			case "down", "j":
				if m.cursor < len(m.sections)-1 {
					m.cursor++
				}
				return *m, nil
			case "g":
				m.cursor = 0
				return *m, nil
			case "G":
				m.cursor = len(m.sections) - 1
				return *m, nil
			case "tab", "l", "right":
				m.paneFocused = true
				return *m, nil
			}
			return *m, nil
		}

		// Pane focused: sidebar-return keys are handled here only when the
		// pane has no inner edit open (so typing "h" into a text input works).
		if !m.activePane().HasInnerFocus() {
			switch k.String() {
			case "shift+tab", "h", "left":
				m.paneFocused = false
				return *m, nil
			}
		}
	}

	if m.paneFocused {
		updated, cmd := m.activePane().Update(msg)
		m.panes[m.cursor] = updated
		return *m, cmd
	}
	return *m, nil
}

func (m *Model) saveCmd() tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveProjectConfig(m.projectCfgPath, m.state.cfg); err != nil {
			m.footer = fmt.Sprintf("Save failed: %v", err)
			return nil
		}
		loaded, err := config.Load(config.LoadOptions{WorkingDir: m.workingDir})
		if err != nil {
			m.footer = fmt.Sprintf("Reload failed: %v", err)
			return nil
		}
		return SavedMsg{Cfg: loaded}
	}
}

var (
	sidebarActiveStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	sidebarItemStyle   = lipgloss.NewStyle()
	paneTitleStyle     = lipgloss.NewStyle().Bold(true)
	warnStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (m Model) View() string {
	if m.helpOpen {
		return m.helpView()
	}
	var sb strings.Builder
	for i, sec := range m.sections {
		label := " " + sec.title
		if i == m.cursor {
			marker := " "
			if m.paneFocused {
				marker = "▸"
			}
			label = sidebarActiveStyle.Render(marker + sec.title)
		} else {
			label = sidebarItemStyle.Render(label)
		}
		sb.WriteString(lipgloss.NewStyle().Width(sidebarWidth).Render(label))
		sb.WriteString("\n")
	}
	sidebar := strings.TrimRight(sb.String(), "\n")

	paneWidth := m.width - sidebarWidth - 6
	if paneWidth < 30 {
		paneWidth = 30
	}
	header := paneTitleStyle.Render(m.sections[m.cursor].title)
	if warns := warningsFor(m.sections[m.cursor].id, m.state.cfg); len(warns) > 0 {
		header += "\n" + warnStyle.Render("⚠ "+strings.Join(warns, " · "))
	}
	pane := header + "\n\n" + m.activePane().View(paneWidth)

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, "  ", pane)

	footer := "Ctrl+S save · Esc cancel · ? help"
	if m.dirty() {
		footer = "* modified · " + footer
	}
	if m.footer != "" {
		footer = m.footer
	}
	return body + "\n\n" + footer
}

func (m Model) helpView() string {
	return strings.Join([]string{
		"Settings keys",
		"",
		"  ↑/↓ or k/j    move (sidebar or list)",
		"  Tab / l / →   enter section",
		"  Shift+Tab / h back to sidebar",
		"  g / G         first / last section",
		"  a             add entry (lists)",
		"  e / Enter     edit entry (lists)",
		"  d             delete entry (lists)",
		"  Ctrl+S        save all changes",
		"  Esc           close sub-form, then cancel",
		"",
		"Press ? to close this help.",
	}, "\n")
}

func (m Model) FocusedFieldTitle() string {
	if m.paneFocused {
		if t := m.activePane().FocusedFieldTitle(); t != "" {
			return t
		}
	}
	return m.sections[m.cursor].title
}

func (m Model) Footer() string { return m.footer }

// BoolValue returns the current value of a named boolean settings field,
// read straight from the working copy. Convenience for tests and the parent
// status line.
func (m Model) BoolValue(title string) bool {
	switch title {
	case "Local only":
		if p, ok := m.state.cfg.Models.Presets[activePresetNameFor(m.state.cfg)]; ok {
			return p.LocalOnly
		}
		return false
	case "Remote providers allowed":
		return m.state.cfg.Privacy.RemoteProvidersAllowed
	case "Allow network":
		return m.state.cfg.Tools.Shell.AllowNetwork
	case "Allow sudo":
		return m.state.cfg.Tools.Shell.AllowSudo
	case "Allow destructive":
		return m.state.cfg.Tools.Shell.AllowDestructive
	case "Auto-approve shell":
		return m.state.cfg.Tools.Shell.AutoApprove
	}
	return false
}

// activePresetNameFor resolves the implementer preset of the default profile
// (same rule as config.activePresetName, duplicated here because that helper
// is package-private to config).
func activePresetNameFor(cfg config.Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	return profile.Roles["implementer"]
}
```

Note: `profile.Roles` is keyed by `routing.AgentRole`; use `profile.Roles[routing.RoleImplementer]` and import `marshal/internal/llm/routing` (the string literal above is shorthand — write the typed constant in real code).

`warningsFor` does not exist yet — add a stub in `validation.go` now so the model compiles (Task 11 fills in the rules):

```go
package settings

import "marshal/internal/app/config"

// warningsFor returns soft, non-blocking warnings for the given section.
func warningsFor(sectionID string, cfg config.Config) []string { return nil }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/... && go build ./...`
Expected: skeleton tests PASS; old flat-form tests SKIP; build OK (parent TUI compiles untouched).

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): two-pane model skeleton with sidebar and section registry"
```

---

### Task 4: scalarPane + Agent section

**Files:**
- Create: `internal/app/tui/settings/scalar.go`
- Create: `internal/app/tui/settings/section_agent.go`
- Modify: `internal/app/tui/settings/sections.go` (agent factory)
- Test: `internal/app/tui/settings/section_agent_test.go`
- Modify: `internal/app/tui/settings/model_test.go` (rewrite the skipped flat-form tests that map to agent-section behavior; delete the rest — their behavior is covered per-section from here on)

**Interfaces:**
- Produces:
  - `func newScalarPane(build func() *huh.Form) *scalarPane` — wraps a huh form; `build` is re-invoked if the form ever reaches `huh.StateCompleted` so the pane stays editable. Implements `sectionPane` (`HasInnerFocus` returns false; `CloseInner` no-op; `FocusedFieldTitle` proxies `form.GetFocusedField().GetKey()`).
  - `func settingsKeyMap() *huh.KeyMap` — the keymap from the old `New()` verbatim: `Quit`=ctrl+c, all `Submit`=ctrl+s, `Confirm.Toggle`=`h,l,right,left,space`. **Change:** drop `h`/`l` from Confirm.Toggle (they now mean "back to sidebar"); keep `right,left,space`.
  - `func numField(label string, value *string, min int, set func(int)) *huh.Input` — moved verbatim from the old `New()` into `scalar.go` as a package-level helper.
  - `func newAgentPane(s *state) sectionPane` — fields: Default profile (Select over sorted `AgentProfiles` keys), Preset (read-only single-option Select), Provider, Model (Inputs writing to the active preset if any, else `cfg.Agent`), Local only (Confirm, preset-only), Max tool iterations (min 1), Max retries, Max turn context tokens, Subtask iterations (numFields), Plan first (Confirm bound to `&s.cfg.Agent.PlanFirst`). This reuses the old flat form's provider/model/preset write-back logic unchanged, including `activePresetFromConfig` (move it into `section_agent.go`).

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/settings/section_agent_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func agentTestConfig() config.Config {
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]string{routing.RoleImplementer: "fast"}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Name: "fast", Provider: "ollama", Model: "qwen3", LocalOnly: true},
	}
	return cfg
}

func TestAgentPaneShowsPresetFields(t *testing.T) {
	m := New(agentTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = keyPress(m, "tab") // enter agent pane
	if got := m.FocusedFieldTitle(); got != "Default profile" {
		t.Errorf("first focused field = %q, want Default profile", got)
	}
}

func TestAgentPaneEditsWriteToWorkingCopy(t *testing.T) {
	m := New(agentTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = keyPress(m, "tab", "down", "down") // focus Provider input
	if got := m.FocusedFieldTitle(); got != "Provider" {
		t.Fatalf("focused = %q, want Provider", got)
	}
	m = keyPress(m, "x", "down") // type then blur (validate fires on blur)
	if got := m.state.cfg.Models.Presets["fast"].Provider; got != "ollamax" {
		t.Errorf("preset provider = %q, want ollamax", got)
	}
	if !m.dirty() {
		t.Error("edit must mark model dirty")
	}
}

func TestAgentPaneLocalOnlyToggle(t *testing.T) {
	m := New(agentTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = keyPress(m, "tab", "down", "down", "down", "down") // Local only
	if got := m.FocusedFieldTitle(); got != "Local only" {
		t.Fatalf("focused = %q, want Local only", got)
	}
	m = keyPress(m, "space", "down")
	if m.BoolValue("Local only") {
		t.Error("space should have toggled Local only off")
	}
}
```

In `model_test.go`: delete the skipped tests whose behavior is now agent-section-specific and re-covered above (`TestSettingsExposeAgentAndToolFields`, `TestTabNavigatesBetweenFields`, `TestTypingUpdatesStringField`, `TestUpDownNavigatesBetweenFields`, `TestConfirmFieldTogglesOnLeft`, `TestNumericFieldValidatesAndWritesBack`). Keep and un-skip, updating to the two-pane reality: `TestNewModelHasFields` (assert `New` returns a model whose view contains "Agent"), `TestCancelReturnsCancelledMsg` (unchanged semantics), `TestSettingsViewKeepsFrameBounded` (assert no view line exceeds `m.width` after `SetSize(80, 30)`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run TestAgentPane -v`
Expected: FAIL (`newAgentPane` undefined / focused field empty on staticPane)

- [ ] **Step 3: Implement scalar.go and section_agent.go**

`scalar.go`:

```go
package settings

import (
	"fmt"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/tui/huhtheme"
)

type scalarPane struct {
	form  *huh.Form
	build func() *huh.Form
	width int
}

func newScalarPane(build func() *huh.Form) *scalarPane {
	p := &scalarPane{build: build}
	p.form = build()
	if c := p.form.Init(); c != nil {
		_ = c()
	}
	return p
}

func settingsKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c"))
	km.Input.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Confirm.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Select.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	// h/l are sidebar-return keys in the two-pane layout; keep arrows+space
	// for confirm toggling.
	km.Confirm.Toggle = key.NewBinding(key.WithKeys("right", "left", "space"))
	return km
}

// newSectionForm builds a huh form with the shared settings theme + keymap.
func newSectionForm(fields ...huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huhtheme.WarmSunset()).
		WithShowHelp(false).
		WithKeyMap(settingsKeyMap())
}

func numField(label string, value *string, min int, set func(int)) *huh.Input {
	return huh.NewInput().
		Key(label).
		Title(label).
		Value(value).
		Validate(func(s string) error {
			v, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("must be a number")
			}
			if min != 0 && v < min {
				v = min
			}
			*value = strconv.Itoa(v)
			set(v)
			return nil
		})
}

func (p *scalarPane) Init() tea.Cmd { return nil }

func (p *scalarPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	updated, cmd := p.form.Update(msg)
	if f, ok := updated.(*huh.Form); ok {
		p.form = f
	}
	if p.form.State == huh.StateCompleted || p.form.State == huh.StateAborted {
		// A pane form never "finishes": rebuild so editing can continue.
		p.form = p.build()
		p.form.WithWidth(p.width)
		if c := p.form.Init(); c != nil {
			_ = c()
		}
	}
	return p, cmd
}

func (p *scalarPane) View(width int) string { return p.form.View() }

func (p *scalarPane) SetWidth(w int) {
	p.width = w
	p.form.WithWidth(w)
}

func (p *scalarPane) HasInnerFocus() bool { return false }
func (p *scalarPane) CloseInner()         {}

func (p *scalarPane) FocusedFieldTitle() string {
	if f := p.form.GetFocusedField(); f != nil {
		return f.GetKey()
	}
	return ""
}
```

`section_agent.go`: move `activePresetFromConfig` and the old flat-form agent/preset field construction here, restructured as:

```go
package settings

import (
	"sort"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

type agentBuffers struct {
	provider, model                       string
	localOnly                             bool
	presetOpt                             string
	maxToolIter, maxRetries               string
	maxTurnTokens, subtaskIters           string
}

func newAgentPane(s *state) sectionPane {
	b := &agentBuffers{}
	return newScalarPane(func() *huh.Form { return buildAgentForm(s, b) })
}
```

`buildAgentForm(s *state, b *agentBuffers) *huh.Form` reproduces the old `New()` field list exactly (Default profile select, read-only Preset select, Provider/Model inputs with preset-or-agent write-back via `activePresetNameFor(s.cfg)`, Local only confirm) plus `numField("Max tool iterations", &b.maxToolIter, 1, …)`, `numField("Max retries", …)`, `numField("Max turn context tokens", &b.maxTurnTokens, 0, func(v int){ s.cfg.Agent.MaxTurnContextTokens = v })`, `numField("Subtask iterations", &b.subtaskIters, 0, func(v int){ s.cfg.Agent.SubtaskIterations = v })`, and `huh.NewConfirm().Key("Plan first").Title("Plan first").Value(&s.cfg.Agent.PlanFirst)`, all assembled with `newSectionForm(fields...)`. Buffers are seeded from `s.cfg` at the top of `buildAgentForm` (same `strconv.Itoa` seeding as the old `New()`). The "Remote providers allowed" confirm moves to the Privacy section (Task 5) — do not include it here.

In `sections.go`, replace the agent placeholder: `{id: "agent", title: "Agent", build: newAgentPane}`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS (agent tests + updated model tests + skeleton tests)

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): scalarPane and agent section"
```

---

### Task 5: masked.go + Privacy, Snapshots, Web sections

**Files:**
- Create: `internal/app/tui/settings/masked.go`
- Create: `internal/app/tui/settings/section_privacy.go`
- Create: `internal/app/tui/settings/section_snapshots.go`
- Create: `internal/app/tui/settings/section_web.go`
- Modify: `internal/app/tui/settings/sections.go` (three factories)
- Test: `internal/app/tui/settings/masked_test.go`, `internal/app/tui/settings/section_scalar_test.go`

**Interfaces:**
- Produces:
  - `func maskKey(key string) string` — `""` → `"(not set)"`; otherwise `"••••" + last-4 runes` (whole-string bullets if len < 4: `"••••"`).
  - `func secretField(title string, get func() string, set func(string)) *huh.Input` — an Input whose Value buffer starts **empty** with `Description("current: " + maskKey(get()) + " — type to replace, leave empty to keep · prefer the env-var field")`; its Validate writes `set(v)` only when the typed value is non-empty. This gives "masked display, clears on edit, empty keeps existing".
  - `newPrivacyPane(s *state) sectionPane` — Confirms: "Remote providers allowed" (`&s.cfg.Privacy.RemoteProvidersAllowed`), "Redact secrets", "Include gitignored files".
  - `newSnapshotsPane(s *state) sectionPane` — Confirm "Enabled" + numFields "Retention days" (min 0 → `RetentionDays`), "Max file bytes" (→ `MaxFileBytes`).
  - `newWebPane(s *state) sectionPane` — Confirm "Enabled"; Input "Fetch timeout" (string buffer seeded `s.cfg.Web.FetchTimeout.String()`, Validate parses `time.ParseDuration` and assigns); Inputs "Search provider", "Search URL" bound to cfg fields directly (huh Input can bind `Value(&s.cfg.Web.SearchProvider)`); `secretField("Search key", get s.cfg.Web.SearchKey, set …)`.

- [ ] **Step 1: Write the failing tests**

`masked_test.go`:

```go
package settings

import "testing"

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":                "(not set)",
		"abc":             "••••",
		"sk-live-1234":    "••••1234",
		"x-key-abcd-WXYZ": "••••WXYZ",
	}
	for in, want := range cases {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q, want %q", in, got, want)
		}
	}
}
```

`section_scalar_test.go`:

```go
package settings

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

// enterSection moves the sidebar cursor to the section with the given id
// and focuses its pane.
func enterSection(t *testing.T, m Model, id string) Model {
	t.Helper()
	for i, sec := range m.sections {
		if sec.id == id {
			m.cursor = i
			m.paneFocused = true
			return m
		}
	}
	t.Fatalf("no section %q", id)
	return m
}

func TestPrivacyPaneToggles(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "privacy")
	if got := m.FocusedFieldTitle(); got != "Remote providers allowed" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "space")
	if !m.state.cfg.Privacy.RemoteProvidersAllowed {
		t.Error("toggle did not write to working copy")
	}
}

func TestSnapshotsPaneNumericEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "snapshots")
	m = keyPress(m, "down") // Retention days
	if got := m.FocusedFieldTitle(); got != "Retention days" {
		t.Fatalf("focused = %q", got)
	}
	// clear the seeded "7" then type 14 and blur
	m = keyPress(m, "backspace", "1", "4", "down")
	if got := m.state.cfg.Snapshots.RetentionDays; got != 14 {
		t.Errorf("retention days = %d, want 14", got)
	}
}

func TestWebPaneMasksSearchKey(t *testing.T) {
	cfg := config.Default()
	cfg.Web.SearchKey = "sk-live-1234"
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "web")
	view := stripANSI(m.View())
	if strings.Contains(view, "sk-live-1234") {
		t.Error("raw search key must never render")
	}
	if !strings.Contains(view, "••••1234") {
		t.Error("masked search key should render")
	}
}

func TestWebPaneEmptySecretKeepsExisting(t *testing.T) {
	cfg := config.Default()
	cfg.Web.SearchKey = "sk-live-1234"
	cfg.Web.FetchTimeout = 30 * time.Second
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "web")
	// navigate to the last field (Search key) and blur without typing
	m = keyPress(m, "down", "down", "down", "down", "up")
	if got := m.state.cfg.Web.SearchKey; got != "sk-live-1234" {
		t.Errorf("blank secret edit must keep the old key, got %q", got)
	}
}
```

Add a `"backspace"` case to the `keyPress` helper in `skeleton_test.go`: `msg = tea.KeyPressMsg{Code: tea.KeyBackspace}`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestMaskKey|TestPrivacyPane|TestSnapshotsPane|TestWebPane' -v`
Expected: FAIL (`maskKey` undefined; sections still staticPane)

- [ ] **Step 3: Implement**

`masked.go`:

```go
package settings

// maskKey renders a secret for display: bullets plus the last four runes.
// The real value is never rendered; save paths always use the raw value.
func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	runes := []rune(key)
	if len(runes) < 4 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}
```

`secretField` goes in `masked.go` too:

```go
func secretField(title string, get func() string, set func(string)) *huh.Input {
	buf := new(string)
	return huh.NewInput().
		Key(title).
		Title(title).
		Value(buf).
		Description("current: " + maskKey(get()) + " — type to replace, leave empty to keep · prefer the env-var field").
		Validate(func(v string) error {
			if v != "" {
				set(v)
			}
			return nil
		})
}
```

(add the `charm.land/huh/v2` import). Note the Description is computed at form-build time; scalarPane's rebuild-on-complete refreshes it after edits.

`section_privacy.go`:

```go
package settings

import "charm.land/huh/v2"

func newPrivacyPane(s *state) sectionPane {
	return newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewConfirm().Key("Remote providers allowed").Title("Remote providers allowed").
				Description("allow remote providers globally").Value(&s.cfg.Privacy.RemoteProvidersAllowed),
			huh.NewConfirm().Key("Redact secrets").Title("Redact secrets").
				Description("scrub likely secrets from context sent to models").Value(&s.cfg.Privacy.RedactSecrets),
			huh.NewConfirm().Key("Include gitignored files").Title("Include gitignored files").
				Description("let indexing and context include gitignored paths").Value(&s.cfg.Privacy.IncludeGitignoredFiles),
		)
	})
}
```

`section_snapshots.go` follows the same shape: Confirm "Enabled" bound to `&s.cfg.Snapshots.Enabled`, then a `snapshotsBuffers{retentionDays, maxFileBytes string}` struct seeded with `strconv.Itoa` values and two `numField`s ("Retention days" → `s.cfg.Snapshots.RetentionDays`, "Max file bytes" → `s.cfg.Snapshots.MaxFileBytes`).

`section_web.go`:

```go
package settings

import (
	"fmt"
	"time"

	"charm.land/huh/v2"
)

func newWebPane(s *state) sectionPane {
	buf := &struct{ timeout string }{}
	return newScalarPane(func() *huh.Form {
		buf.timeout = s.cfg.Web.FetchTimeout.String()
		return newSectionForm(
			huh.NewConfirm().Key("Enabled").Title("Enabled").
				Description("allow web.fetch / web.search tools").Value(&s.cfg.Web.Enabled),
			huh.NewInput().Key("Fetch timeout").Title("Fetch timeout").Value(&buf.timeout).
				Validate(func(v string) error {
					d, err := time.ParseDuration(v)
					if err != nil {
						return fmt.Errorf("must be a duration like 30s")
					}
					s.cfg.Web.FetchTimeout = d
					return nil
				}),
			huh.NewInput().Key("Search provider").Title("Search provider").Value(&s.cfg.Web.SearchProvider),
			huh.NewInput().Key("Search URL").Title("Search URL").Value(&s.cfg.Web.SearchURL),
			secretField("Search key",
				func() string { return s.cfg.Web.SearchKey },
				func(v string) { s.cfg.Web.SearchKey = v }),
		)
	})
}
```

Wire all three factories in `sections.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): privacy, snapshots, web sections with masked secrets"
```

---

### Task 6: listStrings widget + mixedPane + Indexing and Commands sections

**Files:**
- Create: `internal/app/tui/settings/liststrings.go`
- Create: `internal/app/tui/settings/mixed.go`
- Create: `internal/app/tui/settings/section_indexing.go`
- Create: `internal/app/tui/settings/section_commands.go`
- Modify: `internal/app/tui/settings/sections.go`
- Test: `internal/app/tui/settings/liststrings_test.go`, `internal/app/tui/settings/mixed_test.go`

**Interfaces:**
- Produces:
  - `type listStrings struct { title string; items *[]string; cursor int; editing bool; adding bool; input textinput.Model; focused bool }` with `func newListStrings(title string, items *[]string) *listStrings`. Keys while focused: `up/down/k/j` move, `a` append (opens inline textinput), `enter`/`e` edit row inline, `d` delete row, inline edit: `enter` commits to `*items`, `esc` cancels. Methods: `Update(tea.KeyPressMsg) tea.Cmd`, `View(width int) string`, `Editing() bool` (inline input open), `CancelEdit()`, `Focus(bool)`, `Focused() bool`. `items` is a pointer to the slice inside `state.cfg`, so commits mutate the working copy directly.
  - `type mixedPane struct { form *scalarPane; lists []*listStrings; focusIdx int }` with `func newMixedPane(form *scalarPane, lists ...*listStrings) *mixedPane`. Implements `sectionPane`. Focus order: form (focusIdx 0) then each list. `tab` on the last form field is left to huh; explicit pane-level cycling: `ctrl+n` / `ctrl+p`? **No — keep it simple:** `tab` moves focusIdx forward (form → list1 → … → lastList → form), intercepted at mixedPane level before the form sees it; `shift+tab` at focusIdx 0 is NOT consumed (bubbles up to Model = back to sidebar), otherwise moves focusIdx back. `HasInnerFocus()` = any list `.Editing()`; `CloseInner()` cancels the active list's edit. `FocusedFieldTitle()` = form's when focusIdx==0, else the focused list's title.
  - `newIndexingPane(s *state) sectionPane` — mixedPane: form (Confirms "Use treesitter", "Use embeddings", "Summarise files" bound to `s.cfg.Indexing.*`) + list "Ignore patterns" (`&s.cfg.Indexing.Ignore`).
  - `newCommandsPane(s *state) sectionPane` — mixedPane: form (Inputs "Test command" → `&s.cfg.Commands.Test`, "Format command" → `&s.cfg.Commands.Format`, "Vet command" → `&s.cfg.Commands.Vet`, "Project name" → `&s.cfg.Project.Name`) + list "Languages" (`&s.cfg.Project.Languages`).
- Consumes: `charm.land/bubbles/v2/textinput`.

- [ ] **Step 1: Write the failing tests**

`liststrings_test.go`:

```go
package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func lsKey(l *listStrings, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		l.Update(msg)
	}
}

func TestListStringsAppend(t *testing.T) {
	items := []string{"one"}
	l := newListStrings("Ignore patterns", &items)
	l.Focus(true)
	lsKey(l, "a", "t", "w", "o", "enter")
	if len(items) != 2 || items[1] != "two" {
		t.Fatalf("items = %v, want [one two]", items)
	}
}

func TestListStringsEditInline(t *testing.T) {
	items := []string{"one", "two"}
	l := newListStrings("x", &items)
	l.Focus(true)
	lsKey(l, "down", "enter") // edit "two"
	if !l.Editing() {
		t.Fatal("enter should open inline edit")
	}
	lsKey(l, "!", "enter")
	if items[1] != "two!" {
		t.Fatalf("items[1] = %q, want two!", items[1])
	}
}

func TestListStringsEscCancelsEdit(t *testing.T) {
	items := []string{"one"}
	l := newListStrings("x", &items)
	l.Focus(true)
	lsKey(l, "enter", "z")
	l.CancelEdit()
	if l.Editing() || items[0] != "one" {
		t.Fatalf("cancel must discard the edit, items=%v", items)
	}
}

func TestListStringsDelete(t *testing.T) {
	items := []string{"one", "two"}
	l := newListStrings("x", &items)
	l.Focus(true)
	lsKey(l, "d")
	if len(items) != 1 || items[0] != "two" {
		t.Fatalf("items = %v, want [two]", items)
	}
}
```

`mixed_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestIndexingPaneTabCyclesToList(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "indexing")
	if got := m.FocusedFieldTitle(); got != "Use treesitter" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "tab")
	if got := m.FocusedFieldTitle(); got != "Ignore patterns" {
		t.Fatalf("tab should focus the ignore list, got %q", got)
	}
}

func TestIndexingIgnoreListEditMutatesConfig(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "indexing")
	m = keyPress(m, "tab", "a", "z", "enter")
	ignore := m.state.cfg.Indexing.Ignore
	if ignore[len(ignore)-1] != "z" {
		t.Fatalf("ignore = %v, want trailing z", ignore)
	}
}

func TestMixedPaneEscClosesInlineEditNotOverlay(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "indexing")
	m = keyPress(m, "tab", "a", "z")
	var cmd interface{}
	m, c := m.Update(keyMsg("esc"))
	cmd = c
	if cmd != nil {
		t.Fatal("first esc must close the inline edit, not emit CancelledMsg")
	}
	if m.activePane().HasInnerFocus() {
		t.Fatal("inline edit should be closed")
	}
	_ = cmd
}
```

Add a `keyMsg(k string) tea.KeyPressMsg` helper to `skeleton_test.go` extracting the switch from `keyPress` so both helpers share it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestListStrings|TestIndexing|TestMixedPane' -v`
Expected: FAIL (types undefined)

- [ ] **Step 3: Implement liststrings.go**

```go
package settings

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// listStrings is an inline-editable list of strings bound to a slice in the
// working config. Commits mutate *items directly.
type listStrings struct {
	title   string
	items   *[]string
	cursor  int
	adding  bool
	editing bool
	input   textinput.Model
	focused bool
}

func newListStrings(title string, items *[]string) *listStrings {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	return &listStrings{title: title, items: items, input: ti}
}

func (l *listStrings) Focus(on bool)   { l.focused = on }
func (l *listStrings) Focused() bool   { return l.focused }
func (l *listStrings) Editing() bool   { return l.adding || l.editing }

func (l *listStrings) CancelEdit() {
	l.adding = false
	l.editing = false
	l.input.Blur()
}

func (l *listStrings) clampCursor() {
	if l.cursor >= len(*l.items) {
		l.cursor = len(*l.items) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
}

func (l *listStrings) Update(msg tea.KeyPressMsg) tea.Cmd {
	if l.Editing() {
		switch msg.String() {
		case "enter":
			val := strings.TrimSpace(l.input.Value())
			if val != "" {
				if l.adding {
					*l.items = append(*l.items, val)
					l.cursor = len(*l.items) - 1
				} else {
					(*l.items)[l.cursor] = val
				}
			}
			l.CancelEdit()
			return nil
		case "esc":
			l.CancelEdit()
			return nil
		}
		var cmd tea.Cmd
		l.input, cmd = l.input.Update(msg)
		return cmd
	}

	switch msg.String() {
	case "up", "k":
		l.cursor--
		l.clampCursor()
	case "down", "j":
		l.cursor++
		l.clampCursor()
	case "a":
		l.adding = true
		l.input.SetValue("")
		l.input.Focus()
	case "enter", "e":
		if len(*l.items) > 0 {
			l.editing = true
			l.input.SetValue((*l.items)[l.cursor])
			l.input.CursorEnd()
			l.input.Focus()
		}
	case "d":
		if len(*l.items) > 0 {
			*l.items = append((*l.items)[:l.cursor], (*l.items)[l.cursor+1:]...)
			l.clampCursor()
		}
	}
	return nil
}

func (l *listStrings) View(width int) string {
	var b strings.Builder
	b.WriteString(l.title + "\n")
	if len(*l.items) == 0 && !l.adding {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, item := range *l.items {
		marker := "  "
		if l.focused && i == l.cursor {
			marker = "▸ "
		}
		if l.editing && i == l.cursor {
			b.WriteString(marker + l.input.View() + "\n")
			continue
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, item))
	}
	if l.adding {
		b.WriteString("▸ " + l.input.View() + "\n")
	}
	if l.focused && !l.Editing() {
		b.WriteString("  a add · e edit · d delete\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
```

- [ ] **Step 4: Implement mixed.go**

```go
package settings

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// mixedPane stacks a scalar form above one or more string lists, cycling
// focus with Tab. Section files compose it (shell, sandbox, indexing,
// commands).
type mixedPane struct {
	form     *scalarPane
	lists    []*listStrings
	focusIdx int // 0 = form, 1..n = lists[focusIdx-1]
}

func newMixedPane(form *scalarPane, lists ...*listStrings) *mixedPane {
	return &mixedPane{form: form, lists: lists}
}

func (p *mixedPane) activeList() *listStrings {
	if p.focusIdx == 0 {
		return nil
	}
	return p.lists[p.focusIdx-1]
}

func (p *mixedPane) Init() tea.Cmd { return p.form.Init() }

func (p *mixedPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && !p.HasInnerFocus() {
		switch k.String() {
		case "tab":
			p.setFocus((p.focusIdx + 1) % (len(p.lists) + 1))
			return p, nil
		case "shift+tab":
			if p.focusIdx > 0 {
				p.setFocus(p.focusIdx - 1)
				return p, nil
			}
			// focusIdx 0: let the parent Model take it (back to sidebar).
			return p, nil
		}
	}
	if l := p.activeList(); l != nil {
		if isKey {
			return p, l.Update(k)
		}
		return p, nil
	}
	updated, cmd := p.form.Update(msg)
	p.form = updated.(*scalarPane)
	return p, cmd
}

func (p *mixedPane) setFocus(idx int) {
	p.focusIdx = idx
	for i, l := range p.lists {
		l.Focus(i == idx-1)
	}
}

func (p *mixedPane) View(width int) string {
	parts := []string{p.form.View(width)}
	for _, l := range p.lists {
		parts = append(parts, l.View(width))
	}
	return strings.Join(parts, "\n\n")
}

func (p *mixedPane) SetWidth(w int) { p.form.SetWidth(w) }

func (p *mixedPane) HasInnerFocus() bool {
	l := p.activeList()
	return l != nil && l.Editing()
}

func (p *mixedPane) CloseInner() {
	if l := p.activeList(); l != nil {
		l.CancelEdit()
	}
}

func (p *mixedPane) FocusedFieldTitle() string {
	if l := p.activeList(); l != nil {
		return l.title
	}
	return p.form.FocusedFieldTitle()
}
```

One Model change: in `model.go`, the sidebar-return keys `h`/`left` must not steal keys from a focused list (where `h` is meaningless but `left` harmless) or inline edit. The existing guard (`!HasInnerFocus()`) already protects inline edits; additionally, `tab`/`shift+tab` must reach the pane before the Model's own tab handling. Reorder Model.Update: when `paneFocused`, forward `tab`/`shift+tab` to the pane FIRST; only treat `shift+tab` as "back to sidebar" when the pane didn't move its internal focus — implement by asking the pane: add nothing to the interface; instead, Model forwards `shift+tab` to the pane and the pane returns to the Model only when `focusIdx == 0` (mixedPane's case above returns without consuming). Concretely: Model's `shift+tab` branch checks a new optional interface `interface{ AtFirstFocus() bool }`; scalarPane returns `true` always, mixedPane returns `focusIdx == 0`. If `AtFirstFocus()`, Model takes focus back to the sidebar; otherwise it forwards the key to the pane.

```go
// in pane.go
type firstFocuser interface{ AtFirstFocus() bool }
```

`scalarPane.AtFirstFocus() bool { return true }`, `mixedPane.AtFirstFocus() bool { return p.focusIdx == 0 }`, `staticPane.AtFirstFocus() bool { return true }`. Model.Update's pane-focused branch becomes:

```go
		if !m.activePane().HasInnerFocus() {
			switch k.String() {
			case "shift+tab":
				if ff, ok := m.activePane().(firstFocuser); !ok || ff.AtFirstFocus() {
					m.paneFocused = false
					return *m, nil
				}
			case "h", "left":
				if ff, ok := m.activePane().(firstFocuser); !ok || ff.AtFirstFocus() {
					m.paneFocused = false
					return *m, nil
				}
			}
		}
```

(`tab` is simply forwarded — mixedPane consumes it to cycle; scalarPane's huh form uses it for next-field.)

- [ ] **Step 5: Implement the two sections and wire factories**

`section_indexing.go`:

```go
package settings

import "charm.land/huh/v2"

func newIndexingPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewConfirm().Key("Use treesitter").Title("Use treesitter").Value(&s.cfg.Indexing.UseTreesitter),
			huh.NewConfirm().Key("Use embeddings").Title("Use embeddings").Value(&s.cfg.Indexing.UseEmbeddings),
			huh.NewConfirm().Key("Summarise files").Title("Summarise files").Value(&s.cfg.Indexing.SummariseFiles),
		)
	})
	return newMixedPane(form, newListStrings("Ignore patterns", &s.cfg.Indexing.Ignore))
}
```

`section_commands.go`:

```go
package settings

import "charm.land/huh/v2"

func newCommandsPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewInput().Key("Test command").Title("Test command").Value(&s.cfg.Commands.Test),
			huh.NewInput().Key("Format command").Title("Format command").Value(&s.cfg.Commands.Format),
			huh.NewInput().Key("Vet command").Title("Vet command").Value(&s.cfg.Commands.Vet),
			huh.NewInput().Key("Project name").Title("Project name").Value(&s.cfg.Project.Name),
		)
	})
	return newMixedPane(form, newListStrings("Languages", &s.cfg.Project.Languages))
}
```

Wire both factories in `sections.go`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): listStrings widget, mixedPane, indexing and commands sections"
```

---

### Task 7: Shell and Sandbox sections

**Files:**
- Create: `internal/app/tui/settings/section_shell.go`
- Create: `internal/app/tui/settings/section_sandbox.go`
- Modify: `internal/app/tui/settings/sections.go`
- Test: `internal/app/tui/settings/section_shell_test.go`

**Interfaces:**
- Consumes: `newScalarPane`, `newSectionForm`, `numField`, `newMixedPane`, `newListStrings` (Tasks 4/6).
- Produces:
  - `newShellPane(s *state) sectionPane` — mixedPane. Form buffers struct `shellBuffers{timeout, maxOutput, maxJobs, retention string}` seeded from cfg. Fields: numField "Default timeout (s)" → `DefaultTimeoutSeconds`; numField "Max output bytes" → `MaxOutputBytes`; numField "Max background jobs" → `MaxBackgroundJobs`; Input "Background retention" (Validate `time.ParseDuration` → `BackgroundRetention`); Confirms "Allow network", "Allow sudo", "Allow destructive", "Auto-approve shell" bound to cfg; Select[string] "Dynamic argv0 guardrail" with options `deny`, `confirm`, `allow` bound to `&s.cfg.Tools.Shell.GuardrailDynamicArgv0`. Lists: "Allow commands" (`&s.cfg.Tools.Shell.Allow.Commands`), "Confirm commands" (`&s.cfg.Tools.Shell.Confirm.Commands`), "Deny patterns" (`&s.cfg.Tools.Shell.Deny.Patterns`).
  - `newSandboxPane(s *state) sectionPane` — mixedPane. Form: Select[string] "Backend" options `restricted`, `container`, `passthrough` → `&s.cfg.Tools.Shell.Sandbox.Backend`; numFields "Memory limit (MB)", "CPU seconds", "Max processes", "File size limit (MB)" → the four int fields; Input "Container runtime" → `&s.cfg.Tools.Shell.Sandbox.ContainerRuntime`; Input "Container image" → `&s.cfg.Tools.Shell.Sandbox.ContainerImage`; Confirm "Allow fallback" → `&s.cfg.Tools.Shell.Sandbox.AllowFallback`. Lists: "Env allowlist" (`&s.cfg.Tools.Shell.Sandbox.EnvAllowlist`), "Env denylist" (`&s.cfg.Tools.Shell.Sandbox.EnvDenylist`).

- [ ] **Step 1: Write the failing tests**

`section_shell_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestShellPaneTogglesAndLists(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "shell")
	if got := m.FocusedFieldTitle(); got != "Default timeout (s)" {
		t.Fatalf("focused = %q", got)
	}
	// tab to the first list and add an allow command
	m = keyPress(m, "tab")
	if got := m.FocusedFieldTitle(); got != "Allow commands" {
		t.Fatalf("tab should reach Allow commands, got %q", got)
	}
	m = keyPress(m, "a", "l", "s", "enter")
	cmds := m.state.cfg.Tools.Shell.Allow.Commands
	if cmds[len(cmds)-1] != "ls" {
		t.Fatalf("allow commands = %v", cmds)
	}
}

func TestSandboxPaneBackendSelect(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "sandbox")
	if got := m.FocusedFieldTitle(); got != "Backend" {
		t.Fatalf("focused = %q", got)
	}
}

func TestSandboxEnvListsEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "sandbox")
	m = keyPress(m, "tab", "tab") // form → allowlist → denylist
	if got := m.FocusedFieldTitle(); got != "Env denylist" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "a", "F", "O", "O", "enter")
	deny := m.state.cfg.Tools.Shell.Sandbox.EnvDenylist
	if len(deny) != 1 || deny[0] != "FOO" {
		t.Fatalf("denylist = %v", deny)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestShellPane|TestSandbox' -v`
Expected: FAIL (sections still staticPane)

- [ ] **Step 3: Implement both sections**

`section_shell.go` (full field list as specified in Interfaces; representative shape):

```go
package settings

import (
	"fmt"
	"strconv"
	"time"

	"charm.land/huh/v2"
)

type shellBuffers struct {
	timeout, maxOutput, maxJobs, retention string
}

func newShellPane(s *state) sectionPane {
	b := &shellBuffers{}
	form := newScalarPane(func() *huh.Form {
		b.timeout = strconv.Itoa(s.cfg.Tools.Shell.DefaultTimeoutSeconds)
		b.maxOutput = strconv.Itoa(s.cfg.Tools.Shell.MaxOutputBytes)
		b.maxJobs = strconv.Itoa(s.cfg.Tools.Shell.MaxBackgroundJobs)
		b.retention = s.cfg.Tools.Shell.BackgroundRetention.String()
		return newSectionForm(
			numField("Default timeout (s)", &b.timeout, 0, func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v }),
			numField("Max output bytes", &b.maxOutput, 0, func(v int) { s.cfg.Tools.Shell.MaxOutputBytes = v }),
			numField("Max background jobs", &b.maxJobs, 0, func(v int) { s.cfg.Tools.Shell.MaxBackgroundJobs = v }),
			huh.NewInput().Key("Background retention").Title("Background retention").Value(&b.retention).
				Validate(func(v string) error {
					d, err := time.ParseDuration(v)
					if err != nil {
						return fmt.Errorf("must be a duration like 8h")
					}
					s.cfg.Tools.Shell.BackgroundRetention = d
					return nil
				}),
			huh.NewConfirm().Key("Allow network").Title("Allow network").Value(&s.cfg.Tools.Shell.AllowNetwork),
			huh.NewConfirm().Key("Allow sudo").Title("Allow sudo").Value(&s.cfg.Tools.Shell.AllowSudo),
			huh.NewConfirm().Key("Allow destructive").Title("Allow destructive").Value(&s.cfg.Tools.Shell.AllowDestructive),
			huh.NewConfirm().Key("Auto-approve shell").Title("Auto-approve shell").Value(&s.cfg.Tools.Shell.AutoApprove),
			huh.NewSelect[string]().Key("Dynamic argv0 guardrail").Title("Dynamic argv0 guardrail").
				Options(huh.NewOption("deny", "deny"), huh.NewOption("confirm", "confirm"), huh.NewOption("allow", "allow")).
				Value(&s.cfg.Tools.Shell.GuardrailDynamicArgv0),
		)
	})
	return newMixedPane(form,
		newListStrings("Allow commands", &s.cfg.Tools.Shell.Allow.Commands),
		newListStrings("Confirm commands", &s.cfg.Tools.Shell.Confirm.Commands),
		newListStrings("Deny patterns", &s.cfg.Tools.Shell.Deny.Patterns),
	)
}
```

`section_sandbox.go` follows the identical pattern with the Interfaces field list (buffers struct `sandboxBuffers{memory, cpu, procs, fileSize string}`, Select for Backend, two env lists). Wire both factories in `sections.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): shell and sandbox sections"
```

### Task 8: collectionPane + Providers section

Builds the generic list + sub-form editor used by the five collection/map-keyed sections (providers, presets, mcp, hooks, permissions). Providers is the first consumer and exercises the masked-secret path from Task 5 inside a sub-form.

**Files:**
- Create: `internal/app/tui/settings/collection.go`
- Create: `internal/app/tui/settings/section_providers.go`
- Modify: `internal/app/tui/settings/sections.go` (providers factory)
- Test: `internal/app/tui/settings/collection_test.go`, `internal/app/tui/settings/section_providers_test.go`

**Interfaces:**
- Produces:
  - `type collectionEntry interface { Title() string; Key() string }` — a snapshot of one entry returned by the section's accessor; `Key()` is the map key or a stable identifier, `Title()` is the list display label.
  - `type collectionSpec struct { heading string; entries func(s *state) []collectionEntry; add func(s *state, key string) error; editForm func(s *state, key string) *huh.Form; delete func(s *state, key string); keyPrompt string }` — the per-section behavior the generic pane drives. `add` validates uniqueness (returns error on dup/empty); `editForm` returns a huh form bound to the working-copy entry for `key`; `delete` removes the entry.
  - `type collectionPane struct { spec collectionSpec; s *state; cursor int; adding bool; nameInput textinput.Model; form *huh.Form; editingKey string; width int }` with `func newCollectionPane(s *state, spec collectionSpec) *collectionPane`. Implements `sectionPane`.
    - Top-level list state: `↑/↓`/`k`/`j` move cursor; `a` opens the name prompt (inline textinput, same widget style as `listStrings`); `e`/`enter` opens `editForm`; `d` deletes the focused entry (no confirm — deletion of a config entry is reversible by re-adding and the dirty indicator makes it visible). `h`/`shift+tab`/`left` handled by Model (returns to sidebar).
    - Name-prompt state: `enter` calls `spec.add`; on error the prompt stays open with the error rendered below the input; `esc` cancels back to the list.
    - Sub-form state: the huh form owns all keys; `enter`/`ctrl+s` submits → on `StateCompleted` the pane returns to the list (commit happened via the form's field callbacks writing to the working copy). `esc` aborts → `StateAborted` → return to list without committing (the form edited local buffers, so abort discards). **Important:** the sub-form must NOT bind to working-copy fields by pointer if we want cancel to be a true discard — instead `editForm` binds to a fresh local value struct and a submit callback commits into the working copy. See Step 3 for the commit pattern.
    - `HasInnerFocus()` = `adding || form != nil`. `CloseInner()` cancels the deepest open thing (name prompt, then sub-form). `FocusedFieldTitle()` = list heading at top level, the prompt label `"New entry name"` while adding, else the form's focused field key.
    - `AtFirstFocus() bool { return true }` (collection panes never have an internal tab cycle, so `shift+tab` always returns to the sidebar).
  - `newProvidersPane(s *state) sectionPane` — `collectionSpec`: heading `"Providers"`; `entries` returns sorted `[]collectionEntry` of `cfg.Providers`; `add` inserts `cfg.Providers[key] = ProviderConfig{Type: "openai_compatible"}` after checking `key != ""` and not-already-present; `editForm` builds a form with Inputs "Type", "Base URL", "API key env" bound to the entry's `Type`/`BaseURL`/`APIKeyEnv` plus a `secretField("API key", …)` for `APIKey`; `delete` removes the key.
- Consumes: `secretField`, `maskKey` (Task 5), `newSectionForm`, `settingsKeyMap` (Task 4), `charm.land/bubbles/v2/textinput`.

- [ ] **Step 1: Write the failing tests**

`collection_test.go` — a generic harness that uses a throwaway in-memory `state` and a minimal `collectionSpec` so the pane logic is tested without coupling to any real section:

```go
package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

// fakeEntry is a minimal collectionEntry for testing the generic pane.
type fakeEntry struct{ key string }

func (f fakeEntry) Title() string { return f.key }
func (f fakeEntry) Key() string   { return f.key }

func fakeSpec(s *state) collectionSpec {
	return collectionSpec{
		heading: "Fake",
		keyPrompt: "New entry name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Providers))
			for k := range s.cfg.Providers {
				out = append(out, fakeEntry{key: k})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return errEmpty
			}
			if _, ok := s.cfg.Providers[key]; ok {
				return errDuplicate
			}
			s.cfg.Providers[key] = config.ProviderConfig{}
			return nil
		},
		editForm: func(s *state, key string) *huh.Form {
			entry := s.cfg.Providers[key]
			return newSectionForm(
				huh.NewInput().Key("Base URL").Title("Base URL").Value(&entry.BaseURL),
			)
		},
		delete: func(s *state, key string) { delete(s.cfg.Providers, key) },
	}
}

var (
	errEmpty    = fmt.Errorf("name cannot be empty")
	errDuplicate = fmt.Errorf("entry already exists")
)
```

(Add `"fmt"` and `"charm.land/huh/v2"` to the test imports.)

Then the test bodies:

```go
func TestCollectionPaneAddEntry(t *testing.T) {
	st := newState(config.Default())
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // clamp
	p.Update(tea.KeyPressMsg{Code: rune('a'), Text: "a"})
	if !p.adding {
		t.Fatal("a should open the name prompt")
	}
	p.Update(tea.KeyPressMsg{Code: rune('f'), Text: "f"})
	p.Update(tea.KeyPressMsg{Code: rune('s'), Text: "s"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := st.cfg.Providers["fs"]; !ok {
		t.Fatal("enter on the name prompt should add the entry")
	}
	if p.adding {
		t.Fatal("prompt should close after a successful add")
	}
}

func TestCollectionPaneDuplicateRejected(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"fs": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: rune('a'), Text: "a"})
	p.Update(tea.KeyPressMsg{Code: rune('f'), Text: "f"})
	p.Update(tea.KeyPressMsg{Code: rune('s'), Text: "s"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.adding {
		t.Fatal("duplicate add should keep the prompt open")
	}
	if _, ok := p.View(60), 0; false {
	}
	if !strings.Contains(stripANSI(p.View(60)), "already exists") {
		t.Error("duplicate error should render")
	}
}

func TestCollectionPaneDeleteEntry(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"a": {}, "b": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: rune('d'), Text: "d"})
	if _, ok := st.cfg.Providers["a"]; ok {
		t.Fatal("d should delete the focused entry")
	}
}

func TestCollectionPaneEscClosesFormNotOverlay(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"a": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open edit form
	if p.form == nil {
		t.Fatal("enter should open the edit form")
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if p.form != nil {
		t.Fatal("esc should close the sub-form")
	}
	if p.HasInnerFocus() {
		t.Fatal("after closing the form the pane should not report inner focus")
	}
}

func TestCollectionPaneSubFormCommit(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"a": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.Update(tea.KeyPressMsg{Code: rune('x'), Text: "x"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // submit
	if p.form != nil {
		t.Fatal("submit should close the form")
	}
	if got := st.cfg.Providers["a"].BaseURL; got != "x" {
		t.Fatalf("submit should commit to the working copy, got %q", got)
	}
}
```

(Add `"strings"` to imports.)

`section_providers_test.go` — integration through the Model so the masked-secret and sorted-list behavior is verified end to end:

```go
package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func providersTestConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "sk-real-1234", APIKeyEnv: "OLLAMA_KEY", ToolCalling: true},
	}
	return cfg
}

func TestProvidersPaneMasksApiKey(t *testing.T) {
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	view := stripANSI(m.View())
	if strings.Contains(view, "sk-real-1234") {
		t.Error("raw API key must never render in the providers list")
	}
}

func TestProvidersPaneAddAndEdit(t *testing.T) {
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	m = keyPress(m, "a", "a", "n", "t", "h", "r", "o", "p", "i", "c", "enter")
	if _, ok := m.state.cfg.Providers["anthropic"]; !ok {
		t.Fatal("add should create the anthropic provider entry")
	}
}

func TestProvidersPaneSubFormMasksKey(t *testing.T) {
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	m = keyPress(m, "enter") // edit the existing ollama entry
	view := stripANSI(m.View())
	if strings.Contains(view, "sk-real-1234") {
		t.Error("raw API key must never render inside the sub-form")
	}
	if !strings.Contains(view, "••••1234") {
		t.Error("masked key should render in the sub-form description")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestCollectionPane|TestProvidersPane' -v`
Expected: FAIL (`collectionPane`, `collectionSpec`, `collectionEntry` undefined)

- [ ] **Step 3: Implement collection.go**

```go
package settings

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// collectionEntry is one row in a collection pane's list.
type collectionEntry interface {
	Title() string
	Key() string
}

// collectionSpec describes a map-keyed section's list+sub-form behavior.
type collectionSpec struct {
	heading   string
	keyPrompt string
	entries   func(s *state) []collectionEntry
	add       func(s *state, key string) error
	editForm  func(s *state, key string) *huh.Form
	delete    func(s *state, key string)
}

// collectionPane is the generic list + sub-form editor. It owns three
// interaction states: the entry list, a name-prompt (for add), and a huh
// sub-form (for edit). Sub-forms edit a local copy and commit on submit so
// Esc is a true discard.
type collectionPane struct {
	spec       collectionSpec
	s          *state
	cursor     int
	adding     bool
	addErr     string
	nameInput  textinput.Model
	form       *huh.Form
	editingKey string
	width      int
}

func newCollectionPane(s *state, spec collectionSpec) *collectionPane {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	return &collectionPane{spec: spec, s: s, nameInput: ti}
}

func (p *collectionPane) Init() tea.Cmd { return nil }

func (p *collectionPane) AtFirstFocus() bool { return true }

func (p *collectionPane) sortedEntries() []collectionEntry {
	es := p.spec.entries(p.s)
	sort.Slice(es, func(i, j int) bool { return es[i].Key() < es[j].Key() })
	return es
}

func (p *collectionPane) clamp() {
	n := len(p.sortedEntries())
	if p.cursor >= n {
		p.cursor = n - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *collectionPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		if p.form != nil {
			updated, cmd := p.form.Update(msg)
			if f, ok := updated.(*huh.Form); ok {
				p.form = f
			}
			p.checkFormDone()
			return p, cmd
		}
		return p, nil
	}

	// Name-prompt state.
	if p.adding {
		switch k.String() {
		case "enter":
			key := strings.TrimSpace(p.nameInput.Value())
			if err := p.spec.add(p.s, key); err != nil {
				p.addErr = err.Error()
				return p, nil
			}
			p.adding = false
			p.addErr = ""
			p.cursor = len(p.sortedEntries()) - 1
			return p, nil
		case "esc":
			p.adding = false
			p.addErr = ""
			return p, nil
		}
		var cmd tea.Cmd
		p.nameInput, cmd = p.nameInput.Update(k)
		return p, cmd
	}

	// Sub-form state.
	if p.form != nil {
		if k.String() == "esc" {
			p.form = nil
			p.editingKey = ""
			return p, nil
		}
		updated, cmd := p.form.Update(k)
		if f, ok := updated.(*huh.Form); ok {
			p.form = f
		}
		p.checkFormDone()
		return p, cmd
	}

	// List state.
	switch k.String() {
	case "up", "k":
		p.cursor--
		p.clamp()
	case "down", "j":
		p.cursor++
		p.clamp()
	case "a":
		p.adding = true
		p.addErr = ""
		p.nameInput.SetValue("")
		p.nameInput.Focus()
	case "enter", "e":
		es := p.sortedEntries()
		if len(es) > 0 {
			p.editingKey = es[p.cursor].Key()
			p.form = p.spec.editForm(p.s, p.editingKey)
			p.form.WithWidth(p.width)
			if c := p.form.Init(); c != nil {
				_ = c()
			}
		}
	case "d":
		es := p.sortedEntries()
		if len(es) > 0 {
			p.spec.delete(p.s, es[p.cursor].Key())
			p.clamp()
		}
	}
	return p, nil
}

// checkFormDone closes the sub-form when huh reaches a terminal state.
// Commit-on-submit is handled by the editForm's field callbacks (see
// section files); abort (Esc) is handled in Update above and discards.
func (p *collectionPane) checkFormDone() {
	if p.form == nil {
		return
	}
	if p.form.State == huh.StateCompleted || p.form.State == huh.StateAborted {
		p.form = nil
		p.editingKey = ""
	}
}

func (p *collectionPane) SetWidth(w int) {
	p.width = w
	if p.form != nil {
		p.form.WithWidth(w)
	}
}

func (p *collectionPane) HasInnerFocus() bool { return p.adding || p.form != nil }

func (p *collectionPane) CloseInner() {
	if p.adding {
		p.adding = false
		p.addErr = ""
		return
	}
	p.form = nil
	p.editingKey = ""
}

func (p *collectionPane) FocusedFieldTitle() string {
	if p.adding {
		return p.spec.keyPrompt
	}
	if p.form != nil {
		if f := p.form.GetFocusedField(); f != nil {
			return f.GetKey()
		}
		return p.spec.heading
	}
	return p.spec.heading
}

func (p *collectionPane) View(width int) string {
	if p.form != nil {
		return p.form.View()
	}
	if p.adding {
		return p.spec.keyPrompt + "\n▸ " + p.nameInput.View() +
			renderAddErr(p.addErr)
	}
	var b strings.Builder
	b.WriteString(p.spec.heading + "\n")
	es := p.sortedEntries()
	if len(es) == 0 {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, e := range es {
		marker := "  "
		if i == p.cursor {
			marker = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, e.Title()))
	}
	b.WriteString("  a add · e edit · d delete")
	return strings.TrimRight(b.String(), "\n")
}

func renderAddErr(err string) string {
	if err == "" {
		return ""
	}
	return "\n" + warnStyle.Render("⚠ " + err)
}
```

> **Note on sub-form commit vs discard:** the `editForm` builder in each section file binds fields to a **fresh local copy** of the entry, then installs a submit callback (via the form's `WithSubmitFunc` or a trailing hidden field's Validate that fires on the last field's blur) that copies the local copy back into the working copy. This is what makes Esc a true discard. A simpler-but-acceptable alternative is binding directly to the working copy and treating Esc as "revert this entry" — but that violates the spec's "sub-forms edit a local copy and commit on submit" contract, so use the local-copy pattern. The providers section below shows the pattern; subsequent sections copy it.

- [ ] **Step 4: Implement section_providers.go**

```go
package settings

import (
	"fmt"
	"sort"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type providerEntry struct {
	key string
	cfg config.ProviderConfig
}

func (p providerEntry) Title() string {
	return p.key + "  (" + maskKey(p.cfg.APIKey) + ")"
}
func (p providerEntry) Key() string { return p.key }

func newProvidersPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Providers",
		keyPrompt: "New provider name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Providers))
			for k, pc := range s.cfg.Providers {
				out = append(out, providerEntry{key: k, cfg: pc})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Providers[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.Providers[key] = config.ProviderConfig{Type: "openai_compatible"}
			return nil
		},
		editForm: func(s *state, key string) *huh.Form {
			local := s.cfg.Providers[key] // copy
			return newSectionForm(
				huh.NewInput().Key("Type").Title("Type").Value(&local.Type),
				huh.NewInput().Key("Base URL").Title("Base URL").Value(&local.BaseURL),
				huh.NewInput().Key("API key env").Title("API key env").
					Description("env var name resolved at provider construction — preferred over storing the key").
					Value(&local.APIKeyEnv),
				secretField("API key",
					func() string { return s.cfg.Providers[key].APIKey },
					func(v string) { local.APIKey = v }),
				huh.NewConfirm().Key("Tool calling").Title("Tool calling").
					Description("provider advertises native tool-calling support").
					Value(&local.ToolCalling),
			).WithSubmitFunc(func() {
				s.cfg.Providers[key] = local // commit
			})
		},
		delete: func(s *state, key string) { delete(s.cfg.Providers, key) },
	}
	_ = sort.Strings // keep sort import if not used elsewhere
	return newCollectionPane(s, spec)
}
```

> The `secretField` "get" closure reads the **live** working-copy value (so the masked description shows the current key), while "set" writes to the **local** copy (so an empty/aborted edit doesn't clobber the real key). On submit, `local` — including any newly-typed key — is committed. If the user blurs the secret field empty, `secretField`'s Validate skips the set, so `local.APIKey` keeps its zero value `""` — **but** that would overwrite the real key on commit. To preserve "empty keeps existing", seed `local.APIKey = s.cfg.Providers[key].APIKey` before building the form so an untouched field commits the existing value back unchanged. Update the `editForm` to do that seeding.

Wire the factory in `sections.go`: `{id: "providers", title: "Providers", build: newProvidersPane}`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS (collection tests + providers tests + all prior tasks)

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): collectionPane and providers section with masked API key"
```

---

### Task 9: Presets section

Reuses `collectionPane` for `Models.Presets`. Introduces float-field helpers (Temperature/TopP) since `numField` only handles ints.

**Files:**
- Create: `internal/app/tui/settings/section_presets.go`
- Modify: `internal/app/tui/settings/sections.go`
- Test: `internal/app/tui/settings/section_presets_test.go`

**Interfaces:**
- Produces:
  - `func floatField(label string, value *string, set func(float64)) *huh.Input` — a string-buffer input that validates `strconv.ParseFloat` and writes the parsed value via `set`. Co-located in `scalar.go` (next to `numField`).
  - `newPresetsPane(s *state) sectionPane` — `collectionSpec`: heading `"Model Presets"`; `entries` returns sorted presets with `Title()` = `key` + `" (" + Provider + "/" + Model + ")"`; `add` inserts `cfg.Models.Presets[key] = routing.ModelPreset{Name: key}`; `editForm` builds Inputs "Provider", "Model", `numField("Context window", …→ContextWindow)`, `numField("Max output tokens", …→MaxOutputTokens)`, `floatField("Temperature", …→Temperature)`, `floatField("Top P", …→TopP)`, Select[string] "Tool calling" options `native`,`simulated`,`none` → `ToolCalling`, Select[string] "Reasoning effort" options `low`,`medium`,`high`,`none` → `ReasoningEffort`, Confirm "Local only" → `LocalOnly`. Commit-on-submit via the local-copy + `WithSubmitFunc` pattern from Task 8.
- Consumes: `collectionPane` (Task 8), `numField`, `newSectionForm`, `marshal/internal/llm/routing`.

- [ ] **Step 1: Write the failing tests**

`section_presets_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func presetsTestConfig() config.Config {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Name: "fast", Provider: "ollama", Model: "qwen3", Temperature: 0.2, ToolCalling: "native", LocalOnly: true},
	}
	return cfg
}

func TestPresetsPaneListsEntries(t *testing.T) {
	m := New(presetsTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "presets")
	if got := m.FocusedFieldTitle(); got != "Model Presets" {
		t.Fatalf("focused = %q, want Model Presets", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "fast") || !strings.Contains(view, "ollama/qwen3") {
		t.Errorf("preset list should show name + provider/model:\n%s", view)
	}
}

func TestPresetsPaneAddEntry(t *testing.T) {
	m := New(presetsTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "presets")
	m = keyPress(m, "a", "b", "a", "l", "a", "n", "c", "e", "enter")
	if _, ok := m.state.cfg.Models.Presets["balance"]; !ok {
		t.Fatal("add should create the balance preset")
	}
}

func TestPresetsPaneEditTemperature(t *testing.T) {
	m := New(presetsTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "presets")
	m = keyPress(m, "enter") // open sub-form for "fast"
	// Navigate to the Temperature field. Field order: Provider, Model,
	// Context window, Max output tokens, Temperature (index 4).
	for i := 0; i < 4; i++ {
		m = keyPress(m, "down")
	}
	if got := m.FocusedFieldTitle(); got != "Temperature" {
		t.Fatalf("focused = %q, want Temperature", got)
	}
}
```

(Add `"strings"` to imports if not already present in the file.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run TestPresetsPane -v`
Expected: FAIL (presets still staticPane)

- [ ] **Step 3: Implement**

Add `floatField` to `scalar.go` (next to `numField`):

```go
func floatField(label string, value *string, set func(float64)) *huh.Input {
	return huh.NewInput().
		Key(label).
		Title(label).
		Value(value).
		Validate(func(s string) error {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("must be a number")
			}
			*value = strconv.FormatFloat(v, 'f', -1, 64)
			set(v)
			return nil
		})
}
```

`section_presets.go`:

```go
package settings

import (
	"fmt"
	"strconv"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

type presetEntry struct {
	key    string
	preset routing.ModelPreset
}

func (p presetEntry) Title() string {
	return p.key + "  (" + p.preset.Provider + "/" + p.preset.Model + ")"
}
func (p presetEntry) Key() string { return p.key }

func newPresetsPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Model Presets",
		keyPrompt: "New preset name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Models.Presets))
			for k, p := range s.cfg.Models.Presets {
				out = append(out, presetEntry{key: k, preset: p})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Models.Presets[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.Models.Presets[key] = routing.ModelPreset{Name: key}
			return nil
		},
		editForm: func(s *state, key string) *huh.Form {
			local := s.cfg.Models.Presets[key]
			b := &struct {
				ctx, maxOut, temp, topP string
			}{
				ctx:    strconv.Itoa(local.ContextWindow),
				maxOut: strconv.Itoa(local.MaxOutputTokens),
				temp:   strconv.FormatFloat(local.Temperature, 'f', -1, 64),
				topP:   strconv.FormatFloat(local.TopP, 'f', -1, 64),
			}
			return newSectionForm(
				huh.NewInput().Key("Provider").Title("Provider").Value(&local.Provider),
				huh.NewInput().Key("Model").Title("Model").Value(&local.Model),
				numField("Context window", &b.ctx, 0, func(v int) { local.ContextWindow = v }),
				numField("Max output tokens", &b.maxOut, 0, func(v int) { local.MaxOutputTokens = v }),
				floatField("Temperature", &b.temp, func(v float64) { local.Temperature = v }),
				floatField("Top P", &b.topP, func(v float64) { local.TopP = v }),
				huh.NewSelect[string]().Key("Tool calling").Title("Tool calling").
					Options(huh.NewOption("native", "native"), huh.NewOption("simulated", "simulated"), huh.NewOption("none", "none")).
					Value(&local.ToolCalling),
				huh.NewSelect[string]().Key("Reasoning effort").Title("Reasoning effort").
					Options(huh.NewOption("low", "low"), huh.NewOption("medium", "medium"), huh.NewOption("high", "high"), huh.NewOption("none", "none")).
					Value(&local.ReasoningEffort),
				huh.NewConfirm().Key("Local only").Title("Local only").
					Description("block remote providers for this preset").
					Value(&local.LocalOnly),
			).WithSubmitFunc(func() {
				s.cfg.Models.Presets[key] = local
			})
		},
		delete: func(s *state, key string) { delete(s.cfg.Models.Presets, key) },
	}
	return newCollectionPane(s, spec)
}
```

Wire the factory in `sections.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): model presets section with float fields"
```

---

### Task 10: Hooks and Permissions sections (slice collections)

These two sections are backed by **slices**, not maps: `Hooks.Entries []HookConfig` and `Permissions.Rules []PermissionRule`. They reuse `collectionPane` with synthetic string keys (slice index or a composite label) since the pane is keyed by `collectionEntry.Key()`.

**Files:**
- Create: `internal/app/tui/settings/section_hooks.go`
- Create: `internal/app/tui/settings/section_permissions.go`
- Modify: `internal/app/tui/settings/sections.go`
- Test: `internal/app/tui/settings/section_hooks_test.go`, `internal/app/tui/settings/section_permissions_test.go`

**Interfaces:**
- Produces:
  - `newHooksPane(s *state) sectionPane` — `collectionSpec`: heading `"Hooks"`. Because hooks are a slice with no natural key, `entries` returns entries keyed by a stable synthetic id `fmt.Sprintf("hook-%d", i)` with `Title()` = `entry.Event + " " + entry.Matcher + " → " + entry.Command`. `add` appends `s.cfg.Hooks.Entries = append(..., HookConfig{Event: "pre_tool"})` (a sensible default the user then edits). `editForm` builds Inputs "Event", "Matcher", "Command" + `numField("Timeout (ms)", …→TimeoutMS)` bound to a local copy; commit via `WithSubmitFunc` writing `s.cfg.Hooks.Entries[idx] = local`. `delete` removes `s.cfg.Hooks.Entries[idx]` via slice reslicing. The `editForm`/`delete` closures capture `idx` by looking up the entry's position from its `Key()` (parse the `hook-%d` suffix) at call time — **do not** close over a stale index, because add/delete shifts indices. Add a `hookPane` wrapper struct if needed to thread the index lookup cleanly.
  - `newPermissionsPane(s *state) sectionPane` — `collectionSpec`: heading `"Permissions"`. `entries` returns `PermissionRule` rows keyed `fmt.Sprintf("rule-%d", i)` with `Title()` = `rule.Permission + " " + rule.Pattern + " → " + rule.Action`. `add` appends `PermissionRule{Permission: "shell", Pattern: "*", Action: "confirm"}`. `editForm`: Inputs "Permission", "Pattern", "Action" + Select[string] "Action" options `allow`,`confirm`,`deny` (use the Select for Action, plain Input for Permission/Pattern). `delete` reslices `s.cfg.Permissions.Rules`.
- Consumes: `collectionPane` (Task 8), `numField`, `newSectionForm`.

- [ ] **Step 1: Write the failing tests**

`section_hooks_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestHooksPaneAddAndEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "hooks")
	m = keyPress(m, "a", "enter") // hooks add does not prompt for a name — it appends a default entry
	if len(m.state.cfg.Hooks.Entries) != 1 {
		t.Fatalf("add should append a hook entry, got %d", len(m.state.cfg.Hooks.Entries))
	}
	m = keyPress(m, "enter") // edit the new entry
	if got := m.FocusedFieldTitle(); got != "Event" {
		t.Fatalf("focused = %q, want Event", got)
	}
}

func TestHooksPaneDelete(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.Entries = []config.HookConfig{{Event: "pre_tool", Command: "echo hi"}}
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "hooks")
	m = keyPress(m, "d")
	if len(m.state.cfg.Hooks.Entries) != 0 {
		t.Fatal("d should delete the hook entry")
	}
}
```

`section_permissions_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestPermissionsPaneAddAndEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "permissions")
	m = keyPress(m, "a", "enter")
	if len(m.state.cfg.Permissions.Rules) != 1 {
		t.Fatalf("add should append a rule, got %d", len(m.state.cfg.Permissions.Rules))
	}
	m = keyPress(m, "enter") // edit
	if got := m.FocusedFieldTitle(); got != "Permission" {
		t.Fatalf("focused = %q, want Permission", got)
	}
}

func TestPermissionsPaneActionSelect(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions.Rules = []config.PermissionRule{{Permission: "shell", Pattern: "go *", Action: "allow"}}
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "permissions")
	m = keyPress(m, "enter") // edit
	// Permission, Pattern, Action — navigate to Action (index 2)
	m = keyPress(m, "down", "down")
	if got := m.FocusedFieldTitle(); got != "Action" {
		t.Fatalf("focused = %q, want Action", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestHooksPane|TestPermissionsPane' -v`
Expected: FAIL (sections still staticPane)

- [ ] **Step 3: Implement**

> **Slice-key design:** the synthetic `Key()` (`hook-%d`/`rule-%d`) is computed from the entry's **current** slice position at `entries()` call time. Because add/delete shift positions, `editForm` and `delete` must re-resolve the index from the key string (parse the trailing integer) **at call time**, not capture it. Add a small helper `sliceIndexFromKey(key, prefix string) (int, bool)` to `collection.go`. The `add` closure for slice collections appends a default and returns `nil` (the name prompt is skipped — `a` + `enter` appends immediately). To support "no name prompt" for slice collections, extend `collectionPane` so that when `spec.keyPrompt == ""`, pressing `a` calls `spec.add(s, "")` directly and skips the prompt. Update the `adding` branch in `collectionPane.Update` accordingly: if `spec.keyPrompt == ""`, the `a` case calls `spec.add` and never sets `adding = true`.

`section_hooks.go`:

```go
package settings

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type hookEntry struct {
	idx   int
	hook  config.HookConfig
}

func (h hookEntry) Title() string {
	return fmt.Sprintf("%s %s → %s", h.hook.Event, h.hook.Matcher, h.hook.Command)
}
func (h hookEntry) Key() string { return fmt.Sprintf("hook-%d", h.idx) }

func newHooksPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Hooks",
		keyPrompt: "", // slice collection: add appends a default, no name prompt
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Hooks.Entries))
			for i, h := range s.cfg.Hooks.Entries {
				out = append(out, hookEntry{idx: i, hook: h})
			}
			return out
		},
		add: func(s *state, _ string) error {
			s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries, config.HookConfig{Event: "pre_tool"})
			return nil
		},
		editForm: func(s *state, key string) *huh.Form {
			idx, _ := sliceIndexFromKey(key, "hook-")
			local := s.cfg.Hooks.Entries[idx]
			b := &struct{ timeout string }{timeout: strconv.Itoa(local.TimeoutMS)}
			return newSectionForm(
				huh.NewInput().Key("Event").Title("Event").Value(&local.Event),
				huh.NewInput().Key("Matcher").Title("Matcher").Value(&local.Matcher),
				huh.NewInput().Key("Command").Title("Command").Value(&local.Command),
				numField("Timeout (ms)", &b.timeout, 0, func(v int) { local.TimeoutMS = v }),
			).WithSubmitFunc(func() {
				s.cfg.Hooks.Entries[idx] = local
			})
		},
		delete: func(s *state, key string) {
			idx, _ := sliceIndexFromKey(key, "hook-")
			s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries[:idx], s.cfg.Hooks.Entries[idx+1:]...)
		},
	}
	return newCollectionPane(s, spec)
}
```

`section_permissions.go` follows the same shape with `PermissionRule`, the `rule-%d` key prefix, and an `Action` Select. Add `sliceIndexFromKey` to `collection.go`:

```go
func sliceIndexFromKey(key, prefix string) (int, bool) {
	if !strings.HasPrefix(key, prefix) {
		return -1, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(key, prefix))
	if err != nil {
		return -1, false
	}
	return n, true
}
```

(Add `"strconv"` and `"strings"` to `collection.go` imports if missing.)

Update `collectionPane.Update`'s `a` case:

```go
	case "a":
		if p.spec.keyPrompt == "" {
			if err := p.spec.add(p.s, ""); err != nil {
				p.addErr = err.Error()
			}
			p.cursor = len(p.sortedEntries()) - 1
			return p, nil
		}
		p.adding = true
		p.addErr = ""
		p.nameInput.SetValue("")
		p.nameInput.Focus()
```

Wire both factories in `sections.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): hooks and permissions slice-collection sections"
```

---

### Task 11: MCP section (composite: scalar threshold + servers collection + policies map)

MCP combines three things: a scalar `DisclosureThresholdTools` int, a `Servers` map collection (command/args/env), and a `Policies` string→string map. This task introduces a `compositePane` that stacks a scalar form on top of a `collectionPane`, cycling focus like `mixedPane` does between form and lists.

**Files:**
- Create: `internal/app/tui/settings/composite.go`
- Create: `internal/app/tui/settings/section_mcp.go`
- Modify: `internal/app/tui/settings/sections.go`
- Test: `internal/app/tui/settings/composite_test.go`, `internal/app/tui/settings/section_mcp_test.go`

**Interfaces:**
- Produces:
  - `type compositePane struct { form *scalarPane; collection *collectionPane; mapEditors []*mapEditor; focusIdx int }` with `func newCompositePane(form *scalarPane, collection *collectionPane, maps ...*mapEditor) *compositePane`. Implements `sectionPane`. Focus order: form (0) → collection (1) → each mapEditor (2..). `tab`/`shift+tab` cycle the same way as `mixedPane`. `HasInnerFocus()` = collection.HasInnerFocus() || any mapEditor.Editing(). `CloseInner()` delegates to the active sub-pane. `AtFirstFocus()` = `focusIdx == 0`. `FocusedFieldTitle()` proxies the active sub-pane. This is a near-copy of `mixedPane`'s focus logic — extract the shared `setFocus`/cycle helpers into a small `focusCycler` if duplication is bothersome, but a direct copy is acceptable.
  - `newMCPPane(s *state) sectionPane` — composite. Form: `numField("Disclosure threshold tools", …→MCP.DisclosureThresholdTools)`. Collection (`Servers`): `entries` from `cfg.MCP.Servers`; `add` inserts `MCPServerConfig{}`; `editForm` Inputs "Command" + `listStrings`-style "Args" (embedded as a huh Text field or a separate sub-list — simplest: an Input for a space-joined args string, parsed on submit) + an env map. Because the sub-form is a single huh form and can't easily embed a `listStrings`, use **two Inputs**: "Args (space-separated)" and "Env (KEY=VAL, comma-separated)", parsed in `WithSubmitFunc`. Map (`Policies`): a `mapEditor` over `cfg.MCP.Policies` (server name → policy string).
- Consumes: `collectionPane` (Task 8), `mapEditor` (Task 12 — **dependency note below**), `numField`, `newSectionForm`.

> **Dependency ordering:** `mapEditor` is defined in Task 12. To avoid a forward dependency, either (a) implement Task 12's `mapeditor.go` `type mapEditor` + its `newMapEditor`/`Focus`/`Editing`/`CancelEdit`/`View`/`Update` surface in this task as a stub that compiles, then Task 12 fills in the key/value editing; or (b) reorder so Task 12 runs before Task 11. The cleaner choice is (b): **swap Task 11 and Task 12** so `mapeditor.go` exists first. The plan keeps the section-creation order (mcp before swarm/diagnostics) for narrative flow but notes that the implementer should do Task 12's `mapeditor.go` widget first, then this task. Concretely: do Step 1-3 of Task 12 (create `mapeditor.go` with the full `mapEditor` type and its unit tests), then return to this task.

- [ ] **Step 1: Write the failing tests**

`section_mcp_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func mcpTestConfig() config.Config {
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"fs": {Command: "mcp-fs", Args: []string{"--root", "."}, Env: map[string]string{"A": "1"}},
	}
	cfg.MCP.Policies = map[string]string{"fs": "confirm"}
	return cfg
}

func TestMCPPaneListsServers(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	if got := m.FocusedFieldTitle(); got != "Disclosure threshold tools" {
		t.Fatalf("focused = %q, want Disclosure threshold tools", got)
	}
}

func TestMCPPaneTabToServers(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab")
	if got := m.FocusedFieldTitle(); got != "Servers" {
		t.Fatalf("tab should reach the servers collection, got %q", got)
	}
}

func TestMCPPaneTabToPoliciesMap(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab", "tab") // form → servers → policies
	if got := m.FocusedFieldTitle(); got != "Policies" {
		t.Fatalf("tab should reach the policies map, got %q", got)
	}
}

func TestMCPPaneAddServer(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab") // servers
	m = keyPress(m, "a", "g", "i", "t", "enter")
	if _, ok := m.state.cfg.MCP.Servers["git"]; !ok {
		t.Fatal("add should create the git server entry")
	}
}
```

`composite_test.go` — a focused test of the focus cycling using a minimal scalar + empty collection + empty map:

```go
package settings

import (
	"testing"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

func TestCompositePaneFocusCycles(t *testing.T) {
	st := newState(config.Default())
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(huh.NewConfirm().Key("X").Title("X").Value(new(bool)))
	})
	col := newCollectionPane(st, collectionSpec{
		heading: "C", keyPrompt: "k",
		entries: func(s *state) []collectionEntry { return nil },
		add:     func(s *state, key string) error { return nil },
	})
	me := newMapEditor("M", &st.cfg.Diagnostics.Commands)
	cp := newCompositePane(form, col, me)
	cp.SetWidth(60)
	// tab: form → collection
	cp.Update(teaKeyPressTab())
	if cp.focusIdx != 1 {
		t.Fatalf("focusIdx = %d, want 1", cp.focusIdx)
	}
	// tab: collection → map
	cp.Update(teaKeyPressTab())
	if cp.focusIdx != 2 {
		t.Fatalf("focusIdx = %d, want 2", cp.focusIdx)
	}
	// tab: map → form (wrap)
	cp.Update(teaKeyPressTab())
	if cp.focusIdx != 0 {
		t.Fatalf("focusIdx = %d, want 0 (wrap)", cp.focusIdx)
	}
}
```

(Add a `teaKeyPressTab()` helper to `skeleton_test.go` returning `tea.KeyPressMsg{Code: tea.KeyTab}` — or reuse `keyMsg("tab")` from Task 6.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestMCPPane|TestCompositePane' -v`
Expected: FAIL (`compositePane`, `newMapEditor` undefined)

- [ ] **Step 3: Implement composite.go**

```go
package settings

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type compositePane struct {
	form        *scalarPane
	collection  *collectionPane
	mapEditors  []*mapEditor
	focusIdx    int
	width       int
}

func newCompositePane(form *scalarPane, collection *collectionPane, maps ...*mapEditor) *compositePane {
	return &compositePane{form: form, collection: collection, mapEditors: maps}
}

func (p *compositePane) atFirstFocus() bool { return p.focusIdx == 0 }

func (p *compositePane) Init() tea.Cmd { return p.form.Init() }

func (p *compositePane) activeMap() *mapEditor {
	if p.focusIdx < 2 {
		return nil
	}
	return p.mapEditors[p.focusIdx-2]
}

func (p *compositePane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && !p.HasInnerFocus() {
		switch k.String() {
		case "tab":
			p.setFocus((p.focusIdx + 1) % (2 + len(p.mapEditors)))
			return p, nil
		case "shift+tab":
			if p.focusIdx > 0 {
				p.setFocus(p.focusIdx - 1)
				return p, nil
			}
			return p, nil // let Model take it (back to sidebar)
		}
	}
	switch p.focusIdx {
	case 0:
		updated, cmd := p.form.Update(msg)
		p.form = updated.(*scalarPane)
		return p, cmd
	case 1:
		updated, cmd := p.collection.Update(msg)
		if cp, ok := updated.(*collectionPane); ok {
			p.collection = cp
		}
		return p, cmd
	default:
		me := p.activeMap()
		if me == nil {
			return p, nil
		}
		if isKey {
			return p, me.Update(k)
		}
		return p, nil
	}
}

func (p *compositePane) setFocus(idx int) {
	p.focusIdx = idx
	p.collection.atFirstFocusActive = false // no-op placeholder; see note
	for i, me := range p.mapEditors {
		me.Focus(i == idx-2)
	}
}

func (p *compositePane) SetWidth(w int) {
	p.width = w
	p.form.SetWidth(w)
	p.collection.SetWidth(w)
	for _, me := range p.mapEditors {
		me.SetWidth(w)
	}
}

func (p *compositePane) View(width int) string {
	parts := []string{p.form.View(width), p.collection.View(width)}
	for _, me := range p.mapEditors {
		parts = append(parts, me.View(width))
	}
	return strings.Join(parts, "\n\n")
}

func (p *compositePane) HasInnerFocus() bool {
	return p.collection.HasInnerFocus() ||
		(p.activeMap() != nil && p.activeMap().Editing())
}

func (p *compositePane) CloseInner() {
	switch p.focusIdx {
	case 1:
		p.collection.CloseInner()
	default:
		if me := p.activeMap(); me != nil {
			me.CancelEdit()
		}
	}
}

func (p *compositePane) FocusedFieldTitle() string {
	switch p.focusIdx {
	case 0:
		return p.form.FocusedFieldTitle()
	case 1:
		return p.collection.FocusedFieldTitle()
	default:
		if me := p.activeMap(); me != nil {
			return me.FocusedFieldTitle()
		}
	}
	return ""
}
```

> Remove the `p.collection.atFirstFocusActive` placeholder line — `collectionPane` does not need a focus flag because the Model handles sidebar-return via `AtFirstFocus()` on the composite, and the composite always returns true at `focusIdx == 0` (the scalar form). The collection itself never fields `shift+tab` to the sidebar because the composite intercepts it first. Delete that line and the `atFirstFocusActive` field; keep `collectionPane.AtFirstFocus() bool { return true }` unchanged (it's only consulted by the Model when the collection is the **top-level** pane, which it isn't inside a composite).

`section_mcp.go`:

```go
package settings

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type mcpServerEntry struct {
	key    string
	server config.MCPServerConfig
}

func (m mcpServerEntry) Title() string {
	return m.key + "  (" + m.server.Command + ")"
}
func (m mcpServerEntry) Key() string { return m.key }

func newMCPPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		b := &struct{ threshold string }{threshold: strconv.Itoa(s.cfg.MCP.DisclosureThresholdTools)}
		return newSectionForm(
			numField("Disclosure threshold tools", &b.threshold, 0, func(v int) {
				s.cfg.MCP.DisclosureThresholdTools = v
			}),
		)
	})
	serversSpec := collectionSpec{
		heading:   "Servers",
		keyPrompt: "New server name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.MCP.Servers))
			for k, srv := range s.cfg.MCP.Servers {
				out = append(out, mcpServerEntry{key: k, server: srv})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.MCP.Servers[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.MCP.Servers[key] = config.MCPServerConfig{Env: map[string]string{}}
			return nil
		},
		editForm: func(s *state, key string) *huh.Form {
			local := s.cfg.MCP.Servers[key]
			argsBuf := strings.Join(local.Args, " ")
			envBuf := joinEnv(local.Env)
			return newSectionForm(
				huh.NewInput().Key("Command").Title("Command").Value(&local.Command),
				huh.NewInput().Key("Args (space-separated)").Title("Args (space-separated)").Value(&argsBuf),
				huh.NewInput().Key("Env (KEY=VAL, comma-separated)").Title("Env (KEY=VAL, comma-separated)").Value(&envBuf),
			).WithSubmitFunc(func() {
				local.Args = splitArgs(argsBuf)
				local.Env = splitEnv(envBuf)
				s.cfg.MCP.Servers[key] = local
			})
		},
		delete: func(s *state, key string) { delete(s.cfg.MCP.Servers, key) },
	}
	collection := newCollectionPane(s, serversSpec)
	policies := newMapEditor("Policies", &s.cfg.MCP.Policies)
	return newCompositePane(form, collection, policies)
}

func joinEnv(env map[string]string) string {
	parts := make([]string, 0, len(env))
	for k, v := range env {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

func splitEnv(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if k, v, ok := strings.Cut(pair, "="); ok {
			out[k] = v
		}
	}
	return out
}
```

(Add `"strconv"` to imports.)

Wire the factory in `sections.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): compositePane and MCP section"
```

---

### Task 12: mapEditor + Swarm and Diagnostics sections

Builds the key/value map editor widget (used standalone by Diagnostics and embedded by MCP's Policies map in Task 11). Then the Swarm section (scalar budget fields + a `tool_iters` map) and Diagnostics (a single lang→command map).

> **Sequencing note (from Task 11):** implement the `mapEditor` widget and its tests **first** (Steps 1-3 below), then return to Task 11 which consumes it, then come back for the Swarm/Diagnostics section files (Steps 4-5). If executing strictly in plan order, do this whole task before Task 11.

**Files:**
- Create: `internal/app/tui/settings/mapeditor.go`
- Create: `internal/app/tui/settings/section_swarm.go`
- Create: `internal/app/tui/settings/section_diagnostics.go`
- Modify: `internal/app/tui/settings/sections.go`
- Test: `internal/app/tui/settings/mapeditor_test.go`, `internal/app/tui/settings/section_swarm_test.go`, `internal/app/tui/settings/section_diagnostics_test.go`

**Interfaces:**
- Produces:
  - `type mapEditor struct { title string; values *map[string]string; keys []string; cursor int; adding bool; editing bool; keyInput, valInput textinput.Model; focused bool; width int }` with `func newMapEditor(title string, values *map[string]string) *mapEditor`. For string→string maps. Keys are a sorted snapshot of `*values`'s map keys, kept in sync on add/delete. Keys while focused: `↑/↓`/`k`/`j` move; `a` opens a two-field inline prompt (key then value — simplest: key input first, `enter` advances to value input, `enter` commits); `e`/`enter` edits the value of the focused row (key read-only after creation, per spec); `d` deletes the row; `esc` cancels the deepest open input. Methods: `Update(tea.KeyPressMsg) tea.Cmd`, `View(width int) string`, `Focus(bool)`, `Focused() bool`, `Editing() bool`, `CancelEdit()`, `SetWidth(int)`, `FocusedFieldTitle() string`.
  - Also produce a `mapIntEditor` variant for `Swarm.Budget.ToolIters` (`map[string]int`): `func newMapIntEditor(title string, values *map[string]int) *mapIntEditor` with the same surface but the value input validates `strconv.Atoi`. To avoid generics complexity, implement `mapIntEditor` as a separate small type mirroring `mapEditor` with an int value buffer and `numField`-style validation. (A generic `mapEditor[K comparable, V any]` is possible but the two consumers have different value validation, so two concrete types keep the code flat and testable.)
  - `newSwarmPane(s *state) sectionPane` — mixedPane: form (`numField("Max fix rounds", …→MaxFixRounds)`, `numField("Max total tokens", …→MaxTotalTokens)`) + `mapIntEditor("Tool iters", &s.cfg.Swarm.Budget.ToolIters)`. Because `mixedPane` was built around `listStrings`, either (a) generalize `mixedPane` to accept a `focusedList` interface (`Focus`/`Focused`/`Editing`/`CancelEdit`/`View`/`Update`/`title`) that both `listStrings` and `mapEditor`/`mapIntEditor` satisfy, or (b) use `compositePane` (Task 11) with a nil collection. Choose (a): define `type focusableList interface { Focus(bool); Focused() bool; Editing() bool; CancelEdit(); View(int) string; title() string; updateKey(tea.KeyPressMsg) tea.Cmd }` and have `mixedPane` store `[]focusableList`; `listStrings`, `mapEditor`, and `mapIntEditor` all satisfy it. Update `mixedPane`'s `activeList()`/`setFocus`/`HasInnerFocus`/`CloseInner`/`FocusedFieldTitle` to use the interface. This is a small refactor of Task 6's `mixedPane` — update its tests still pass.
  - `newDiagnosticsPane(s *state) sectionPane` — a `mapEditor` over `&s.cfg.Diagnostics.Commands`, wrapped so it satisfies `sectionPane` directly (a thin adapter, or just build a `compositePane` with a nil form — simplest: a dedicated `mapPane` adapter struct in `mapeditor.go` that wraps a single `*mapEditor` and implements `sectionPane`).
- Consumes: `charm.land/bubbles/v2/textinput`, `mixedPane`/`compositePane`, `numField`.

- [ ] **Step 1: Write the failing tests**

`mapeditor_test.go`:

```go
package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func meKey(m *mapEditor, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		m.Update(msg)
	}
}

func TestMapEditorAdd(t *testing.T) {
	v := map[string]string{}
	m := newMapEditor("Commands", &v)
	m.Focus(true)
	meKey(m, "a", "g", "o", "enter", "g", "o", " ", "v", "e", "t", "enter")
	if v["go"] != "go vet" {
		t.Fatalf("map = %v, want go→go vet", v)
	}
}

func TestMapEditorEditValue(t *testing.T) {
	v := map[string]string{"go": "go vet ./..."}
	m := newMapEditor("Commands", &v)
	m.Focus(true)
	meKey(m, "enter", "g", "o", " ", "b", "u", "i", "l", "d", "enter")
	if v["go"] != "go build" {
		t.Fatalf("value = %q, want go build", v["go"])
	}
}

func TestMapEditorDelete(t *testing.T) {
	v := map[string]string{"go": "x", "py": "y"}
	m := newMapEditor("Commands", &v)
	m.Focus(true)
	meKey(m, "d")
	if _, ok := v["go"]; ok {
		t.Fatal("d should delete the focused row")
	}
}

func TestMapIntEditorAdd(t *testing.T) {
	v := map[string]int{}
	m := newMapIntEditor("Tool iters", &v)
	m.Focus(true)
	meKey(m, "a", "t", "e", "s", "t", "e", "r", "enter", "9", "enter")
	if v["tester"] != 9 {
		t.Fatalf("map = %v, want tester→9", v)
	}
}
```

`section_swarm_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestSwarmPaneScalarAndMap(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "swarm")
	if got := m.FocusedFieldTitle(); got != "Max fix rounds" {
		t.Fatalf("focused = %q, want Max fix rounds", got)
	}
	m = keyPress(m, "tab") // → tool iters map
	if got := m.FocusedFieldTitle(); got != "Tool iters" {
		t.Fatalf("tab should reach the tool iters map, got %q", got)
	}
}

func TestSwarmPaneToolItersEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "swarm")
	m = keyPress(m, "tab", "a", "t", "e", "s", "t", "e", "r", "enter", "7", "enter")
	if got := m.state.cfg.Swarm.Budget.ToolIters["tester"]; got != 7 {
		t.Fatalf("tool iters tester = %d, want 7", got)
	}
}
```

`section_diagnostics_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestDiagnosticsPaneMapEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "diagnostics")
	if got := m.FocusedFieldTitle(); got != "Commands" {
		t.Fatalf("focused = %q, want Commands", got)
	}
	m = keyPress(m, "a", "p", "y", "enter", "r", "u", "f", "f", " ", "c", "h", "e", "c", "k", "enter")
	if got := m.state.cfg.Diagnostics.Commands["py"]; got != "ruff check" {
		t.Fatalf("diagnostics py = %q, want ruff check", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestMapEditor|TestMapInt|TestSwarmPane|TestDiagnosticsPane' -v`
Expected: FAIL (`mapEditor`, `mapIntEditor` undefined)

- [ ] **Step 3: Implement mapeditor.go**

```go
package settings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type mapEditor struct {
	title    string
	values   *map[string]string
	keys     []string
	cursor   int
	adding   bool
	editing  bool
	phase    int // 0 = key, 1 = value (during add)
	keyInput textinput.Model
	valInput textinput.Model
	focused  bool
	width    int
}

func newMapEditor(title string, values *map[string]string) *mapEditor {
	ki := textinput.New()
	ki.SetVirtualCursor(true)
	vi := textinput.New()
	vi.SetVirtualCursor(true)
	m := &mapEditor{title: title, values: values, keyInput: ki, valInput: vi}
	m.refreshKeys()
	return m
}

func (m *mapEditor) refreshKeys() {
	m.keys = make([]string, 0, len(*m.values))
	for k := range *m.values {
		m.keys = append(m.keys, k)
	}
	sort.Strings(m.keys)
	m.clamp()
}

func (m *mapEditor) clamp() {
	if m.cursor >= len(m.keys) {
		m.cursor = len(m.keys) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *mapEditor) Focus(on bool) { m.focused = on }
func (m *mapEditor) Focused() bool { return m.focused }
func (m *mapEditor) Editing() bool { return m.adding || m.editing }
func (m *mapEditor) SetWidth(w int) { m.width = w }

func (m *mapEditor) CancelEdit() {
	m.adding = false
	m.editing = false
	m.phase = 0
	m.keyInput.Blur()
	m.valInput.Blur()
}

func (m *mapEditor) FocusedFieldTitle() string {
	if m.Editing() {
		if m.adding && m.phase == 0 {
			return m.title + " key"
		}
		return m.title + " value"
	}
	return m.title
}

func (m *mapEditor) Update(msg tea.KeyPressMsg) tea.Cmd {
	if m.Editing() {
		return m.updateEdit(msg)
	}
	switch msg.String() {
	case "up", "k":
		m.cursor--
		m.clamp()
	case "down", "j":
		m.cursor++
		m.clamp()
	case "a":
		m.adding = true
		m.phase = 0
		m.keyInput.SetValue("")
		m.keyInput.Focus()
	case "enter", "e":
		if len(m.keys) > 0 {
			m.editing = true
			m.valInput.SetValue((*m.values)[m.keys[m.cursor]])
			m.valInput.CursorEnd()
			m.valInput.Focus()
		}
	case "d":
		if len(m.keys) > 0 {
			delete(*m.values, m.keys[m.cursor])
			m.refreshKeys()
		}
	}
	return nil
}

func (m *mapEditor) updateEdit(msg tea.KeyPressMsg) tea.Cmd {
	if m.editing {
		switch msg.String() {
		case "enter":
			(*m.values)[m.keys[m.cursor]] = m.valInput.Value()
			m.CancelEdit()
			return nil
		case "esc":
			m.CancelEdit()
			return nil
		}
		var cmd tea.Cmd
		m.valInput, cmd = m.valInput.Update(msg)
		return cmd
	}
	// adding: phase 0 = key, phase 1 = value
	switch msg.String() {
	case "enter":
		if m.phase == 0 {
			m.phase = 1
			m.keyInput.Blur()
			m.valInput.SetValue("")
			m.valInput.Focus()
			return nil
		}
		key := strings.TrimSpace(m.keyInput.Value())
		val := m.valInput.Value()
		if key != "" {
			(*m.values)[key] = val
			m.refreshKeys()
		}
		m.CancelEdit()
		return nil
	case "esc":
		m.CancelEdit()
		return nil
	}
	var cmd tea.Cmd
	if m.phase == 0 {
		m.keyInput, cmd = m.keyInput.Update(msg)
	} else {
		m.valInput, cmd = m.valInput.Update(msg)
	}
	return cmd
}

func (m *mapEditor) View(width int) string {
	var b strings.Builder
	b.WriteString(m.title + "\n")
	if len(m.keys) == 0 && !m.adding {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, k := range m.keys {
		marker := "  "
		if m.focused && i == m.cursor {
			marker = "▸ "
		}
		if m.editing && i == m.cursor {
			b.WriteString(fmt.Sprintf("%s%s = %s\n", marker, k, m.valInput.View()))
			continue
		}
		b.WriteString(fmt.Sprintf("%s%s = %s\n", marker, k, (*m.values)[k]))
	}
	if m.adding {
		if m.phase == 0 {
			b.WriteString("▸ key: " + m.keyInput.View() + "\n")
		} else {
			b.WriteString("  " + m.keyInput.Value() + " = " + m.valInput.View() + "\n")
		}
	}
	if m.focused && !m.Editing() {
		b.WriteString("  a add · e edit value · d delete")
	}
	return strings.TrimRight(b.String(), "\n")
}

// mapIntEditor is the int-valued variant for swarm tool_iters.
type mapIntEditor struct {
	*mapEditor
	intValues *map[string]int
}

func newMapIntEditor(title string, values *map[string]int) *mapIntEditor {
	strMap := map[string]string{}
	for k, v := range *values {
		strMap[k] = strconv.Itoa(v)
	}
	// Share the string map as the editing surface; commit back to ints.
	base := &mapEditor{title: title, values: &strMap, keyInput: textinput.New(), valInput: textinput.New()}
	base.keyInput.SetVirtualCursor(true)
	base.valInput.SetVirtualCursor(true)
	base.refreshKeys()
	return &mapIntEditor{mapEditor: base, intValues: values}
}
```

> The `mapIntEditor` delegation above is illustrative — the cleanest implementation gives `mapIntEditor` its own `Update` that validates the value input as an int (reusing `numField`-style logic) and writes to `*intValues` on commit. The string-map sharing trick works for rendering but the int validation must happen in `updateEdit`. Implement `mapIntEditor` with its own `Update`/`updateEdit`/`View` if the embedding approach gets awkward; the tests only assert on `*intValues` mutations.

Also add the `mapPane` adapter (a `sectionPane` wrapping a single `*mapEditor`) so Diagnostics can be a top-level pane:

```go
type mapPane struct{ m *mapEditor }

func newMapPane(m *mapEditor) *mapPane { return &mapPane{m: m} }

func (p *mapPane) Init() tea.Cmd                          { return nil }
func (p *mapPane) AtFirstFocus() bool                     { return true }
func (p *mapPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return p, p.m.Update(k)
	}
	return p, nil
}
func (p *mapPane) View(width int) string { return p.m.View(width) }
func (p *mapPane) SetWidth(w int)        { p.m.SetWidth(w) }
func (p *mapPane) HasInnerFocus() bool   { return p.m.Editing() }
func (p *mapPane) CloseInner()           { p.m.CancelEdit() }
func (p *mapPane) FocusedFieldTitle() string { return p.m.FocusedFieldTitle() }
```

- [ ] **Step 4: Generalize mixedPane to focusableList and implement sections**

Refactor `mixedPane` (Task 6) to accept the `focusableList` interface so `mapEditor`/`mapIntEditor` can be embedded. In `mixed.go`:

```go
type focusableList interface {
	Focus(bool)
	Focused() bool
	Editing() bool
	CancelEdit()
	View(int) string
	title() string
	updateKey(tea.KeyPressMsg) tea.Cmd
}

func (l *listStrings) title() string                       { return l.title }
func (l *listStrings) updateKey(k tea.KeyPressMsg) tea.Cmd { return l.Update(k) }
func (m *mapEditor) title() string                         { return m.title }
func (m *mapEditor) updateKey(k tea.KeyPressMsg) tea.Cmd   { return m.Update(k) }
func (m *mapIntEditor) title() string                       { return m.title }
func (m *mapIntEditor) updateKey(k tea.KeyPressMsg) tea.Cmd { return m.Update(k) }
```

Change `mixedPane.lists` from `[]*listStrings` to `[]focusableList` and update `activeList()`/`setFocus()`/`Update()`/`View()`/`HasInnerFocus()`/`CloseInner()`/`FocusedFieldTitle()` to use the interface methods. Existing `listStrings` tests still pass because the method set is satisfied.

`section_swarm.go`:

```go
package settings

import (
	"strconv"

	"charm.land/huh/v2"
)

func newSwarmPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		b := &struct{ fix, total string }{
			fix:   strconv.Itoa(s.cfg.Swarm.Budget.MaxFixRounds),
			total: strconv.Itoa(s.cfg.Swarm.Budget.MaxTotalTokens),
		}
		return newSectionForm(
			numField("Max fix rounds", &b.fix, 0, func(v int) { s.cfg.Swarm.Budget.MaxFixRounds = v }),
			numField("Max total tokens", &b.total, 0, func(v int) { s.cfg.Swarm.Budget.MaxTotalTokens = v }),
		)
	})
	return newMixedPane(form, newMapIntEditor("Tool iters", &s.cfg.Swarm.Budget.ToolIters))
}
```

`section_diagnostics.go`:

```go
package settings

func newDiagnosticsPane(s *state) sectionPane {
	return newMapPane(newMapEditor("Commands", &s.cfg.Diagnostics.Commands))
}
```

Wire both factories in `sections.go`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS (mapeditor tests + swarm + diagnostics + the Task 11 MCP tests if done, + all prior)

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): mapEditor, mapIntEditor, swarm and diagnostics sections"
```

---

### Task 13: validation.go soft-warning rules

Fills in the `warningsFor` stub from Task 3 with the non-blocking amber warnings from the design spec.

**Files:**
- Modify: `internal/app/tui/settings/validation.go`
- Test: `internal/app/tui/settings/validation_test.go`

**Interfaces:**
- Produces: `func warningsFor(sectionID string, cfg config.Config) []string` returning the warnings for the active section. Rules (from spec §Validation):
  - `agent`/`providers`: "Remote providers allowed but no provider configured" when `cfg.Privacy.RemoteProvidersAllowed && len(cfg.Providers) == 0`.
  - `providers`: "API key stored in plaintext" for each provider with a non-empty `APIKey` (info-level, still amber).
  - `shell`: "sudo runs without confirmation" when `AllowSudo && AutoApprove`; "destructive commands run without confirmation" when `AllowDestructive && AutoApprove`.
  - `sandbox`: "container backend set but no image configured — will fall back or error" when `Backend == "container" && ContainerImage == ""`.
  - Other sections: `nil`.

- [ ] **Step 1: Write the failing tests**

`validation_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestWarningsRemoteProvidersNoProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	w := warningsFor("agent", cfg)
	if len(w) == 0 {
		t.Fatal("expected a remote-providers/no-provider warning")
	}
}

func TestWarningsSudoAutoApprove(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.AllowSudo = true
	cfg.Tools.Shell.AutoApprove = true
	w := warningsFor("shell", cfg)
	found := false
	for _, msg := range w {
		if strings.Contains(msg, "sudo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sudo/auto-approve warning, got %v", w)
	}
}

func TestWarningsDestructiveAutoApprove(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.AllowDestructive = true
	cfg.Tools.Shell.AutoApprove = true
	w := warningsFor("shell", cfg)
	found := false
	for _, msg := range w {
		if strings.Contains(msg, "destructive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected destructive/auto-approve warning, got %v", w)
	}
}

func TestWarningsContainerNoImage(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Sandbox.Backend = "container"
	cfg.Tools.Shell.Sandbox.ContainerImage = ""
	w := warningsFor("sandbox", cfg)
	if len(w) == 0 {
		t.Fatal("expected container/no-image warning")
	}
}

func TestWarningsProviderPlaintextKey(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{"a": {APIKey: "sk-1234"}}
	w := warningsFor("providers", cfg)
	found := false
	for _, msg := range w {
		if strings.Contains(msg, "plaintext") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plaintext-key info warning, got %v", w)
	}
}

func TestWarningsNoneForSafeConfig(t *testing.T) {
	w := warningsFor("web", config.Default())
	if len(w) != 0 {
		t.Fatalf("web section should have no warnings, got %v", w)
	}
}
```

(Add `"strings"` to imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run TestWarnings -v`
Expected: FAIL (stub returns nil)

- [ ] **Step 3: Implement validation.go**

```go
package settings

import (
	"fmt"

	"marshal/internal/app/config"
)

func warningsFor(sectionID string, cfg config.Config) []string {
	switch sectionID {
	case "agent", "providers":
		var ws []string
		if cfg.Privacy.RemoteProvidersAllowed && len(cfg.Providers) == 0 {
			ws = append(ws, "remote providers allowed but none configured")
		}
		if sectionID == "providers" {
			for name, p := range cfg.Providers {
				if p.APIKey != "" {
					ws = append(ws, fmt.Sprintf("provider %q stores an API key in plaintext — prefer api_key_env", name))
				}
			}
		}
		return ws
	case "shell":
		var ws []string
		if cfg.Tools.Shell.AllowSudo && cfg.Tools.Shell.AutoApprove {
			ws = append(ws, "sudo runs without confirmation (auto-approve on)")
		}
		if cfg.Tools.Shell.AllowDestructive && cfg.Tools.Shell.AutoApprove {
			ws = append(ws, "destructive commands run without confirmation (auto-approve on)")
		}
		return ws
	case "sandbox":
		if cfg.Tools.Shell.Sandbox.Backend == "container" && cfg.Tools.Shell.Sandbox.ContainerImage == "" {
			return []string{"container backend set but no image configured — will fall back or error at runtime"}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "feat(settings): soft validation warnings for risky config combinations"
```

---

### Task 14: Final integration and acceptance pass

No new features — verifies the full surface builds, all sections are wired (no remaining `staticPane` placeholders), the parent TUI is untouched, and the acceptance criteria from the spec hold. Cleans up the temporarily-skipped `model_test.go` tests and the `keyPress`/`keyMsg` helpers.

**Files:**
- Modify: `internal/app/tui/settings/sections.go` (assert no placeholder factories remain — they should all be replaced by Tasks 4-12)
- Modify: `internal/app/tui/settings/model_test.go` (un-skip/re-rewrite any remaining skipped tests to the two-pane reality; delete tests fully superseded by per-section tests)
- Modify: `internal/app/tui/settings/skeleton_test.go` (consolidate `keyPress`/`keyMsg`/`stripANSI` helpers; remove duplication)
- Test: `internal/app/tui/settings/integration_test.go` (new — end-to-end edit + save round-trip through the Model)

**Interfaces:** none new.

- [ ] **Step 1: Verify every section factory is real**

Run: `grep -n "placeholder" internal/app/tui/settings/sections.go`
Expected: no matches (all 15 factories replaced). If any `staticPane` placeholder remains, wire the missing section from its task before continuing.

- [ ] **Step 2: Write the integration test**

`integration_test.go` — exercises the full edit → save → reload path through the Model and the extended `SaveProjectConfig`, proving the two-pane model and the save extension work together:

```go
package settings

import (
	"path/filepath"
	"testing"

	"marshal/internal/app/config"
)

func TestIntegrationEditAndSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	// Start from defaults; flip a privacy bool and add a provider via the TUI.
	m := New(config.Default(), dir, path)
	m.SetSize(100, 40)

	// Privacy: toggle Remote providers allowed.
	m = enterSection(t, m, "privacy")
	m = keyPress(m, "space")
	if !m.state.cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("privacy toggle did not take")
	}

	// Providers: add an entry.
	m = enterSection(t, m, "providers")
	m = keyPress(m, "a", "o", "l", "l", "a", "m", "a", "enter")
	if _, ok := m.state.cfg.Providers["ollama"]; !ok {
		t.Fatal("provider add did not take")
	}

	if !m.dirty() {
		t.Fatal("model should be dirty after edits")
	}

	// Save.
	_, cmd := m.Update(keyMsg("ctrl+s"))
	if cmd == nil {
		t.Fatal("ctrl+s should produce a save command")
	}
	msg := cmd()
	saved, ok := msg.(SavedMsg)
	if !ok {
		t.Fatalf("expected SavedMsg, got %T", msg)
	}

	// The saved config reflects both edits.
	if !saved.Cfg.Privacy.RemoteProvidersAllowed {
		t.Error("saved config lost the privacy edit")
	}
	if _, ok := saved.Cfg.Providers["ollama"]; !ok {
		t.Error("saved config lost the provider entry")
	}

	// The file on disk round-trips through Load.
	loaded, err := config.Load(config.LoadOptions{HomeDir: filepath.Join(dir, "no-home"), WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !loaded.Privacy.RemoteProvidersAllowed {
		t.Error("disk config lost the privacy edit")
	}
	if _, ok := loaded.Providers["ollama"]; !ok {
		t.Error("disk config lost the provider entry")
	}
}
```

(Add `"strings"`/`"path/filepath"` to imports as needed; reuse `keyMsg` from `skeleton_test.go`. Add a `"ctrl+s"` case to `keyMsg` if not present: `msg = tea.KeyPressMsg{Code: tea.KeyCtrlS}` — verify the exact constant in `charm.land/bubbletea/v2`; it may be `Code: rune('s'), Mod: tea.ModCtrl`.)

- [ ] **Step 3: Clean up model_test.go and skeleton_test.go**

- In `model_test.go`: delete the tests that were skipped in Task 3 and superseded by per-section tests (the flat-form `TestSettingsExposeAgentAndToolFields`, `TestTabNavigatesBetweenFields`, `TestTypingUpdatesStringField`, `TestUpDownNavigatesBetweenFields`, `TestConfirmFieldTogglesOnLeft`, `TestNumericFieldValidatesAndWritesBack`). Keep and update to the two-pane model: `TestNewModelHasFields` (assert the view contains `"Agent"` and `"Providers"`), `TestCancelReturnsCancelledMsg` (Esc at sidebar emits `CancelledMsg`), `TestSettingsViewKeepsFrameBounded` (assert no view line exceeds `m.width` after `SetSize(80, 30)` — note the two-pane layout is wider than the old form, so recompute the bound: `sidebarWidth + paneWidth + padding`; the test should assert `maxW <= m.width`).
- In `skeleton_test.go`: ensure `keyPress`, `keyMsg`, `stripANSI`, `enterSection` (moved here from `section_scalar_test.go` if it's shared), and the `ansiRE` regex are defined once and exported to the package's test files via the shared `_test.go` package. Remove any duplicate definitions.

- [ ] **Step 4: Run the full verification suite**

Run, in order:
```bash
gofmt -w .
go build ./cmd/marshal
go vet ./...
go test ./...
```
Expected: all PASS. `go build` succeeds with `CGO_ENABLED=1` (the tree-sitter dependency). No `staticPane` placeholders remain. The parent TUI files `internal/app/tui/model.go` and `internal/app/tui/view.go` are unmodified (`git diff internal/app/tui/model.go internal/app/tui/view.go` shows no changes from this branch's base).

- [ ] **Step 5: Verify the parent TUI wiring is untouched**

Run: `git diff --stat $(git merge-base HEAD main) -- internal/app/tui/model.go internal/app/tui/view.go internal/app/tui/status.go`
Expected: no output (zero changes to the parent TUI render/route layer). If changes appear, revert them — the public settings API contract requires the parent to need no edits.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/settings/
git commit -m "test(settings): integration round-trip and final acceptance cleanup"
```

---

## Task dependency graph

```
Task 1 (name configFile structs)
  └─ Task 2 (extend SaveProjectConfig)
Task 3 (two-pane skeleton)
  ├─ Task 4 (scalarPane + agent)
  │    └─ Task 5 (masked + privacy/snapshots/web)
  │    └─ Task 6 (listStrings + mixedPane + indexing/commands)
  │         └─ Task 7 (shell + sandbox)
  │         └─ Task 8 (collectionPane + providers)
  │              ├─ Task 9 (presets)
  │              ├─ Task 10 (hooks + permissions)
  │              └─ Task 12 (mapEditor + swarm/diagnostics) ← do widget before Task 11
  │                   └─ Task 11 (compositePane + MCP)
  └─ Task 13 (validation)
Task 14 (final integration) ← after all of the above
```

**Recommended execution order:** 1 → 2 → 3 → 4 → 5 → 6 → 7 → 12 (widget only) → 8 → 9 → 10 → 11 → 12 (sections) → 13 → 14.

The deviation from numerical order (12's widget before 8/11) is because `mapEditor` is consumed by both Task 11 (MCP policies) and Task 12's own sections; building the widget first avoids a forward reference. The section files in Task 12 (swarm, diagnostics) can be done after Task 11 since they don't depend on `compositePane`.

## Acceptance checklist (spec §Acceptance criteria)

- [ ] `/settings` opens the two-pane overlay (parent wiring unchanged — verified in Task 14 Step 5).
- [ ] All 15 sections navigable and editable (Task 14 Step 1 confirms no placeholders remain).
- [ ] Saved changes appear in `.marshal/config.toml`; unrelated sections preserved (Task 2 round-trip + Task 14 integration test).
- [ ] `go test ./...` passes.
- [ ] `gofmt -w .` and `go vet ./...` pass.
- [ ] Parent TUI wiring (`model.go`, `view.go`) unchanged.


