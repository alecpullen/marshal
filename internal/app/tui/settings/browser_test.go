package settings

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
)

// stripANSI removes ANSI escape sequences from text so test assertions
// can match visible content without lipgloss styling.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestBrowserFiltersAndRendersRows(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "shell")

	view := b.View(80, 12)
	if !strings.Contains(view, "Shell · Allow network") {
		t.Fatalf("filtered view should list shell keys, got:\n%s", view)
	}
	if !strings.Contains(view, "Settings") {
		t.Errorf("panel title missing")
	}
}

func TestBrowserToggleSavesAndEmitsChangedMsg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	b := NewBrowser(config.Default(), path, "shell.allow_network")

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if cmd == nil {
		t.Fatal("mutating update must emit a command")
	}
	msg := cmd()
	changed, ok := msg.(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", msg)
	}
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}
	if !changed.Cfg.Tools.Shell.AllowNetwork {
		t.Error("ChangedMsg.Cfg does not carry the change")
	}
	if len(changed.Receipts) == 0 || !strings.Contains(changed.Receipts[0], "allow_network") {
		t.Errorf("receipts missing, got %v", changed.Receipts)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("changed setting was not saved: %v", err)
	}
}

func TestBrowserSaveFailurePreservesConfigWithoutReceipt(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingPath, nil, 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	b := NewBrowser(config.Default(), filepath.Join(blockingPath, "config.toml"), "shell.allow_network")

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if cmd == nil {
		t.Fatal("mutating update must emit a command")
	}
	msg := cmd()
	changed, ok := msg.(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", msg)
	}
	if changed.SaveErr == nil {
		t.Fatal("save should fail")
	}
	if !changed.Cfg.Tools.Shell.AllowNetwork {
		t.Fatal("save failure must preserve the in-memory config")
	}
	if len(changed.Receipts) != 0 {
		t.Fatalf("save failure must not emit success receipts: %v", changed.Receipts)
	}
}

// TestBrowserRetriesFailedSaveOnRepeatedCommit reproduces the bug where a
// failed save's baseline rollforward masks the "unsaved" state: repeating
// the exact same commit gesture (re-confirming an inline edit with the same
// value) diffs empty against the rolled-forward baseline and, before this
// fix, silently no-op'd instead of retrying the persistence attempt.
func TestBrowserRetriesFailedSaveOnRepeatedCommit(t *testing.T) {
	dir := t.TempDir()
	blockingPath := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockingPath, nil, 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	cfgPath := filepath.Join(blockingPath, "config.toml")

	b := NewBrowser(config.Default(), cfgPath, "agent.max_retries")
	if got := b.list.CursorRow().ID; got != "agent.max_retries" {
		t.Fatalf("filtered cursor = %q, want agent.max_retries", got)
	}

	// commit opens the inline scalar edit, sets it to "9", and confirms.
	commit := func() ChangedMsg {
		if cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
			t.Fatal("opening an inline edit must not itself persist")
		}
		if !b.list.Editing() {
			t.Fatal("expected inline edit mode after enter")
		}
		// Clear the pre-filled value and type "9"
		for range b.list.InputValue() {
			b.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		}
		for _, r := range "9" {
			b.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("confirming an inline edit must emit a command")
		}
		msg, ok := cmd().(ChangedMsg)
		if !ok {
			t.Fatalf("want ChangedMsg, got %T", msg)
		}
		return msg
	}

	first := commit()
	if first.SaveErr == nil {
		t.Fatal("expected the first save to fail (parent path is a file)")
	}
	if len(first.Receipts) != 0 {
		t.Fatalf("failed save must not emit receipts: %v", first.Receipts)
	}

	// Fix the underlying problem: cfgPath's parent now exists as a real
	// directory.
	if err := os.Remove(blockingPath); err != nil {
		t.Fatalf("remove blocking file: %v", err)
	}
	if err := os.Mkdir(blockingPath, 0o755); err != nil {
		t.Fatalf("create real directory: %v", err)
	}

	// Re-confirm the exact same inline edit. The in-memory config already
	// holds "9" from the failed attempt, so this diffs empty against
	// baseline — it must still retry the save rather than silently no-op.
	second := commit()
	if second.SaveErr != nil {
		t.Fatalf("retry save should succeed now that the path is fixed: %v", second.SaveErr)
	}
	if len(second.Receipts) == 0 {
		t.Fatal("a successful retry must emit a receipt")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("retried setting was not saved to disk: %v", err)
	}
}

