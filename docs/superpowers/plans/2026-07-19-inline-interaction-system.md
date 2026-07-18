# Inline Interaction System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Marshal's four full-screen TUI takeovers (settings, memory, connect, help) with one interaction system: transcript prints, a single docked panel above the input, and a command-first `/set` for settings.

**Architecture:** A new `internal/app/tui/dock` package owns the one panel slot rendered between the transcript and the input area. A flat settings `Registry` (extracted from the existing frame builders in `internal/app/tui/settings`) powers `/set`, completions, and a docked settings browser. Help, receipts, and memory detail become styled transcript messages. Spec: `docs/superpowers/specs/2026-07-19-inline-interaction-system-design.md`.

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), lipgloss v2, existing `chrome`/`theme`/`fuzzy` packages. Build needs `CGO_ENABLED=1`.

## Global Constraints

- Persistence writes go to the project-local config only: `config.SaveProjectConfig(path, cfg)` with `path = projectConfigPath(m.state.WorkingDir)` (helper already exists in `internal/app/tui/model.go`).
- Settings changes are **immediate** (validate → apply → save → receipt). No Ctrl+S transaction.
- All colors via `theme.Current()` slots; `✓`/`✗` receipt glyphs must carry meaning without color (NO_COLOR-safe).
- Dock height budget: `min(natural, 40% of frame height)`, floor 6 rows. Must stay usable at 80×24.
- Footer (`help.Footer`, `help.Rows`) and status line are untouched throughout.
- Each task ends green: `gofmt -w . && go vet ./... && go test ./internal/...`.
- Commit after every task (and at intermediate green points inside large tasks).

**Reading list for every implementer:** `internal/app/tui/model.go` (Update routing, `dispatchCommand`, `openPicker`, `resize`), `internal/app/tui/view.go` (`viewString` row stack), `internal/app/tui/picker/picker.go`, `internal/app/tui/chrome/chrome.go`, `internal/app/tui/settings/{field.go,fieldlist.go,panestack.go,sections.go,state.go,search.go}`.

---

### Task 1: `dock` package — Panel interface and Host

**Files:**
- Create: `internal/app/tui/dock/dock.go`
- Test: `internal/app/tui/dock/dock_test.go`

**Interfaces:**
- Consumes: nothing (leaf package; imports only bubbletea + lipgloss).
- Produces: `dock.Panel` interface `{ Update(tea.Msg) tea.Cmd; View(width, maxHeight int) string }`, `dock.CloseMsg`, `dock.Close() tea.Msg`, `dock.MaxRows(frameHeight int) int`, and `dock.Host` with methods `Open(Panel)`, `CloseNow()`, `IsOpen() bool`, `Panel() Panel`, `Update(tea.Msg) tea.Cmd`, `View(width, frameHeight int) string`, `Rows() int`.

- [ ] **Step 1: Write the failing test**

```go
// internal/app/tui/dock/dock_test.go
package dock

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakePanel struct{ lastW, lastH int }

func (f *fakePanel) Update(tea.Msg) tea.Cmd { return nil }
func (f *fakePanel) View(w, maxH int) string {
	f.lastW, f.lastH = w, maxH
	return "line1\nline2\nline3"
}

func TestMaxRows(t *testing.T) {
	cases := []struct{ frame, want int }{
		{24, 9},  // 40% of 24
		{40, 16}, // 40% of 40
		{10, 6},  // floor
	}
	for _, c := range cases {
		if got := MaxRows(c.frame); got != c.want {
			t.Errorf("MaxRows(%d) = %d, want %d", c.frame, got, c.want)
		}
	}
}

func TestHostLifecycle(t *testing.T) {
	var h Host
	if h.IsOpen() || h.Rows() != 0 || h.View(80, 24) != "" {
		t.Fatal("empty host must be closed, zero rows, empty view")
	}
	p := &fakePanel{}
	h.Open(p)
	if !h.IsOpen() {
		t.Fatal("host should be open after Open")
	}
	v := h.View(80, 24)
	if !strings.Contains(v, "line2") {
		t.Fatalf("view should render panel content, got %q", v)
	}
	if p.lastH != MaxRows(24) {
		t.Errorf("panel got maxHeight %d, want %d", p.lastH, MaxRows(24))
	}
	if h.Rows() != 3 {
		t.Errorf("Rows() = %d after render, want 3", h.Rows())
	}
	h.CloseNow()
	if h.IsOpen() || h.Rows() != 0 {
		t.Fatal("CloseNow must reset panel and rows")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/dock/`
Expected: FAIL — package does not exist / undefined symbols.

- [ ] **Step 3: Write the implementation**

```go
// Package dock hosts a single interactive panel docked above the input
// area, fzf-style: the transcript stays visible above it. The TUI model
// owns one Host; opening a panel while another is open replaces it.
package dock

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Panel is anything the dock can host. Panels own their key handling,
// including Esc: emit the panel's own cancel/close message, or CloseMsg
// for panels without one. picker.Model satisfies this interface as-is.
type Panel interface {
	Update(msg tea.Msg) tea.Cmd
	// View renders at most maxHeight rows (borders included) at width.
	View(width, maxHeight int) string
}

// CloseMsg asks the model to close the dock.
type CloseMsg struct{}

// Close is a convenience tea.Msg constructor for panels.
func Close() tea.Msg { return CloseMsg{} }

// MaxRows is the dock's height budget: 40% of the frame height, floor 6.
func MaxRows(frameHeight int) int {
	return max(frameHeight*2/5, 6)
}

// Host owns the single dock slot.
type Host struct {
	panel Panel
	rows  int
}

func (h *Host) Open(p Panel)  { h.panel = p }
func (h *Host) CloseNow()     { h.panel, h.rows = nil, 0 }
func (h *Host) IsOpen() bool  { return h.panel != nil }
func (h *Host) Panel() Panel  { return h.panel }

// Rows is the height of the last rendered view (0 when closed). The
// model subtracts it from the transcript viewport height, mirroring the
// sddPanelRows pattern.
func (h *Host) Rows() int { return h.rows }

func (h *Host) Update(msg tea.Msg) tea.Cmd {
	if h.panel == nil {
		return nil
	}
	return h.panel.Update(msg)
}

func (h *Host) View(width, frameHeight int) string {
	if h.panel == nil {
		h.rows = 0
		return ""
	}
	v := h.panel.View(width, MaxRows(frameHeight))
	h.rows = lipgloss.Height(v)
	return v
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/dock/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/dock/
git commit -m "feat(tui): add dock package hosting a single panel above the input"
```

---

### Task 2: Rehost the picker in the dock

**Files:**
- Modify: `internal/app/tui/model.go` (fields, key routing, `openPicker`, `resize`, viewport-height sync)
- Modify: `internal/app/tui/view.go` (`viewString`: replace `chrome.Overlay` with a dock row)
- Test: `internal/app/tui/view_test.go`, `internal/app/tui/smoke_pickers_test.go` (update expectations)

