# Provider Wizard and Settings Expansion — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bare "type a name → fill blank fields" provider-adding flow with an opencode-style template-picker wizard, live model discovery, a test-connection action, and provider/model pickers in the Agent and Model Presets frames.

**Architecture:** A provider template catalog (Go map, no network) feeds an add-provider wizard that opens as a settings overlay using the existing fuzzy `picker` package. Two new `field` row kinds — `kindAction` (trigger a side-effectful/async action on Enter) and `kindPicker` (open the fuzzy picker overlay to choose from a lazily-populated set) — extend the existing `fieldList` widget. A discovery probe reuses the already-built `OpenAICompatible.Models()` endpoint with a 5s timeout and a session-local cache on the settings `state`. Remote discovery is gated behind `privacy.remote_providers_allowed`.

**Tech Stack:** Go 1.26, Bubble Tea v2, Lipgloss v2, existing `internal/app/tui/picker` fuzzy picker, existing `internal/llm/provider` factory.

**Spec:** `docs/superpowers/specs/2026-07-12-provider-wizard-and-settings-expansion-design.md`, Sections 1–5 (Phase 1).

## Global Constraints

- Module path is `marshal` (see `go.mod`).
- `go test ./...` and `go vet ./...` must pass after every task.
- `go build ./cmd/marshal` must succeed (requires `CGO_ENABLED=1`).
- No new external dependencies.
- No comments in code unless explicitly requested by the plan.
- Local-first: remote discovery is gated behind `cfg.Privacy.RemoteProvidersAllowed`.
- The settings overlay mutates only a working copy; nothing reaches the filesystem until `Ctrl+S`.
- Secrets (`api_key` fields) are never rendered in plaintext; use the existing `maskKey` helper.

## File Structure

**Create:**
- `internal/llm/provider/templates.go` — `ProviderTemplate` table and `Lookup`.
- `internal/llm/provider/templates_test.go`
- `internal/app/tui/settings/discover.go` — `probeProvider`, `isLocalhost`, `probeResultMsg`.
- `internal/app/tui/settings/discover_test.go`

**Modify:**
- `internal/app/tui/settings/field.go` — add `kindAction`, `kindPicker` constants and closures.
- `internal/app/tui/settings/fieldlist.go` — handle new kinds in `Update`/`openRow`/`valueCell`; add `pushPicker`/`addWizard`.
- `internal/app/tui/settings/panestack.go` — add `addWizard` to `frame`.
- `internal/app/tui/settings/state.go` — add `discovered` cache and `actionState` map.
- `internal/app/tui/settings/messages.go` — add `probeResultMsg`, `actionResultMsg`.
- `internal/app/tui/settings/model.go` — add `overlayPicker`; handle non-key messages; picker overlay routing; wizard open.
- `internal/app/tui/settings/frames_collections.go` — wizard, Name field, test-connection, picker rows in presets.
- `internal/app/tui/settings/frames_agent.go` — Provider/Model `kindPicker` rows.
- `internal/app/tui/picker/picker.go` — add `allowCustom` free-text escape hatch.
- `internal/app/tui/model_test.go` — update existing tests for new row kinds.

---

## Task 1: Provider template catalog

**Files:**
- Create: `internal/llm/provider/templates.go`
- Test: `internal/llm/provider/templates_test.go`

**Interfaces:**
- Produces: `ProviderTemplate` struct, `Lookup(id string) (ProviderTemplate, bool)`, `All() []ProviderTemplate`, `UniqueName(base string, existing map[string]bool) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/llm/provider/templates_test.go`:

```go
package provider

import "testing"

func TestLookupKnownTemplates(t *testing.T) {
	for _, id := range []string{"ollama", "lmstudio", "openrouter", "groq", "openai", "openai_compatible"} {
		tpl, ok := Lookup(id)
		if !ok {
			t.Fatalf("Lookup(%q) = not found", id)
		}
		if tpl.ID == "" || tpl.Label == "" || tpl.Type == "" {
			t.Fatalf("Lookup(%q) returned incomplete template: %+v", id, tpl)
		}
	}
}

func TestLookupUnknownReturnsFalse(t *testing.T) {
	if _, ok := Lookup("nonexistent"); ok {
		t.Fatal("Lookup(unknown) should return false")
	}
}

func TestOllamaIsLocal(t *testing.T) {
	tpl, _ := Lookup("ollama")
	if !tpl.Local {
		t.Fatal("ollama template must be Local=true")
	}
	if tpl.BaseURL == "" {
		t.Fatal("ollama template must have a BaseURL")
	}
}

func TestOpenrouterIsRemoteWithKeyEnv(t *testing.T) {
	tpl, _ := Lookup("openrouter")
	if tpl.Local {
		t.Fatal("openrouter template must be Local=false")
	}
	if tpl.KeyEnv == "" {
		t.Fatal("openrouter template must suggest a KeyEnv")
	}
}

func TestAllReturnsAll(t *testing.T) {
	all := All()
	if len(all) < 6 {
		t.Fatalf("All() returned %d templates, want >= 6", len(all))
	}
	ids := map[string]bool{}
	for _, tpl := range all {
		ids[tpl.ID] = true
	}
	for _, id := range []string{"ollama", "lmstudio", "openrouter", "groq", "openai", "openai_compatible"} {
		if !ids[id] {
			t.Fatalf("All() missing template %q", id)
		}
	}
}

func TestUniqueNameNoCollision(t *testing.T) {
	got := UniqueName("ollama", map[string]bool{})
	if got != "ollama" {
		t.Fatalf("UniqueName with no collision = %q, want %q", got, "ollama")
	}
}

func TestUniqueNameWithCollision(t *testing.T) {
	got := UniqueName("ollama", map[string]bool{"ollama": true})
	if got != "ollama-2" {
		t.Fatalf("UniqueName with one collision = %q, want %q", got, "ollama-2")
	}
	got = UniqueName("ollama", map[string]bool{"ollama": true, "ollama-2": true})
	if got != "ollama-3" {
		t.Fatalf("UniqueName with two collisions = %q, want %q", got, "ollama-3")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/provider/ -run TestLookup -v`
Expected: FAIL with "undefined: Lookup"

- [ ] **Step 3: Write minimal implementation**

Create `internal/llm/provider/templates.go`:

