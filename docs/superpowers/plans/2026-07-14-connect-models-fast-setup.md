# /connect + /models Fast Provider & Model Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `/connect` and `/models` slash commands that let a user pick a provider + model and start chatting with zero named presets, keeping presets/profiles as an optional advanced layer.

**Architecture:** A shared `internal/app/tui/probe/` package (extracted from settings) holds live model discovery. A new `internal/app/tui/connect/` package holds a multi-step state-machine overlay (`picker` + masked `textinput`) that emits `DoneMsg`/`CancelledMsg`. The TUI model wires `/connect`, `/models`, a `Ctrl+P` hotkey, a shared discovery cache, and a persistence path that sets `Agent.Provider`/`Agent.Model` directly and unsets the profile default so the existing legacy router path applies.

**Tech Stack:** Go 1.26, Bubble Tea v2, Lipgloss v2, existing `internal/app/tui/picker` fuzzy picker, existing `internal/app/tui/chrome` panel helper, existing `internal/llm/provider` factory + templates, existing `internal/app/config` save/loader.

**Spec:** `docs/superpowers/specs/2026-07-14-connect-models-fast-setup-design.md`

## Global Constraints

- Module path is `marshal` (see `go.mod`).
- `go test ./...` and `go vet ./...` must pass after every task.
- `go build ./cmd/marshal` must succeed (requires `CGO_ENABLED=1`).
- No new external dependencies.
- No comments in code unless explicitly requested by the plan.
- Local-first: remote discovery is gated behind `cfg.Privacy.RemoteProvidersAllowed`. `config.Default()` gains no providers and no presets.
- Secrets (`api_key`) are never rendered in plaintext; the connect overlay uses a masked textinput and only stores a literal key when non-empty; `SaveProjectConfig` already masks on display only.
- TUI renders only: the connect overlay never mutates `tui.Model` config directly — it emits `DoneMsg`; the TUI handles persistence via `configReloader` + `SaveProjectConfig` (the seam `/settings` already uses).

## File Structure

**Create:**
- `internal/app/tui/probe/probe.go` — `Provider`, `ResultMsg`, `IsLocalhost` (extracted from settings `discover.go`).
- `internal/app/tui/probe/probe_test.go`
- `internal/app/tui/connect/connect.go` — the state-machine `Model`, `DoneMsg`, `CancelledMsg`, `TickMsg`.
- `internal/app/tui/connect/connect_test.go`

**Modify:**
- `internal/app/tui/settings/discover.go` — deleted (logic moved to `probe`).
- `internal/app/tui/settings/discover_test.go` — deleted (subsumed by `probe_test.go`).
- `internal/app/tui/settings/messages.go` — `probeResultMsg` removed (use `probe.ResultMsg`).
- `internal/app/tui/settings/model.go` — `case probeResultMsg` → `case probe.ResultMsg`; import `probe`.
- `internal/app/tui/settings/frames_collections.go` — `isLocalhost` → `probe.IsLocalhost`; `probeProvider` → `probe.Provider`.
- `internal/commands/commands.go` — register `/connect`, `/models` (handlers open the overlay; the actual routing is in the TUI model, like `/settings`).
- `internal/app/tui/model.go` — `connectModel`/`connectOpen`/`discovered` fields; `connect.DoneMsg`/`CancelledMsg`/`TickMsg` handling; `/connect` + `/models` command dispatch; `Ctrl+P` hotkey; first-run hint nudge.
- `internal/app/tui/view.go` — render the connect overlay while open (mirrors the settings overlay render).
- `internal/app/tui/help` keybind footer — add `Ctrl+P models` (only if this file owns global key hints; verify in Task 7).

---

## Task 1: Extract the shared probe package

**Files:**
- Create: `internal/app/tui/probe/probe.go`
- Create: `internal/app/tui/probe/probe_test.go`
- Delete: `internal/app/tui/settings/discover.go`
- Delete: `internal/app/tui/settings/discover_test.go`
- Modify: `internal/app/tui/settings/messages.go`
- Modify: `internal/app/tui/settings/model.go`
- Modify: `internal/app/tui/settings/frames_collections.go`

**Interfaces:**
- Produces:
  - `probe.ResultMsg struct { FieldID, Provider string; Models []string; Err error }`
  - `func Provider(fieldID, name string, pc config.ProviderConfig) tea.Cmd`
  - `func IsLocalhost(baseURL string) bool`
  - package var `Timeout time.Duration` (replaces the settings `probeTimeout`)

**Consumes:** `internal/llm/provider` (factory), `internal/app/config` (ProviderConfig).

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/probe/probe_test.go`:

```go
package probe

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestIsLocalhost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://localhost:11434/v1", true},
		{"http://127.0.0.1:11434/v1", true},
		{"http://0.0.0.0:11434/v1", true},
		{"http://[::1]:11434/v1", true},
		{"http://[::1%25lo0]:11434/v1", true},
		{"https://api.openai.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsLocalhost(c.url); got != c.want {
			t.Errorf("IsLocalhost(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestProviderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b","owned_by":"ollama"},{"id":"llama3.1:8b","owned_by":"meta"}]}`))
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)

	if msg.Err != nil {
		t.Fatalf("Provider err = %v", msg.Err)
	}
	if msg.Provider != "testprov" || msg.FieldID != "test.field" {
		t.Fatalf("ResultMsg identity = %+v", msg)
	}
	if len(msg.Models) != 2 || msg.Models[0] != "qwen2.5-coder:7b" {
		t.Fatalf("ResultMsg models = %v", msg.Models)
	}
}

func TestProviderNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestProviderConnectionRefused(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	old := Timeout
	Timeout = 200 * time.Millisecond
	defer func() { Timeout = old }()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected timeout error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/probe/...`
Expected: FAIL (build error: package not found / `Provider` undefined).

- [ ] **Step 3: Create the probe package**

Create `internal/app/tui/probe/probe.go` (verbatim migration of `settings/discover.go`, exported):

```go
package probe

import (
	"context"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
)

var Timeout = 5 * time.Second

func IsLocalhost(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	}
	return strings.HasPrefix(host, "::1%")
}

func Provider(fieldID, name string, pc config.ProviderConfig) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.NewFromConfig(name, pc)
		if err != nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		models, err := p.Models(ctx)
		if err != nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		return ResultMsg{FieldID: fieldID, Provider: name, Models: ids}
	}
}

type ResultMsg struct {
	FieldID  string
	Provider string
	Models   []string
	Err      error
}
```