**Interfaces:**
- Consumes: `dock.Host`, `dock.MaxRows` from Task 1; existing `picker.PickedMsg`/`picker.CancelledMsg` handling.
- Produces: `Model.dock dock.Host` field and `Model.dockRows() int` — the single interactive-layer slot every later task opens panels into. `openPicker` unchanged in signature.

- [ ] **Step 1: Write the failing view test**

Add to `internal/app/tui/view_test.go` (follow the file's existing model-construction helpers):

```go
func TestPickerRendersDockedAboveInput(t *testing.T) {
	m := newTestModel(t) // use the file's existing constructor helper
	m.resize(100, 40)
	m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")
	out := stripANSI(m.viewString())
	lines := strings.Split(out, "\n")
	panelLine, inputLine := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Interaction mode") {
			panelLine = i
		}
		if inputLine == -1 && strings.Contains(l, ">") && panelLine != -1 {
			inputLine = i
		}
	}
	if panelLine == -1 {
		t.Fatal("picker panel not rendered")
	}
	if inputLine == -1 || inputLine < panelLine {
		t.Fatalf("picker must sit above the input area (panel=%d input=%d)", panelLine, inputLine)
	}
	// Docked, not centered: the panel's top border starts at the left edge.
	if !strings.HasPrefix(strings.TrimRight(lines[panelLine], " "), "╭") {
		t.Errorf("panel should be left-aligned, got %q", lines[panelLine])
	}
}
```

Adjust the input-line detection to whatever marker `renderInputArea` actually emits (check an existing view test for the idiom) — the assertion that matters is *panel row above input row, left-aligned, transcript still present above*.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestPickerRendersDocked`
Expected: FAIL — picker currently rendered via centered `chrome.Overlay`.

- [ ] **Step 3: Wire the dock into the model**

In `internal/app/tui/model.go`:

1. Add field to `Model` (near `pickerModel`): `dock dock.Host` (import `marshal/internal/app/tui/dock`).
2. In `openPicker` (~line 1940), after `m.pickerModel = p`, add `m.dock.Open(p)`. (Keep `pickerModel` for this task — it is deleted in Task 9; `openSDDPlanPicker` at ~2047 gets the same one-line addition.)
3. Everywhere `m.pickerModel = nil` is set on `PickedMsg`/`CancelledMsg` (~lines 543, 563, 569), also call `m.dock.CloseNow()`.
4. Key routing (~line 574): change the guard `if m.pickerModel != nil { ... m.pickerModel.Update(msg) }` to route through the dock: `if m.dock.IsOpen() { return m, m.dock.Update(msg) }` for `tea.KeyPressMsg`. Check line 1545's `pickerModel != nil` guard and switch it to `m.dock.IsOpen()`.
5. Add the rows accessor and height budget:

```go
// dockRows reports the rows the docked panel occupied at last render,
// so the transcript viewport shrinks while a panel is open.
func (m Model) dockRows() int { return m.dock.Rows() }
```

6. In `resize` (line ~431) and in the viewport-height recomputation (~line 1100), add `-m.dockRows()` to the subtraction chain, next to `-m.sddPanelRows()`.

- [ ] **Step 4: Render the dock row in view.go**

In `viewString()` replace the trailing overlay block:

```go
// before (delete):
out := lipgloss.JoinVertical(lipgloss.Left, rows...)
if m.pickerModel != nil {
	return chrome.Overlay(out, m.pickerModel.View(m.width, m.height), m.width, m.height)
}
return out
```

```go
// after: dock row sits between the transient panels and the input area.
if d := m.dock.View(m.width, m.height); d != "" {
	rows = append(rows, d)
}
rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
return lipgloss.JoinVertical(lipgloss.Left, rows...)
```

(Move the `rows = append(rows, m.renderInputArea(), ...)` line so the dock row precedes it.) Remove the now-unused `chrome` import from view.go if nothing else uses it — `chrome.Overlay` itself is deleted in Task 9.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/app/tui/...`
Expected: new test PASSES; fix any existing picker/view tests that asserted centered placement (update them to the docked expectation, don't delete coverage).

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/
git commit -m "feat(tui): dock the command picker above the input instead of centered overlay"
```

---

### Task 3: Settings Registry

**Files:**
- Create: `internal/app/tui/settings/registry.go`
- Test: `internal/app/tui/settings/registry_test.go`

**Interfaces:**
- Consumes: existing `sectionList()`, `newState(cfg)`, `field` (kinds `kindToggle`/`kindScalar`/`kindEnum`), `maskKey` (masked.go), `fuzzy.Rank`.
- Produces:
  - `settings.BuildRegistry(cfg config.Config) *Registry`
  - `(*Registry).Config() config.Config` — the working config after Apply calls
  - `(*Registry).Lookup(key string) (*field, bool)`
  - `(*Registry).Keys() []string` — ordered dotted keys
  - `(*Registry).Match(query string) []*field` — fuzzy
  - `(*Registry).Describe(key string) (kind, current string, options []string, err error)` — for `/set <key>` prints and value completion
  - `(*Registry).Apply(key, value string) (oldVal, newVal string, err error)` — validate + apply, no persistence

- [ ] **Step 1: Write the failing test**

```go
// internal/app/tui/settings/registry_test.go
package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestRegistryKeysUniqueAndPopulated(t *testing.T) {
	r := BuildRegistry(config.Default())
	keys := r.Keys()
	if len(keys) < 30 {
		t.Fatalf("registry suspiciously small: %d keys", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("duplicate key %q", k)
		}
		seen[k] = true
		if _, ok := r.Lookup(k); !ok {
			t.Errorf("Keys() entry %q not Lookup-able", k)
		}
	}
}

// Parity with the old full-screen tree: every leaf field with an id in
// every section's root frame must appear in the registry.
func TestRegistryParityWithSectionFrames(t *testing.T) {
	st := newState(config.Default())
	r := BuildRegistry(config.Default())
	for _, sp := range sectionList() {
		for _, f := range sp.root(st).list.Rows() {
			if f.id == "" {
				continue
			}
			if _, ok := r.Lookup(f.id); !ok {
				t.Errorf("section %s field %q missing from registry", sp.id, f.id)
			}
		}
	}
}

func TestRegistryApplyToggle(t *testing.T) {
	r := BuildRegistry(config.Default())
	// shell.allow_network is a known toggle id (see field.go docs).
	oldV, newV, err := r.Apply("shell.allow_network", "on")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if newV != "on" {
		t.Errorf("newV = %q, want on", newV)
	}
	if oldV == newV {
		t.Errorf("old and new both %q — toggle did not report a change", oldV)
	}
	if !r.Config().Tools.Shell.AllowNetwork {
		t.Error("Apply did not mutate the working config")
	}
}

func TestRegistryApplyErrors(t *testing.T) {
	r := BuildRegistry(config.Default())
	if _, _, err := r.Apply("no.such.key", "x"); err == nil {
		t.Error("unknown key must error")
	}
	if _, _, err := r.Apply("shell.allow_network", "maybe"); err == nil {
		t.Error("bad bool must error")
	}
	// Find any enum field and feed it a bogus value.
	for _, k := range r.Keys() {
		f, _ := r.Lookup(k)
		if f.kind == kindEnum && f.setStr != nil {
			if _, _, err := r.Apply(k, "definitely-not-an-option"); err == nil ||
				!strings.Contains(err.Error(), "one of") {
				t.Errorf("enum %s: want 'must be one of' error, got %v", k, err)
			}
			return
		}
	}
}
```

If `shell.allow_network` turns out not to be the exact id in `frames_shell.go`, use the real toggle id from that file and fix the `Config()` field assertion to match — do not weaken the assertions.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run TestRegistry`
Expected: FAIL — `BuildRegistry` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/app/tui/settings/registry.go
package settings

import (
	"fmt"
	"sort"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/fuzzy"
)

// Registry is a flat, screen-independent index of every leaf field in
// the settings tree, keyed by dotted id ("shell.allow_network"). It owns
// a working config that the field closures mutate; callers persist via
// config.SaveProjectConfig(path, r.Config()) after Apply.
//
// Nested drill frames (collections: MCP servers, hooks, permissions,
// presets) are intentionally not flattened — matching search.go — so
// /set only addresses leaf fields; collections are edited in the
// browser panel.
type Registry struct {
	st      *state
	order   []string
	byID    map[string]*field
	section map[string]string // id -> section title, for Match haystacks
}

func BuildRegistry(cfg config.Config) *Registry {
	st := newState(cfg)
	r := &Registry{st: st, byID: map[string]*field{}, section: map[string]string{}}
	for _, sp := range sectionList() {
		for _, f := range sp.root(st).list.Rows() {
			if f.id == "" {
				continue
			}
			if _, dup := r.byID[f.id]; dup {
				continue // first wins; parity test flags real duplicates
			}
			r.order = append(r.order, f.id)
			r.byID[f.id] = f
			r.section[f.id] = sp.title
		}
	}
	return r
}

func (r *Registry) Config() config.Config { return r.st.cfg }

func (r *Registry) Lookup(key string) (*field, bool) {
	f, ok := r.byID[key]
	return f, ok
}

func (r *Registry) Keys() []string {
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

func (r *Registry) Match(query string) []*field {
	hay := make([]string, len(r.order))
	for i, id := range r.order {
		f := r.byID[id]
		hay[i] = r.section[id] + " " + id + " " + f.title + " " + strings.Join(f.keywords, " ")
	}
	var out []*field
	for _, idx := range fuzzy.Rank(query, hay) {
		out = append(out, r.byID[r.order[idx]])
	}
	return out
}

// Describe reports a field's kind name, display value, and enum options
// for `/set <key>` prints and value completion.
func (r *Registry) Describe(key string) (kind, current string, options []string, err error) {
	f, ok := r.byID[key]
	if !ok {
		return "", "", nil, fmt.Errorf("unknown setting %q", key)
	}
	switch f.kind {
	case kindToggle:
		return "toggle", onOff(f.getBool()), []string{"on", "off"}, nil
	case kindEnum:
		return "enum", f.getStr(), f.options(), nil
	case kindScalar:
		v := f.getStr()
		if f.masked {
			v = maskKey(v)
		}
		return "scalar", v, nil, nil
	default:
		return "", "", nil, fmt.Errorf("%s is edited in /settings (collection or action)", key)
	}
}

// Apply validates and applies value to key, returning display strings
// for the transcript receipt. It does not persist.
func (r *Registry) Apply(key, value string) (oldVal, newVal string, err error) {
	f, ok := r.byID[key]
	if !ok {
		return "", "", fmt.Errorf("unknown setting %q", key)
	}
	switch f.kind {
	case kindToggle:
		b, perr := parseOnOff(value)
		if perr != nil {
			return "", "", perr
		}
		oldVal = onOff(f.getBool())
		f.setBool(b)
		return oldVal, onOff(b), nil
	case kindScalar, kindEnum:
		if f.setStr == nil {
			return "", "", fmt.Errorf("%s is read-only", key)
		}
		if f.kind == kindEnum {
			opts := f.options()
			found := false
			for _, o := range opts {
				if o == value {
					found = true
					break
				}
			}
			if !found {
				return "", "", fmt.Errorf("%s must be one of: %s", key, strings.Join(opts, ", "))
			}
		}
		oldVal = f.getStr()
		if err := f.setStr(value); err != nil {
			return "", "", err
		}
		newVal = f.getStr()
		if f.masked {
			oldVal, newVal = maskKey(oldVal), maskKey(newVal)
		}
		return oldVal, newVal, nil
	default:
		return "", "", fmt.Errorf("%s is edited in /settings (collection or action)", key)
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a toggle value (use on/off)", s)
}
```

If `maskKey` has a different name or signature in `masked.go`, use the real one.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/settings/`
Expected: PASS (including all existing settings tests — the registry must not disturb them).

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/registry.go internal/app/tui/settings/registry_test.go
git commit -m "feat(settings): flat registry of leaf fields keyed by dotted id"
```

---

### Task 4: `/set` command, receipts, and completions

**Files:**
- Modify: `internal/commands/commands.go` (register `set`)
- Modify: `internal/app/tui/model.go` (`dispatchCommand` case, `handleSetCommand`, `applyNewConfig` extraction, completion sources)
- Test: `internal/commands/commands_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/completions_test.go`

**Interfaces:**
- Consumes: `settings.BuildRegistry`, `(*Registry).Apply/Describe/Keys/Match` (Task 3); `config.SaveProjectConfig(path string, cfg config.Config) error`; `m.state.AddMessage(session.RoleSystem, text, session.ContentTypePlain)`.
- Produces: `/set` command; `Model.handleSetCommand(args []string)`; `Model.applyNewConfig(cfg config.Config)` (also reused by the existing `settings.SavedMsg` handler and by Task 5); `Model.setReg *settings.Registry` lazy cache with `Model.settingsRegistry() *settings.Registry`.

- [ ] **Step 1: Register the command (test first)**

In `internal/commands/commands_test.go`, extend the registration-coverage test (the file has one that lists expected command names) to include `"set"`. Then in `commands.go` register alongside `/settings`:

```go
{
	Name:        "set",
	Description: "Change a setting inline (\"/set\" alone browses)",
	Args:        "<key> [value]",
	Handler:     func(state *session.State, args []string) string { return "" },
},
```

The handler is a no-op string like `/settings`'s: behavior lives in the TUI interception (`dispatchCommand`), keeping policy out of the commands package.

- [ ] **Step 2: Extract `applyNewConfig`**

In `model.go`, the `settings.SavedMsg` handler (~line 461) applies a freshly loaded config to the session. Extract its body into:

```go
// applyNewConfig installs cfg as the live session config and invalidates
// anything derived from it (cached /set registry, completion sources).
func (m *Model) applyNewConfig(cfg config.Config) {
	m.state.Config = cfg
	m.setReg = nil
	// ...whatever the SavedMsg handler already did (theme refresh, etc.)
	// moves here verbatim; the handler then calls this method.
}
```

Run `go test ./internal/app/tui/...` — behavior-preserving refactor, everything stays green. Commit: `refactor(tui): extract applyNewConfig from settings.SavedMsg handler`.

- [ ] **Step 3: Write the failing `/set` behavior test**

In `internal/app/tui/model_test.go` (use the existing test-model constructor; point the working dir at `t.TempDir()` so `SaveProjectConfig` writes somewhere real):

```go
func TestSetCommandAppliesAndPrintsReceipt(t *testing.T) {
	m := newTestModel(t)
	m.state.WorkingDir = t.TempDir()

	m.dispatchCommand("/set shell.allow_network on")

	if !m.state.Config.Tools.Shell.AllowNetwork {
		t.Error("config not applied")
	}
	msgs := m.state.Messages()
	last := msgs[len(msgs)-1].Content
	for _, want := range []string{"✓", "shell.allow_network", "→ on", ".marshal/config.toml"} {
		if !strings.Contains(last, want) {
			t.Errorf("receipt %q missing %q", last, want)
		}
	}
	if _, err := os.Stat(filepath.Join(m.state.WorkingDir, ".marshal", "config.toml")); err != nil {
		t.Errorf("project config not written: %v", err)
	}
}

func TestSetCommandBadValuePrintsError(t *testing.T) {
	m := newTestModel(t)
	m.state.WorkingDir = t.TempDir()
	m.dispatchCommand("/set shell.allow_network maybe")
	msgs := m.state.Messages()
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "✗") {
		t.Errorf("want ✗ error receipt, got %q", last)
	}
	if m.state.Config.Tools.Shell.AllowNetwork {
		t.Error("failed set must not mutate session config")
	}
}

func TestSetCommandKeyOnlyPrintsCurrentValue(t *testing.T) {
	m := newTestModel(t)
	m.dispatchCommand("/set shell.allow_network")
	msgs := m.state.Messages()
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "shell.allow_network") || !strings.Contains(last, "toggle") {
		t.Errorf("want current-value print with kind, got %q", last)
	}
}
```

Match `m.state.Messages()` / message-content accessors to what `session.State` actually exposes (see existing model tests). Run to verify FAIL.

- [ ] **Step 4: Implement `handleSetCommand`**

In `dispatchCommand`'s switch add `case "set": m.handleSetCommand(args); m.refreshViewport(); return m, nil` and implement:

```go
// settingsRegistry returns the cached /set registry, rebuilt after any
// config change (applyNewConfig clears it).
func (m *Model) settingsRegistry() *settings.Registry {
	if m.setReg == nil {
		m.setReg = settings.BuildRegistry(m.state.Config)
	}
	return m.setReg
}

func (m *Model) handleSetCommand(args []string) {
	sys := func(text string) { m.state.AddMessage(session.RoleSystem, text, session.ContentTypePlain) }
	switch len(args) {
	case 0:
		// Until Task 5 lands the browser, fall back to the settings view.
		// Task 5 replaces this line with m.openSettingsBrowser("").
		m.dispatchCommand("/settings")
		return
	case 1:
		key := args[0]
		reg := m.settingsRegistry()
		kind, current, options, err := reg.Describe(key)
		if err != nil {
			// Not an exact key: suggest matches.
			matches := reg.Match(key)
			if len(matches) == 0 {
				sys(fmt.Sprintf("✗ no setting matches %q", key))
				return
			}
			var b strings.Builder
			b.WriteString("Settings matching \"" + key + "\":")
			for i, f := range matches {
				if i == 8 {
					b.WriteString("\n  …")
					break
				}
				b.WriteString("\n  " + f.id)
			}
			sys(b.String())
			return
		}
		line := fmt.Sprintf("%s = %s (%s)", key, current, kind)
		if len(options) > 0 {
			line += " · options: " + strings.Join(options, ", ")
		}
		sys(line)
		return
	default:
		key, value := args[0], strings.Join(args[1:], " ")
		reg := m.settingsRegistry()
		oldV, newV, err := reg.Apply(key, value)
		if err != nil {
			m.setReg = nil // discard any partial working state
			sys("✗ " + key + ": " + err.Error())
			return
		}
		path := projectConfigPath(m.state.WorkingDir)
		saveErr := config.SaveProjectConfig(path, reg.Config())
		// Per spec: apply in memory even if the save failed, so the user
		// can fix permissions and retry with /set.
		m.applyNewConfig(reg.Config())
		if saveErr != nil {
			sys(fmt.Sprintf("✗ %s applied in session, but save failed: %v", key, saveErr))
			return
		}
		sys(fmt.Sprintf("✓ %s: %s → %s · %s", key, oldV, newV, relPath(m.state.WorkingDir, path)))
	}
}
```

`relPath` is a tiny helper (`filepath.Rel` with fallback to the input) — add it next to `projectConfigPath`. Note `field.id` is unexported; if `model.go` needs the id in the suggestions loop, add a tiny exported accessor in the settings package: `func (f *field) ID() string { return f.id }` won't export the type — instead have `Match` return `[]string` of ids, or add `func (r *Registry) MatchKeys(query string) []string`. **Pick `MatchKeys(query string) []string` and use it here and in completions** — adjust Task 3's `Match` accordingly (keep the field-returning variant package-private if the browser needs it).

- [ ] **Step 5: Run behavior tests**

Run: `go test ./internal/app/tui/ -run TestSetCommand`
Expected: PASS. Commit: `feat(tui): /set command with immediate save and transcript receipts`.

- [ ] **Step 6: Completions (test first)**

In `internal/app/tui/completions_test.go` add:

```go
func TestSetArgumentCompletion(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/set shell")
	m.updateCompletionPopups() // use the real update entrypoint; see model.go
	p := m.activeCompletionPopup()
	if p == nil || !p.isVisible() {
		t.Fatal("expected key completion popup for /set argument")
	}
	found := false
	for _, it := range p.matches() {
		if strings.Contains(it.Text, "shell.allow_network") {
			found = true
		}
	}
	if !found {
		t.Error("shell.allow_network not offered")
	}
}

func TestSetValueCompletionForToggle(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/set shell.allow_network ")
	m.updateCompletionPopups()
	p := m.activeCompletionPopup()
	if p == nil || !p.isVisible() {
		t.Fatal("expected value completion popup")
	}
	texts := ""
	for _, it := range p.matches() {
		texts += it.Text + " "
	}
	if !strings.Contains(texts, "on") || !strings.Contains(texts, "off") {
		t.Errorf("toggle values not offered, got %q", texts)
	}
}
```

Implementation in `updateCompletionPopups` (model.go): when the input value matches `/set` + space, feed a dedicated `m.setPopup *completionPopup`:

```go
if rest, ok := strings.CutPrefix(m.input.Value(), "/set "); ok {
	fieldsDone := strings.Count(rest, " ")
	reg := m.settingsRegistry()
	var items []completionItem
	var query string
	if fieldsDone == 0 { // completing the key
		query = rest
		for _, k := range reg.Keys() {
			_, cur, _, _ := reg.Describe(k)
			items = append(items, completionItem{Text: k, Detail: cur, Kind: completionCommand})
		}
	} else { // completing the value for toggle/enum keys
		key := strings.Fields(rest)[0]
		query = strings.TrimSpace(strings.TrimPrefix(rest, key))
		if _, _, options, err := reg.Describe(key); err == nil && len(options) > 0 {
			for _, o := range options {
				items = append(items, completionItem{Text: o, Kind: completionCommand})
			}
		}
	}
	m.setPopup = newCompletionPopup(items)
	m.setPopup.update(query)
} else {
	m.setPopup = nil
}
```

Fit this to the file's actual idioms: `completionItem` field names (`Detail` may not exist — check the struct; if not, drop the current-value detail), the `completionKind` enum values, `activeCompletionPopup()` must return `m.setPopup` when non-nil and visible, and accept-behavior must replace the current token only (mirror how the `@file` popup inserts). Keep the popup ordering: `setPopup` takes precedence over `cmdPopup` when input starts with `/set `.

- [ ] **Step 7: Run all TUI tests, commit**

Run: `go test ./internal/app/tui/...`
Expected: PASS

```bash
git add internal/app/tui/ internal/commands/
git commit -m "feat(tui): key and value completion for /set"
```

---

### Task 5: Docked settings browser; delete the full-screen settings view

**Files:**
- Create: `internal/app/tui/settings/browser.go`
- Test: `internal/app/tui/settings/browser_test.go`
- Modify: `internal/app/tui/model.go` (`/settings` + `/set` no-arg open the browser; handle `settings.ChangedMsg`; delete `settingsOpen`/`settingsModel`/`syncSettingsSaveBlock`)
- Modify: `internal/app/tui/view.go` (delete the `settingsOpen` branches at ~59 and ~325)
- Delete: `internal/app/tui/settings/model.go`, `chrome.go`, `help.go`, `search.go`, `sections.go`'s pane wiring if it lives in model.go (keep `sectionList()` itself), `reset.go` **only if** its confirm idiom is unused by `fieldlist.go` (check first), plus their tests. Keep: `field.go`, `fieldlist.go`, `panestack.go`, `setters.go`, `validation.go`, `masked.go`, `configdiff.go`, `state.go`, all `frames_*.go`, `registry.go`.

**Interfaces:**
- Consumes: `dock.Panel` (Task 1), `Registry` (Task 3), `applyNewConfig` (Task 4), existing `paneStack`, `fieldList`, `chrome.Panel`, `chrome.ClipLines`, `configDiff`.
- Produces:
  - `settings.NewBrowser(cfg config.Config, cfgPath, query string) *BrowserPanel` implementing `dock.Panel`
  - `settings.ChangedMsg{ Receipts []string; Cfg config.Config; SaveErr error }`
  - `settings.BrowserClosedMsg{}`
  - `Model.openSettingsBrowser(query string)`

- [ ] **Step 1: Write the failing panel tests**

```go
// internal/app/tui/settings/browser_test.go
package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func key(s string) tea.Msg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} } // match the constructor used in existing settings tests