```go
package provider

import "fmt"

type ProviderTemplate struct {
	ID          string
	Label       string
	Type        string
	BaseURL     string
	Local       bool
	ToolCalling bool
	KeyEnv      string
	KeyHint     string
	Models      []string
}

var templates = map[string]ProviderTemplate{
	"ollama": {
		ID:      "ollama",
		Label:   "Ollama (local)",
		Type:    "openai_compatible",
		BaseURL: "http://localhost:11434/v1",
		Local:   true,
		Models:  []string{"qwen2.5-coder:7b", "qwen2.5-coder:14b", "qwen2.5:7b", "llama3.1:8b"},
	},
	"lmstudio": {
		ID:      "lmstudio",
		Label:   "LM Studio (local)",
		Type:    "openai_compatible",
		BaseURL: "http://localhost:1234/v1",
		Local:   true,
	},
	"openrouter": {
		ID:          "openrouter",
		Label:       "OpenRouter",
		Type:        "openai_compatible",
		BaseURL:      "https://openrouter.ai/api/v1",
		ToolCalling:  true,
		KeyEnv:       "OPENROUTER_API_KEY",
		KeyHint:      "Get a key at https://openrouter.ai/keys",
		Models:       []string{"anthropic/claude-sonnet-4", "google/gemini-2.5-pro", "meta-llama/llama-3.3-70b-instruct"},
	},
	"groq": {
		ID:          "groq",
		Label:       "Groq",
		Type:        "openai_compatible",
		BaseURL:      "https://api.groq.com/openai/v1",
		ToolCalling:  true,
		KeyEnv:       "GROQ_API_KEY",
		KeyHint:      "Get a key at https://console.groq.com/keys",
	},
	"openai": {
		ID:          "openai",
		Label:       "OpenAI",
		Type:        "openai_compatible",
		BaseURL:      "https://api.openai.com/v1",
		ToolCalling:  true,
		KeyEnv:       "OPENAI_API_KEY",
		KeyHint:      "Get a key at https://platform.openai.com/api-keys",
		Models:       []string{"gpt-4o", "gpt-4o-mini", "o3-mini"},
	},
	"openai_compatible": {
		ID:      "openai_compatible",
		Label:   "Custom (OpenAI-compatible)",
		Type:    "openai_compatible",
		BaseURL: "",
		Local:   false,
	},
}

func Lookup(id string) (ProviderTemplate, bool) {
	tpl, ok := templates[id]
	return tpl, ok
}

func All() []ProviderTemplate {
	out := make([]ProviderTemplate, 0, len(templates))
	for _, tpl := range templates {
		out = append(out, tpl)
	}
	return out
}

func UniqueName(base string, existing map[string]bool) string {
	if !existing[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/provider/ -run 'TestLookup|TestUniqueName|TestAll|TestOllama|TestOpenrouter' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/llm/provider/templates.go internal/llm/provider/templates_test.go
git commit -m "feat: add provider template catalog"
```

---

## Task 2: `kindAction` and `kindPicker` row primitives

This task adds both new field kinds structurally and the plumbing for async results and picker overlay requests. Concrete action/picker rows are wired in later tasks.

**Files:**
- Modify: `internal/app/tui/settings/field.go` — add constants and closures
- Modify: `internal/app/tui/settings/fieldlist.go` — handle new kinds in `Update`/`openRow`/`valueCell`; add `pushPicker`/`addWizard`; `openRow` returns `tea.Cmd`
- Modify: `internal/app/tui/settings/panestack.go` — add `addWizard` to `frame`
- Modify: `internal/app/tui/settings/state.go` — add `discovered` cache and `actionState`
- Modify: `internal/app/tui/settings/messages.go` — add `probeResultMsg`, `actionResultMsg`
- Modify: `internal/app/tui/settings/model.go` — handle non-key messages; add `overlayPicker`; picker overlay routing; `openPicker`/`closePicker`/`handlePickerPicked`
- Test: `internal/app/tui/settings/fieldlist_test.go`