// TestBrowserPendingSaveDoesNotRetryOnNavigationOrFilterTyping guards the
// other half of the fix: while a save is pending retry, pure cursor
// navigation and filter typing must stay cheap no-ops that never touch
// disk, even though they also produce an empty diff against baseline.
func TestBrowserPendingSaveDoesNotRetryOnNavigationOrFilterTyping(t *testing.T) {
	dir := t.TempDir()
	blockingPath := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockingPath, nil, 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	cfgPath := filepath.Join(blockingPath, "config.toml")

	b := NewBrowser(config.Default(), cfgPath, "shell.allow_network")
	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	changed := cmd().(ChangedMsg)
	if changed.SaveErr == nil {
		t.Fatal("expected the toggle save to fail")
	}
	if !b.savePending {
		t.Fatal("a failed save must leave savePending set")
	}

	// Fix the underlying problem so a wrongly-triggered retry would succeed
	// and be observable.
	if err := os.Remove(blockingPath); err != nil {
		t.Fatalf("remove blocking file: %v", err)
	}
	if err := os.Mkdir(blockingPath, 0o755); err != nil {
		t.Fatalf("create real directory: %v", err)
	}

	navKeys := []tea.KeyPressMsg{
		{Code: tea.KeyDown},
		{Code: tea.KeyUp},
	}
	for _, k := range navKeys {
		if cmd := b.Update(k); cmd != nil {
			t.Fatalf("navigation key %v must be a no-op while save is pending, got a command", k)
		}
	}
	// Filter typing: textinput.Update may still return its own cursor-blink
	// tea.Cmd, so the no-op assertion is "no ChangedMsg / no disk write",
	// not "cmd == nil".
	if cmd := b.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); cmd != nil {
		if _, ok := cmd().(ChangedMsg); ok {
			t.Fatal("filter typing must not persist while save is pending")
		}
	}

	if !b.savePending {
		t.Fatal("savePending must remain set: nothing should have retried yet")
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("navigation/filter typing must never touch disk")
	}
}

func TestBrowserRowsShowHumanTitlesWithSection(t *testing.T) {
	b := NewBrowser(config.Default(), "", "remote providers")
	view := b.View(80, 24)
	if !strings.Contains(view, "Privacy · Remote providers allowed") {
		t.Fatalf("row label should show section + human title, got:\n%s", view)
	}
	if strings.Contains(view, "▸ privacy.remote_providers") {
		t.Errorf("row label must not show the dotted key as the primary label:\n%s", view)
	}
}

func TestBrowserCursorDescShowsDottedKey(t *testing.T) {
	b := NewBrowser(config.Default(), "", "remote providers")
	view := b.View(80, 24)
	if !strings.Contains(view, "privacy.remote_providers") {
		t.Fatalf("dotted key should appear in the description line, got:\n%s", view)
	}
}

func TestBrowserCollectionRowShowsSectionTitle(t *testing.T) {
	b := NewBrowser(config.Default(), "", "providers collection")
	view := b.View(80, 24)
	if !strings.Contains(view, "Providers") {
		t.Fatalf("collection row should show the section title, got:\n%s", view)
	}
}

func TestBrowserViewHonorsMaxHeight(t *testing.T) {
	for _, pickerOpen := range []bool{false, true} {
		b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
		if pickerOpen {
			b.pickerModel = picker.New("Pick", "", []picker.Item{{Label: "choice", Value: "choice"}})
		}
		for _, maxHeight := range []int{0, 1, 2, 3, 4, 6} {
			view := b.View(80, maxHeight)
			height := 0
			if view != "" {
				height = lipgloss.Height(view)
			}
			if height > maxHeight {
				t.Errorf("pickerOpen=%t: View(80, %d) rendered %d rows", pickerOpen, maxHeight, height)
			}
		}
	}
}