func TestBrowserFiltersAndRendersRows(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "shell")
	v := b.View(80, 12)
	if !strings.Contains(v, "shell.allow_network") {
		t.Fatalf("filtered view should list shell keys, got:\n%s", v)
	}
	if !strings.Contains(v, "Settings") {
		t.Error("panel title missing")
	}
}

func TestBrowserToggleSavesAndEmitsChangedMsg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	b := NewBrowser(config.Default(), path, "shell.allow_network")
	// Cursor starts on the first (only strong) match; Enter/space toggles.
	cmd := b.Update(key(" "))
	if cmd == nil {
		t.Fatal("mutating update must emit a command")
	}
	msg := cmd()
	ch, ok := msg.(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", msg)
	}
	if ch.SaveErr != nil {
		t.Fatalf("save failed: %v", ch.SaveErr)
	}
	if !ch.Cfg.Tools.Shell.AllowNetwork {
		t.Error("ChangedMsg.Cfg does not carry the change")
	}
	if len(ch.Receipts) == 0 || !strings.Contains(ch.Receipts[0], "allow_network") {
		t.Errorf("receipts missing, got %v", ch.Receipts)
	}
}

func TestBrowserEscEmitsClosed(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc at root must emit close")
	}
	if _, ok := cmd().(BrowserClosedMsg); !ok {
		t.Fatal("want BrowserClosedMsg")
	}
}
```

Copy the exact `tea.KeyPressMsg` construction idiom from `fieldlist_test.go` / `model_test.go` in the settings package — do not invent one.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/tui/settings/ -run TestBrowser`
Expected: FAIL — `NewBrowser` undefined.

