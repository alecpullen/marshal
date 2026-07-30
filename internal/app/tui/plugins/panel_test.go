package plugins

import (
	"os"
	"path/filepath"
	"slices"
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

// TestPanelOwnsAsyncResults pins that every async result this panel emits is
// claimed via dock.MessageOwner. The result types are unexported, so the model
// cannot route them by type; an unclaimed message is dropped and its Update
// case becomes dead code, leaving the action silently non-functional.
func TestPanelOwnsAsyncResults(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	for _, msg := range []tea.Msg{
		installResultMsg{},
		removeResultMsg{},
		scanResultMsg{},
	} {
		if !p.OwnsMsg(msg) {
			t.Errorf("OwnsMsg(%T) = false, want true — this message would be dropped", msg)
		}
	}
	if p.OwnsMsg(tea.KeyPressMsg{}) {
		t.Error("OwnsMsg claimed a keypress; the dock routes those already")
	}
}

// TestScanWithEmptySourceReportsError pins that an empty source surfaces an
// error rather than silently returning a nil command.
func TestScanWithEmptySourceReportsError(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.stack = append(p.stack, p.installFrame())
	p.installSource = "   "

	if cmd := p.runScan(); cmd != nil {
		t.Fatal("expected no command for an empty source")
	}
	if p.installErr == "" {
		t.Error("empty source produced no error message — the action looks broken")
	}
	if p.activeList().ErrMsg == "" {
		t.Error("error was not surfaced on the visible list")
	}
}

// TestConfirmInstallWithoutScanReportsError pins the same for the confirm step.
func TestConfirmInstallWithoutScanReportsError(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.stack = append(p.stack, p.installFrame())

	if cmd := p.runInstallFromScan(); cmd != nil {
		t.Fatal("expected no command without a scan")
	}
	if p.activeList().ErrMsg == "" {
		t.Error("confirming without a scan was silent")
	}
}

// TestScanErrorIsVisible pins that a failed scan renders. p.installErr used to
// be assigned and never read, so failures showed nothing at all.
func TestScanErrorIsVisible(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.stack = append(p.stack, p.installFrame())
	p.busy = "scanning"

	p.Update(scanResultMsg{Err: errBoom{}})

	if p.busy != "" {
		t.Error("busy state not cleared after a failed scan")
	}
	if !strings.Contains(p.activeList().ErrMsg, "boom") {
		t.Errorf("scan error not surfaced: %q", p.activeList().ErrMsg)
	}
}

// TestInFlightAndConfirmationAreRendered pins that a slow clone and a
// completed action both show something, rather than looking like a no-op.
func TestInFlightAndConfirmationAreRendered(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)

	p.busy = "scanning"
	if out := p.View(60, 12); !strings.Contains(out, "scanning") {
		t.Errorf("in-flight scan not shown in the view:\n%s", out)
	}

	p.busy = ""
	p.status = "installed foo (global)"
	if out := p.View(60, 12); !strings.Contains(out, "installed foo") {
		t.Errorf("confirmation not shown in the view:\n%s", out)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

// TestAbandonedScanRemovesTempClone pins A-02: the scan's clone outlives
// runScan (the confirm step copies from it), so it cannot use a deferred
// RemoveAll. Every path that abandons the scan must clean it up, or each
// abandoned install leaks a full repository clone.
func TestAbandonedScanRemovesTempClone(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "scan", "clone")
	if err := os.MkdirAll(cloneDir, 0755); err != nil {
		t.Fatal(err)
	}
	p.scanDir = cloneDir
	p.scannedContents = &plugins.Contents{}

	p.resetInstall()

	if p.scanDir != "" {
		t.Error("scanDir not cleared")
	}
	if _, err := os.Stat(filepath.Dir(cloneDir)); !os.IsNotExist(err) {
		t.Errorf("temp clone not removed: %v", err)
	}
}

// TestConfirmScreenListsAllExecutableContent pins A-03: MCPPolicies counted
// toward suppressing the "(none)" row but were never displayed, so a plugin
// carrying only policies asked the user to confirm executable content the
// screen refused to show.
func TestConfirmScreenListsAllExecutableContent(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.scannedContents = &plugins.Contents{
		MCPPolicies: map[string]string{"net-policy": "deny egress"},
	}
	p.stack = append(p.stack, p.installFrame())

	var titles []string
	for _, f := range settings.FieldListRows(p.activeList()) {
		titles = append(titles, settings.FieldTitle(f))
	}
	// Scope the check to the executable section: a "(none)" under Passive
	// content is correct here, since the plugin carries no skills or commands.
	exec := titles
	for i, tl := range titles {
		if tl == "Executable content" {
			exec = titles[i:]
			break
		}
	}
	joined := strings.Join(exec, "|")
	if !strings.Contains(joined, "net-policy") {
		t.Errorf("MCP policy not shown on the confirmation screen: %v", titles)
	}
	if strings.Contains(joined, "(none)") {
		t.Errorf("claimed no executable content while carrying a policy: %v", exec)
	}
}

// TestConfirmScreenOrdersMCPEntriesStably pins A-04: map order reshuffled the
// trust-review list between renders of identical data.
func TestConfirmScreenOrdersMCPEntriesStably(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.scannedContents = &plugins.Contents{
		MCPServers: map[string]config.MCPServerConfig{
			"zebra": {Command: "z"}, "alpha": {Command: "a"},
			"mike": {Command: "m"}, "bravo": {Command: "b"},
		},
	}

	var first []string
	for i := 0; i < 25; i++ {
		p.stack = []*settings.Frame{p.installFrame()}
		var got []string
		for _, f := range settings.FieldListRows(p.activeList()) {
			if id := settings.FieldID(f); strings.HasPrefix(id, "confirm.mcp.") {
				got = append(got, id)
			}
		}
		if first == nil {
			first = got
			continue
		}
		if !slices.Equal(got, first) {
			t.Fatalf("MCP order unstable across renders:\n first=%v\n now  =%v", first, got)
		}
	}
	if !slices.IsSorted(first) {
		t.Errorf("MCP entries not sorted: %v", first)
	}
}
