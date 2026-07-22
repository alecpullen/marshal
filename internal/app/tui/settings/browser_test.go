package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
)

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
	if got := b.list.CursorRow().id; got != "agent.max_retries" {
		t.Fatalf("filtered cursor = %q, want agent.max_retries", got)
	}

	// commit opens the inline scalar edit, sets it to "9", and confirms.
	commit := func() ChangedMsg {
		if cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
			t.Fatal("opening an inline edit must not itself persist")
		}
		if !b.list.editing {
			t.Fatal("expected inline edit mode after enter")
		}
		b.list.input.SetValue("9")
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
		if row.id == "section.providers" {
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
		if row.id == "section.providers" {
			b.list.SetCursor(index)
			break
		}
	}
	if got := b.list.CursorRow().id; got != "section.providers" {
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