// TestBrowserViewHonorsMaxHeightWhileDrilled extends
// TestBrowserViewHonorsMaxHeight to the b.stack != nil branch (drilled into
// a collection), which the original test never exercised.
func TestBrowserViewHonorsMaxHeightWhileDrilled(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "providers")
	for index, row := range b.list.Rows() {
		if row.ID == "section.providers" {
			b.list.SetCursor(index)
			break
		}
	}
	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if b.stack == nil {
		t.Fatal("expected to be drilled into the providers collection")
	}

	for _, maxHeight := range []int{0, 1, 2, 3, 4, 6} {
		view := b.View(80, maxHeight)
		height := 0
		if view != "" {
			height = lipgloss.Height(view)
		}
		if height > maxHeight {
			t.Errorf("drilled View(80, %d) rendered %d rows", maxHeight, height)
		}
	}
}

func TestBrowserDrillsIntoCollectionAndBack(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "providers")
	for index, row := range b.list.Rows() {
		if row.ID == "section.providers" {
			b.list.SetCursor(index)
			break
		}
	}
	if got := b.list.CursorRow().ID; got != "section.providers" {
		t.Fatalf("provider collection cursor = %q, want section.providers", got)
	}

	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if b.stack == nil {
		t.Fatal("enter on provider collection should open its existing frame")
	}
	if !strings.Contains(b.View(80, 12), "Settings › Providers") {
		t.Fatalf("drill title missing breadcrumb:\n%s", b.View(80, 12))
	}

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("esc at a collection root should return to the flat browser")
	}
	if b.stack != nil {
		t.Fatal("esc at a collection root should leave drill mode")
	}
}

func TestBrowserEscEmitsClosed(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc at root must emit close")
	}
	if _, ok := cmd().(BrowserClosedMsg); !ok {
		t.Fatal("want BrowserClosedMsg")
	}
}

func TestUnfilteredBrowserGroupsBySection(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	view := b.View(80, 40)
	if !strings.Contains(view, "Agent") {
		t.Fatalf("unfiltered view should show Agent section header, got:\n%s", view)
	}
	if !strings.Contains(view, "Shell") {
		t.Fatalf("unfiltered view should show Shell section header, got:\n%s", view)
	}
	// Verify that header rows exist in the field list with kindHeader.
	rows := b.list.Rows()
	headerCount := 0
	for _, row := range rows {
		if row.Kind == kindHeader {
			headerCount++
		}
	}
	if headerCount < 2 {
		t.Fatalf("expected at least 2 header rows, got %d", headerCount)
	}
}

func TestFilteredBrowserHasNoHeaders(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "shell")
	// When filtered, the matchedFields path should not produce kindHeader rows.
	for _, row := range b.list.Rows() {
		if row.Kind == kindHeader {
			t.Fatalf("filtered browser should not have header rows, got id=%q", row.ID)
		}
	}
}

func TestCursorSkipsHeaders(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	// In the unfiltered view, the first non-header row should be the cursor.
	rows := b.list.Rows()
	if len(rows) == 0 {
		t.Fatal("unfiltered browser should have rows")
	}
	// Cursor should not be on a header row.
	if rows[b.list.Cursor()].Kind == kindHeader {
		t.Fatal("cursor must not land on a header row")
	}
	// Navigate down through all rows and verify cursor never lands on a header.
	for i := 0; i < len(rows)*2; i++ {
		b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if rows[b.list.Cursor()].Kind == kindHeader {
			t.Fatalf("cursor landed on header row %q at index %d", rows[b.list.Cursor()].Title, b.list.Cursor())
		}
	}
	// Navigate up through all rows and verify cursor never lands on a header.
	for i := 0; i < len(rows)*2; i++ {
		b.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		if rows[b.list.Cursor()].Kind == kindHeader {
			t.Fatalf("cursor landed on header row %q at index %d", rows[b.list.Cursor()].Title, b.list.Cursor())
		}
	}
}