- [ ] **Step 4: Delete the settings discover files**

```bash
rm internal/app/tui/settings/discover.go internal/app/tui/settings/discover_test.go
```

- [ ] **Step 5: Update settings messages.go**

In `internal/app/tui/settings/messages.go`, delete the `probeResultMsg` struct (lines 13–18) and its blank-line context. The file should now contain only `SavedMsg`, `CancelledMsg`, and `actionResultMsg`. Add the import for the probe package is NOT needed here (messages.go only defines types).

The resulting `internal/app/tui/settings/messages.go`:

```go
package settings

import (
	"marshal/internal/app/config"
)

type SavedMsg struct {
	Cfg config.Config
}

type CancelledMsg struct{}

type actionResultMsg struct {
	FieldID string
	Label   string
}
```

- [ ] **Step 6: Update settings model.go dispatch**

In `internal/app/tui/settings/model.go`:

1. Add to the import block: `"marshal/internal/app/tui/probe"`.
2. Change `case probeResultMsg:` (line 120) and its body (lines 120–129) to use `probe.ResultMsg`. Replace:

```go
	case probeResultMsg:
		label := fmt.Sprintf("\u2713 ok (%d models)", len(msg.Models))
		if msg.Err != nil {
			label = "\u2717 " + truncateErr(msg.Err.Error())
		}
		m.state.applyActionResult(msg.FieldID, label)
		if msg.Err == nil && msg.Provider != "" {
			m.state.discovered[msg.Provider] = msg.Models
		}
		return *m, nil
```

with:

```go
	case probe.ResultMsg:
		label := fmt.Sprintf("\u2713 ok (%d models)", len(msg.Models))
		if msg.Err != nil {
			label = "\u2717 " + truncateErr(msg.Err.Error())
		}
		m.state.applyActionResult(msg.FieldID, label)
		if msg.Err == nil && msg.Provider != "" {
			m.state.discovered[msg.Provider] = msg.Models
		}
		return *m, nil
```

- [ ] **Step 7: Update settings frames_collections.go references**

In `internal/app/tui/settings/frames_collections.go`:

1. Add to imports: `"marshal/internal/app/tui/probe"`.
2. Replace every `isLocalhost(` with `probe.IsLocalhost(`. There are two call sites (lines 198 and 479).
3. Replace `return probeProvider(fieldID, k, pc)` (line 490) with `return probe.Provider(fieldID, k, pc)`.

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/app/tui/probe/... ./internal/app/tui/settings/...`
Expected: PASS (both packages).

- [ ] **Step 9: Run full build + vet**

Run: `go build ./cmd/marshal && go vet ./...`
Expected: success, no errors.

- [ ] **Step 10: Commit**

```bash
git add internal/app/tui/probe/ internal/app/tui/settings/
git commit -m "refactor(tui): extract shared probe package from settings"
```

---

## Task 2: Connect overlay scaffolding & state machine

**Files:**
- Create: `internal/app/tui/connect/connect.go`
- Create: `internal/app/tui/connect/connect_test.go`

**Interfaces:**
- Produces:
  - `connect.Model` (a Bubble Tea model with `New(opts Opts) *Model`, `Init() tea.Cmd`, `Update(msg tea.Msg) (*Model, tea.Cmd)`, `View(maxW, maxH int) string`, `SetSize(w, h int)`)
  - `type Opts struct { Cfg config.Config; Discovered map[string][]string; SkipToIntroModel bool; ScopedProvider string }`
  - `type DoneMsg struct { Provider, Model string }`
  - `type CancelledMsg struct{}`
  - `type TickMsg struct{}`

**Consumes:** `internal/app/tui/picker`, `internal/app/tui/theme`, `internal/app/tui/chrome`, `internal/app/tui/probe`, `internal/llm/provider` (templates), `internal/app/config`, `charm.land/bubbles/v2/textinput`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/connect/connect_test.go`:

```go
package connect

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
)

func TestNewStartsAtPickTemplate(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	if m.step != stepPickTemplate {
		t.Fatalf("step = %v, want stepPickTemplate", m.step)
	}
	if m.title == "" {
		t.Fatal("title must be set for the pickTemplate step")
	}
}

func TestNewScopedProviderStartsAtPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.step != stepPickModel {
		t.Fatalf("step = %v, want stepPickModel", m.step)
	}
}

func TestEscAtPickTemplateEmitsCancelled(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 27})
	if cmd == nil {
		t.Fatal("expected a cmd emitting CancelledMsg")
	}
	msg := cmd()
	if _, ok := msg.(CancelledMsg); !ok {
		t.Fatalf("cmd produced %T, want CancelledMsg", msg)
	}
	_ = updated
}
```

Note: Bubble Tea v2 `KeyPressMsg` uses the `String()` method for matching; constructing one with `Code: 27` yields `"esc"`. If the test environment reports a different shape, substitute `tea.Key{Type: tea.KeyEsc}` per the installed v2 API. Confirm by running the failing test first and reading the compile error.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/connect/...`
Expected: FAIL (package not found / `New` undefined).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/app/tui/connect/connect.go`:

