# Provider Wizard and Settings Expansion — Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add settings polish — duplicate + reorder collection entries, per-section reset-to-defaults, and a save-time diff preview overlay — all reusing the `kindAction` primitive and overlay machinery introduced in Phase 1.

**Architecture:** Three features, one shared primitive. Duplicate/reorder add `yank`/`paste`/`moveUp`/`moveDown` closures to the existing `field` and new `y`/`p`/`shift+↑`/`shift+↓` keys to `fieldList`. Reset-to-defaults appends a confirm-gated `kindAction` row to every section root frame via a wrapper in `New`. Save-diff replaces the immediate `Ctrl+S` save with an `overlayDiff` that computes a reflect-based config diff (secrets masked) and commits on `Enter`.

**Tech Stack:** Go 1.26, Bubble Tea v2, Lipgloss v2, `reflect` for the diff.

**Spec:** `docs/superpowers/specs/2026-07-12-provider-wizard-and-settings-expansion-design.md`, Sections 6–8 (Phase 2).

**Assumes Phase 1 is complete:** `kindAction`, `kindPicker`, `overlayPicker`, `state.actionState`, `state.applyActionResult`, `Model.openPicker/closePicker`, `pickerRequest`, `truncateErr`, and the `overlayKind` enum exist.

## Global Constraints

- Module path is `marshal` (see `go.mod`).
- `go test ./...` and `go vet ./...` must pass after every task.
- `go build ./cmd/marshal` must succeed (requires `CGO_ENABLED=1`).
- No new external dependencies.
- No comments in code unless explicitly requested by the plan.
- The settings overlay mutates only a working copy; nothing reaches the filesystem until `Ctrl+S` (Phase 2 inserts the diff overlay between `Ctrl+S` and the save).
- Secrets (`api_key`/`search_key` fields) are never rendered in plaintext; use the existing `maskKey` helper.

## File Structure

**Create:**
- `internal/app/tui/settings/configdiff.go` — reflect-based `configDiff` + masking.
- `internal/app/tui/settings/configdiff_test.go`
- `internal/app/tui/settings/reset.go` — `resetSection`, `resetField`, `withResetRow`.

**Modify:**
- `internal/app/tui/settings/field.go` — add `yank`/`paste`/`moveUp`/`moveDown`/`disarm` closures.
- `internal/app/tui/settings/fieldlist.go` — `y`/`p`/`shift+up`/`shift+down` keys; yanked state; `disarmRow`/`DisarmCurrent`.
- `internal/app/tui/settings/panestack.go` — `entriesDrillExt`/`listDrillExt` with opts; `entriesDrill`/`listDrill` delegate.
- `internal/app/tui/settings/model.go` — `overlayDiff`; `openDiff`/`updateDiffOverlay`/`diffOverlay`; `Ctrl+S` change; `New` wraps roots with reset row; Esc disarms.
- `internal/app/tui/settings/frames_collections.go` — providers/presets yank/paste; hooks/permissions moveUp/moveDown/yank/paste.
- `internal/app/tui/settings/frames_basic.go` — languages/indexing reorder + duplicate.
- `internal/app/tui/settings/help.go` — new keys + save-diff note.
- `internal/app/tui/model_test.go` — update existing tests for footer/key changes.

---

## Task 1: Duplicate + reorder closures and fieldList keys