func TestHintsFollowCursorRowKind(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	rows := b.list.Rows()
	if len(rows) == 0 {
		t.Fatal("unfiltered browser should have rows")
	}

	// Find the first non-header row and set cursor there.
	cursorIdx := -1
	for i, row := range rows {
		if row.Kind != kindHeader {
			cursorIdx = i
			break
		}
	}
	if cursorIdx < 0 {
		t.Fatal("no non-header rows found")
	}
	b.list.SetCursor(cursorIdx)

	// Walk through all non-header rows and verify the hint matches the kind.
	for i := 0; i < len(rows); i++ {
		row := rows[b.list.Cursor()]
		if row.Kind == kindHeader {
			b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			continue
		}
		hint := rowHints(b, true)
		view := b.View(80, 24)
		switch row.Kind {
		case kindToggle:
			if !strings.Contains(view, "Space toggle") {
				t.Errorf("cursor on kindToggle row %q: expected hint containing 'Space toggle', view:\n%s", row.ID, view)
			}
		case kindScalar:
			if row.SetStr == nil {
				if !strings.Contains(view, "read-only") {
					t.Errorf("cursor on read-only kindScalar row %q: expected hint containing 'read-only', view:\n%s", row.ID, view)
				}
			} else {
				if !strings.Contains(view, "↵ edit") {
					t.Errorf("cursor on editable kindScalar row %q: expected hint containing '↵ edit', view:\n%s", row.ID, view)
				}
			}
		case kindEnum:
			if !strings.Contains(view, "←→ cycle · ↵ pick") {
				t.Errorf("cursor on kindEnum row %q: expected hint containing '←→ cycle · ↵ pick', view:\n%s", row.ID, view)
			}
		case kindDrill:
			if !strings.Contains(view, "↵ open") {
				t.Errorf("cursor on kindDrill row %q: expected hint containing '↵ open', view:\n%s", row.ID, view)
			}
		case kindAction:
			if !strings.Contains(view, "↵ run") {
				t.Errorf("cursor on kindAction row %q: expected hint containing '↵ run', view:\n%s", row.ID, view)
			}
		case kindPicker:
			if !strings.Contains(view, "↵ pick") {
				t.Errorf("cursor on kindPicker row %q: expected hint containing '↵ pick', view:\n%s", row.ID, view)
			}
		}
		// Verify the hint text matches what rowHints returns.
		if hint != "" && !strings.Contains(view, hint) {
			t.Errorf("cursor on row %q (kind=%v): rowHints returned %q but view does not contain it:\n%s", row.ID, row.Kind, hint, view)
		}
		// Move cursor down for next iteration.
		b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
}

func TestBrowserRoutesPasteIntoActiveFieldEdit(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	var got string
	b.list = newFieldList(func() []*field {
		return []*field{scalarField("t.Paste", "Token",
			func() string { return got },
			func(v string) error { got = v; return nil })}
	})
	b.list.SetSize(60, 20)

	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open inline edit
	if !b.list.Editing() {
		t.Fatal("enter should open inline edit")
	}
	b.Update(tea.PasteMsg{Content: "pasted-secret"})
	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // apply
	if got != "pasted-secret" {
		t.Fatalf("pasted value should apply to the field, got %q", got)
	}
}

func TestBrowserPasteIntoFilter(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	if got := b.FilterValue(); got != "" {
		t.Fatalf("initial filter should be empty, got %q", got)
	}

	b.Update(tea.PasteMsg{Content: "shell"})
	if got := b.FilterValue(); got != "shell" {
		t.Fatalf("filter value = %q, want \"shell\"", got)
	}

	view := b.View(80, 12)
	if !strings.Contains(view, "Shell · Allow network") {
		t.Fatalf("pasted filter should refresh the list, got:\n%s", view)
	}
}

func TestBrowserTwoColumnShowsDescInDetailPane(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	var got string
	f := scalarField("t.token", "Token",
		func() string { return got },
		func(v string) error { got = v; return nil })
	f.Desc = "the token used for things"
	b.list = newFieldList(func() []*field { return []*field{f} })

	// 140 cols of dock width → interior 135 ≥ WideBreakpoint → two columns.
	out := b.View(140, 20)
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if strings.Contains(line, "Token") && strings.Contains(line, "the token used for things") {
			return // desc is beside the row (detail pane), not under it
		}
	}
	t.Fatalf("expected desc in a detail pane beside the cursor row, got:\n%s", stripANSI(out))
}

func TestBrowserSingleColumnKeepsInlineDesc(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	var got string
	f := scalarField("t.token", "Token",
		func() string { return got },
		func(v string) error { got = v; return nil })
	f.Desc = "the token used for things"
	b.list = newFieldList(func() []*field { return []*field{f} })

	// 80 cols → interior 75 < WideBreakpoint → single column, desc under row.
	out := stripANSI(b.View(80, 20))
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Token") && !strings.Contains(line, "the token") {
			if i+1 < len(lines) && strings.Contains(lines[i+1], "the token used for things") {
				return // desc on its own line under the row
			}
		}
	}
	t.Fatalf("expected inline desc under the cursor row, got:\n%s", out)
}