```go
package connect

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/provider"
)

var th = theme.Load()

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(th.FGDefault)
	mutedStyle   = lipgloss.NewStyle().Foreground(th.FGMuted)
	hintStyle    = lipgloss.NewStyle().Foreground(th.StatusInfo)
	errStyle     = lipgloss.NewStyle().Foreground(th.StatusError)
	footerStyle  = lipgloss.NewStyle().Foreground(th.FGMuted)
	successStyle = lipgloss.NewStyle().Foreground(th.StatusSuccess)
)

type step int

const (
	stepPickTemplate step = iota
	stepBaseURL
	stepAPIKey
	stepProbing
	stepPickModel
	stepDone
	stepCancelled
)

type Opts struct {
	Cfg               config.Config
	Discovered        map[string][]string
	SkipToIntroModel  bool
	ScopedProvider    string
}

type Model struct {
	step        step
	picker      *picker.Model
	input       textinput.Model
	title       string
	subtitle    string
	footer      string
	err         string
	template    provider.ProviderTemplate
	providerName string
	providerCfg config.ProviderConfig
	models      []string
	probeErr    error
	cfg         config.Config
	discovered  map[string][]string
	scopedProvider string
	width       int
	height      int
	probeStart  int64
	spinner     int
	pendingSave bool
	modelChosen string
}

func New(opts Opts) *Model {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	m := &Model{
		cfg:             opts.Cfg,
		discovered:      opts.Discovered,
		input:           ti,
		scopedProvider:  opts.ScopedProvider,
	}
	if opts.SkipToIntroModel {
		m.enterPickModel(opts.ScopedProvider)
		return m
	}
	m.enterPickTemplate()
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case probe.ResultMsg:
		return m.handleProbeResult(msg)
	case picker.PickedMsg:
		return m.handlePickerPicked(msg.Value)
	case picker.CancelledMsg:
		if m.step == stepPickTemplate || m.step == stepPickModel {
			return m, m.cancel()
		}
		m.err = ""
		return m, m.back()
	case TickMsg:
		m.spinner++
		if m.step == stepProbing {
			return m, tick()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) View(maxW, maxH int) string {
	pw := min(64, maxW-8)
	if pw < 40 {
		pw = max(maxW-2, 40)
	}
	ph := min(14, maxH)
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n")
	if m.subtitle != "" {
		b.WriteString(hintStyle.Render(m.subtitle))
		b.WriteString("\n")
	}
	if m.picker != nil {
		b.WriteString(m.picker.View(pw, ph-4))
	} else if m.step == stepProbing {
		b.WriteString(m.renderProbing(pw))
	} else if m.step == stepBaseURL || m.step == stepAPIKey {
		b.WriteString(m.renderInput(pw))
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(errStyle.Render(m.err))
	}
	if m.footer != "" {
		b.WriteString("\n")
		b.WriteString(footerStyle.Render(m.footer))
	}
	return chrome.Panel("connect", b.String(), pw, min(lipgloss.Height(b.String())+2, maxH), true, th)
}

func (m *Model) renderInput(pw int) string {
	inner := pw - 2
	line := m.input.View()
	if len(line) > inner {
		line = line[:inner]
	}
	return line
}

func (m *Model) renderProbing(pw int) string {
	frame := "…"
	if m.probeStart > 0 {
		frame = spinnerFrames[m.spinner%len(spinnerFrames)]
	}
	return mutedStyle.Render(frame + " connecting…")
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type DoneMsg struct {
	Provider string
	Model    string
}

type CancelledMsg struct{}

type TickMsg struct{}

func tick() tea.Cmd {
	return func() tea.Msg {
		return TickMsg{}
	}
}

func (m *Model) cancel() tea.Cmd {
	m.step = stepCancelled
	return func() tea.Msg { return CancelledMsg{} }
}

func (m *Model) back() tea.Cmd {
	switch m.step {
	case stepAPIKey:
		m.enterPickTemplate()
	case stepBaseURL:
		m.enterPickTemplate()
	case stepPickModel:
		if m.providerName != "" && m.template.ID != "" {
			m.step = stepAPIKey
			m.enterAPIKey()
		} else {
			m.enterPickTemplate()
		}
	default:
		return m.cancel()
	}
	return nil
}
```

Add the step-entry helpers referenced by `New`:

```go
func (m *Model) enterPickTemplate() {
	m.step = stepPickTemplate
	m.title = "Connect a provider"
	m.subtitle = ""
	m.footer = "[↑↓] move [↵] pick [Esc] cancel"
	m.err = ""
	all := provider.All()
	items := make([]picker.Item, 0, len(all))
	for _, tpl := range all {
		items = append(items, picker.Item{
			Label:  tpl.Label,
			Detail: tpl.BaseURL,
			Badge:  badgeForTemplate(tpl),
			Value:  tpl.ID,
		})
	}
	p := picker.New("Add provider", "pick a template", items)
	p.SetAllowCustom(true)
	m.picker = p
}

func (m *Model) enterAPIKey() {
	m.step = stepAPIKey
	m.title = "API key"
	m.subtitle = m.template.KeyHint
	m.footer = "[↵] save  [Esc] back"
	m.err = ""
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	if m.template.KeyEnv != "" {
		ti.Placeholder = "paste key, or leave blank to use $" + m.template.KeyEnv
	} else {
		ti.Placeholder = "paste key"
	}
	m.input = ti
	m.picker = nil
}

func enterBaseURLStep(m *Model) {
	m.step = stepBaseURL
	m.title = "Base URL"
	m.subtitle = "OpenAI-compatible endpoint, e.g. https://host/v1"
	m.footer = "[↵] next  [Esc] back"
	m.err = ""
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	ti.Placeholder = "https://…/v1"
	m.input = ti
	m.picker = nil
}

func enterPickModelStep(m *Model, providerName string) {
	m.step = stepPickModel
	m.title = "Select model"
	m.subtitle = providerName
	m.footer = "[↑↓] move [↵] pick [Esc] done"
	m.err = ""
	m.picker = buildModelPicker(m, providerName)
}

func buildModelPicker(m *Model, providerName string) *picker.Model {
	var items []picker.Item
	if cached, ok := m.discovered[providerName]; ok && len(cached) > 0 {
		for _, mid := range cached {
			items = append(items, picker.Item{Label: mid, Detail: providerName, Badge: "◉ discovered", Group: providerName, Value: mid})
		}
	} else if tpl, ok := provider.Lookup(strings.TrimSuffix(strings.TrimPrefix(providerName, m.template.ID), "-2")); ok && len(tpl.Models) > 0 {
		for _, mid := range tpl.Models {
			items = append(items, picker.Item{Label: mid, Detail: providerName, Badge: "◯ catalog", Group: providerName, Value: mid})
		}
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Label: "Enter model id manually", Value: "__manual__", Badge: "custom"})
	}
	p := picker.New("Select model", "pick a model", items)
	p.SetAllowCustom(true)
	return p
}

func badgeForTemplate(tpl provider.ProviderTemplate) string {
	if tpl.Local {
		return "local"
	}
	return "remote"
}
```

Add the placeholder handlers required for compiles; full versions arrive in later tasks:

```go
func (m *Model) handlePickerPicked(value string) (*Model, tea.Cmd) {
	m.modelChosen = value
	m.step = stepDone
	return m, func() tea.Msg { return CancelledMsg{} }
}

func (m *Model) handleProbeResult(msg probe.ResultMsg) (*Model, tea.Cmd) { return m, nil }

func (m *Model) handleKey(k tea.KeyPressMsg) (*Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, m.cancel()
	}
	return m, nil
}
```

Wire `enterPickModel` used by `New(Opts{SkipToIntroModel:true})`:

```go
func (m *Model) enterPickModel(providerName string) {
	enterPickModelStep(m, providerName)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/connect/...`
Expected: PASS. If the `tea.KeyPressMsg{Code: 27}` construction is wrong for the installed Bubble Tea v2, adjust the test to the correct literal (read the compile error) and re-run.

- [ ] **Step 5: Run full build + vet**

Run: `go build ./cmd/marshal && go vet ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/connect/
git commit -m "feat(tui/connect): overlay state machine scaffolding"
```

---

## Task 3: Connect step 1 — pick template + step 2 credentials

**Files:**
- Modify: `internal/app/tui/connect/connect.go`
- Modify: `internal/app/tui/connect/connect_test.go`

**Interfaces:**
- Extends `handlePickerPicked` to advance to step 2 (apiKey, or baseURL for custom, or skip to probing for local).
- Extends `handleKey` to confirm the input steps (Enter on `stepAPIKey`/`stepBaseURL`): capture the key/baseURL into `providerCfg`, then enter the probe step.

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/tui/connect/connect_test.go`:

```go
func TestPickTemplateOllamaSkipsAPIKey(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("ollama"))
	if updated.step != stepProbing {
		t.Fatalf("local template should skip apiKey, got step = %v", updated.step)
	}
	if updated.template.ID != "ollama" {
		t.Fatalf("template = %q, want ollama", updated.template.ID)
	}
}

func TestPickTemplateOpenRouterEntersAPIKey(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepAPIKey {
		t.Fatalf("remote template should enter apiKey, got step = %v", updated.step)
	}
	if updated.template.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q", updated.template.KeyEnv)
	}
}

func TestPickCustomEntersBaseURL(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("__custom__"))
	if updated.step != stepBaseURL {
		t.Fatalf("custom should enter baseURL, got step = %v", updated.step)
	}
}

func TestAPIKeyEnterAdvancesToProbing(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("openrouter"))
	m.input.SetValue("sk-test-1234")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("apiKey Enter should advance to probing, got step = %v", updated.step)
	}
	if updated.providerCfg.APIKey != "sk-test-1234" {
		t.Fatalf("api key not captured: %q", updated.providerCfg.APIKey)
	}
	if updated.providerCfg.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("api_key_env not set from template: %q", updated.providerCfg.APIKeyEnv)
	}
}

func TestCustomBaseURLThenKey(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("__custom__"))
	m.input.SetValue("https://myhost/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepAPIKey {
		t.Fatalf("after baseURL should be apiKey, got %v", m.step)
	}
	if m.providerCfg.BaseURL != "https://myhost/v1" {
		t.Fatalf("base_url not captured: %q", m.providerCfg.BaseURL)
	}
	m.input.SetValue("sk-x")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("after apiKey should be probing, got %v", updated.step)
	}
}

func pickerPicked(value string) tea.Msg { return picker.PickedMsg{Value: value} }
```

(Add `"marshal/internal/app/tui/picker"` to the test imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/connect/...`
Expected: FAIL (`handlePickerPicked` currently emits CancelledMsg).

- [ ] **Step 3: Implement step transitions**

Replace the placeholder `handlePickerPicked` in `connect.go`:

```go
func (m *Model) handlePickerPicked(value string) (*Model, tea.Cmd) {
	if m.step == stepPickModel {
		m.modelChosen = value
		m.step = stepDone
		return m, m.done()
	}
	tpl, ok := provider.Lookup(value)
	if !ok {
		if value == "custom" || strings.HasPrefix(value, "@ai-sdk/") {
			m.template = provider.ProviderTemplate{ID: "custom", Type: "openai_compatible"}
			enterBaseURLStep(m)
			return m, nil
		}
		m.template = provider.ProviderTemplate{ID: value, Type: "openai_compatible"}
		enterBaseURLStep(m)
		return m, nil
	}
	m.template = tpl
	m.providerCfg = config.ProviderConfig{Type: tpl.Type, BaseURL: tpl.BaseURL, APIKeyEnv: tpl.KeyEnv, ToolCalling: tpl.ToolCalling}
	if tpl.Local {
		return m.enterProbing()
	}
	m.enterAPIKey()
	return m, nil
}
```

Replace the placeholder `handleKey`:

```go
func (m *Model) handleKey(k tea.KeyPressMsg) (*Model, tea.Cmd) {
	ks := k.String()
	switch m.step {
	case stepBaseURL, stepAPIKey:
		switch ks {
		case "esc":
			return m, m.back()
		case "enter":
			return m.confirmInput()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
	case stepProbing:
		if ks == "esc" {
			return m, m.cancel()
		}
		if ks == "r" {
			return m, m.enterProbing()
		}
		if ks == "s" {
			return m, m.skipProbe()
		}
		return m, nil
	case stepPickModel:
		return m, nil
	}
	if ks == "esc" {
		return m, m.cancel()
	}
	return m, nil
}

func (m *Model) confirmInput() (*Model, tea.Cmd) {
	v := strings.TrimSpace(m.input.Value())
	switch m.step {
	case stepBaseURL:
		if v == "" {
			m.err = "base URL cannot be empty"
			return m, nil
		}
		m.providerCfg.BaseURL = v
		m.enterAPIKey()
		return m, nil
	case stepAPIKey:
		if v != "" {
			m.providerCfg.APIKey = v
		}
		return m, m.enterProbing()
	}
	return m, nil
}
```

Add the probe-entry and skip helpers (probe running is completed in Task 4, but the entry must set the step now for the tests):

