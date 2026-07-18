package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestBrowserFiltersAndRendersRows(t *testing.T) {
	b := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "shell")

	view := b.View(80, 12)
	if !strings.Contains(view, "shell.allow_network") {
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
