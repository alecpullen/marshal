package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/settings"
	"marshal/internal/plugins"
)

func TestPanelRootList(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	lockPath := plugins.GlobalLockPath(home)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		t.Fatal(err)
	}
	lf := plugins.Lockfile{
		Plugins: []plugins.LockEntry{
			{Name: "demo", Source: "https://example.com/demo.git", Commit: "abc123def"},
		},
	}
	if err := lf.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	p := NewPanel(home, work, true, session.New(config.Config{}, work, time.Now(), session.Persistence{}))
	rows := settings.FieldListRows(p.list)
	found := false
	for _, r := range rows {
		if settings.FieldID(r) == "plugin.demo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin.demo row, rows: %v", rows)
	}
}

func TestPanelViewRendersPushedInstallFrame(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	p := NewPanel(home, work, true, session.New(config.Config{}, work, time.Now(), session.Persistence{}))

	// With no plugins installed, the only selectable row is "＋ Install plugin".
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(p.stack) == 0 {
		t.Fatalf("expected install frame pushed onto stack")
	}

	view := p.View(80, 20)
	if !strings.Contains(view, "Install plugin") {
		t.Fatalf("expected install frame title in view, got:\n%s", view)
	}
	if !strings.Contains(view, "Source") {
		t.Fatalf("expected install frame Source field in view, got:\n%s", view)
	}
	if strings.Contains(view, "＋ Install plugin") {
		t.Fatalf("expected root list hidden while install frame is active, got:\n%s", view)
	}
}
