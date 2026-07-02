# Settings TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a runtime settings TUI reachable from the main Marshal TUI with `Ctrl+O` so users can edit essential project config (profile, provider/model preset, local-only, remote-allowed) and persist it to `.marshal/config.toml`.

**Architecture:** A new `internal/app/tui/settings` package owns the settings form Bubble Tea model. `internal/app/tui/model.go` delegates to it when `settingsOpen` is true. `internal/app/config` gains `SaveProjectConfig` for writing only the essentials the TUI touches while preserving unrelated project config. `internal/app/app.go` passes a config-reloader callback that updates `session.State.Config` and the agent runner's provider/model when settings are saved.

**Tech Stack:** Go 1.26.1, `github.com/charmbracelet/bubbletea` v1.3.10, `github.com/charmbracelet/bubbles` v1.0.0, `github.com/pelletier/go-toml/v2` v2.4.2, existing `marshal/internal/app/config`, `marshal/internal/app/session`, `marshal/internal/agent`.

## Global Constraints

- Keep v1 focused on essentials: profile, active preset provider/model/local-only, remote providers allowed.
- Do not edit tool rules, commands, indexing toggles, context budgets, or global config in v1.
- The TUI must remain usable if no provider is configured (local-first fallback).
- All changes are written to `.marshal/config.toml`; the global config file is read-only for this feature.
- `go test ./...` must pass.

## File Structure

- Create `internal/app/config/save.go` — `SaveProjectConfig`.
- Create `internal/app/config/save_test.go` — save/load roundtrip tests.
- Create `internal/app/tui/settings/messages.go` — `SavedMsg`, `CancelledMsg`.
- Create `internal/app/tui/settings/field.go` — field interface and concrete field types.
- Create `internal/app/tui/settings/model.go` — settings Bubble Tea model, update logic.
- Create `internal/app/tui/settings/view.go` — rendering.
- Create `internal/app/tui/settings/model_test.go` — model unit tests.
- Modify `internal/app/tui/model.go` — add settings mode routing and `ConfigReloader` option.
- Modify `internal/app/tui/model_test.go` — add integration tests.
- Modify `internal/app/app.go` — pass config reloader to TUI.
- Modify `docs/10-mvp-implementation-checklist.md` — add Milestone N.5.

---

### Task 1: Add `config.SaveProjectConfig`

**Files:**
- Create: `internal/app/config/save.go`
- Create: `internal/app/config/save_test.go`

**Interfaces:**
- Produces: `func SaveProjectConfig(path string, cfg Config) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/config/save_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/llm/routing"
)

func TestSaveProjectConfigRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:      "coder",
			Provider:  "ollama",
			Model:     "qwen2.5-coder:14b",
			LocalOnly: true,
		},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Profile.Default != "local_balanced" {
		t.Fatalf("profile default = %q, want local_balanced", loaded.Profile.Default)
	}
	if loaded.Agent.Provider != "ollama" || loaded.Agent.Model != "qwen2.5-coder:14b" {
		t.Fatalf("agent = %+v", loaded.Agent)
	}
	if loaded.Privacy.RemoteProvidersAllowed {
		t.Fatal("remote_providers_allowed = true, want false")
	}
	preset := loaded.Models.Presets["coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset coder = %+v", preset)
	}
}

func TestSaveProjectConfigPreservesUnrelatedSections(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`
[commands]
test = "go test ./..."
format = "gofmt -w ."

[indexing]
use_treesitter = true
`), 0644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Commands.Test != "go test ./..." {
		t.Fatalf("commands.test = %q", loaded.Commands.Test)
	}
	if !loaded.Indexing.UseTreesitter {
		t.Fatal("indexing.use_treesitter was not preserved")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/config -run 'TestSaveProjectConfig' -v
```

Expected: FAIL — `SaveProjectConfig` undefined.

- [ ] **Step 3: Implement `SaveProjectConfig`**