- [ ] **Step 3: Implement `BrowserPanel`**

Structure (fit rendering details to `fieldlist.go`'s existing row renderer — reuse it rather than reimplementing):

```go
// internal/app/tui/settings/browser.go
package settings

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/theme"
)

// ChangedMsg reports settings changes that were applied and saved.
// Receipts are preformatted "key: old → new" lines for the transcript.
type ChangedMsg struct {
	Receipts []string
	Cfg      config.Config
	SaveErr  error
}

// BrowserClosedMsg is emitted when the user closes the browser at its root.
type BrowserClosedMsg struct{}

// BrowserPanel is the docked settings browser: a filter input over the
// flat registry (picker-style), with Enter drilling into collection
// frames via the existing paneStack machinery. Every mutation saves
// immediately and emits ChangedMsg.
type BrowserPanel struct {
	reg      *Registry
	cfgPath  string
	filter   textinput.Model
	list     *fieldList // rows = current registry matches (flat mode)
	stack    *paneStack // non-nil while drilled into a section
	baseline config.Config
}

func NewBrowser(cfg config.Config, cfgPath, query string) *BrowserPanel {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	ti.SetValue(query)
	b := &BrowserPanel{reg: BuildRegistry(cfg), cfgPath: cfgPath, filter: ti, baseline: cfg}
	b.list = newFieldList(b.matchedFields)
	return b
}

func (b *BrowserPanel) matchedFields() []*field {
	q := strings.TrimSpace(b.filter.Value())
	return b.reg.matchFields(q) // Task 3's field-returning matcher; empty q = all, in section order
}

func (b *BrowserPanel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	var cmd tea.Cmd
	switch {
	case b.stack != nil:
		// Drill mode: full fieldList idioms via paneStack; Esc pops,
		// Esc at stack root returns to flat mode.
		if k.Code == tea.KeyEsc && b.stack.atRoot() {
			b.stack = nil
		} else {
			cmd = b.stack.Update(msg)
		}
	case b.list.editing || b.list.picking || b.list.adding:
		// An inline editor is active on the flat list — it owns keys.
		cmd = b.list.Update(msg)
	case k.Code == tea.KeyEsc:
		return func() tea.Msg { return BrowserClosedMsg{} }
	case k.Code == tea.KeyUp || k.Code == tea.KeyDown ||
		k.Code == tea.KeyEnter || k.Text == " ":
		// Movement + activation go to the list; drill rows push a frame.
		cmd = b.list.Update(msg)
		if f := b.list.TakePushRequest(); f != nil {
			b.stack = newPaneStack(f)
		}
	default:
		// Everything else edits the filter, picker-style.
		b.filter, cmd = b.filter.Update(k)
		b.list.Refresh()
		b.list.SetCursor(0)
	}
	return b.flushChanges(cmd)
}

// flushChanges diffs the working config against the last-saved baseline;
// on change it saves and emits ChangedMsg (batched with the inner cmd).
func (b *BrowserPanel) flushChanges(inner tea.Cmd) tea.Cmd {
	lines := configDiff(b.baseline, b.reg.Config())
	if len(lines) == 0 {
		return inner
	}
	receipts := make([]string, len(lines))
	for i, l := range lines {
		receipts[i] = l.String() // use configdiff's actual line-formatting; add a String() method if none exists
	}
	saveErr := config.SaveProjectConfig(b.cfgPath, b.reg.Config())
	b.baseline = b.reg.Config()
	changed := func() tea.Msg {
		return ChangedMsg{Receipts: receipts, Cfg: b.baseline, SaveErr: saveErr}
	}
	if inner == nil {
		return changed
	}
	return tea.Batch(inner, changed)
}

func (b *BrowserPanel) View(width, maxHeight int) string {
	pw := min(72, width-2)
	inner := pw - 2
	title := "Settings"
	var body string
	if b.stack != nil {
		title = "Settings › " + b.stack.breadcrumb(b.stack.top().title)
		b.stack.SetSize(inner, maxHeight-4)
		body = b.stack.top().list.View() // reuse fieldList's renderer; check its real name/signature in fieldlist.go
	} else {
		b.list.SetSize(inner, maxHeight-4)
		body = "/ " + b.filter.View() + "\n" +
			strings.Repeat("─", inner) + "\n" + b.list.View()
	}
	footer := fmt.Sprintf("%d settings · ⏎ edit · esc close", len(b.list.Rows()))
	content := body + "\n" + footer
	ph := min(strings.Count(content, "\n")+3, maxHeight)
	return chrome.Panel(title, content, pw, ph, true, theme.Current())
}
```

Reconcile with reality as you implement: `fieldList`'s render method name and whether `editing/picking/adding` are directly readable from the same package (they are — same package), `configDiff`'s `diffLine` formatting (reuse whatever the old diff overlay used to stringify lines; extract it into a method on `diffLine` if it was inline in deleted code), and `matchFields` per Task 4's note. Style the footer/separator with the package's existing muted styles.

- [ ] **Step 4: Run panel tests**

Run: `go test ./internal/app/tui/settings/ -run TestBrowser`
Expected: PASS. Commit: `feat(settings): docked browser panel with immediate save`.

- [ ] **Step 5: Wire into the model and delete the full-screen view**

In `model.go`:

```go
func (m *Model) openSettingsBrowser(query string) {
	m.dock.Open(settings.NewBrowser(m.state.Config, projectConfigPath(m.state.WorkingDir), query))
}
```

- `dispatchCommand` `case "settings":` body becomes `m.openSettingsBrowser(strings.Join(args, " ")); m.refreshViewport(); return m, nil`.
- Task 4's `case 0` fallback in `handleSetCommand` becomes `m.openSettingsBrowser("")`; the no-exact-match branch of `case 1` becomes `m.openSettingsBrowser(key)` (replacing the suggestions print).
- Add message handlers in `Update`:

```go
case settings.ChangedMsg:
	m.applyNewConfig(msg.Cfg)
	for _, r := range msg.Receipts {
		m.state.AddMessage(session.RoleSystem, "✓ "+r+" · "+relPath(m.state.WorkingDir, projectConfigPath(m.state.WorkingDir)), session.ContentTypePlain)
	}
	if msg.SaveErr != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ save failed: %v", msg.SaveErr), session.ContentTypePlain)
	}
	m.refreshViewport()
	return m, nil

case settings.BrowserClosedMsg:
	m.dock.CloseNow()
	return m, nil
```

- Delete: `settingsOpen`, `settingsModel` fields; the `settingsOpen` routing block (~492); `syncSettingsSaveBlock`; `settings.SavedMsg` handler (its body already lives in `applyNewConfig`); view.go branches at ~59 and ~325; `m.settingsModel.SetSize` in the resize handler (~446).
- Delete files listed in the header **after** confirming no survivor references them (`go build ./...` is the arbiter). Move any helper the browser still needs (e.g. `diffLine` stringification, styles) into surviving files first.

- [ ] **Step 6: Full test pass and commit**

Run: `gofmt -w . && go vet ./... && go test ./internal/...`
Expected: PASS — update `view_test.go`/`model_test.go` tests that referenced the full-screen settings view to assert the docked browser instead (keep the coverage: open, edit, receipt, close).

```bash
git add -A internal/ && git commit -m "feat(tui): docked settings browser replaces full-screen settings view"
```

---

### Task 6: Help becomes a transcript print

**Files:**
- Modify: `internal/commands/commands.go` (`/help` handler: styled cheatsheet)
- Modify: `internal/app/tui/model.go` (`?` on empty input → `/help`; delete `helpOpen` field + guard ~594-597 + open site ~669-675)
- Modify: `internal/app/tui/view.go` (delete `helpOpen` branch ~65-71)
- Modify: `internal/app/tui/help/help.go` (delete `Overlay` + `OverlayHints`; keep `Footer`, `FooterHints`, `Rows`)
- Test: `internal/commands/commands_test.go`, `internal/app/tui/help/help_test.go`, `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: existing `Registry.List()` in the commands package; `dispatchCommand`.
- Produces: `/help [command]` transcript output. No new exported symbols.

- [ ] **Step 1: Write the failing handler test**

In `internal/commands/commands_test.go`:

```go
func TestHelpPrintsCheatsheet(t *testing.T) {
	r := DefaultRegistry() // or however commands_test.go builds the full registry today
	cmd, _ := r.Lookup("help")
	out := cmd.Handler(nil, nil)
	for _, want := range []string{"Keys", "Commands", "/set", "/model", "⏎ send", "esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("cheatsheet missing %q", want)
		}
	}
}