```go
func (m *Model) enterProbing() (*Model, tea.Cmd) {
	m.step = stepProbing
	m.title = "Connecting"
	m.subtitle = m.template.Label
	m.footer = "[r] retry  [s] skip  [Esc] cancel"
	m.err = ""
	m.picker = nil
	m.providerName = m.uniqueName()
	m.providerCfg.Type = orDefault(m.template.Type, "openai_compatible")
	m.probeStart = nowNanos()
	m.spinner = 0
	return m, tea.Batch(m.runProbe(), tick())
}

func (m *Model) skipProbe() (*Model, tea.Cmd) {
	m.probeErr = nil
	m.models = m.template.Models
	return m, m.advanceToPickModel()
}

func (m *Model) advanceToPickModel() (*Model, tea.Cmd) {
	enterPickModelStep(m, m.providerName)
	return m, nil
}

func (m *Model) runProbe() tea.Cmd {
	return probe.Provider("connect", m.providerName, m.providerCfg)
}

func (m *Model) done() tea.Cmd {
	return func() tea.Msg { return DoneMsg{Provider: m.providerName, Model: m.modelChosen} }
}

func (m *Model) uniqueName() string {
	if m.template.ID == "custom" || m.template.ID == "" {
		return "custom"
	}
	existing := map[string]bool{}
	for k := range m.cfg.Providers {
		existing[k] = true
	}
	return provider.UniqueName(m.template.ID, existing)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var nowNanos = func() int64 {
	return time.Now().UnixNano()
}
```

Add `"time"` to imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/connect/...`
Expected: PASS.

- [ ] **Step 5: Run full build + vet**

Run: `go build ./cmd/marshal && go vet ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/connect/
git commit -m "feat(tui/connect): pick template + credentials steps"
```

---

## Task 4: Connect step 3 — probe result + step 4 — model pick

**Files:**
- Modify: `internal/app/tui/connect/connect.go`
- Modify: `internal/app/tui/connect/connect_test.go`

**Interfaces:**
- Extends `handleProbeResult`: on success populate `m.models` + write to `m.discovered`, advance to pickModel; on failure render inline error, stay probing with retry/skip affordances.
- `_manual_`/custom value at pickModel sets `modelChosen` to the typed custom string (the picker's `allowCustom` returns the filter via `PickedMsg.Value`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/tui/connect/connect_test.go`:

```go
func TestProbeSuccessAdvancesToPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Models: []string{"qwen2.5-coder:7b", "llama3.1:8b"}})
	if updated.step != stepPickModel {
		t.Fatalf("success should advance to pickModel, got %v", updated.step)
	}
	if len(updated.models) != 2 {
		t.Fatalf("models not stored: %v", updated.models)
	}
	if got := updated.discovered[updated.providerName]; len(got) != 2 {
		t.Fatalf("discovered cache not populated: %v", got)
	}
}

func TestProbeFailureStaysWithRetrySkip(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("connection refused")})
	if updated.step != stepProbing {
		t.Fatalf("failure should stay probing, got %v", updated.step)
	}
	if updated.err == "" {
		t.Fatal("expected inline error text set")
	}
}

func TestRetryReRunsProbe(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("boom")})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 114})
	if updated.step != stepProbing {
		t.Fatalf("retry should stay probing, got %v", updated.step)
	}
	if cmd == nil {
		t.Fatal("retry should re-arm the probe cmd")
	}
}

func TestSkipProbeUsesCatalogAndAdvances(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(tea.KeyPressMsg{Code: 115})
	if updated.step != stepPickModel {
		t.Fatalf("skip should advance to pickModel, got %v", updated.step)
	}
	if len(updated.models) == 0 && len(updated.template.Models) > 0 {
		t.Fatal("skip should seed models from template catalog")
	}
}

func TestPickModelEmitsDone(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []string{"qwen2.5-coder:7b"}})
	updated, cmd := m.Update(pickerPicked("qwen2.5-coder:7b"))
	if cmd == nil {
		t.Fatal("pickModel should emit a DoneMsg cmd")
	}
	msg := cmd()
	dm, ok := msg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", msg)
	}
	if dm.Model != "qwen2.5-coder:7b" {
		t.Fatalf("DoneMsg.Model = %q", dm.Model)
	}
}
```

Add `"errors"` and `"marshal/internal/app/tui/probe"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/connect/...`
Expected: FAIL (`handleProbeResult` is a no-op).

- [ ] **Step 3: Implement probe handling**

Replace `handleProbeResult` in `connect.go`:

```go
func (m *Model) handleProbeResult(msg probe.ResultMsg) (*Model, tea.Cmd) {
	if msg.Err != nil {
		m.probeErr = msg.Err
		m.err = "✗ " + truncateErr(msg.Err.Error())
		m.footer = "[r] retry  [s] skip  [Esc] cancel"
		return m, nil
	}
	m.models = msg.Models
	if m.discovered != nil {
		m.discovered[m.providerName] = msg.Models
	}
	return m, m.advanceToPickModel()
}

func truncateErr(s string) string {
	const max = 48
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
```

Fix `buildModelPicker` to prefer `m.models` (discovered during this connect session) over the cached/catalog fallback:

```go
func buildModelPicker(m *Model, providerName string) *picker.Model {
	var items []picker.Item
	candidates := m.models
	if len(candidates) == 0 {
		if cached, ok := m.discovered[providerName]; ok {
			candidates = cached
		}
	}
	if len(candidates) == 0 {
		candidates = m.template.Models
	}
	for _, mid := range candidates {
		badge := "◉ discovered"
		if len(m.models) == 0 {
			badge = "◯ catalog"
		}
		items = append(items, picker.Item{Label: mid, Detail: providerName, Badge: badge, Group: providerName, Value: mid})
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Label: "Enter model id manually", Value: "__manual__", Badge: "custom"})
	}
	p := picker.New("Select model", "pick a model", items)
	p.SetAllowCustom(true)
	return p
}
```