Create `internal/app/config/save.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/llm/routing"
)

// SaveProjectConfig writes the essential settings-editable sections of cfg to
// path (typically .marshal/config.toml). It preserves any unrelated sections
// already present in the file.
func SaveProjectConfig(path string, cfg Config) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load existing project config: %w", err)
	}

	defaultProfile := cfg.Profile.Default
	file.Profile = &struct {
		Default *string `toml:"default"`
	}{Default: &defaultProfile}

	activePresetName := activePresetName(cfg)
	if activePresetName == "" {
		agentProvider := cfg.Agent.Provider
		agentModel := cfg.Agent.Model
		file.Agent = &struct {
			Provider *string `toml:"provider"`
			Model    *string `toml:"model"`
		}{Provider: &agentProvider, Model: &agentModel}
	} else {
		file.Agent = nil
	}

	remoteAllowed := cfg.Privacy.RemoteProvidersAllowed
	if file.Privacy == nil {
		file.Privacy = &struct {
			RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
			RedactSecrets          *bool `toml:"redact_secrets"`
			IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
		}{}
	}
	file.Privacy.RemoteProvidersAllowed = &remoteAllowed
	if activePresetName != "" {
		if preset, ok := cfg.Models.Presets[activePresetName]; ok {
			if file.Models == nil {
				file.Models = &struct {
					Presets map[string]modelPresetConfig `toml:"presets"`
				}{Presets: map[string]modelPresetConfig{}}
			}
			if file.Models.Presets == nil {
				file.Models.Presets = map[string]modelPresetConfig{}
			}
			file.Models.Presets[activePresetName] = modelPresetConfig{
				Provider:        preset.Provider,
				Model:           preset.Model,
				ContextWindow:   preset.ContextWindow,
				MaxOutputTokens: preset.MaxOutputTokens,
				Temperature:     preset.Temperature,
				TopP:            preset.TopP,
				ToolCalling:     preset.ToolCalling,
				ReasoningEffort: preset.ReasoningEffort,
				LocalOnly:       preset.LocalOnly,
			}
		}
	}

	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write project config: %w", err)
	}
	return nil
}

func activePresetName(cfg Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	presetName, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return ""
	}
	return presetName
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/config -run 'TestSaveProjectConfig' -v
```

Expected: PASS.

- [ ] **Step 5: Run the full package test suite**

Run:

```bash
go test ./internal/app/config/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/config/save.go internal/app/config/save_test.go
git commit -m "feat(config): add SaveProjectConfig for settings TUI"
```

---

### Task 2: Add settings messages and field types

**Files:**
- Create: `internal/app/tui/settings/messages.go`
- Create: `internal/app/tui/settings/field.go`

**Interfaces:**
- Produces: `SavedMsg`, `CancelledMsg`, `field` interface, `stringField`, `boolField`, `selectField`.

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/settings/field_test.go`:

```go
package settings

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
)

func TestBoolFieldToggle(t *testing.T) {
	cfg := config.Default()
	f := newBoolField("local-only", "Local only", &cfg.Privacy.RemoteProvidersAllowed, nil)
	if f.Value() != false {
		t.Fatalf("initial value = %v", f.Value())
	}
	f.Update(tea.KeyMsg{Type: tea.KeySpace})
	if f.Value() != true {
		t.Fatalf("toggled value = %v", f.Value())
	}
}
```

(Note: this test will fail to compile until the types exist; add the `tea` import once they do.)

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/tui/settings -run TestBoolFieldToggle -v
```

Expected: FAIL — package/types do not exist.

- [ ] **Step 3: Implement messages and fields**

Create `internal/app/tui/settings/messages.go`:

```go
package settings

import (
	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
)

type SavedMsg struct {
	Cfg config.Config
}

type CancelledMsg struct{}
```

Create `internal/app/tui/settings/field.go`:

```go
package settings

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type field interface {
	Label() string
	Focus()
	Blur()
	Update(msg tea.Msg)
	View(width int) string
}

type stringField struct {
	label     string
	input     textinput.Model
	onChange  func(string)
}

func newStringField(label, value string, onChange func(string)) *stringField {
	inp := textinput.New()
	inp.SetValue(value)
	inp.Prompt = ""
	return &stringField{label: label, input: inp, onChange: onChange}
}

func (f *stringField) Label() string { return f.label }

func (f *stringField) Focus() {
	f.input.Focus()
}

func (f *stringField) Blur() {
	f.input.Blur()
}

func (f *stringField) Update(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f.input, _ = f.input.Update(msg)
		f.onChange(f.input.Value())
	}
}

func (f *stringField) View(width int) string {
	return fmt.Sprintf("%s: %s", f.label, f.input.View())
}

type boolField struct {
	label     string
	value     *bool
	onChange  func(bool)
}

func newBoolField(label string, desc string, value *bool, onChange func(bool)) *boolField {
	return &boolField{label: fmt.Sprintf("%s (%s)", label, desc), value: value, onChange: onChange}
}

func (f *boolField) Label() string { return f.label }

func (f *boolField) Focus() {}

func (f *boolField) Blur() {}

func (f *boolField) Update(msg tea.Msg) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeySpace, tea.KeyEnter:
			*f.value = !*f.value
			if f.onChange != nil {
				f.onChange(*f.value)
			}
		}
	}
}

func (f *boolField) View(width int) string {
	marker := " "
	if *f.value {
		marker = "x"
	}
	return fmt.Sprintf("[%s] %s", marker, f.label)
}

func (f *boolField) Value() bool { return *f.value }

type selectField struct {
	label    string
	options  []string
	selected int
	onChange func(string)
}

func newSelectField(label string, options []string, current string, onChange func(string)) *selectField {
	selected := 0
	for i, opt := range options {
		if opt == current {
			selected = i
			break
		}
	}
	return &selectField{label: label, options: options, selected: selected, onChange: onChange}
}

func (f *selectField) Label() string { return f.label }

func (f *selectField) Focus() {}

func (f *selectField) Blur() {}

func (f *selectField) Update(msg tea.Msg) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyLeft, tea.KeyUp:
			if f.selected > 0 {
				f.selected--
				f.onChange(f.options[f.selected])
			}
		case tea.KeyRight, tea.KeyDown, tea.KeyEnter, tea.KeySpace:
			if f.selected < len(f.options)-1 {
				f.selected++
				f.onChange(f.options[f.selected])
			}
		}
	}
}

func (f *selectField) View(width int) string {
	var parts []string
	for i, opt := range f.options {
		if i == f.selected {
			parts = append(parts, fmt.Sprintf(">%s<", opt))
		} else {
			parts = append(parts, opt)
		}
	}
	return fmt.Sprintf("%s: %s", f.label, strings.Join(parts, "  "))
}

func (f *selectField) Value() string {
	if len(f.options) == 0 {
		return ""
	}
	return f.options[f.selected]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/tui/settings -run TestBoolFieldToggle -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/messages.go internal/app/tui/settings/field.go internal/app/tui/settings/field_test.go
git commit -m "feat(tui/settings): add settings field types and messages"
```

---

### Task 3: Add settings model and view

**Files:**
- Create: `internal/app/tui/settings/model.go`
- Create: `internal/app/tui/settings/view.go`
- Create: `internal/app/tui/settings/model_test.go`

**Interfaces:**
- Produces: `settings.Model`, `settings.New(cfg config.Config, projectConfigPath string) Model`.

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/settings/model_test.go`:

```go
package settings

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func newTestConfig() config.Config {
	cfg := config.Default()
	cfg.Profile.Default = "local_balanced"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
	}
	return cfg
}

func TestNewModelHasFields(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	if len(m.fields) == 0 {
		t.Fatal("expected fields")
	}
}