func TestHelpForSingleCommand(t *testing.T) {
	r := DefaultRegistry()
	cmd, _ := r.Lookup("help")
	out := cmd.Handler(nil, []string{"set"})
	if !strings.Contains(out, "/set") || !strings.Contains(out, "<key> [value]") {
		t.Errorf("per-command help incomplete: %q", out)
	}
}
```

- [ ] **Step 2: Implement the handler**

Rework the existing `/help` handler in `commands.go`:

```go
Handler: func(state *session.State, args []string) string {
	if len(args) > 0 {
		c, ok := reg.Lookup(args[0]) // reg is the enclosing registry; match the file's existing closure pattern
		if !ok {
			return fmt.Sprintf("Unknown command: /%s", args[0])
		}
		out := "/" + c.Name
		if c.Args != "" {
			out += " " + c.Args
		}
		return out + "\n  " + c.Description
	}
	var b strings.Builder
	b.WriteString("Keys\n")
	b.WriteString("  ⏎ send · esc stop · tab/shift+tab mode · ctrl+p connect\n")
	b.WriteString("  pgup/pgdn scroll · ctrl+u/ctrl+d half-page · end bottom\n")
	b.WriteString("Commands\n")
	for _, c := range reg.List() { // List already sorts and hides Hidden
		line := "  /" + c.Name
		if c.Args != "" {
			line += " " + c.Args
		}
		b.WriteString(fmt.Sprintf("%-28s %s\n", line, c.Description))
	}
	return strings.TrimRight(b.String(), "\n")
},
```

Verify the key list against `help.Footer`/the current overlay content so it states real bindings, then delete stale ones. Existing `/help` handler code is replaced, not appended to.

- [ ] **Step 3: `?` triggers `/help`; delete the overlay**

In `model.go`, the `?`-key handling (~line 669-675) currently sets `m.helpOpen = true`. Replace with: when input is empty, `return m.dispatchCommand("/help")`; otherwise fall through so `?` types normally. Delete the `helpOpen` guard (~594-597), the field (~142), and the view branch (view.go ~65-71). In `help/help.go` delete `Overlay` and `OverlayHints`; update `help_test.go` accordingly (Footer tests stay).

- [ ] **Step 4: Run, fix, commit**

Run: `go test ./internal/... && go vet ./...`
Expected: PASS.

```bash
git add -A internal/ && git commit -m "feat(tui): /help prints cheatsheet to transcript; remove full-screen overlay"
```

---

### Task 7: Connect in the dock

**Files:**
- Modify: `internal/app/tui/connect/connect.go` (add `Panel` adapter)
- Modify: `internal/app/tui/model.go` (open sites, routing, delete `connectOpen`)
- Modify: `internal/app/tui/view.go` (delete the `connectOpen` branch ~56-58)
- Test: `internal/app/tui/connect/connect_test.go`, `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: `dock.Host`; existing `connect.DoneMsg`/`connect.CancelledMsg`/`probe.ResultMsg`/`connect.TickMsg` handling in model.go.
- Produces: `connect.Panel` (wraps `*connect.Model`, satisfies `dock.Panel`).

