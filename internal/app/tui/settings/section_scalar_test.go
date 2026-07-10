package settings

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

// enterSection moves the sidebar cursor to the section with the given id
// and focuses its pane.
func enterSection(t *testing.T, m Model, id string) Model {
	t.Helper()
	for i, sec := range m.sections {
		if sec.id == id {
			m.cursor = i
			m.paneFocused = true
			return m
		}
	}
	t.Fatalf("no section %q", id)
	return m
}

func TestPrivacyPaneToggles(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "privacy")
	if got := m.FocusedFieldTitle(); got != "Remote providers allowed" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "space")
	if !m.state.cfg.Privacy.RemoteProvidersAllowed {
		t.Error("toggle did not write to working copy")
	}
}

func TestSnapshotsPaneNumericEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "snapshots")
	m = keyPress(m, "down") // Retention days
	if got := m.FocusedFieldTitle(); got != "Retention days" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "backspace", "1", "4", "down")
	if got := m.state.cfg.Snapshots.RetentionDays; got != 14 {
		t.Errorf("retention days = %d, want 14", got)
	}
}

func TestWebPaneMasksSearchKey(t *testing.T) {
	cfg := config.Default()
	cfg.Web.SearchKey = "sk-live-1234"
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "web")
	view := stripANSI(m.View())
	if strings.Contains(view, "sk-live-1234") {
		t.Error("raw search key must never render")
	}
	if !strings.Contains(view, "••••1234") {
		t.Error("masked search key should render")
	}
}

func TestWebPaneEmptySecretKeepsExisting(t *testing.T) {
	cfg := config.Default()
	cfg.Web.SearchKey = "sk-live-1234"
	cfg.Web.FetchTimeout = 30 * time.Second
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "web")
	m = keyPress(m, "down", "down", "down", "down", "up")
	if got := m.state.cfg.Web.SearchKey; got != "sk-live-1234" {
		t.Errorf("blank secret edit must keep the old key, got %q", got)
	}
}