func TestCancelReturnsCancelledMsg(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	if _, ok := msg.(CancelledMsg); !ok {
		t.Fatalf("expected CancelledMsg, got %T", msg)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/tui/settings -run 'TestNewModelHasFields|TestCancelReturnsCancelledMsg' -v
```

Expected: FAIL — `Model` undefined.

- [ ] **Step 3: Implement the model and view**

Create `internal/app/tui/settings/model.go`:

```go
package settings

import (
	"fmt"
	"path/filepath"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

type Model struct {
	cfg             config.Config
	workingDir      string
	projectCfgPath  string
	fields          []field
	focused         int
	footer          string
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	m := Model{
		cfg:            cfg,
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}

	profileNames := make([]string, 0, len(cfg.AgentProfiles))
	for name := range cfg.AgentProfiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	if len(profileNames) == 0 {
		profileNames = []string{""}
	}

	m.fields = append(m.fields, newSelectField(
		"Default profile",
		profileNames,
		cfg.Profile.Default,
		func(v string) { m.cfg.Profile.Default = v },
	))

	activePreset := activePresetFromConfig(cfg)
	presetName := ""
	if activePreset.Name != "" {
		presetName = activePreset.Name
	}

	m.fields = append(m.fields, newStringField(
		"Preset",
		presetName,
		func(v string) {
			// Preset name is read-only in v1; edits apply to the active preset.
		},
	))

	provider := cfg.Agent.Provider
	model := cfg.Agent.Model
	localOnly := false
	if activePreset.Name != "" {
		provider = activePreset.Provider
		model = activePreset.Model
		localOnly = activePreset.LocalOnly
	}

	m.fields = append(m.fields, newStringField(
		"Provider",
		provider,
		func(v string) {
			if activePreset.Name != "" {
				if p, ok := m.cfg.Models.Presets[activePreset.Name]; ok {
					p.Provider = v
					m.cfg.Models.Presets[activePreset.Name] = p
				}
			} else {
				m.cfg.Agent.Provider = v
			}
		},
	))

	m.fields = append(m.fields, newStringField(
		"Model",
		model,
		func(v string) {
			if activePreset.Name != "" {
				if p, ok := m.cfg.Models.Presets[activePreset.Name]; ok {
					p.Model = v
					m.cfg.Models.Presets[activePreset.Name] = p
				}
			} else {
				m.cfg.Agent.Model = v
			}
		},
	))

	m.fields = append(m.fields, newBoolField(
		"Local only",
		"block remote providers for this preset",
		&localOnly,
		func(v bool) {
			if activePreset.Name != "" {
				if p, ok := m.cfg.Models.Presets[activePreset.Name]; ok {
					p.LocalOnly = v
					m.cfg.Models.Presets[activePreset.Name] = p
				}
			}
		},
	))

	m.fields = append(m.fields, newBoolField(
		"Remote providers allowed",
		"allow remote providers globally",
		&m.cfg.Privacy.RemoteProvidersAllowed,
		nil,
	))

	if len(m.fields) > 0 {
		m.fields[0].Focus()
	}
	return m
}

func activePresetFromConfig(cfg config.Config) routing.ModelPreset {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return routing.ModelPreset{}
	}
	name, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return routing.ModelPreset{}
	}
	if preset, ok := cfg.Models.Presets[name]; ok {
		return preset
	}
	return routing.ModelPreset{Name: name}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return m, func() tea.Msg { return CancelledMsg{} }
		case tea.KeyCtrlS:
			return m, m.saveCmd()
		case tea.KeyTab:
			m.nextField()
			return m, nil
		case tea.KeyShiftTab:
			m.prevField()
			return m, nil
		default:
			if m.focused >= 0 && m.focused < len(m.fields) {
				m.fields[m.focused].Update(msg)
			}
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) nextField() {
	if m.focused >= 0 && m.focused < len(m.fields) {
		m.fields[m.focused].Blur()
	}
	m.focused++
	if m.focused >= len(m.fields) {
		m.focused = 0
	}
	m.fields[m.focused].Focus()
}

func (m *Model) prevField() {
	if m.focused >= 0 && m.focused < len(m.fields) {
		m.fields[m.focused].Blur()
	}
	m.focused--
	if m.focused < 0 {
		m.focused = len(m.fields) - 1
	}
	m.fields[m.focused].Focus()
}

func (m *Model) saveCmd() tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveProjectConfig(m.projectCfgPath, m.cfg); err != nil {
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

func (m Model) Footer() string {
	return m.footer
}
```

Create `internal/app/tui/settings/view.go`:

```go
package settings

import (
	"fmt"
	"strings"
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString("┌── Settings ───────────────────────────────────────────┐\n")
	for _, f := range m.fields {
		line := f.View(50)
		b.WriteString(fmt.Sprintf("│ %s\n", line))
	}
	b.WriteString("├───────────────────────────────────────────────────────┤\n")
	if m.footer != "" {
		b.WriteString(fmt.Sprintf("│ %s\n", m.footer))
	}
	b.WriteString("│ [Ctrl+S] Save  [Esc] Cancel  [Tab] Next field         │\n")
	b.WriteString("└───────────────────────────────────────────────────────┘\n")
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/tui/settings -run 'TestNewModelHasFields|TestCancelReturnsCancelledMsg' -v
```

Expected: PASS.

- [ ] **Step 5: Run the full package test suite**

Run:

```bash
go test ./internal/app/tui/settings/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/settings/model.go internal/app/tui/settings/view.go internal/app/tui/settings/model_test.go
git commit -m "feat(tui/settings): add settings model and view"
```

---

### Task 4: Wire settings into the main TUI model

**Files:**
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`

**Interfaces:**
- Produces: `ConfigReloader` option, `Ctrl+O` handling, settings overlay rendering.

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/tui/model_test.go`:

```go
func TestCtrlOOpensSettings(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen to be true")
	}
}

func TestSettingsCancelClosesOverlay(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}
	m, _ = m.Update(settings.CancelledMsg{})
	if m.settingsOpen {
		t.Fatal("expected settingsOpen to be false after cancel")
	}
}
```

Add the needed imports to `model_test.go`:

```go
"github.com/charmbracelet/bubbletea"
"marshal/internal/app/tui/settings"
```

- [ ] **Step 2: Run tests to verify they fail"

Run:

```bash
go test ./internal/app/tui -run 'TestCtrlOOpensSettings|TestSettingsCancelClosesOverlay' -v
```

Expected: FAIL — `settingsOpen` and helper undefined.

- [ ] **Step 3: Add the cancelled-message helper and update main model**

Modify `internal/app/tui/model.go`:

Add import:

```go
"marshal/internal/app/tui/settings"
```

Add type and option:

```go
type ConfigReloader func(cfg config.Config) error

func WithConfigReloader(fn ConfigReloader) Option {
	return func(m *Model) {
		m.configReloader = fn
	}
}
```

Add fields to `Model`:

```go
settingsOpen     bool
settingsModel    settings.Model
configReloader   ConfigReloader
```

In `Update`, before the existing key handling, intercept `Ctrl+O` and settings mode:

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	tc := m.state.PendingApproval()

	switch msg := msg.(type) {
	case settings.SavedMsg:
		m.state.Config = msg.Cfg
		if m.configReloader != nil {
			if err := m.configReloader(msg.Cfg); err != nil {
				m.state.SetProviderError(err)
				m.settingsModel = settings.New(msg.Cfg, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
				return m, nil
			}
		}
		m.settingsOpen = false
		return m, nil
	case settings.CancelledMsg:
		m.settingsOpen = false
		return m, nil
	}

	if m.settingsOpen {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.Type == tea.KeyCtrlO {
				m.settingsOpen = false
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.settingsModel, cmd = m.settingsModel.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	// ... existing cases ...
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlO {
			m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
			m.settingsOpen = true
			return m, nil
		}
		// ... rest of existing key handling ...
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func projectConfigPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "config.toml")
}
```

- [ ] **Step 4: Update view to render settings overlay**

In `internal/app/tui/model.go` `View()`, at the end before returning, if `m.settingsOpen`, return `m.settingsModel.View()` (or append it over the main view). For v1, render the settings screen full-screen.

```go
func (m Model) View() string {
	if m.settingsOpen {
		return m.settingsModel.View()
	}
	// existing view body
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/tui -run 'TestCtrlOOpensSettings|TestSettingsCancelClosesOverlay' -v
```

Expected: PASS.

- [ ] **Step 6: Run the full package test suite**

Run:

```bash
go test ./internal/app/tui/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go internal/app/tui/settings/messages.go
git commit -m "feat(tui): wire settings overlay into main model"
```

---

### Task 5: Pass config reloader from app.go

**Files:**
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: `tui.ConfigReloader`, `tui.WithConfigReloader`.

- [ ] **Step 1: Add the reloader option in app.go**

In `internal/app/app.go`, after building the runner and before creating TUI options, add:

```go
configReloader := func(newCfg config.Config) error {
	state.Config = newCfg
	if runner == nil {
		return nil
	}
	resolver := newRoutedProviderResolver(newCfg)
	route, p, err := resolver.Resolve(routing.TaskProfile{Class: "edit"})
	if err != nil {
		return err
	}
	runner.Provider = p
	runner.Model = route.Preset.Model
	runner.RouteResolver = resolver
	state.SetActiveRoute(session.RouteInfo{
		Role:      route.Role,
		Profile:   route.Profile,
		Preset:    route.Preset.Name,
		Provider:  route.Preset.Provider,
		Model:     route.Preset.Model,
		LocalOnly: route.Preset.LocalOnly,
		Legacy:    route.Legacy,
		Active:    true,
	})
	return nil
}
```

Then pass it to the TUI:

```go
var tuiOpts []tui.Option
if runner, err := buildAgentRunner(ctx, cfg, state, database, projectID); err == nil {
	tuiOpts = append(tuiOpts, tui.WithRunner(ctx, runner))
	tuiOpts = append(tuiOpts, tui.WithConfigReloader(configReloader))
} else {
	state.SetProviderError(err)
}
```

- [ ] **Step 2: Run tests to verify the app still builds and passes**

Run:

```bash
go test ./internal/app/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/app/app.go
git commit -m "feat(app): pass config reloader to TUI for settings changes"
```

---

### Task 6: Update MVP checklist

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

- [ ] **Step 1: Insert Milestone N.5**

After the Milestone N section and before Milestone O, add:

```markdown
## Milestone N.5: Settings TUI

- [x] Add `config.SaveProjectConfig`
- [x] Add settings form fields
- [x] Add settings Bubble Tea model
- [x] Wire settings overlay into main TUI
- [x] Apply saved config to agent runner
- [x] Add `Ctrl+O` shortcut
```

- [ ] **Step 2: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: add Milestone N.5 settings TUI to checklist"
```

---

### Task 7: Final verification

- [ ] **Step 1: Run the full test suite**

Run:

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go test ./...
```

Expected: PASS, no vet warnings.

- [ ] **Step 2: Manual smoke test**

Run:

```bash
go build ./cmd/marshal
./marshal
```

Press `Ctrl+O`, change the model string, press `Ctrl+S`, and verify the status bar reflects the new model on the next turn.

- [ ] **Step 3: Check status**

Run:

```bash
git status --short
```

Expected: only the files from this plan are modified or untracked.

- [ ] **Step 4: Final review (optional but recommended)**

Request a code review on the full diff from the branch base to current HEAD using `superpowers:requesting-code-review`.