- [ ] **Step 1: Add the adapter (test first)**

```go
// in connect_test.go
func TestPanelSatisfiesDock(t *testing.T) {
	var _ dock.Panel = Panel{}
}
```

```go
// in connect.go
// Panel adapts Model to the dock.Panel interface: Model.Update returns
// (*Model, tea.Cmd) for historical reasons but mutates in place.
type Panel struct{ *Model }

func (p Panel) Update(msg tea.Msg) tea.Cmd {
	_, cmd := p.Model.Update(msg)
	return cmd
}
```

(`View(maxW, maxH int) string` is already on `*Model` and promotes through embedding.)

- [ ] **Step 2: Rehost in the model**

In `model.go`, everywhere `m.connectOpen = true` is set (the `/connect`, `/models`, Ctrl+P sites — grep `connectOpen`): keep `m.connectModel = connect.New(...)` (typed access for `DoneMsg` handling stays) and add `m.dock.Open(connect.Panel{m.connectModel})`. On `connect.DoneMsg`/`connect.CancelledMsg`: replace `m.connectOpen = false` with `m.dock.CloseNow()`.

**Message routing:** connect consumes non-key messages (`probe.ResultMsg`, `connect.TickMsg`). Find the current `connectOpen` routing block (near the `settingsOpen` one, ~492-507) and preserve its message set, but deliver through `m.dock.Update(msg)`. The generic key routing from Task 2 stays; this block additionally forwards the connect-specific non-key messages while the dock panel is the connect panel (`if _, ok := m.dock.Panel().(connect.Panel); ok`).