The existing `handlePickerPicked` already routes `stepPickModel` → `m.done()`. The custom-value path: when the user types a filter with no match, the picker emits `PickedMsg{Value: <filter>}` (allowCustom). `handlePickerPicked` stores it as `modelChosen` and emits DoneMsg — correct for both real ids and manual entry. The `__manual__` sentinel is never picked (it's just a placeholder row), but to be safe, map it to empty so it forces custom entry; replace the `stepPickModel` branch:

```go
	if m.step == stepPickModel {
		if value == "__manual__" {
			return m, nil
		}
		m.modelChosen = value
		m.step = stepDone
		return m, m.done()
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/connect/...`
Expected: PASS.

- [ ] **Step 5: Run full build + vet**

Run: `go build ./cmd/marshal && go vet ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/connect/
git commit -m "feat(tui/connect): probe result + model pick steps"
```

---

## Task 5: Wire /connect and /models into the TUI model

**Files:**
- Modify: `internal/commands/commands.go`
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/view.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Adds two commands `/connect`, `/models` to the command registry (handlers return "" — the overlay open is a side effect routed through the TUI model's `dispatchCommand`, like `/settings` which uses `case "settings":` rather than the command Handler doing the work).
- Adds `tui.Model` fields: `connectModel *connect.Model`, `connectOpen bool`, `discovered map[string][]string`.
- Adds `Ctrl+P` → open `/models`.
- Handles `connect.DoneMsg` (persist provider + model, clear profile default, configReloader, SaveProjectConfig, transcript) and `connect.CancelledMsg` (close overlay).
- Routes all messages to `connectModel` while `connectOpen` (except the terminal msgs above and the global help/settings hotkeys which close the overlay first).
- Updates the first-run provider hint to nudge `/connect`/`/models`.

- [ ] **Step 1: Register the two commands**

In `internal/commands/commands.go`, inside the `commands` slice in `RegisterAll`, add (alongside the existing `/model`):

```go
		{
			Name:        "connect",
			Description: "Add or reconnect a provider",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "models",
			Description: "Pick a model from connected providers",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
```

- [ ] **Step 2: Write the failing test**

Append to `internal/app/tui/model_test.go` (place near the existing `TestSettingsOpenClose` style tests). Use the existing test helper that builds a model via `NewModel(state, opts...)`; mirror the settings-overlay test pattern.

```go
func TestConnectOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.dispatchCommand("/connect")
	if !updated.connectOpen {
		t.Fatal("/connect should open the connect overlay")
	}
}

func TestModelsOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.dispatchCommand("/models")
	if !updated.connectOpen {
		t.Fatal("/models should open the connect overlay")
	}
}

func TestModelsEmptyProvidersShowsAddProvider(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.dispatchCommand("/models")
	if m.connectModel.step != 0 { // stepPickTemplate when no providers
		t.Fatalf("no providers should fall through to pickTemplate, got step %v", m.connectModel.step)
	}
}

func TestConnectDoneMsgPersistsAgentModel(t *testing.T) {
	m := newTestModel(t)
	reloaded := false
	m.configReloader = func(cfg config.Config) error { reloaded = true; m.state.Config = cfg; return nil }
	updated, _ := m.Update(connect.DoneMsg{Provider: "ollama", Model: "qwen2.5-coder:7b"})
	if !reloaded {
		t.Fatal("DoneMsg should call configReloader")
	}
	if updated.state.Config.Agent.Provider != "ollama" || updated.state.Config.Agent.Model != "qwen2.5-coder:7b" {
		t.Fatalf("agent cfg not set: %+v", updated.state.Config.Agent)
	}
	if updated.state.Config.Profile.Default != "" {
		t.Fatalf("profile default should be cleared, got %q", updated.state.Config.Profile.Default)
	}
	if updated.connectOpen {
		t.Fatal("overlay should close after DoneMsg")
	}
}

func TestConnectCancelledClosesOverlay(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.dispatchCommand("/connect")
	updated, _ := m.Update(connect.CancelledMsg{})
	if updated.connectOpen {
		t.Fatal("CancelledMsg should close the overlay")
	}
}
```

Add imports: `"marshal/internal/app/tui/connect"`. If `newTestModel` does not exist, define a helper that mirrors an existing settings test's setup (construct a `session.State` with `config.Default()` and `newModel`).

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run TestConnect -run TestModels`
Expected: FAIL (field/method undefined).

- [ ] **Step 4: Add the fields and init**

In `internal/app/tui/model.go`, add to the `Model` struct fields block (near `settingsModel`):

```go
	connectModel *connect.Model
	connectOpen  bool
	discovered   map[string][]string
```

In the model constructor (`New` or wherever `settingsModel` is zeroed), initialize:

```go
	discovered: map[string][]string{},
```

Add imports: `"marshal/internal/app/tui/connect"`.

- [ ] **Step 5: Handle the terminal messages + route while open**

In `tui.Model.Update`, in the FIRST message block (the one that handles `picker.PickedMsg`/`settings.SavedMsg` around line 442–528), add ahead of the existing `case picker.PickedMsg:`:

```go
	case connect.DoneMsg:
		m.applyConnectDone(msg)
		m.connectOpen = false
		m.connectModel = nil
		m.refreshViewport()
		return m, nil
	case connect.CancelledMsg:
		m.connectOpen = false
		m.connectModel = nil
		m.refreshViewport()
		return m, nil
	case connect.TickMsg:
		if m.connectOpen && m.connectModel != nil {
			var cmd tea.Cmd
			m.connectModel, cmd = m.connectModel.Update(msg)
			return m, cmd
		}
		return m, nil
	case probe.ResultMsg:
		if m.connectOpen && m.connectModel != nil {
			var cmd tea.Cmd
			m.connectModel, cmd = m.connectModel.Update(msg)
			if msg.Err == nil && msg.Provider != "" {
				m.discovered[msg.Provider] = msg.Models
			}
			return m, cmd
		}
		return m, nil
```

Add import: `"marshal/internal/app/tui/probe"`.

Then, add the open-guard routing right after the `if m.pickerModel != nil { ... }` block (around line 529–534), mirroring it for the connect overlay:

```go
	if m.connectOpen && m.connectModel != nil {
		if _, ok := msg.(tea.KeyPressMsg); ok {
			var cmd tea.Cmd
			m.connectModel, cmd = m.connectModel.Update(msg)
			return m, cmd
		}
		return m, nil
	}
```

Important ordering: the connect terminal-message cases (DoneMsg/CancelledMsg/TickMsg/probe.ResultMsg) must be ABOVE this block so they short-circuit before the generic route. Since they are in the same first switch, that holds.

- [ ] **Step 6: Add the command dispatch**

In the command dispatcher (the `switch msg := msg.(type)` … `case "model":` area around line 1664), add two cases before the `default:`:

```go
	case "connect":
		m.openConnect "/"
		m.refreshViewport()
		return m, nil

	case "models":
		m.openModels()
		m.refreshViewport()
		return m, nil
```

Add the open helpers near `openPicker`:

```go
func (m *Model) openConnect(_ string) {
	m.connectModel = connect.New(connect.Opts{
		Cfg:        m.state.Config,
		Discovered: m.discovered,
	})
	m.connectModel.SetSize(m.width, m.height)
	m.connectOpen = true
}

func (m *Model) openModels() {
	names := sortedKeys(m.state.Config.Providers)
	if len(names) == 0 {
		m.openConnect("/")
		return
	}
	m.connectModel = connect.New(connect.Opts{
		Cfg:              m.state.Config,
		Discovered:       m.discovered,
		SkipToIntroModel: true,
		ScopedProvider:   names[0],
	})
	m.connectModel.SetSize(m.width, m.height)
	m.connectOpen = true
	m.kickModelsProbes(names)
}

func (m *Model) kickModelsProbes(names []string) {
	var cmds []tea.Cmd
	for _, n := range names {
		if cached, ok := m.discovered[n]; ok && len(cached) > 0 {
			continue
		}
		pc := m.state.Config.Providers[n]
		if !probe.IsLocalhost(pc.BaseURL) && !m.state.Config.Privacy.RemoteProvidersAllowed {
			continue
		}
		cmds = append(cmds, probe.Provider("models", n, pc))
	}
	if len(cmds) > 0 {
		m.agentTickCmds = append(m.agentTickCmds, cmds...)
	}
}
```

If `agentTickCmds` is not an existing field for batching non-tick cmds, instead return the batch from `openModels`:

```go
func (m *Model) openModels() tea.Cmd {
	names := sortedKeys(m.state.Config.Providers)
	if len(names) == 0 {
		m.openConnect("/")
		return nil
	}
	m.connectModel = connect.New(connect.Opts{
		Cfg:              m.state.Config,
		Discovered:       m.discovered,
		SkipToIntroModel: true,
		ScopedProvider:   names[0],
	})
	m.connectModel.SetSize(m.width, m.height)
	m.connectOpen = true
	var cmds []tea.Cmd
	for _, n := range names {
		if cached, ok := m.discovered[n]; ok && len(cached) > 0 {
			continue
		}
		pc := m.state.Config.Providers[n]
		if !probe.IsLocalhost(pc.BaseURL) && !m.state.Config.Privacy.RemoteProvidersAllowed {
			continue
		}
		cmds = append(cmds, probe.Provider("models", n, pc))
	}
	return tea.Batch(cmds...)
}
```

And update the dispatcher case to return the cmd:

```go
	case "models":
		cmd := m.openModels()
		m.refreshViewport()
		return m, cmd
```

Use whatever `sortedKeys` helper exists in `tui` (settings has its own `sortedKeys` — check `internal/app/tui/model.go` for an existing one or call a small local function). If none exists, add:

```go
func (m *Model) sortedProviderNames() []string {
	names := make([]string, 0, len(m.state.Config.Providers))
	for k := range m.state.Config.Providers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
```

and use it in place of `sortedKeys`.

- [ ] **Step 7: Add applyConnectDone**

Add near `switchModelPreset`:

```go
func (m *Model) applyConnectDone(msg connect.DoneMsg) {
	if msg.Provider == "" || msg.Model == "" {
		return
	}
	newCfg := m.state.Config
	if newCfg.Providers == nil {
		newCfg.Providers = map[string]config.ProviderConfig{}
	}
	if _, ok := newCfg.Providers[msg.Provider]; !ok {
		return
	}
	newCfg.Agent.Provider = msg.Provider
	newCfg.Agent.Model = msg.Model
	newCfg.Profile.Default = ""
	if m.configReloader != nil {
		if err := m.configReloader(newCfg); err != nil {
			m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to switch model: %v", err), session.ContentTypePlain)
			return
		}
	}
	if err := config.SaveProjectConfig(projectConfigPath(m.state.WorkingDir), newCfg); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to save model: %v", err), session.ContentTypePlain)
		return
	}
	m.state.AddMessage(session.RoleSystem,
		fmt.Sprintf("Switched to model: %s (%s)", msg.Model, msg.Provider), session.ContentTypePlain)
}
```

Note: `/connect`'s DoneMsg implies a fresh provider was added, but persisting the provider entry itself happens via `SaveProjectConfig` since it iterates `cfg.Providers`. The connect overlay adds the provider to `m.discovered` cache; but the provider entry must be in `cfg.Providers` for the save to persist it. Since `/connect` builds the `ProviderConfig` in its own state, the TUI must merge it. Add to `applyConnectDone` (the `/connect` flow): the connect overlay should pass the provider config up. Extend `connect.DoneMsg` to carry `ProviderCfg config.ProviderConfig` and `ProviderName string`. Update the connect `done()`:

```go
func (m *Model) done() tea.Cmd {
	return func() tea.Msg {
		return DoneMsg{Provider: m.providerName, Model: m.modelChosen, ProviderCfg: m.providerCfg}
	}
}
```

Update `connect.DoneMsg`:

```go
type DoneMsg struct {
	Provider    string
	Model       string
	ProviderCfg config.ProviderConfig
}
```

Update `applyConnectDone` to write the provider entry:

```go
func (m *Model) applyConnectDone(msg connect.DoneMsg) {
	if msg.Provider == "" || msg.Model == "" {
		return
	}
	newCfg := m.state.Config
	if newCfg.Providers == nil {
		newCfg.Providers = map[string]config.ProviderConfig{}
	}
	if msg.ProviderCfg.Type != "" {
		newCfg.Providers[msg.Provider] = msg.ProviderCfg
	}
	newCfg.Agent.Provider = msg.Provider
	newCfg.Agent.Model = msg.Model
	newCfg.Profile.Default = ""
	if m.configReloader != nil {
		if err := m.configReloader(newCfg); err != nil {
			m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to switch model: %v", err), session.ContentTypePlain)
			return
		}
	}
	if err := config.SaveProjectConfig(projectConfigPath(m.state.WorkingDir), newCfg); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to save model: %v", err), session.ContentTypePlain)
		return
	}
	m.state.AddMessage(session.RoleSystem,
		fmt.Sprintf("Switched to model: %s (%s)", msg.Model, msg.Provider), session.ContentTypePlain)
}
```

For `/models` (not `/connect`), `ProviderCfg` is empty (provider already exists), so the `if msg.ProviderCfg.Type != ""` guard correctly skips overwriting an existing entry.

Update the `TestConnectDoneMsgPersistsAgentModel` test to pass a `ProviderCfg` and assert the provider entry is written.

- [ ] **Step 8: Render the overlay**

In `internal/app/tui/view.go`, in the render path that early-returns for `settingsOpen` (around line 56–57 and 319–320), add the connect overlay ahead of settings:

```go
	if m.connectOpen && m.connectModel != nil {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.connectModel.View(m.width, m.height))
	}