Adds the closures to `field`, the `y`/`p`/`shift+up`/`shift+down` keys to `fieldList`, the yank buffer, and the disarm mechanism (used by Task 3's reset confirm). No frame wiring yet — that's Task 5.

**Files:**
- Modify: `internal/app/tui/settings/field.go`
- Modify: `internal/app/tui/settings/fieldlist.go`
- Test: `internal/app/tui/settings/fieldlist_test.go`

**Interfaces:**
- Produces: `field.yank func() any`, `field.paste func(any) error`, `field.moveUp func()`, `field.moveDown func()`, `field.disarm func()`; `fieldList.yankedID`/`yankedData`; `fieldList.DisarmCurrent()`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/fieldlist_test.go`:

```go
func TestYankPasteDuplicates(t *testing.T) {
	got := ""
	fl := newFieldList(func() []*field {
		return []*field{
			{id: "x.a", title: "A", kind: kindScalar,
				getStr: func() string { return "a" },
				setStr: func(v string) error { got = v; return nil },
				yank:   func() any { return "a-data" },
				paste:  func(data any) error { got = data.(string) + "-pasted"; return nil },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	fl.Update(tea.KeyPressMsg{Text: "y"})
	if fl.yankedData != "a-data" {
		t.Fatalf("yank: yankedData = %v, want a-data", fl.yankedData)
	}
	fl.Update(tea.KeyPressMsg{Text: "p"})
	if got != "a-data-pasted" {
		t.Fatalf("paste: got = %q, want a-data-pasted", got)
	}
	if fl.yankedData != nil {
		t.Fatal("paste should clear the yank buffer")
	}
}

func TestMoveUpDownCallsClosures(t *testing.T) {
	calls := []string{}
	fl := newFieldList(func() []*field {
		return []*field{
			{id: "x.a", title: "A", kind: kindScalar,
				getStr: func() string { return "a" }, setStr: func(string) error { return nil },
				moveUp:   func() { calls = append(calls, "up") },
				moveDown: func() { calls = append(calls, "down") },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	fl.Update(tea.KeyPressMsg{Text: "shift+up"})
	fl.Update(tea.KeyPressMsg{Text: "shift+down"})
	want := []string{"up", "down"}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDisarmCalledOnCursorMove(t *testing.T) {
	disarmed := false
	fl := newFieldList(func() []*field {
		return []*field{
			{id: "x.a", title: "A", kind: kindScalar,
				getStr: func() string { return "a" }, setStr: func(string) error { return nil },
				disarm: func() { disarmed = true },
			},
			{id: "x.b", title: "B", kind: kindScalar,
				getStr: func() string { return "b" }, setStr: func(string) error { return nil },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()
	fl.SetCursor(0)

	fl.Update(tea.KeyPressMsg{Text: "j"})
	if !disarmed {
		t.Fatal("leaving row A via j should call its disarm")
	}
}

func TestDisarmCurrent(t *testing.T) {
	disarmed := false
	fl := newFieldList(func() []*field {
		return []*field{
			{id: "x.a", title: "A", kind: kindScalar,
				getStr: func() string { return "a" }, setStr: func(string) error { return nil },
				disarm: func() { disarmed = true },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()
	fl.SetCursor(0)

	fl.DisarmCurrent()
	if !disarmed {
		t.Fatal("DisarmCurrent should call the cursor row's disarm")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestYankPaste|TestMoveUpDown|TestDisarm' -v`
Expected: FAIL with "unknown field 'yank'"/"unknown field 'disarm'"/"undefined: DisarmCurrent"

- [ ] **Step 3: Add closures to `field`**

In `internal/app/tui/settings/field.go`, append to the `field` struct (after the Phase 1 `pickPending` closure):

```go
	// collection ops (Phase 2)
	yank     func() any
	paste    func(any) error
	moveUp   func()
	moveDown func()
	// disarm is called by the fieldList when the cursor leaves this row
	// (used by the reset-to-defaults confirm idiom).
	disarm   func()
```

- [ ] **Step 4: Add yank buffer, keys, and disarm to `fieldList`**

In `internal/app/tui/settings/fieldlist.go`, add fields to the `fieldList` struct (after `addWizard`):

```go
	yankedID   string
	yankedData any
```

Add the `disarmRow`/`DisarmCurrent` helpers:

```go
func (fl *fieldList) disarmRow(i int) {
	fl.Refresh()
	if i >= 0 && i < len(fl.rows) && fl.rows[i].disarm != nil {
		fl.rows[i].disarm()
	}
}

func (fl *fieldList) DisarmCurrent() {
	fl.Refresh()
	fl.disarmRow(fl.cursor)
}
```

In the `Update` switch (the `switch k.String()` block, alongside the existing `case "d":`), add the new keys. Insert before `case "d":`:

```go
	case "y":
		if row != nil && row.yank != nil {
			fl.yankedID = row.id
			fl.yankedData = row.yank()
		}
		return nil
	case "p":
		if fl.yankedData != nil && row != nil && row.paste != nil {
			if err := row.paste(fl.yankedData); err != nil {
				fl.errMsg = err.Error()
			} else {
				fl.yankedID = ""
				fl.yankedData = nil
				fl.Refresh()
			}
		}
		return nil
	case "shift+up":
		if row != nil && row.moveUp != nil {
			row.moveUp()
			fl.Refresh()
		}
		return nil
	case "shift+down":
		if row != nil && row.moveDown != nil {
			row.moveDown()
			fl.Refresh()
		}
		return nil
```

Update the existing cursor-move cases to disarm the row being left. Change:

```go
	case "up", "k":
		if fl.cursor > 0 {
			fl.cursor--
		}
	case "down", "j":
		if fl.cursor < len(fl.rows)-1 {
			fl.cursor++
		}
	case "g":
		fl.cursor = 0
	case "G":
		fl.cursor = len(fl.rows) - 1
```

to:

```go
	case "up", "k":
		if fl.cursor > 0 {
			fl.disarmRow(fl.cursor)
			fl.cursor--
		}
	case "down", "j":
		if fl.cursor < len(fl.rows)-1 {
			fl.disarmRow(fl.cursor)
			fl.cursor++
		}
	case "g":
		fl.disarmRow(fl.cursor)
		fl.cursor = 0
	case "G":
		fl.disarmRow(fl.cursor)
		fl.cursor = len(fl.rows) - 1
```

- [ ] **Step 5: Add footer hints**

In `internal/app/tui/settings/model.go`, the `renderFooter` default case builds hints from `row.kind`. After the existing `case kindDrill:` block (which appends `[↵] open` and `[d] delete`), append duplicate/reorder hints keyed off the new closures, not the kind — so they appear for any row that supports them. After the `row.kind` switch and before the `if fl.onAdd != nil` check, add:

```go
		if row.yank != nil {
			parts = append(parts, seg("y", "yank"))
		}
		if row.paste != nil && fl.yankedData != nil {
			parts = append(parts, seg("p", "paste"))
		}
		if row.moveUp != nil || row.moveDown != nil {
			parts = append(parts, seg("shift↑↓", "move"))
		}
```

(Place this inside the `if row := fl.CursorRow(); row != nil { ... }` block, after the `switch row.kind`.)

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestYankPaste|TestMoveUpDown|TestDisarm' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/settings/field.go internal/app/tui/settings/fieldlist.go internal/app/tui/settings/model.go internal/app/tui/settings/fieldlist_test.go
git commit -m "feat: add duplicate/reorder closures and y/p/shift keys to settings fieldList"
```

---

## Task 2: Config diff helper

A reflect-based recursive diff over `config.Config` producing human-readable `+`/`-`/`~` lines, with secret fields (`APIKey`, `SearchKey`) masked via `maskKey`.

**Files:**
- Create: `internal/app/tui/settings/configdiff.go`
- Test: `internal/app/tui/settings/configdiff_test.go`

**Interfaces:**
- Produces: `diffLine{Prefix, Path, Detail}`, `configDiff(before, after config.Config) []diffLine`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/settings/configdiff_test.go`:

```go
package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func findDiff(lines []diffLine, path string) (diffLine, bool) {
	for _, l := range lines {
		if l.Path == path {
			return l, true
		}
	}
	return diffLine{}, false
}

func TestConfigDiffNoChanges(t *testing.T) {
	cfg := config.Default()
	lines := configDiff(cfg, cfg)
	if len(lines) != 0 {
		t.Fatalf("expected no diff lines, got %d: %v", len(lines), lines)
	}
}

func TestConfigDiffScalarChange(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Privacy.RemoteProvidersAllowed = true

	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Privacy.RemoteProvidersAllowed")
	if !ok {
		t.Fatalf("missing Privacy.RemoteProvidersAllowed in %v", lines)
	}
	if l.Prefix != "~" {
		t.Fatalf("prefix = %q, want ~", l.Prefix)
	}
	if !strings.Contains(l.Detail, "false") || !strings.Contains(l.Detail, "true") {
		t.Fatalf("detail = %q, want false → true", l.Detail)
	}
}

func TestConfigDiffAddedProvider(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}

	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Providers.ollama")
	if !ok {
		t.Fatalf("missing Providers.ollama in %v", lines)
	}
	if l.Prefix != "+" {
		t.Fatalf("prefix = %q, want +", l.Prefix)
	}
}

func TestConfigDiffRemovedPreset(t *testing.T) {
	before := config.Default()
	before.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}
	after := config.Default()

	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Models.Presets.coder")
	if !ok {
		t.Fatalf("missing Models.Presets.coder in %v", lines)
	}
	if l.Prefix != "-" {
		t.Fatalf("prefix = %q, want -", l.Prefix)
	}
}

func TestConfigDiffMasksAPIKey(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "sk-supersecret-1234"},
	}

	lines := configDiff(before, after)
	for _, l := range lines {
		if strings.Contains(l.Path, "APIKey") {
			if strings.Contains(l.Detail, "supersecret") {
				t.Fatalf("APIKey diff leaked plaintext: %q", l.Detail)
			}
			if !strings.Contains(l.Detail, "••••") {
				t.Fatalf("APIKey diff not masked: %q", l.Detail)
			}
		}
	}
}

func TestConfigDiffSliceChange(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Indexing.Ignore = append([]string{"newpattern/**"}, before.Indexing.Ignore...)

	lines := configDiff(before, after)
	found := false
	for _, l := range lines {
		if l.Prefix == "+" && strings.Contains(l.Path, "Indexing.Ignore") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an added Indexing.Ignore item in %v", lines)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run TestConfigDiff -v`
Expected: FAIL with "undefined: configDiff"

- [ ] **Step 3: Write minimal implementation**

Create `internal/app/tui/settings/configdiff.go`:

```go
package settings

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"marshal/internal/app/config"
)

type diffLine struct {
	Prefix string
	Path   string
	Detail string
}

var secretFieldNames = map[string]bool{"APIKey": true, "SearchKey": true}

func configDiff(before, after config.Config) []diffLine {
	var lines []diffLine
	diffValue("", reflect.ValueOf(before), reflect.ValueOf(after), &lines)
	return lines
}

func diffValue(path string, b, a reflect.Value, lines *[]diffLine) {
	b = deref(b)
	a = deref(a)
	if !b.IsValid() && !a.IsValid() {
		return
	}
	if !b.IsValid() {
		*lines = append(*lines, diffLine{Prefix: "+", Path: path, Detail: ": " + fmtScalar(path, a)})
		return
	}
	if !a.IsValid() {
		*lines = append(*lines, diffLine{Prefix: "-", Path: path, Detail: ": " + fmtScalar(path, b)})
		return
	}
	if b.Kind() != a.Kind() {
		*lines = append(*lines, diffLine{Prefix: "~", Path: path, Detail: ": " + fmtScalar(path, b) + " → " + fmtScalar(path, a)})
		return
	}
	switch b.Kind() {
	case reflect.Struct:
		t := b.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			diffValue(joinPath(path, f.Name), b.Field(i), a.Field(i), lines)
		}
	case reflect.Map:
		diffMap(path, b, a, lines)
	case reflect.Slice:
		diffSlice(path, b, a, lines)
	default:
		if !reflect.DeepEqual(b.Interface(), a.Interface()) {
			*lines = append(*lines, diffLine{Prefix: "~", Path: path, Detail: ": " + fmtScalar(path, b) + " → " + fmtScalar(path, a)})
		}
	}
}

func diffMap(path string, b, a reflect.Value, lines *[]diffLine) {
	seen := map[string]bool{}
	for _, k := range b.MapKeys() {
		seen[k.String()] = true
	}
	for _, k := range a.MapKeys() {
		seen[k.String()] = true
	}
	sorted := make([]string, 0, len(seen))
	for k := range seen {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		bv := b.MapIndex(reflect.ValueOf(k))
		av := a.MapIndex(reflect.ValueOf(k))
		diffValue(joinPath(path, k), bv, av, lines)
	}
}

func diffSlice(path string, b, a reflect.Value, lines *[]diffLine) {
	n := b.Len()
	if a.Len() > n {
		n = a.Len()
	}
	for i := 0; i < n; i++ {
		fp := fmt.Sprintf("%s[%d]", path, i)
		var bv, av reflect.Value
		if i < b.Len() {
			bv = b.Index(i)
		}
		if i < a.Len() {
			av = a.Index(i)
		}
		diffValue(fp, bv, av, lines)
	}
}

func deref(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func joinPath(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "." + seg
}

func fmtScalar(path string, v reflect.Value) string {
	s := fmt.Sprintf("%v", v.Interface())
	if isSecretPath(path) {
		return maskKey(s)
	}
	return s
}

func isSecretPath(path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}
	return secretFieldNames[parts[len(parts)-1]]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run TestConfigDiff -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/configdiff.go internal/app/tui/settings/configdiff_test.go
git commit -m "feat: add reflect-based config diff helper with secret masking"
```

---

## Task 3: Reset to defaults

Appends a confirm-gated "Reset <section> to defaults" `kindAction` row to every section root frame. First Enter arms ("again to confirm"); a second Enter applies `resetSection`; moving off the row or Esc disarms via the `disarm` closure from Task 1.

**Files:**
- Create: `internal/app/tui/settings/reset.go`
- Modify: `internal/app/tui/settings/model.go` — `New` wraps each root; Esc path disarms.

**Interfaces:**
- Produces: `resetSection(cfg *config.Config, sectionID string)`, `resetField(s *state, sectionID, title string) *field`, `withResetRow(s *state, sectionID, title string, f *frame) *frame`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/settings/reset_test.go`:

```go
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestResetSectionProviders(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	resetSection(&cfg, "providers")
	if cfg.Providers != nil {
		t.Fatalf("providers = %v, want nil", cfg.Providers)
	}
}

func TestResetSectionShell(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.AllowNetwork = true
	cfg.Tools.Shell.AutoApprove = true
	resetSection(&cfg, "shell")
	def := config.Default()
	if cfg.Tools.Shell.AllowNetwork != def.Tools.Shell.AllowNetwork {
		t.Fatalf("AllowNetwork = %v, want %v", cfg.Tools.Shell.AllowNetwork, def.Tools.Shell.AllowNetwork)
	}
}

func TestResetFieldConfirmThenApply(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	f := resetField(st, "providers", "Providers")

	if label := f.actLabel(); label != "reset to defaults" {
		t.Fatalf("idle label = %q, want 'reset to defaults'", label)
	}

	_ = f.act()
	if label := f.actLabel(); label != "again to confirm" {
		t.Fatalf("armed label = %q, want 'again to confirm'", label)
	}

	_ = f.act()
	if label := f.actLabel(); label != "✓ reset" {
		t.Fatalf("applied label = %q, want '✓ reset'", label)
	}
	if st.cfg.Providers != nil {
		t.Fatal("reset should have cleared providers")
	}
}

func TestResetFieldDisarmClearsConfirm(t *testing.T) {
	st := newState(config.Default())
	f := resetField(st, "shell", "Shell")
	_ = f.act()
	if f.actLabel() != "again to confirm" {
		t.Fatal("should be armed")
	}
	if f.disarm != nil {
		f.disarm()
	}
	if f.actLabel() != "reset to defaults" {
		t.Fatalf("after disarm label = %q, want 'reset to defaults'", f.actLabel())
	}
}

func TestEverySectionRootHasResetRow(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	for i, sp := range m.specs {
		m.cursor = i
		m.paneFocused = true
		rows := m.activePane().top().list.Rows()
		found := false
		for _, r := range rows {
			if r.kind == kindAction && strings.HasPrefix(r.id, sp.id+".reset") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("section %q (%d) has no reset row", sp.id, i)
		}
	}
}
```

Add `"strings"` to the imports of `reset_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestReset|TestEverySection' -v`
Expected: FAIL with "undefined: resetSection"/"undefined: resetField"

- [ ] **Step 3: Write `reset.go`**

Create `internal/app/tui/settings/reset.go`:

```go
package settings

import (
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/trust"
)

func resetSection(cfg *config.Config, sectionID string) {
	def := config.Default()
	switch sectionID {
	case "agent":
		cfg.Agent = def.Agent
		cfg.Profile = def.Profile
	case "providers":
		cfg.Providers = nil
	case "presets":
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	case "privacy":
		cfg.Privacy = def.Privacy
	case "shell":
		cfg.Tools.Shell = def.Tools.Shell
	case "sandbox":
		cfg.Tools.Shell.Sandbox = def.Tools.Shell.Sandbox
	case "indexing":
		cfg.Indexing = def.Indexing
	case "web":
		cfg.Web = def.Web
	case "swarm":
		cfg.Swarm = def.Swarm
	case "mcp":
		cfg.MCP = def.MCP
	case "snapshots":
		cfg.Snapshots = def.Snapshots
	case "hooks":
		cfg.Hooks = def.Hooks
	case "permissions":
		cfg.Permissions = def.Permissions
	case "diagnostics":
		cfg.Diagnostics = def.Diagnostics
	case "commands":
		cfg.Commands = def.Commands
	}
	_ = trust.Resolver(nil)
}

func resetField(s *state, sectionID, title string) *field {
	id := sectionID + ".reset"
	return &field{
		id:    id,
		title: "Reset " + title + " to defaults",
		kind:  kindAction,
		desc:  "restore this section to built-in defaults (undoable until save)",
		actLabel: func() string {
			if as, ok := s.actionState[id]; ok && as.label != "" {
				return as.label
			}
			return "reset to defaults"
		},
		disarm: func() { delete(s.actionState, id) },
		act: func() tea.Cmd {
			if as, ok := s.actionState[id]; ok && as.label == "again to confirm" {
				resetSection(&s.cfg, sectionID)
				s.applyActionResult(id, "✓ reset")
				return nil
			}
			s.actionState[id] = actionState{pending: true, label: "again to confirm"}
			return nil
		},
	}
}

func withResetRow(s *state, sectionID, title string, f *frame) *frame {
	base := f.list.fields
	f.list.fields = func() []*field {
		return append(base(), resetField(s, sectionID, title))
	}
	return f
}
```

Note: the `_ = trust.Resolver(nil)` line and the `trust` import are not needed — remove them. The `resetSection` function only touches `config`. Drop the `trust` import and that line. (Included here as a reminder to keep imports minimal.)

Corrected `reset.go` imports:

```go
import (
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)
```

- [ ] **Step 4: Wrap each root frame in `New`**

In `internal/app/tui/settings/model.go`, the `New` function builds panes:

```go
	for i, sp := range specs {
		panes[i] = newPaneStack(sp.root(st))
	}
```

Change to:

```go
	for i, sp := range specs {
		panes[i] = newPaneStack(withResetRow(st, sp.id, sp.title, sp.root(st)))
	}
```

- [ ] **Step 5: Disarm on Esc**

In `internal/app/tui/settings/model.go`, the Esc branch (when not editing) pops the pane or closes. Before the `if m.activePane().pop()` call, disarm the current row so an armed reset confirm doesn't survive navigation:

```go
	if ks == "esc" {
		if editing {
			m.activePane().top().list.CancelEdit()
			return *m, nil
		}
		m.activePane().top().list.DisarmCurrent()
		if m.activePane().pop() {
			return *m, nil
		}
		if m.paneFocused && !m.sidebarHidden {
			m.paneFocused = false
			return *m, nil
		}
		return *m, m.requestClose()
	}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestReset|TestEverySection' -v`
Expected: PASS

- [ ] **Step 7: Run full settings tests**

Run: `go test ./internal/app/tui/settings/ -v`
Expected: all PASS (some existing tests that assert row counts per section may need updating in Task 6)

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/settings/reset.go internal/app/tui/settings/reset_test.go internal/app/tui/settings/model.go
git commit -m "feat: add per-section reset-to-defaults action row with confirm idiom"
```

---

## Task 4: Save diff preview overlay

`Ctrl+S` now opens an `overlayDiff` listing the structured diff (Task 2) instead of saving immediately. `Enter` commits via the existing `saveCmd`; `Esc` returns to editing. Empty diff shows "no changes".

**Files:**
- Modify: `internal/app/tui/settings/model.go` — `overlayDiff` constant, `diffLines` field, `openDiff`, `updateDiffOverlay`, `diffOverlay` View, `Ctrl+S` change, overlay routing.

**Interfaces:**
- Produces: `overlayDiff` overlay kind; `Model.openDiff() tea.Cmd`; diff overlay rendering.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/model_test.go`:

```go
func TestCtrlSOpensDiffOverlay(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	if m.overlay != overlayDiff {
		t.Fatalf("overlay = %v, want overlayDiff", m.overlay)
	}
}

func TestDiffOverlayShowsNoChangesWhenClean(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	view := m.View()
	if !strings.Contains(view, "no changes") {
		t.Fatalf("clean diff view should say 'no changes':\n%s", view)
	}
}

func TestDiffOverlayShowsChangeWhenDirty(t *testing.T) {
	cfg := config.Default()
	m := New(cfg, "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m.state.cfg.Privacy.RemoteProvidersAllowed = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	view := m.View()
	if !strings.Contains(view, "RemoteProvidersAllowed") {
		t.Fatalf("dirty diff view should mention the changed field:\n%s", view)
	}
}

func TestDiffOverlayEscReturnsToEditing(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	m, _ = m.Update(tea.KeyPressMsg{Text: "esc"})
	if m.overlay != overlayNone {
		t.Fatalf("overlay = %v, want overlayNone after Esc", m.overlay)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestCtrlSOpensDiff|TestDiffOverlay' -v`
Expected: FAIL with "undefined: overlayDiff" or `Ctrl+S` still saves

- [ ] **Step 3: Add the overlay kind and field**

In `internal/app/tui/settings/model.go`, add `overlayDiff` to the `overlayKind` iota (after `overlayPicker` from Phase 1):

```go
const (
	overlayNone overlayKind = iota
	overlaySearch
	overlayHelp
	overlayPicker
	overlayDiff
)
```

Add `diffLines []diffLine` to the `Model` struct (after the picker fields from Phase 1).

- [ ] **Step 4: Change `Ctrl+S` to open the diff**

In `Update`, the global keys switch currently has:

```go
	case "ctrl+s":
		return *m, m.saveCmd()
```

Change to:

```go
	case "ctrl+s":
		return *m, m.openDiff()
```

- [ ] **Step 5: Add overlay routing, open/save/cancel, and View**

Add `overlayDiff` routing with the other overlays. After the `overlayPicker` block (from Phase 1), add:

```go
	if m.overlay == overlayDiff {
		return m.updateDiffOverlay(k)
	}
```

Add the methods:

```go
func (m *Model) openDiff() tea.Cmd {
	m.diffLines = configDiff(m.state.snapshot, m.state.cfg)
	m.overlay = overlayDiff
	return nil
}

func (m *Model) updateDiffOverlay(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.overlay = overlayNone
		return *m, nil
	case "enter":
		if len(m.diffLines) == 0 {
			return *m, nil
		}
		m.overlay = overlayNone
		return *m, m.saveCmd()
	}
	return *m, nil
}
```

Add rendering in `View` (after the `overlayPicker` check):

```go
	if m.overlay == overlayDiff {
		return m.diffOverlay(fw, fh)
	}
```

Add the `diffOverlay` method:

```go
func (m Model) diffOverlay(fw, fh int) string {
	var b strings.Builder
	if len(m.diffLines) == 0 {
		b.WriteString(flDescStyle.Render("no changes"))
		b.WriteString("\n")
	} else {
		for _, d := range m.diffLines {
			line := d.Prefix + " " + d.Path + d.Detail
			switch d.Prefix {
			case "+":
				line = flOnStyle.Render(line)
			case "-":
				line = flErrStyle.Render(line)
			default:
				line = flDescStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	footer := "[\u21b5] save  [Esc] cancel"
	if len(m.diffLines) == 0 {
		footer = "[Esc] close"
	}
	content := strings.TrimRight(b.String(), "\n") + "\n" + footerTextStyle.Render(footer)
	h := min(fh, len(m.diffLines)+5)
	if h < 6 {
		h = 6
	}
	return renderPanel("Save changes?", content, fw, h, true)
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestCtrlSOpensDiff|TestDiffOverlay' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/settings/model.go internal/app/tui/settings/model_test.go
git commit -m "feat: add save diff preview overlay before commit"
```

---

## Task 5: Wire duplicate + reorder into collection frames

Wires `yank`/`paste` (map-backed providers/presets) and `moveUp`/`moveDown`/`yank`/`paste` (slice-backed hooks/permissions/languages/indexing) via extended drill builders.

**Files:**
- Modify: `internal/app/tui/settings/panestack.go` — `entriesDrillExt`/`listDrillExt` + delegation
- Modify: `internal/app/tui/settings/frames_collections.go` — providers/presets yank/paste; hooks/permissions reorder+dup
- Modify: `internal/app/tui/settings/frames_basic.go` — languages/indexing reorder+dup
- Test: `internal/app/tui/settings/frames_collections_test.go`

**Interfaces:**
- Produces: `entriesOpts{moveUp, moveDown, yank, paste}`, `entriesDrillExt(...)`, `listDrillExt(...)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/frames_collections_test.go`:

```go
func TestProviderYankPasteDuplicates(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "sk-1234"},
	}
	st := newState(cfg)

	f := providersFrame(st)
	rows := f.list.Rows()
	var ollamaRow *field
	for _, r := range rows {
		if r.id == "providers.ollama" {
			ollamaRow = r
			break
		}
	}
	if ollamaRow == nil {
		t.Fatal("no ollama row")
	}
	data := ollamaRow.yank()
	pc, ok := data.(yankedMapEntry)
	if !ok {
		t.Fatalf("yank data = %T, want yankedMapEntry", data)
	}
	if pc.key != "ollama" {
		t.Fatalf("yanked key = %q, want ollama", pc.key)
	}

	if err := ollamaRow.paste(data); err != nil {
		t.Fatalf("paste err = %v", err)
	}
	cp, ok := st.cfg.Providers["ollama-copy"]
	if !ok {
		t.Fatal("paste should create ollama-copy")
	}
	if cp.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("copy BaseURL = %q, want preserved", cp.BaseURL)
	}
}

func TestHooksReorderMoveUp(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.Entries = []config.HookConfig{
		{Event: "pre_tool", Command: "a.sh"},
		{Event: "turn_end", Command: "b.sh"},
	}
	st := newState(cfg)

	f := hooksFrame(st)
	rows := f.list.Rows()
	var row1 *field
	for _, r := range rows {
		if r.id == "hooks.1" {
			row1 = r
			break
		}
	}
	if row1 == nil {
		t.Fatal("no hooks.1 row")
	}
	if row1.moveUp == nil {
		t.Fatal("hooks rows must support moveUp")
	}
	row1.moveUp()
	if st.cfg.Hooks.Entries[0].Command != "b.sh" {
		t.Fatalf("after moveUp, entries[0].Command = %q, want b.sh", st.cfg.Hooks.Entries[0].Command)
	}
}

func TestPermissionsDuplicateInPlace(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions.Rules = []config.PermissionRule{
		{Permission: "shell", Pattern: "*", Action: "confirm"},
	}
	st := newState(cfg)

	f := permissionsFrame(st)
	rows := f.list.Rows()
	var row0 *field
	for _, r := range rows {
		if r.id == "permissions.0" {
			row0 = r
			break
		}
	}
	if row0 == nil {
		t.Fatal("no permissions.0 row")
	}
	data := row0.yank()
	if err := row0.paste(data); err != nil {
		t.Fatalf("paste err = %v", err)
	}
	if len(st.cfg.Permissions.Rules) != 2 {
		t.Fatalf("rules len = %d, want 2 after duplicate", len(st.cfg.Permissions.Rules))
	}
	if st.cfg.Permissions.Rules[1].Action != "confirm" {
		t.Fatalf("duplicated rule Action = %q, want confirm", st.cfg.Permissions.Rules[1].Action)
	}
}

func TestLanguagesReorderMoveDown(t *testing.T) {
	cfg := config.Default()
	cfg.Project.Languages = []string{"go", "markdown"}
	st := newState(cfg)

	f := commandsFrame(st)
	var langDrill *field
	for _, r := range f.list.Rows() {
		if r.id == "project.languages" {
			langDrill = r
			break
		}
	}
	detail := langDrill.build()
	rows := detail.list.Rows()
	var row0 *field
	for _, r := range rows {
		if r.id == "project.languages.0" {
			row0 = r
			break
		}
	}
	if row0.moveDown == nil {
		t.Fatal("languages items must support moveDown")
	}
	row0.moveDown()
	if st.cfg.Project.Languages[0] != "markdown" {
		t.Fatalf("after moveDown, languages[0] = %q, want markdown", st.cfg.Project.Languages[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestProviderYankPaste|TestHooksReorder|TestPermissionsDuplicate|TestLanguagesReorder' -v`
Expected: FAIL (rows have no yank/moveUp)

- [ ] **Step 3: Add extended drill builders**

In `internal/app/tui/settings/panestack.go`, add an options struct and the extended builders. The existing `entriesDrill` and `listDrill` delegate with zero opts.

Add after the existing `entriesDrill`:

```go
type entriesOpts struct {
	moveUp   func(k string)
	moveDown func(k string)
	yank     func(k string) any
	paste    func(k string, data any) error
}

func entriesDrillExt(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *frame, del func(string), opts entriesOpts) *field {
	buildFields := func() []*field {
		ks := keys()
		out := make([]*field, len(ks))
		for i, k := range ks {
			k := k
			row := &field{
				id: id + "." + k, title: rowTitle(k), kind: kindDrill,
				summary: func() string { return "" },
				build:   func() *frame { return buildEntry(k) },
				del:     func() { del(k) },
			}
			if opts.moveUp != nil {
				row.moveUp = func() { opts.moveUp(k) }
			}
			if opts.moveDown != nil {
				row.moveDown = func() { opts.moveDown(k) }
			}
			if opts.yank != nil {
				row.yank = func() any { return opts.yank(k) }
			}
			if opts.paste != nil {
				row.paste = func(data any) error { return opts.paste(k, data) }
			}
			out[i] = row
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d entries", len(keys())) },
		build: func() *frame {
			return newCollectionFrame(title, keyPrompt, buildFields, add)
		},
	}
}
```

Change the existing `entriesDrill` to delegate:

```go
func entriesDrill(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *frame, del func(string)) *field {
	return entriesDrillExt(id, title, keyPrompt, keys, rowTitle, add, buildEntry, del, entriesOpts{})
}
```

Add the `yankedMapEntry` helper type (used by map-backed yank/paste):

```go
type yankedMapEntry struct {
	key string
	val any
}
```

Add `listDrillExt` for slice-backed string lists. After the existing `listDrill`:

```go
func listDrillExt(id, title string, items *[]string, opts entriesOpts) *field {
	buildFields := func() []*field {
		out := make([]*field, len(*items))
		for i := range *items {
			i := i
			row := &field{
				id: fmt.Sprintf("%s.%d", id, i), title: (*items)[i], kind: kindScalar,
				getStr: func() string { return (*items)[i] },
				setStr: func(v string) error {
					if v == "" {
						return fmt.Errorf("cannot be empty")
					}
					(*items)[i] = v
					return nil
				},
				del: func() { *items = append((*items)[:i], (*items)[i+1:]...) },
			}
			if opts.moveUp != nil {
				row.moveUp = func() { opts.moveUp(strconv.Itoa(i)) }
			}
			if opts.moveDown != nil {
				row.moveDown = func() { opts.moveDown(strconv.Itoa(i)) }
			}
			if opts.yank != nil {
				row.yank = func() any { return opts.yank(strconv.Itoa(i)) }
			}
			if opts.paste != nil {
				row.paste = func(data any) error { return opts.paste(strconv.Itoa(i), data) }
			}
			out[i] = row
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d items", len(*items)) },
		build: func() *frame {
			return newCollectionFrame(title, "New entry", buildFields, func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("cannot be empty")
				}
				*items = append(*items, v)
				return nil
			})
		},
	}
}
```

Change the existing `listDrill` to delegate:

```go
func listDrill(id, title string, items *[]string) *field {
	return listDrillExt(id, title, items, entriesOpts{})
}
```

(`strconv` and `strings` are already imported in `panestack.go`.)

- [ ] **Step 4: Wire providers yank/paste**

In `internal/app/tui/settings/frames_collections.go`, the `providersFrame` uses `entriesDrill`. Switch it to `entriesDrillExt` with yank/paste opts. The `entriesDrill(...)` call currently passes 8 args; add the `entriesOpts` as the 9th.

Replace the `entriesDrill("providers", ...)` call with `entriesDrillExt("providers", ..., entriesOpts{...})` — same first 8 args, then:

```go
		entriesOpts{
			yank: func(k string) any {
				return yankedMapEntry{key: k, val: s.cfg.Providers[k]}
			},
			paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.Providers {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.key, existing)
				if s.cfg.Providers == nil {
					s.cfg.Providers = map[string]config.ProviderConfig{}
				}
				s.cfg.Providers[name] = ye.val.(config.ProviderConfig)
				return nil
			},
		})
```

Add the `uniqueCopyName` helper in `frames_collections.go`:

```go
func uniqueCopyName(base string, existing map[string]bool) string {
	candidate := base + "-copy"
	if !existing[candidate] {
		return candidate
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-copy-%d", base, i)
		if !existing[c] {
			return c
		}
	}
}
```

- [ ] **Step 5: Wire presets yank/paste**

In `presetsFrame`, switch `entriesDrill("presets", ...)` to `entriesDrillExt` with the same yank/paste shape but over `s.cfg.Models.Presets` and `routing.ModelPreset`:

```go
		entriesOpts{
			yank: func(k string) any {
				return yankedMapEntry{key: k, val: s.cfg.Models.Presets[k]}
			},
			paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.Models.Presets {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.key, existing)
				if s.cfg.Models.Presets == nil {
					s.cfg.Models.Presets = map[string]routing.ModelPreset{}
				}
				s.cfg.Models.Presets[name] = ye.val.(routing.ModelPreset)
				return nil
			},
		})
```

- [ ] **Step 6: Wire hooks reorder + duplicate**

In `hooksFrame`, switch `entriesDrill("hooks", ...)` to `entriesDrillExt` with moveUp/moveDown/yank/paste:

```go
		entriesOpts{
			moveUp: func(k string) {
				i, _ := strconv.Atoi(k)
				if i <= 0 {
					return
				}
				e := s.cfg.Hooks.Entries
				e[i-1], e[i] = e[i], e[i-1]
			},
			moveDown: func(k string) {
				i, _ := strconv.Atoi(k)
				e := s.cfg.Hooks.Entries
				if i >= len(e)-1 {
					return
				}
				e[i+1], e[i] = e[i], e[i+1]
			},
			yank: func(k string) any {
				i, _ := strconv.Atoi(k)
				if i >= len(s.cfg.Hooks.Entries) {
					return nil
				}
				return s.cfg.Hooks.Entries[i]
			},
			paste: func(k string, data any) error {
				hc, ok := data.(config.HookConfig)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				i, _ := strconv.Atoi(k)
				s.cfg.Hooks.Entries = insertHook(s.cfg.Hooks.Entries, i+1, hc)
				return nil
			},
		})
```

Add the `insertHook` helper:

```go
func insertHook(e []config.HookConfig, at int, hc config.HookConfig) []config.HookConfig {
	if at < 0 {
		at = 0
	}
	if at > len(e) {
		at = len(e)
	}
	out := make([]config.HookConfig, 0, len(e)+1)
	out = append(out, e[:at]...)
	out = append(out, hc)
	out = append(out, e[at:]...)
	return out
}
```

Add `"strconv"` to `frames_collections.go` imports (already imported).

- [ ] **Step 7: Wire permissions reorder + duplicate**

In `permissionsFrame`, switch `entriesDrill("permissions", ...)` to `entriesDrillExt`:

```go
		entriesOpts{
			moveUp: func(k string) {
				i, _ := strconv.Atoi(k)
				if i <= 0 {
					return
				}
				r := s.cfg.Permissions.Rules
				r[i-1], r[i] = r[i], r[i-1]
			},
			moveDown: func(k string) {
				i, _ := strconv.Atoi(k)
				r := s.cfg.Permissions.Rules
				if i >= len(r)-1 {
					return
				}
				r[i+1], r[i] = r[i], r[i+1]
			},
			yank: func(k string) any {
				i, _ := strconv.Atoi(k)
				if i >= len(s.cfg.Permissions.Rules) {
					return nil
				}
				return s.cfg.Permissions.Rules[i]
			},
			paste: func(k string, data any) error {
				rr, ok := data.(config.PermissionRule)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				i, _ := strconv.Atoi(k)
				s.cfg.Permissions.Rules = insertRule(s.cfg.Permissions.Rules, i+1, rr)
				return nil
			},
		})
```

Add the `insertRule` helper:

```go
func insertRule(r []config.PermissionRule, at int, rr config.PermissionRule) []config.PermissionRule {
	if at < 0 {
		at = 0
	}
	if at > len(r) {
		at = len(r)
	}
	out := make([]config.PermissionRule, 0, len(r)+1)
	out = append(out, r[:at]...)
	out = append(out, rr)
	out = append(out, r[at:]...)
	return out
}
```

- [ ] **Step 8: Wire languages + indexing reorder + duplicate**

In `internal/app/tui/settings/frames_basic.go`, the `commandsFrame` uses `listDrill("project.languages", "Languages", &s.cfg.Project.Languages)` and `indexingFrame` uses `listDrill("indexing.ignore", "Ignore patterns", &s.cfg.Indexing.Ignore)`. Switch both to `listDrillExt` with reorder + duplicate:

```go
	listDrillExt("project.languages", "Languages", &s.cfg.Project.Languages, listStringOpts(&s.cfg.Project.Languages))
```

```go
	listDrillExt("indexing.ignore", "Ignore patterns", &s.cfg.Indexing.Ignore, listStringOpts(&s.cfg.Indexing.Ignore))
```

Add the shared `listStringOpts` helper in `frames_basic.go`:

```go
func listStringOpts(items *[]string) entriesOpts {
	return entriesOpts{
		moveUp: func(k string) {
			i, _ := strconv.Atoi(k)
			if i <= 0 {
				return
			}
			(*items)[i-1], (*items)[i] = (*items)[i], (*items)[i-1]
		},
		moveDown: func(k string) {
			i, _ := strconv.Atoi(k)
			if i >= len(*items)-1 {
				return
			}
			(*items)[i+1], (*items)[i] = (*items)[i], (*items)[i+1]
		},
		yank: func(k string) any {
			i, _ := strconv.Atoi(k)
			if i >= len(*items) {
				return nil
			}
			return (*items)[i]
		},
		paste: func(k string, data any) error {
			s, ok := data.(string)
			if !ok {
				return fmt.Errorf("nothing yanked")
			}
			i, _ := strconv.Atoi(k)
			*items = insertString(*items, i+1, s)
			return nil
		},
	}
}

func insertString(s []string, at int, v string) []string {
	if at < 0 {
		at = 0
	}
	if at > len(s) {
		at = len(s)
	}
	out := make([]string, 0, len(s)+1)
	out = append(out, s[:at]...)
	out = append(out, v)
	out = append(out, s[at:]...)
	return out
}
```

Add imports `"strconv"` and `"fmt"` to `frames_basic.go` (the file currently imports only `"time"`).

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./internal/app/tui/settings/ -run 'TestProviderYankPaste|TestHooksReorder|TestPermissionsDuplicate|TestLanguagesReorder' -v`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/app/tui/settings/panestack.go internal/app/tui/settings/frames_collections.go internal/app/tui/settings/frames_basic.go internal/app/tui/settings/frames_collections_test.go
git commit -m "feat: wire duplicate and reorder into collection frames"
```

---

## Task 6: Help text, existing tests, and full verification

Updates the help overlay for the new keys/save behavior, fixes any existing tests broken by the reset row (extra row per section) and the `Ctrl+S` diff change, and runs the full suite.

**Files:**
- Modify: `internal/app/tui/settings/help.go`
- Modify: `internal/app/tui/model_test.go`

- [ ] **Step 1: Update the help overlay**

In `internal/app/tui/settings/help.go`, add the new keys and the save-diff note. Replace the `lines` slice:

```go
	lines := []string{
		"Settings keys",
		"",
		"  j/k or ↑/↓     move",
		"  g / G          first / last",
		"  Enter          open · edit · drill in",
		"  Space          toggle on/off",
		"  ←/→            cycle enum values",
		"  a / d          add / delete entry",
		"  y / p          yank / paste (duplicate)",
		"  Shift+↑/↓      reorder entry",
		"  h / Shift+Tab  back to sidebar",
		"  Esc            up one level · discard edit",
		"  /              search all settings",
		"  Ctrl+S         review changes, then save",
		"  ?              close this help",
	}
```

- [ ] **Step 2: Run the full settings test suite to find failures**

Run: `go test ./internal/app/tui/settings/ ./internal/app/tui/... 2>&1 | grep -E 'FAIL|---' | head -40`
Expected: list any failures.

Likely failures and fixes:

- `TestSettingsNavigationThroughMainModel` / `TestSettingsNavigationWithDefaultConfig` (in `internal/app/tui/model_test.go`): these navigate by row index and assert `FocusedFieldTitle()`. The reset row appended to each section root adds one row, shifting indices for sections whose root the test walks (e.g., Agent). Update the `sendKey` counts so the cursor lands on the intended row, or assert against a row that hasn't moved (most scalar rows keep their relative order; only a trailing row was added).

- `TestCtrlOOpensSettings` / `TestSettingsCancelClosesOverlay`: unaffected (they open/close, not navigate rows).

- Any test that presses `Ctrl+S` and expects an immediate `SavedMsg`: now `Ctrl+S` opens `overlayDiff`. Update those tests to also send `enter` to confirm the save (or `esc` to cancel). Search for `ctrl+s` in the test files:

Run: `grep -rn "ctrl+s" internal/app/tui/`

For each match in a test that expects a save, append a follow-up `enter` keypress after the `ctrl+s` so the diff overlay commits.

- [ ] **Step 3: Apply the test fixes**

For each failing test identified in Step 2, update row navigation counts and `Ctrl+S` → `Ctrl+S` then `Enter` sequences. Keep assertions semantically equivalent (same field focused, same saved/cancelled outcome).

- [ ] **Step 4: Run all settings tests**

Run: `go test ./internal/app/tui/settings/ -v`
Expected: all PASS

- [ ] **Step 5: Run the full test suite**

Run: `go test ./... 2>&1 | tail -30`
Expected: all PASS

- [ ] **Step 6: Run vet and build**

Run: `go vet ./... && go build ./cmd/marshal`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/settings/help.go internal/app/tui/model_test.go
git commit -m "test: update help text and existing tests for reset row and save-diff overlay"
```

---

## Self-Review

**Spec coverage:**
- Section 6 (Duplicate + reorder): Task 1 (closures + keys), Task 5 (wiring) ✓. Map-backed yank/paste (providers/presets), slice-backed reorder + in-place duplicate (hooks/permissions/languages/indexing) ✓. Footer hints `[y] yank [p] paste [shift↑↓] move` only when supported ✓.
- Section 7 (Reset to defaults): Task 3 ✓. Per-section `kindAction` row on every root ✓. Confirm idiom (idle → "again to confirm" → "✓ reset") ✓. Disarm on cursor move / Esc ✓.
- Section 8 (Save diff preview): Tasks 2 + 4 ✓. `Ctrl+S` opens `overlayDiff` ✓. Structured `+`/`-`/`~` diff ✓. Secrets masked (Task 2 `TestConfigDiffMasksAPIKey`) ✓. Enter commits via `saveCmd`, Esc returns ✓. No-change "no changes" line ✓.

**Type consistency:**
- `diffLine{Prefix, Path, Detail}` — defined Task 2, used Task 4 (`diffOverlay`, `m.diffLines`).
- `field.yank func() any` / `field.paste func(any) error` / `field.moveUp`/`moveDown`/`disarm` — defined Task 1, wired Task 5, used by reset `disarm` Task 3.
- `entriesOpts{moveUp, moveDown, yank, paste}` — defined Task 5, used by `entriesDrillExt`/`listDrillExt` and the four frame wirings.
- `yankedMapEntry{key, val}` — defined Task 5, used by providers/presets yank/paste.
- `resetSection` / `resetField` / `withResetRow` — defined Task 3, `withResetRow` called in `New`.
- `overlayDiff` — defined Task 4, routed in `Update`/`View`.

**Placeholder scan:** No TBDs. All code blocks complete. Step 2/3 of Task 6 instruct a grep-and-fix loop with concrete guidance (the exact failures depend on Phase 1's final row layout), which is the honest approach since Phase 1 isn't merged yet.

**Scope check:** Phase 2 is the three polish features. No Phase 1 work leaks in except reusing `kindAction`/`overlayPicker` machinery that Phase 1 establishes.