Delete `connectOpen` field and the view.go branch.

- [ ] **Step 3: Run, fix smoke tests, commit**

Run: `go test ./internal/app/tui/...`
Expected: PASS — update any test asserting centered connect placement.

```bash
git add -A internal/ && git commit -m "feat(tui): connect flow runs in the dock instead of full-screen"
```

---

### Task 8: Memory — docked browser, transcript detail prints

**Files:**
- Create: `internal/app/tui/memory/panel.go`
- Test: `internal/app/tui/memory/panel_test.go`
- Modify: `internal/app/tui/memory/messages.go` (add `ShowMsg`, `DeletedMsg`; `ClosedMsg` stays)
- Modify: `internal/app/tui/model.go` (`/memory` opens the panel; handle new msgs; delete `memoryOpen`/`memoryModel`)
- Modify: `internal/app/tui/view.go` (delete `memoryOpen` branch ~62-64)
- Delete: `internal/app/tui/memory/view.go` and the full-screen parts of `model.go` — keep whatever loads entries from `db` (extract into `entries.go` if load logic is tangled with the screen model).

**Interfaces:**
- Consumes: `dock.Panel`; the existing entry-loading query used by `memory.New(database *db.DB, projectID int64)` (read `memory/model.go` for the exact db call and entry struct).
- Produces:
  - `memory.NewPanel(database *db.DB, projectID int64) *BrowserPanel` implementing `dock.Panel`
  - `memory.ShowMsg{ ID int64 }` — user picked an entry; model prints its detail
  - `memory.DeletedMsg{ Title string; Err error }` — after `d`-confirm delete
  - `memory.ClosedMsg{}` (existing) — Esc

- [ ] **Step 1: Write the failing panel test**

Mirror how `memory/model_test.go` builds its fake/fixture DB — reuse its helpers:

