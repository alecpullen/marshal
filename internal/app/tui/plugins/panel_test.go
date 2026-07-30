package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