// TestBrowserTwoColumnShowsDescInDetailPaneWhileDrilled extends
// TestBrowserTwoColumnShowsDescInDetailPane to the b.stack != nil branch
// (drilled into a collection). The drilled stack reuses the same list+detail
// join — this test pins that the join still happens when the body is a
// drilled collection rather than the flat top-level list.
// drillBrowserToRow sends KeyDown messages until the cursor lands on a row
// whose Title contains want, or the loop budget is exhausted.
func drillBrowserToRow(t *testing.T, b *BrowserPanel, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		row := b.activeList().CursorRow()
		if row != nil && strings.Contains(row.Title, want) {
			return
		}
		b.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	rows := b.activeList().Rows()
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID + ":" + r.Title
	}
	t.Fatalf("drillBrowserToRow(%q) exhausted budget; rows: %v", want, ids)
}

func TestGlobalTargetRowWritesUserConfigOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath := config.UserConfigPath(home)
	projectPath := filepath.Join(t.TempDir(), "config.toml")
	b := NewBrowser(config.Default(), projectPath, "", WithUserConfigPath(userPath))

	drillBrowserToRow(t, b, "Theme")
	// Cycle the enum to a new value (interface rows are enum fields).
	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd == nil {
		t.Fatal("cycling an enum must produce a command")
	}
	msg := cmd()
	changed, ok := msg.(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", msg)
	}
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}
	if !changed.Saved {
		t.Fatal("global-target commit must set Saved=true")
	}

	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("project config must not be written for a global-target row")
	}
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TUI.Theme == config.Default().TUI.Theme {
		t.Fatalf("user config was not written: theme still %q", cfg.TUI.Theme)
	}
}

func TestGlobalTargetSaveFailureDoesNotWriteProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath := config.UserConfigPath(home)
	projectPath := filepath.Join(t.TempDir(), "config.toml")
	b := NewBrowser(config.Default(), projectPath, "", WithUserConfigPath(userPath))

	// Make user-config path unwritable: parent is a regular file so
	// MkdirAll in SaveUserConfigValue fails.
	badParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badParent, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	b.userConfigPath = filepath.Join(badParent, "config.toml")

	drillBrowserToRow(t, b, "Theme")
	b.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // enum cycle

	// Project config must not exist — the global-target failure must not
	// fall through to writing the project config.
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("project config must not be written on global-target failure: %v", err)
	}
}

func TestHintsShowWriteTarget(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	drillBrowserToRow(t, b, "Theme")
	hints := rowHints(b, b.stack == nil)
	if !strings.Contains(hints, "user config") {
		t.Fatalf("hints should name the write target, got %q", hints)
	}
}

func TestDetailPaneShowsProvenance(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "",
		WithProvenance(func(tomlPath string) string {
			if tomlPath == "tui.theme" {
				return "set by: user config · overrides: default"
			}
			return ""
		}))
	// Put the cursor on the theme row and render wide (two-column).
	drillBrowserToRow(t, b, "Theme")
	out := stripANSI(b.View(140, 20))
	if !strings.Contains(out, "set by: user config") {
		t.Fatalf("detail pane should show provenance:\n%s", out)
	}
}

func TestBrowserTwoColumnShowsDescInDetailPaneWhileDrilled(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "providers")
	for index, row := range b.list.Rows() {
		if row.ID == "section.providers" {
			b.list.SetCursor(index)
			break
		}
	}
	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if b.stack == nil {
		t.Fatal("expected to be drilled into the providers collection")
	}

	// The cursor row is the first entry of the drilled providers frame, the
	// "Reset Providers to defaults" action whose desc is "restore this
	// section to built-in defaults (applies immediately)". Asserting the row
	// label and the desc appear on the same rendered line proves the two-
	// column join happens on the drilled branch, not just the flat one.
	out := stripANSI(b.View(140, 20))
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Reset Providers to defaults") &&
			strings.Contains(line, "restore this section to built-in defaults") {
			return // desc is beside the row in the detail pane
		}
	}
	t.Fatalf("expected desc beside the cursor row in two-column drill, got:\n%s", out)
}