```go
func TestMemoryPanelListsAndPicks(t *testing.T) {
	database, projectID := newTestDB(t) // reuse the fixture helper from model_test.go
	p := NewPanel(database, projectID)
	v := p.View(80, 12)
	if !strings.Contains(v, "Memory") {
		t.Fatalf("panel title missing:\n%s", v)
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on an entry must emit ShowMsg")
	}
	if _, ok := cmd().(ShowMsg); !ok {
		t.Fatalf("want ShowMsg, got %T", cmd())
	}
}

func TestMemoryPanelDeleteNeedsConfirm(t *testing.T) {
	database, projectID := newTestDB(t)
	p := NewPanel(database, projectID)
	if cmd := p.Update(keyRune('d')); cmd != nil { // first d arms
		t.Fatal("first d must only arm the confirm")
	}
	cmd := p.Update(keyRune('d')) // second d deletes
	if cmd == nil {
		t.Fatal("second d must delete and emit DeletedMsg")
	}
	if _, ok := cmd().(DeletedMsg); !ok {
		t.Fatalf("want DeletedMsg, got %T", cmd())
	}
}
```

(`keyRune` = the package's existing key-construction idiom; if the fixture helper has a different name, use it.)

- [ ] **Step 2: Implement the panel**

`panel.go` follows the picker's structure (filter input, fuzzy matches, cursor, `chrome.Panel` chrome) over the entry list the old screen loaded, with three behaviors: Enter emits `ShowMsg{ID}`; `d` twice (armed flag resets on any other key or cursor move) deletes via the same db call the old screen used and emits `DeletedMsg`; Esc emits `ClosedMsg`. Row format: `title` left, `age · kind` right in muted style (reuse the formatting from the deleted `view.go` where it fits a single row). Filter uses `fuzzy.Rank` over title + kind, matching `picker.refilter`. Copy `picker.go`'s `View` windowing (`chrome.ClipLines`, footer line `⏎ show · d delete · esc close`).

- [ ] **Step 3: Wire the model**

`dispatchCommand` `case "memory":` — keep the nil-db guard message, then `m.dock.Open(memory.NewPanel(m.memoryDB, m.memoryProject)); m.refreshViewport(); return m, nil`. Add handlers:

```go
case memory.ShowMsg:
	m.dock.CloseNow()
	// Load the entry via the same db accessor the panel uses and print it.
	text := memory.RenderEntry(m.memoryDB, msg.ID) // implement in memory package: title, metadata line, body
	m.state.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
	m.refreshViewport()
	return m, nil

case memory.DeletedMsg:
	if msg.Err != nil {
		m.state.AddMessage(session.RoleSystem, "✗ delete failed: "+msg.Err.Error(), session.ContentTypePlain)
	} else {
		m.state.AddMessage(session.RoleSystem, "✓ deleted memory: "+msg.Title, session.ContentTypePlain)
	}
	m.refreshViewport()
	return m, nil

case memory.ClosedMsg:
	m.dock.CloseNow()
	return m, nil
```

`memory.RenderEntry(database *db.DB, id int64) string` is part of this task: fetch the entry, format `title`, a muted `kind · confidence · age` line, then the body. Delete `memoryOpen`, `memoryModel`, the view.go branch, the resize `SetSize` call, and the full-screen files.

- [ ] **Step 4: Run, commit**

Run: `go test ./internal/... && go vet ./...`
Expected: PASS.

```bash
git add -A internal/ && git commit -m "feat(tui): memory browser docks above input; entries print to transcript"
```

---

### Task 9: Cleanup — retire `chrome.Overlay`, `pickerModel`, and full-screen remnants

**Files:**
- Modify: `internal/app/tui/model.go` (delete `pickerModel` field — use `m.dock.Panel().(*picker.Model)` type assertions where typed access is needed; `pickerCommand` stays)
- Modify: `internal/app/tui/chrome/chrome.go` + `chrome_test.go` (delete `Overlay` and its test if no callers remain — verify with `grep -rn "chrome.Overlay" internal/`)
- Modify: `internal/app/tui/view_test.go`, `internal/app/tui/settings/nocolor_test.go`

**Interfaces:**
- Consumes: everything previously landed.
- Produces: final state — no full-screen takeovers, one dock slot.

- [ ] **Step 1: Delete `pickerModel`**

Replace remaining `m.pickerModel` reads with dock access:

```go
if p, ok := m.dock.Panel().(*picker.Model); ok {
	// ...
}
```

Sites: the `PickedMsg`/`CancelledMsg` handling only needs `pickerCommand` (keep), the `SetFilter` path in `openPicker` keeps its local variable. Remove the field and any assignment.

- [ ] **Step 2: Delete `chrome.Overlay`**

Run `grep -rn "chrome.Overlay" internal/` — must return nothing outside chrome itself. Delete the function and its test case.

- [ ] **Step 3: Final assertions**

Add to `view_test.go`:

```go
// No full-screen takeover survives: with a dock panel open, the title
// bar, transcript, input, footer, and status line are all still present.
func TestNoFullScreenTakeovers(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.openSettingsBrowser("")
	out := stripANSI(m.viewString())
	for _, want := range []string{"marshal", "Settings"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame missing %q while dock open", want)
		}
	}
	if got := strings.Count(out, "\n") + 1; got != 40 {
		t.Errorf("frame height %d, want 40", got)
	}
}
```

Also verify the NO_COLOR path: extend `settings/nocolor_test.go` (or its pattern) to render `BrowserPanel.View` under NO_COLOR and assert no escape sequences leak.

- [ ] **Step 4: Full verification**

```bash
gofmt -w . && go vet ./... && go test ./internal/...
```

Expected: all PASS. Then run the app manually (`go run ./cmd/marshal` in a scratch repo) and walk: `/set shell.allow_network on` → receipt; `/settings` → browse/edit/esc; `/help` → cheatsheet; Ctrl+P → connect in dock; `/memory` → list/show; `/model` → picker docked. Resize to 80×24 and repeat `/settings`.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "refactor(tui): remove chrome.Overlay and last full-screen view remnants"
```

---

## Plan self-review notes (already applied)

- **Spec coverage:** dock host (T1), picker rehost + placement (T2), registry (T3), `/set` + receipts + completion (T4), docked browser + immediate save + full-screen deletion (T5), help (T6), connect (T7), memory (T8), cleanup + `chrome.Overlay` removal + NO_COLOR/80×24 checks (T9). Save-failure-keeps-in-memory rule: T4 Step 4 and T5 `ChangedMsg.SaveErr`. Non-goals (global config, collection `/set` paths, palette) are excluded throughout.
- **Known reconciliation points** (flagged inline, not placeholders — the exact names exist in the repo and must be read, not guessed): `tea.KeyPressMsg` construction idiom, `fieldList`'s render method name, `completionItem` field names, `configDiff` line stringification, memory test fixture helper, `maskKey` signature.
- **Type consistency:** `dock.Host` methods used in T2/T5/T7/T8 match T1's definitions; `Registry` methods used in T4/T5 match T3 (with the `MatchKeys`/`matchFields` split decided in T4 Step 4).