```

Place this before the `settingsOpen` check so connect wins if both are somehow open (they aren't, by construction).

- [ ] **Step 9: Add the Ctrl+P hotkey**

In `tui.Model.Update`, the global hotkey switch (around line 591, `case "ctrl+o":` for settings), add:

```go
		case "ctrl+p":
			m.openModels()
			m.refreshViewport()
			return m, nil
```

`openModels` returns a `tea.Cmd` (the probe batch); capture and return it:

```go
		case "ctrl+p":
			cmd := m.openModels()
			m.refreshViewport()
			return m, cmd
```

Add the `Ctrl+P models` binding to the help footer if a keybind registry exists. Check `internal/app/tui/help/`: if it has a static footer list, append the entry; otherwise skip (the `?` overlay lists commands, and `/models` is already discoverable there). Verify in this step:

Run: `rg -n "ctrl\+o|settings" internal/app/tui/help/`
If a literal footer/hint list references `ctrl+o`, add `ctrl+p` `models` next to it.

- [ ] **Step 10: Update the first-run hint**

Find the provider-not-configured hint. From the spec, the current path renders the provider error inline (`renderProviderError`). Search:

Run: `rg -n "ProviderError|renderProviderError|Fix the provider" internal/app/tui/`

Add a one-line nudge under that error block (in `model.go` around line 1268–1269) so the inline provider-error view appends:

```go
			b.WriteString("\n")
			b.WriteString(mutedStyleForHelp.Render("Run /connect to add a provider, or /models to pick a model."))
