package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestRollbackRequiresConfirmation pins U-03: Ctrl+R is reverse-i-search in
// every readline shell, so it gets pressed reflexively. It used to rewrite the
// working tree on that single keystroke.
func TestRollbackRequiresConfirmation(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.StoreBackup([]session.BackupFile{{Path: "app.go", Content: "original", Exists: true}})
	m := New(state)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !state.HasBackup() {
		t.Fatal("first Ctrl+R rolled back without confirmation")
	}

	// An unrelated key must cancel the armed rollback.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'x'})
	m = updated.(Model)
	if m.rollbackArmed {
		t.Error("armed rollback survived an unrelated keypress")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !state.HasBackup() {
		t.Error("rollback fired on a re-armed first press after being cancelled")
	}
}