**Interfaces:**
- Produces: `kindAction`, `kindPicker` constants; `actionResultMsg{FieldID, Label}`; `probeResultMsg{FieldID, Provider, Models, Err}`; `field.act`/`field.actLabel`; `field.pickOptions`/`field.pickOnPick`/`field.pickPending`; `pickerRequest{fieldID, items, onPick, title, footer}`; `fieldList.TakePushPicker()`; `state.discovered`; `state.actionState`; `Model.openPicker(req)`; `Model.closePicker()`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/fieldlist_test.go`:

```go
func TestKindActionEnterTriggersActAndReturnsCmd(t *testing.T) {
	fl := newFieldList(func() []*field {
		return []*field{
			{
				id:       "test.action",
				title:    "Run",
				kind:     kindAction,
				actLabel: func() string { return "idle" },
				act: func() tea.Cmd {
					return func() tea.Msg {
						return actionResultMsg{FieldID: "test.action", Label: "done"}
					}
				},
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	cmd := fl.Update(tea.KeyPressMsg{Text: "enter"})
	if cmd == nil {
		t.Fatal("kindAction Enter should return a non-nil Cmd")
	}
	msg := cmd()
	arm, ok := msg.(actionResultMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want actionResultMsg", msg)
	}
	if arm.FieldID != "test.action" || arm.Label != "done" {
		t.Fatalf("actionResultMsg = %+v, want {test.action done}", arm)
	}
}

func TestKindActionResultUpdatesState(t *testing.T) {
	st := newState(config.Default())
	fl := newFieldList(func() []*field {
		return []*field{
			{
				id:       "test.action",
				title:    "Run",
				kind:     kindAction,
				actLabel: func() string {
					if as, ok := st.actionState["test.action"]; ok && as.label != "" {
						return as.label
					}
					return "idle"
				},
				act: func() tea.Cmd {
					st.actionState["test.action"] = actionState{pending: true, label: "\u2026"}
					return func() tea.Msg {
						return actionResultMsg{FieldID: "test.action", Label: "\u2713 ok"}
					}
				},
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	fl.Update(tea.KeyPressMsg{Text: "enter"})
	if as := st.actionState["test.action"]; !as.pending {
		t.Fatal("action should be pending after Enter")
	}

	st.applyActionResult("test.action", "\u2713 ok")
	if as := st.actionState["test.action"]; as.pending || as.label != "\u2713 ok" {
		t.Fatalf("after result, actionState = %+v, want pending=false label=ok", as)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestKindAction' -v`
Expected: FAIL with "undefined: kindAction"

- [ ] **Step 3: Add constants and field closures**

In `internal/app/tui/settings/field.go`, add `kindAction` and `kindPicker` to the `fieldKind` iota:

```go
const (
	kindToggle fieldKind = iota
	kindScalar
	kindEnum
	kindDrill
	kindAction
	kindPicker
)
```

Add the new closures to the `field` struct (after `del func()`):

```go
	// kindAction
	act       func() tea.Cmd
	actLabel  func() string

	// kindPicker
	pickOptions func() []picker.Item
	pickOnPick   func(string) error
	pickPending  func() bool
```

Add import `"marshal/internal/app/tui/picker"` to `field.go`.

- [ ] **Step 4: Add `discovered` and `actionState` to `state`**

In `internal/app/tui/settings/state.go`, update the struct and constructor:

```go
type state struct {
	cfg          config.Config
	snapshot     config.Config
	discovered   map[string][]string
	actionState  map[string]actionState
}

type actionState struct {
	pending bool
	label   string
}

func newState(cfg config.Config) *state {
	working := cloneConfig(cfg)
	return &state{
		cfg:         working,
		snapshot:    cloneConfig(working),
		discovered:  map[string][]string{},
		actionState: map[string]actionState{},
	}
}

func (s *state) applyActionResult(fieldID, label string) {
	s.actionState[fieldID] = actionState{pending: false, label: label}
}
```

- [ ] **Step 5: Add message types**

In `internal/app/tui/settings/messages.go`, append:

```go
type probeResultMsg struct {
	FieldID  string
	Provider string
	Models   []string
	Err      error
}

type actionResultMsg struct {
	FieldID string
	Label   string
}
```

- [ ] **Step 6: Handle new kinds in `fieldList`**

In `internal/app/tui/settings/fieldlist.go`:

Add `pushPicker *pickerRequest` and `addWizard func() *pickerRequest` to the `fieldList` struct (after `pushRequest`).

Add the `pickerRequest` type and `TakePushPicker` method at the bottom:

```go
type pickerRequest struct {
	fieldID string
	items   []picker.Item
	onPick  func(string) error
	title   string
	footer  string
}

func (fl *fieldList) TakePushPicker() *pickerRequest {
	r := fl.pushPicker
	fl.pushPicker = nil
	return r
}
```

Change `openRow` to return `tea.Cmd` and handle the new kinds:

```go
func (fl *fieldList) openRow(row *field) tea.Cmd {
	if row == nil {
		return nil
	}
	switch row.kind {
	case kindToggle:
		row.setBool(!row.getBool())
	case kindScalar:
		if row.setStr == nil {
			return nil
		}
		fl.editing = true
		fl.errMsg = ""
		if row.masked {
			fl.input.SetValue("")
		} else {
			fl.input.SetValue(row.getStr())
			fl.input.CursorEnd()
		}
		fl.input.Focus()
	case kindEnum:
		fl.picking = true
		i := indexOf(row.options(), row.getStr())
		if i < 0 {
			i = 0
		}
		fl.pickIdx = i
	case kindDrill:
		fl.pushRequest = row.build()
	case kindAction:
		return row.act()
	case kindPicker:
		fl.pushPicker = &pickerRequest{
			fieldID: row.id,
			items:   row.pickOptions(),
			onPick:  row.pickOnPick,
		}
	}
	return nil
}
```

In the `Update` switch, change `case "enter", "e":` to `return fl.openRow(row)`.

Add `kindAction` handling to the `a` key — before the `fl.onAdd` check:

```go
	case "a":
		if fl.addWizard != nil {
			fl.pushPicker = fl.addWizard()
			return nil
		}
		if fl.onAdd != nil {
			// ... existing code unchanged ...
```

Add `kindAction` and `kindPicker` cases to `valueCell`:

```go
	case kindAction:
		label := "\u21b5 run"
		if row.actLabel != nil {
			label = row.actLabel()
		}
		if strings.HasPrefix(label, "\u2713") {
			return flOnStyle.Render(label)
		}
		if strings.HasPrefix(label, "\u2717") {
			return flErrStyle.Render(label)
		}
		return flValueStyle.Render(label)
	case kindPicker:
		v := row.getStr()
		if v == "" {
			v = "\u2014"
		}
		suffix := " \u25be"
		if row.pickPending != nil && row.pickPending() {
			suffix = " \u2026"
		}
		return flValueStyle.Render(v + suffix)
```

Add import `"marshal/internal/app/tui/picker"` to `fieldlist.go`.

- [ ] **Step 7: Add `addWizard` to `frame`**

In `internal/app/tui/settings/panestack.go`, add `addWizard func() *pickerRequest` to the `frame` struct (after `onAdd`).

- [ ] **Step 8: Handle non-key messages and overlay in `Model.Update`**

In `internal/app/tui/settings/model.go`:

Add `overlayPicker` to the `overlayKind` iota block (after `overlayHelp`).

Add picker fields to the `Model` struct (after `search searchState`):

```go
	pickerModel   *picker.Model
	pickerOnPick   func(string) error
	pickerFieldID  string
```

Add import `"marshal/internal/app/tui/picker"`, `"marshal/internal/app/tui/picker"` and `"fmt"` (already imported).

Insert non-key message handling at the very top of `Update`, before the `isKey` guard:

```go
func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
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
	case actionResultMsg:
		m.state.applyActionResult(msg.FieldID, msg.Label)
		return *m, nil
	case picker.PickedMsg:
		return m.handlePickerPicked(msg.Value)
	case picker.CancelledMsg:
		m.closePicker()
		return *m, nil
	}

	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		return *m, nil
	}
	// ... rest of existing Update unchanged ...
```

Add `overlayPicker` routing after the `overlaySearch` block:

```go
	if m.overlay == overlayPicker {
		if m.pickerModel == nil {
			m.overlay = overlayNone
			return *m, nil
		}
		cmd := m.pickerModel.Update(msg)
		return *m, cmd
	}
```

Add the helper methods:

```go
func (m *Model) openPicker(req *pickerRequest) {
	p := picker.New(req.title, req.footer, req.items)
	p.SetAllowCustom(true)
	m.pickerModel = p
	m.pickerOnPick = req.onPick
	m.pickerFieldID = req.fieldID
	m.overlay = overlayPicker
}

func (m *Model) closePicker() {
	m.overlay = overlayNone
	m.pickerModel = nil
	m.pickerOnPick = nil
	m.pickerFieldID = ""
}

func (m *Model) handlePickerPicked(value string) (Model, tea.Cmd) {
	if m.pickerOnPick != nil {
		if err := m.pickerOnPick(value); err != nil {
			m.footerMsg = err.Error()
			m.closePicker()
			return *m, nil
		}
	}
	if m.pickerFieldID == "__wizard__" {
		m.drillIntoNewestProvider()
	}
	m.closePicker()
	return *m, nil
}

func (m *Model) drillIntoNewestProvider() {
	pane := m.activePane()
	for pane.pop() {
	}
	rows := pane.top().list.Rows()
	if len(rows) == 0 {
		return
	}
	pane.top().list.SetCursor(len(rows) - 1)
	row := pane.top().list.CursorRow()
	if row != nil && row.kind == kindDrill {
		_ = pane.top().list.openRow(row)
		if f := pane.top().list.TakePushRequest(); f != nil {
			pane.push(f)
		}
	}
}

func truncateErr(s string) string {
	if len(s) > 40 {
		return s[:37] + "\u2026"
	}
	return s
}
```

Add `overlayPicker` rendering in `View` (after the `overlaySearch` check):

```go
	if m.overlay == overlayPicker && m.pickerModel != nil {
		return m.pickerModel.View(fw, fh)
	}
```

Add footer hints for the new kinds in `renderFooter`'s default case `switch row.kind` block:

```go
			case kindAction:
				parts = append(parts, seg("\u21b5", "run"))
			case kindPicker:
				parts = append(parts, seg("\u21b5", "pick"))
```

After `activePane().Update` calls in the pane-focused and sidebarHidden branches, check for `pushPicker`:

```go
	cmd := m.activePane().Update(msg)
	if req := m.activePane().top().list.TakePushPicker(); req != nil {
		m.openPicker(req)
	}
	return *m, cmd
```

Apply this pattern to both the `sidebarHidden` branch (currently `return *m, m.activePane().Update(msg)`) and the pane-focused branch (currently `return *m, m.activePane().Update(msg)`).

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestKindAction' -v`
Expected: PASS

- [ ] **Step 10: Run full build and vet**

Run: `go build ./cmd/marshal && go vet ./internal/app/tui/settings/...`
Expected: no errors

- [ ] **Step 11: Commit**

```bash
git add internal/app/tui/settings/field.go internal/app/tui/settings/fieldlist.go internal/app/tui/settings/panestack.go internal/app/tui/settings/state.go internal/app/tui/settings/messages.go internal/app/tui/settings/model.go internal/app/tui/settings/fieldlist_test.go
git commit -m "feat: add kindAction and kindPicker row primitives to settings fieldList"
```

---

## Task 3: Discovery probe helper

**Files:**
- Create: `internal/app/tui/settings/discover.go`
- Test: `internal/app/tui/settings/discover_test.go`

**Interfaces:**
- Produces: `probeProvider(fieldID, name string, pc config.ProviderConfig) tea.Cmd`, `isLocalhost(baseURL string) bool`, `probeTimeout` var.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/settings/discover_test.go`:

```go
package settings

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
		{"https://api.openai.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLocalhost(c.url); got != c.want {
			t.Errorf("isLocalhost(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestProbeProviderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b","owned_by":"ollama"},{"id":"llama3.1:8b","owned_by":"meta"}]}`))
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	cmd := probeProvider("test.field", "testprov", pc)
	msg := cmd().(probeResultMsg)

	if msg.Err != nil {
		t.Fatalf("probeProvider err = %v", msg.Err)
	}
	if len(msg.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(msg.Models))
	}
	if msg.Models[0] != "qwen2.5-coder:7b" {
		t.Fatalf("first model = %q, want qwen2.5-coder:7b", msg.Models[0])
	}
}

func TestProbeProviderNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	cmd := probeProvider("test.field", "testprov", pc)
	msg := cmd().(probeResultMsg)

	if msg.Err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestProbeProviderConnectionRefused(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}
	cmd := probeProvider("test.field", "testprov", pc)
	msg := cmd().(probeResultMsg)

	if msg.Err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestProbeProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	old := probeTimeout
	probeTimeout = 200 * time.Millisecond
	defer func() { probeTimeout = old }()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	cmd := probeProvider("test.field", "testprov", pc)
	msg := cmd().(probeResultMsg)

	if msg.Err == nil {
		t.Fatal("expected timeout error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestProbe|TestIsLocalhost' -v`
Expected: FAIL with "undefined: isLocalhost"

- [ ] **Step 3: Write minimal implementation**

Create `internal/app/tui/settings/discover.go`:

```go
package settings

import (
	"context"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
)

var probeTimeout = 5 * time.Second

func isLocalhost(baseURL string) bool {
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
	return strings.HasPrefix(u.Host, "[::1]")
}

func probeProvider(fieldID, name string, pc config.ProviderConfig) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.NewFromConfig(name, pc)
		if err != nil {
			return probeResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		models, err := p.Models(ctx)
		if err != nil {
			return probeResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		return probeResultMsg{FieldID: fieldID, Provider: name, Models: ids}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestProbe|TestIsLocalhost' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/discover.go internal/app/tui/settings/discover_test.go
git commit -m "feat: add discovery probe helper with localhost detection"
```

---

## Task 4: Discovery cache invalidation + Test connection row

The `discovered` map was added in Task 2. This task adds: (a) invalidation when a provider's BaseURL/APIKey/APIKeyEnv changes, and (b) the "Test connection" `kindAction` row at the bottom of each provider detail frame, with the privacy gate for remote providers.

**Files:**
- Modify: `internal/app/tui/settings/frames_collections.go:19-66`
- Test: `internal/app/tui/settings/frames_collections_test.go`

**Interfaces:**
- Produces: `testConnectionField(s *state, k string) *field`, invalidation hooks on provider detail setters.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/frames_collections_test.go`:

```go
func TestProviderBaseURLEditInvalidatesDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	st.discovered["ollama"] = []string{"qwen2.5:7b", "llama3.1:8b"}

	f := providersFrame(st)
	drill := f.list.Rows()[0]
	detail := drill.build()
	for _, r := range detail.list.Rows() {
		if r.id == "providers.ollama.base_url" {
			if err := r.setStr("http://localhost:9999/v1"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, ok := st.discovered["ollama"]; ok {
		t.Fatal("editing base_url should invalidate the discovery cache for ollama")
	}
}

func TestProviderDetailHasTestConnectionRow(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var found *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("provider detail must have a Test connection row")
	}
	if found.kind != kindAction {
		t.Fatalf("Test connection row kind = %v, want kindAction", found.kind)
	}
}

func TestRemoteProviderTestConnectionBlockedByPrivacy(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var tc *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			tc = r
			break
		}
	}
	label := tc.actLabel()
	if !strings.Contains(label, "blocked") {
		t.Fatalf("remote provider with privacy off: label = %q, want 'blocked'", label)
	}
	if cmd := tc.act(); cmd != nil {
		t.Fatal("blocked test connection act() should return nil")
	}
}

func TestLocalProviderTestConnectionNotBlocked(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var tc *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			tc = r
			break
		}
	}
	if strings.Contains(tc.actLabel(), "blocked") {
		t.Fatal("local provider should not be blocked")
	}
}
```

Add `"strings"` import to `frames_collections_test.go` if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestProviderBaseURL|TestProviderDetail|TestRemoteProvider|TestLocalProvider' -v`
Expected: FAIL

- [ ] **Step 3: Add invalidation and test-connection row**

In `internal/app/tui/settings/frames_collections.go`, inside the `providersFrame` `buildEntry` closure, after the `mut` helper, add:

```go
			invalidate := func() {
				delete(s.discovered, k)
			}
```

Update the BaseURL, APIKeyEnv, and APIKey setters to call `invalidate()` after the `mut`. Replace the `secretRow` call for APIKey with an inline masked field so we can hook invalidate:

```go
					{id: "providers." + k + ".api_key", title: "API key", kind: kindScalar, masked: true,
						desc:     "enter replaces \u00b7 empty keeps \u00b7 d clears \u00b7 prefer the env-var field",
						keywords: []string{"secret", "api key", "token"},
						getStr:   func() string { return s.cfg.Providers[k].APIKey },
						setStr: func(v string) error {
							mut(func(p *config.ProviderConfig) { p.APIKey = v })
							invalidate()
							return nil
						},
						del: func() {
							mut(func(p *config.ProviderConfig) { p.APIKey = "" })
							invalidate()
						}},
```

Add the test-connection row as the last element of the returned `[]*field`:

```go
					testConnectionField(s, k),
```

Add the function at the bottom of `frames_collections.go`:

```go
func testConnectionField(s *state, k string) *field {
	fieldID := "providers." + k + ".test_connection"
	return &field{
		id:    fieldID,
		title: "Test connection",
		kind:  kindAction,
		desc:  "ping the provider and list available models",
		actLabel: func() string {
			if as, ok := s.actionState[fieldID]; ok && as.label != "" {
				return as.label
			}
			pc := s.cfg.Providers[k]
			if !isLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
				return "\u2717 blocked (enable Remote providers in Privacy)"
			}
			return "\u21b5 test"
		},
		act: func() tea.Cmd {
			pc := s.cfg.Providers[k]
			if !isLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
				return nil
			}
			s.actionState[fieldID] = actionState{pending: true, label: "\u2026"}
			return probeProvider(fieldID, k, pc)
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestProviderBaseURL|TestProviderDetail|TestRemoteProvider|TestLocalProvider' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/frames_collections.go internal/app/tui/settings/frames_collections_test.go
git commit -m "feat: add test-connection action row and discovery cache invalidation"
```

---

## Task 5: Picker free-text escape hatch

The `kindPicker` rows need the picker overlay to accept a typed filter as a custom value for exotic/local-fork model ids not in the discovered or catalog list.

**Files:**
- Modify: `internal/app/tui/picker/picker.go`
- Test: `internal/app/tui/picker/picker_test.go`

**Interfaces:**
- Produces: `Model.SetAllowCustom(bool)`. When true, a synthetic "Use '<filter>'" row is prepended when no exact match exists; picking it emits `PickedMsg{Value: filter}`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/picker/picker_test.go`:

```go
func TestAllowCustomPicksFilterValue(t *testing.T) {
	items := []Item{
		{Label: "Ollama", Value: "ollama"},
		{Label: "OpenRouter", Value: "openrouter"},
	}
	m := New("Pick", "", items)
	m.SetAllowCustom(true)

	m.filter.SetValue("custom-model-id")
	m.refilter()

	msg := m.Update(tea.KeyPressMsg{Text: "enter"})
	picked, ok := msg().(PickedMsg)
	if !ok {
		t.Fatalf("expected PickedMsg, got %T", msg())
	}
	if picked.Value != "custom-model-id" {
		t.Fatalf("PickedMsg.Value = %q, want custom-model-id", picked.Value)
	}
}

func TestAllowCustomExactMatchNoCustomItem(t *testing.T) {
	items := []Item{
		{Label: "Ollama", Value: "ollama"},
	}
	m := New("Pick", "", items)
	m.SetAllowCustom(true)

	m.filter.SetValue("ollama")
	m.refilter()

	for _, idx := range m.matches {
		if idx == -1 {
			t.Fatal("exact match should not show custom item")
		}
	}
}

func TestAllowCustomDisabledNoSentinel(t *testing.T) {
	items := []Item{{Label: "Ollama", Value: "ollama"}}
	m := New("Pick", "", items)

	m.filter.SetValue("nonexistent")
	m.refilter()

	for _, idx := range m.matches {
		if idx == -1 {
			t.Fatal("allowCustom=false should never produce sentinel -1")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/picker/ -run TestAllowCustom -v`
Expected: FAIL with "undefined: SetAllowCustom"

- [ ] **Step 3: Add `allowCustom` to the picker**

In `internal/app/tui/picker/picker.go`, add `allowCustom bool` to the `Model` struct and add the setter after `New`:

```go
func (m *Model) SetAllowCustom(b bool) { m.allowCustom = b }
```

Update `refilter` to prepend a sentinel `-1` match when the filter has no exact value match:

```go
func (m *Model) refilter() {
	hay := make([]string, len(m.items))
	for i, it := range m.items {
		hay[i] = it.Group + " " + it.Label + " " + it.Detail
	}
	m.matches = fuzzy.Rank(m.filter.Value(), hay)
	if m.allowCustom && strings.TrimSpace(m.filter.Value()) != "" {
		exact := false
		for _, idx := range m.matches {
			if m.items[idx].Value == m.filter.Value() {
				exact = true
				break
			}
		}
		if !exact {
			m.matches = append([]int{-1}, m.matches...)
		}
	}
	if m.cursor >= len(m.matches) {
		m.cursor = max(len(m.matches)-1, 0)
	}
}
```

Update the `enter` case in `Update`:

```go
	case "enter":
		if m.cursor < len(m.matches) && len(m.matches) > 0 {
			idx := m.matches[m.cursor]
			if idx == -1 {
				return func() tea.Msg { return PickedMsg{Value: m.filter.Value()} }
			}
			v := m.items[idx].Value
			return func() tea.Msg { return PickedMsg{Value: v} }
		}
		return nil
```

Update `View` to render the sentinel. Replace `it := m.items[idx]` with:

```go
		var it Item
		if idx == -1 {
			it = Item{Label: "Use '" + m.filter.Value() + "'", Value: m.filter.Value(), Badge: "custom"}
		} else {
			it = m.items[idx]
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/picker/ -run TestAllowCustom -v`
Expected: PASS

- [ ] **Step 5: Run all picker tests**

Run: `go test ./internal/app/tui/picker/ -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/picker/picker.go internal/app/tui/picker/picker_test.go
git commit -m "feat: add free-text escape hatch to picker overlay"
```

---

## Task 6: Add-provider wizard

Replaces the Providers frame's `a` key: instead of the bare "type a name" prompt, it opens a template picker. Picking a template creates a pre-filled provider entry and drills into it.

**Files:**
- Modify: `internal/app/tui/settings/frames_collections.go` — add `providersWizard`, set `addWizard` on the root frame
- Test: `internal/app/tui/settings/frames_collections_test.go`

**Interfaces:**
- Produces: `providersWizard(s *state) func() *pickerRequest`, `badgeForTemplate(tpl) string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/frames_collections_test.go`:

```go
func TestWizardCreatesProviderFromTemplate(t *testing.T) {
	cfg := config.Default()
	st := newState(cfg)

	req := providersWizard(st)()
	if req == nil {
		t.Fatal("providersWizard should return a pickerRequest")
	}

	var foundOllama bool
	for _, item := range req.items {
		if item.Value == "ollama" {
			foundOllama = true
		}
	}
	if !foundOllama {
		t.Fatal("wizard items should include the ollama template")
	}

	if err := req.onPick("ollama"); err != nil {
		t.Fatalf("wizard onPick(ollama) = %v", err)
	}
	pc, ok := st.cfg.Providers["ollama"]
	if !ok {
		t.Fatal("wizard should have created providers.ollama")
	}
	if pc.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("created provider BaseURL = %q, want http://localhost:11434/v1", pc.BaseURL)
	}
}

func TestWizardCollisionAppendsSuffix(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":   {Type: "openai_compatible"},
		"ollama-2": {Type: "openai_compatible"},
	}
	st := newState(cfg)

	req := providersWizard(st)()
	if err := req.onPick("ollama"); err != nil {
		t.Fatalf("wizard onPick = %v", err)
	}
	if _, ok := st.cfg.Providers["ollama-3"]; !ok {
		t.Fatal("wizard should have created providers.ollama-3 on collision")
	}
}

func TestProvidersFrameHasAddWizard(t *testing.T) {
	st := newState(config.Default())
	f := providersFrame(st)
	if f.addWizard == nil {
		t.Fatal("providers root frame must have addWizard set")
	}
	req := f.addWizard()
	if req == nil || req.title != "Add provider" {
		t.Fatalf("addWizard request = %+v, want title 'Add provider'", req)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestWizard|TestProvidersFrameHasAddWizard' -v`
Expected: FAIL with "undefined: providersWizard"

- [ ] **Step 3: Create the wizard builder**

In `internal/app/tui/settings/frames_collections.go`, add imports `"strings"` and `"marshal/internal/llm/provider"` and `"marshal/internal/app/tui/picker"`.

Add the wizard function:

```go
func providersWizard(s *state) func() *pickerRequest {
	return func() *pickerRequest {
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
		return &pickerRequest{
			fieldID: "__wizard__",
			items:   items,
			title:   "Add provider",
			footer:  "pick a template",
			onPick: func(tplID string) error {
				tpl, ok := provider.Lookup(tplID)
				if !ok {
					return fmt.Errorf("unknown template %q", tplID)
				}
				existing := map[string]bool{}
				for k := range s.cfg.Providers {
					existing[k] = true
				}
				name := provider.UniqueName(tpl.ID, existing)
				if s.cfg.Providers == nil {
					s.cfg.Providers = map[string]config.ProviderConfig{}
				}
				s.cfg.Providers[name] = config.ProviderConfig{
					Type:        tpl.Type,
					BaseURL:      tpl.BaseURL,
					APIKeyEnv:    tpl.KeyEnv,
					ToolCalling:  tpl.ToolCalling,
				}
				return nil
			},
		}
	}
}

func badgeForTemplate(tpl provider.ProviderTemplate) string {
	if tpl.Local {
		return "local"
	}
	return "remote"
}
```

Set `addWizard` on the providers root frame. In `providersFrame`, change the return to:

```go
	f := rootDrillFrame("Providers", drill)
	f.addWizard = providersWizard(s)
	f.list.addWizard = f.addWizard
	return f
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestWizard|TestProvidersFrameHasAddWizard' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/frames_collections.go internal/app/tui/settings/frames_collections_test.go
git commit -m "feat: add provider template-picker wizard to Providers frame"
```

---

## Task 7: Provider Name field

Adds a "Name" field at the top of the provider detail frame so the auto-named entry can be renamed inline.

**Files:**
- Modify: `internal/app/tui/settings/frames_collections.go` — add Name field to provider detail
- Test: `internal/app/tui/settings/frames_collections_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/frames_collections_test.go`:

```go
func TestProviderNameFieldRenamesKey(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "secret"},
	}
	st := newState(cfg)

	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var nameRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Name" {
			nameRow = r
			break
		}
	}
	if nameRow == nil {
		t.Fatal("provider detail must have a Name row")
	}
	if nameRow.getStr() != "ollama" {
		t.Fatalf("Name = %q, want ollama", nameRow.getStr())
	}
	if err := nameRow.setStr("my-ollama"); err != nil {
		t.Fatalf("rename err = %v", err)
	}
	if _, ok := st.cfg.Providers["ollama"]; ok {
		t.Fatal("old key should be deleted after rename")
	}
	pc, ok := st.cfg.Providers["my-ollama"]
	if !ok {
		t.Fatal("new key should exist after rename")
	}
	if pc.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("renamed provider BaseURL = %q, want preserved", pc.BaseURL)
	}
}

func TestProviderNameFieldRejectsCollision(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible"},
		"openrouter": {Type: "openai_compatible"},
	}
	st := newState(cfg)

	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var nameRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Name" {
			nameRow = r
			break
		}
	}
	if err := nameRow.setStr("openrouter"); err == nil {
		t.Fatal("rename to existing key should error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run TestProviderNameField -v`
Expected: FAIL (no Name row)

- [ ] **Step 3: Add the Name field**

In `internal/app/tui/settings/frames_collections.go`, inside the provider detail `buildEntry` closure, add a Name field as the first element of the returned `[]*field`. The closure parameter `k` is a local variable that closures capture by reference, so reassigning it in the Name setter updates all sibling field closures:

```go
			return newFrame(k, func() []*field {
				return []*field{
					scalarField("providers."+k+".name", "Name",
						func() string { return k },
						func(v string) error {
							v = strings.TrimSpace(v)
							if v == "" {
								return fmt.Errorf("name cannot be empty")
							}
							if v == k {
								return nil
							}
							if _, ok := s.cfg.Providers[v]; ok {
								return fmt.Errorf("name already exists")
							}
							pc := s.cfg.Providers[k]
							delete(s.cfg.Providers, k)
							s.cfg.Providers[v] = pc
							delete(s.discovered, k)
							k = v
							return nil
						}),
					scalarField("providers."+k+".type", "Type",
						// ... existing Type field unchanged ...
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run TestProviderNameField -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/frames_collections.go internal/app/tui/settings/frames_collections_test.go
git commit -m "feat: add renameable Name field to provider detail frame"
```

---

## Task 8: Provider/Model pickers in Agent and Presets frames

Replaces the free-text Provider and Model `kindScalar` rows in the Agent and Model Presets frames with `kindPicker` rows.

**Files:**
- Modify: `internal/app/tui/settings/frames_collections.go` — add `providerPickerField`/`modelPickerField` helpers; replace presets Provider/Model fields
- Modify: `internal/app/tui/settings/frames_agent.go` — replace Provider/Model fields
- Test: `internal/app/tui/settings/frames_agent_test.go`
- Test: `internal/app/tui/settings/frames_collections_test.go`

**Interfaces:**
- Produces: `providerPickerField(s, id, getProvider, setProvider) *field`, `modelPickerField(s, id, providerName, getModel, setModel) *field`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/frames_agent_test.go`:

```go
func TestAgentProviderFieldIsKindPicker(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	f := agentFrame(st)

	var providerRow *field
	for _, r := range f.list.Rows() {
		if r.title == "Provider" {
			providerRow = r
			break
		}
	}
	if providerRow == nil {
		t.Fatal("Agent frame must have a Provider row")
	}
	if providerRow.kind != kindPicker {
		t.Fatalf("Provider row kind = %v, want kindPicker", providerRow.kind)
	}
	values := map[string]bool{}
	for _, item := range providerRow.pickOptions() {
		values[item.Value] = true
	}
	if !values["ollama"] || !values["openrouter"] {
		t.Fatalf("provider picker items missing configured providers, got %v", values)
	}
}

func TestAgentProviderPickerEmptyState(t *testing.T) {
	st := newState(config.Default())
	f := agentFrame(st)

	var providerRow *field
	for _, r := range f.list.Rows() {
		if r.title == "Provider" {
			providerRow = r
			break
		}
	}
	items := providerRow.pickOptions()
	if len(items) == 0 || items[0].Value != "__add_provider__" {
		t.Fatalf("empty provider picker should have an 'Add a provider' item, got %v", items)
	}
}

func TestAgentModelPickerUsesDiscoveredCache(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	st.discovered["ollama"] = []string{"qwen2.5-coder:7b", "llama3.1:8b"}

	f := agentFrame(st)
	var modelRow *field
	for _, r := range f.list.Rows() {
		if r.title == "Model" {
			modelRow = r
			break
		}
	}
	if modelRow.kind != kindPicker {
		t.Fatalf("Model row kind = %v, want kindPicker", modelRow.kind)
	}
	values := map[string]bool{}
	for _, item := range modelRow.pickOptions() {
		values[item.Value] = true
	}
	if !values["qwen2.5-coder:7b"] {
		t.Fatal("model picker should include discovered models")
	}
}
```

Add to `internal/app/tui/settings/frames_collections_test.go`:

```go
func TestPresetProviderFieldIsKindPicker(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}
	st := newState(cfg)
	drill := presetsFrame(st).list.Rows()[0]
	detail := drill.build()

	var providerRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Provider" {
			providerRow = r
			break
		}
	}
	if providerRow == nil {
		t.Fatal("preset detail must have a Provider row")
	}
	if providerRow.kind != kindPicker {
		t.Fatalf("preset Provider row kind = %v, want kindPicker", providerRow.kind)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestAgentProvider|TestAgentModel|TestPresetProvider' -v`
Expected: FAIL (Provider/Model rows are still kindScalar)

- [ ] **Step 3: Create the picker helper functions**

In `internal/app/tui/settings/frames_collections.go`, add these helpers (imports for `picker` and `provider` are already added in Task 6):

```go
func providerPickerField(s *state, id string, getProvider func() string, setProvider func(string) error) *field {
	return &field{
		id:    id,
		title: "Provider",
		kind:  kindPicker,
		desc:  "configured provider for this role",
		getStr: func() string { return getProvider() },
		pickOptions: func() []picker.Item {
			names := sortedKeys(s.cfg.Providers)
			if len(names) == 0 {
				return []picker.Item{{Label: "Add a provider\u2026", Value: "__add_provider__", Badge: "required"}}
			}
			items := make([]picker.Item, 0, len(names))
			current := getProvider()
			for _, n := range names {
				badge := ""
				if n == current {
					badge = "\u25cf now"
				}
				if isLocalhost(s.cfg.Providers[n].BaseURL) {
					if badge != "" {
						badge += " "
					}
					badge += "local"
				}
				items = append(items, picker.Item{Label: n, Value: n, Badge: badge})
			}
			return items
		},
		pickOnPick: func(v string) error {
			if v == "__add_provider__" {
				return fmt.Errorf("add a provider first in the Providers section")
			}
			return setProvider(v)
		},
	}
}

func modelPickerField(s *state, id string, providerName func() string, getModel func() string, setModel func(string) error) *field {
	return &field{
		id:    id,
		title: "Model",
		kind:  kindPicker,
		desc:  "model id for this role",
		getStr: func() string { return getModel() },
		pickOptions: func() []picker.Item {
			pn := providerName()
			current := getModel()
			var items []picker.Item
			if cached, ok := s.discovered[pn]; ok && len(cached) > 0 {
				for _, m := range cached {
					badge := "\u25c9 discovered"
					if m == current {
						badge = "\u25cf now \u25c9 discovered"
					}
					items = append(items, picker.Item{Label: m, Value: m, Badge: badge})
				}
			} else if tpl, ok := provider.Lookup(pn); ok && len(tpl.Models) > 0 {
				for _, m := range tpl.Models {
					badge := "\u25cb catalog"
					if m == current {
						badge = "\u25cf now \u25cb catalog"
					}
					items = append(items, picker.Item{Label: m, Value: m, Badge: badge})
				}
			} else {
				items = []picker.Item{{Label: "Test connection to discover", Value: "__discover__", Badge: "refresh"}}
			}
			return items
		},
		pickOnPick: func(v string) error {
			if v == "__discover__" {
				return fmt.Errorf("test the provider connection first to discover models")
			}
			return setModel(v)
		},
	}
}
```

- [ ] **Step 4: Replace Agent frame Provider/Model fields**

In `internal/app/tui/settings/frames_agent.go`, replace the `scalarField` calls for Provider and Model with `providerPickerField` and `modelPickerField`:

```go
			providerPickerField(s, "agent.provider",
				func() string { return provider },
				setProvider),
			modelPickerField(s, "agent.model",
				func() string {
					if name := activePresetNameFor(s.cfg); name != "" {
						return provider
					}
					return provider
				},
				func() string { return model },
				setModel),
```

The `provider` and `model` local variables in `agentFrame` are computed at the top of the `func() []*field` closure — they read from `getActive()` and `s.cfg.Agent`. The `modelPickerField`'s `providerName` func should return the currently selected provider so the model picker scopes to it. Use `func() string { return provider }` — but note `provider` is computed fresh on each `Refresh()` call (inside the outer `func() []*field`), and the `pickOptions` closure captures it. This works because `pickOptions` is called at picker-open time, which triggers a `Refresh()` first.

Actually, `provider` and `model` are local variables computed inside `func() []*field` which runs on every `Refresh()`. The `pickOptions` closure captures these. Since `pickOptions` is called when the picker opens (after the latest `Refresh()`), it sees the current values. This is correct.

For the `modelPickerField`'s `providerName` func, use `func() string { return provider }` where `provider` is the local from the outer closure.

- [ ] **Step 5: Replace Presets frame Provider/Model fields**

In `internal/app/tui/settings/frames_collections.go`, inside the `presetsFrame` `buildEntry` closure, replace the `scalarField` calls:

```go
				providerPickerField(s, "presets."+k+".provider",
					func() string { return s.cfg.Models.Presets[k].Provider },
					func(v string) error { mut(func(p *routing.ModelPreset) { p.Provider = v }); return nil }),
				modelPickerField(s, "presets."+k+".model",
					func() string { return s.cfg.Models.Presets[k].Provider },
					func() string { return s.cfg.Models.Presets[k].Model },
					func(v string) error { mut(func(p *routing.ModelPreset) { p.Model = v }); return nil }),
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestAgentProvider|TestAgentModel|TestPresetProvider' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/settings/frames_collections.go internal/app/tui/settings/frames_agent.go internal/app/tui/settings/frames_agent_test.go internal/app/tui/settings/frames_collections_test.go
git commit -m "feat: replace provider/model free-text fields with pickers in Agent and Presets"
```

---

## Task 9: Update existing tests and full verification

The existing `model_test.go` tests (`TestSettingsNavigationThroughMainModel`, `TestSettingsTypingThroughMainModel`, `TestSettingsBoolFieldToggleThroughMainModel`) reference "Provider" and "Model" fields as `kindScalar` and navigate to them. Since they are now `kindPicker`, the navigation and typing behavior changes: `kindPicker` opens the overlay on Enter (not inline edit), and typing into the pane while a picker row is focused should not toggle anything.

**Files:**
- Modify: `internal/app/tui/model_test.go:1251-1395` (update existing settings tests)

- [ ] **Step 1: Run the full test suite to identify failures**

Run: `go test ./internal/app/tui/... -v 2>&1 | grep -E 'FAIL|PASS' | head -30`
Expected: Some existing tests may fail because Provider/Model rows changed from `kindScalar` to `kindPicker`.

- [ ] **Step 2: Update `TestSettingsTypingThroughMainModel`**

In `internal/app/tui/model_test.go`, the test `TestSettingsTypingThroughMainModel` navigates to the "Provider" field and types into it, verifying that typing doesn't toggle "Local only". Since Provider is now `kindPicker`, typing characters into the pane goes to the `fieldList` which ignores non-bound characters (they're not `j`/`k`/`enter`/etc.). The test's assertion that typing doesn't toggle "Local only" should still hold. Verify the test passes. If it fails because the cursor navigation changed (the "Provider" row moved position due to the new Name field in providers or other row additions), update the navigation indices.

- [ ] **Step 3: Update `TestSettingsNavigationThroughMainModel` and `TestSettingsBoolFieldToggleThroughMainModel`**

These tests navigate by cursor position. If the row positions changed (e.g., Provider is no longer at the same index because of the new Name field in the provider detail or new fields in the Agent frame), update the `sendKey` counts to navigate to the correct rows. The tests use `FocusedFieldTitle()` to verify which row is focused — update the expected titles if the test asserts on a field that moved.

- [ ] **Step 4: Run all settings tests**

Run: `go test ./internal/app/tui/settings/ -v`
Expected: all PASS

- [ ] **Step 5: Run the full test suite**

Run: `go test ./... 2>&1 | tail -20`
Expected: all PASS

- [ ] **Step 6: Run vet**

Run: `go vet ./...`
Expected: no errors

- [ ] **Step 7: Run build**

Run: `go build ./cmd/marshal`
Expected: success

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/model_test.go
git commit -m "test: update existing settings tests for kindPicker provider/model rows"
```

---

## Self-Review

**Spec coverage:**
- Section 1 (Provider template catalog): Task 1 ✓
- Section 2 (Add-provider wizard): Tasks 6, 7 ✓
- Section 3 (kindAction primitive): Task 2 ✓
- Section 4 (Live model discovery + test connection): Tasks 3, 4 ✓
- Section 5 (Provider/Model pickers): Tasks 5, 8 ✓
- Privacy gating: Task 4 (test-connection gate) ✓, Task 8 (picker options don't need gating — they read from config, no network) ✓
- Free-text escape hatch: Task 5 ✓
- Discovery cache invalidation: Task 4 ✓

**Type consistency:**
- `probeResultMsg{FieldID, Provider, Models, Err}` — used consistently in Task 2 (messages.go), Task 3 (discover.go), Task 4 (testConnectionField via probeProvider).
- `actionResultMsg{FieldID, Label}` — defined Task 2, used in fieldlist_test.go.
- `pickerRequest{fieldID, items, onPick, title, footer}` — defined Task 2, used in Task 6 (wizard), Task 8 (pickers).
- `state.discovered` / `state.actionState` — defined Task 2, used Tasks 3/4/8.
- `providerPickerField` / `modelPickerField` — defined Task 8, used in frames_agent.go and frames_collections.go.

**Placeholder scan:** No TBDs, no "implement later", no "add appropriate error handling". All code is complete.

**Scope check:** Phase 1 is focused on the provider experience. Phase 2 (duplicate/reorder, reset, diff preview) is a separate plan.