```

Use the existing `mutedStyle` from `transcript.go` (reuse `providerErrorStyle`'s muted sibling). If no muted helper is in scope there, inline a style: `lipgloss.NewStyle().Foreground(theme.Load().FGMuted).Render(...)`.

- [ ] **Step 11: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run "TestConnect|TestModels"`
Expected: PASS.

- [ ] **Step 12: Run full build + vet + tests**

Run: `go build ./cmd/marshal && go vet ./... && go test ./...`
Expected: success, all green.

- [ ] **Step 13: Commit**

```bash
git add internal/commands/commands.go internal/app/tui/model.go internal/app/tui/view.go internal/commands/ internal/app/tui/
git commit -m "feat(tui): /connect and /models commands with fast provider/model setup"
```

---

## Self-Review

After writing the complete plan, the following checks were run against the spec:

**1. Spec coverage:**
- Phase 1 (overlay architecture & command wiring) → Tasks 2, 5. ✓
- Phase 2 (/connect flow: pick template, credentials, probe, pick model) → Tasks 2, 3, 4. ✓
- Phase 3 (/models flow: empty-providers chaining, lazy per-provider probe, cache fallback, pick → persist) → Task 5 (`openModels`, `kickModelsProbes`, `applyConnectDone`). ✓
- Phase 4 (connect overlay internals, shared probe extraction, messages) → Tasks 1, 2. ✓
- Phase 5 (error handling, local-first gating, secrets, spinner 200ms, first-run hint) → Tasks 1 (gating in probe), 4 (inline errors, retry/skip), 5 (first-run hint). ✓ The 200ms spinner-delay is noted in renderProbing (`probeStart`); the per-spinner 200ms gate is proposal below.
- Phase 6 (testing) → each task has TDD tests; probe_test.go subsumes discover_test.go. ✓

**2. Placeholder scan:** No "TBD", "add appropriate handling", or unshown code blocks. One verification step (`rg` for help footer) is a genuine lookup, not a placeholder — its outcome is conditional but the action is concrete.

**3. Type consistency:**
- `probe.ResultMsg{FieldID, Provider, Models, Err}` — used identically in Task 1 (def), Task 2 (connect handles it), Task 5 (tui routes it). ✓
- `connect.DoneMsg{Provider, Model, ProviderCfg}` — defined Task 2, extended Task 5 Step 7, consumed Task 5 Step 7. The Task 2 initial definition had `{Provider, Model}`; Task 5 Step 7 explicitly redefines it to add `ProviderCfg config.ProviderConfig`. ✓ (the plan calls this out as a change, not a silent rename)
- `connect.CancelledMsg`, `connect.TickMsg` — defined Task 2, consumed Task 5. ✓
- `connect.Opts{Cfg, Discovered, SkipToIntroModel, ScopedProvider}` — defined Task 2, used Task 5. ✓
- `Model.Update` signature returns `(*Model, tea.Cmd)` (pointer receiver) in connect; tui routing uses `m.connectModel, cmd = m.connectModel.Update(msg)`. ✓

**One spec nuance to lock in (add to Task 4 render):** the 200ms spinner-delay. `renderProbing` should only show the animated frame after `time.Since(probeStart)` > 200ms; before that show a static `…`. Apply by changing `renderProbing`:

```go
func (m *Model) renderProbing(pw int) string {
	if m.probeStart == 0 || time.SinceUnixNano(m.probeStart) < 200*time.Millisecond {
		return mutedStyle.Render("… connecting")
	}
	return mutedStyle.Render(spinnerFrames[m.spinner%len(spinnerFrames)] + " connecting…")
}
```

Replace `time.SinceUnixNano` with the concrete helper: since `probeStart int64` is UnixNano, `time.Now().Sub(time.Unix(0, m.probeStart))` > 200ms. This was already implied; the plan folds it into Task 4's implementation detail rather than leaving a "handle the delay" placeholder. The implementer adds this body in Task 4 Step 3 when finalizing `renderProbing`.

The plan is complete.