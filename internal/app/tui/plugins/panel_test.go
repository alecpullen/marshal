package plugins

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// The plugins panel is framed, and its frame never renders past the dock's
// height budget.
func TestPluginsPanelIsFramed(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	const maxHeight = 20
	view := p.View(80, maxHeight)
	rows := strings.Split(ansi.Strip(view), "\n")
	if !strings.HasPrefix(rows[0], "╭─ Plugins") {
		t.Fatalf("first row should open the Plugins frame, got %q", rows[0])
	}
	if len(rows) > maxHeight {
		t.Fatalf("view is %d rows tall, past the %d-row budget", len(rows), maxHeight)
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

// --- removal fixtures and helpers -----------------------------------------
//
// The removal tests below drive Panel.Update and the commands it emits, so the
// assertions cover the path a user actually takes: drill in, arm, confirm.

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// newRemovalPanel builds a trusted panel over a fresh home/work pair, so both
// the global and the project lockfile are local temp files.
func newRemovalPanel(t *testing.T) *Panel {
	t.Helper()
	return NewPanel(t.TempDir(), t.TempDir(), true, nil)
}

// installFixture writes a store directory and a lockfile entry for name in
// scope, so a removal has something real to delete and to leave behind.
// It returns the installed store directory.
func installFixture(t *testing.T, p *Panel, scope, name string) string {
	t.Helper()
	storeDir, lockPath := p.globalStoreDir(), p.globalLockPath()
	if scope == scopeProject {
		storeDir, lockPath = p.projectStoreDir(), p.projectLockPath()
	}
	dir := filepath.Join(storeDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create plugin store %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("# "+name+"\n"), 0644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	lf, err := plugins.ReadLockfile(lockPath)
	if err != nil {
		t.Fatalf("read lockfile %s: %v", lockPath, err)
	}
	lf.Upsert(plugins.LockEntry{
		Name:   name,
		Source: "https://example.com/" + name + ".git",
		Commit: "abc1234567",
	})
	if err := lf.Write(lockPath); err != nil {
		t.Fatalf("write lockfile %s: %v", lockPath, err)
	}
	return dir
}

// entryFor resolves the scopedEntry a fresh lockfile read reports, the way the
// list's drill row builds one.
func entryFor(t *testing.T, p *Panel, scope, name string) scopedEntry {
	t.Helper()
	for _, e := range p.lockEntries() {
		if e.Scope == scope && e.Name == name {
			return e
		}
	}
	t.Fatalf("no lockfile entry scope=%q name=%q in %+v", scope, name, p.lockEntries())
	return scopedEntry{}
}

func readLock(t *testing.T, p *Panel, scope string) *plugins.Lockfile {
	t.Helper()
	path := p.globalLockPath()
	if scope == scopeProject {
		path = p.projectLockPath()
	}
	lf, err := plugins.ReadLockfile(path)
	if err != nil {
		t.Fatalf("read lockfile %s: %v", path, err)
	}
	return lf
}

func mustKeepLockEntry(t *testing.T, p *Panel, scope, name string) {
	t.Helper()
	if _, ok := readLock(t, p, scope).Find(name); !ok {
		t.Fatalf("lockfile entry %q (%s) did not survive", name, scope)
	}
}

func mustDropLockEntry(t *testing.T, p *Panel, scope, name string) {
	t.Helper()
	if e, ok := readLock(t, p, scope).Find(name); ok {
		t.Fatalf("lockfile entry %q (%s) survived removal: %+v", name, scope, e)
	}
}

// press delivers a key through Panel.Update and feeds the emitted command's
// message straight back, the way the dock host does.
func press(t *testing.T, p *Panel, key tea.KeyPressMsg) {
	t.Helper()
	cmd := p.Update(key)
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		p.Update(msg)
	}
}

// focusRow parks the active list's cursor on the row with the given field ID.
func focusRow(t *testing.T, p *Panel, id string) {
	t.Helper()
	for i, r := range settings.FieldListRows(p.activeList()) {
		if settings.FieldID(r) == id {
			p.activeList().SetCursor(i)
			return
		}
	}
	t.Fatalf("no row %q in the active list", id)
}

// openRemoveRow stacks e's detail frame with its Remove row focused, the way
// drilling in from the list does.
func openRemoveRow(t *testing.T, p *Panel, e scopedEntry) {
	t.Helper()
	p.stack = append(p.stack, p.detailFrame(e))
	focusRow(t, p, "action.remove")
}

func mustExistPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to survive: %v", path, err)
	}
}

func mustBeGonePath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, Lstat error = %v", path, err)
	}
}

// TestRemoveConfirmationDisarmsOnNavigationAndEscape pins that an armed
// confirmation never survives leaving the row or the detail frame — otherwise
// it would carry into a selection the user was never shown as armed.
func TestRemoveConfirmationDisarmsOnNavigationAndEscape(t *testing.T) {
	t.Run("leaving the remove row", func(t *testing.T) {
		p := newRemovalPanel(t)
		target := installFixture(t, p, scopeGlobal, "demo")
		bystander := installFixture(t, p, scopeGlobal, "other")
		e := entryFor(t, p, scopeGlobal, "demo")
		openRemoveRow(t, p, e)

		press(t, p, keyEnter) // arms
		if !p.removeArmed[removalIdentity(e)] {
			t.Fatal("remove did not arm on the first enter")
		}
		press(t, p, keyUp)   // leaves the row
		press(t, p, keyDown) // and comes back
		if p.removeArmed[removalIdentity(e)] {
			t.Fatal("armed confirmation survived navigation away from the row")
		}
		press(t, p, keyEnter) // must only arm again

		mustExistPath(t, target)
		mustExistPath(t, bystander)
		mustKeepLockEntry(t, p, scopeGlobal, "demo")
		mustKeepLockEntry(t, p, scopeGlobal, "other")
		if !p.removeArmed[removalIdentity(e)] {
			t.Fatal("enter after navigation armed nothing — it confirmed instead")
		}

		// Two consecutive confirmations on the same row still delete.
		press(t, p, keyEnter)
		mustBeGonePath(t, target)
		mustDropLockEntry(t, p, scopeGlobal, "demo")
		mustExistPath(t, bystander)
		mustKeepLockEntry(t, p, scopeGlobal, "other")
	})

	t.Run("leaving and reopening the detail", func(t *testing.T) {
		p := newRemovalPanel(t)
		target := installFixture(t, p, scopeGlobal, "demo")
		bystander := installFixture(t, p, scopeGlobal, "other")
		e := entryFor(t, p, scopeGlobal, "demo")
		openRemoveRow(t, p, e)

		press(t, p, keyEnter) // arms
		press(t, p, keyEsc)   // pops the detail frame
		if len(p.stack) != 0 {
			t.Fatal("escape did not pop the detail frame")
		}
		if p.removeArmed[removalIdentity(e)] {
			t.Fatal("armed confirmation survived closing the detail frame")
		}
		mustExistPath(t, target)

		openRemoveRow(t, p, e)
		press(t, p, keyEnter) // must only arm again
		mustExistPath(t, target)
		mustExistPath(t, bystander)
		mustKeepLockEntry(t, p, scopeGlobal, "demo")
		if !p.removeArmed[removalIdentity(e)] {
			t.Fatal("enter after reopening the detail armed nothing — it confirmed instead")
		}

		press(t, p, keyEnter)
		mustBeGonePath(t, target)
		mustDropLockEntry(t, p, scopeGlobal, "demo")
		mustExistPath(t, bystander)
		mustKeepLockEntry(t, p, scopeGlobal, "other")
	})
}

// TestRemoveConfirmationIsScoped pins that the armed flag belongs to one
// scoped entry. The lockfile holds the same name per scope, so a name-only key
// would let the global copy's confirmation delete the project copy.
func TestRemoveConfirmationIsScoped(t *testing.T) {
	p := newRemovalPanel(t)
	globalDir := installFixture(t, p, scopeGlobal, "dup")
	projectDir := installFixture(t, p, scopeProject, "dup")

	globalEntry := entryFor(t, p, scopeGlobal, "dup")
	projectEntry := entryFor(t, p, scopeProject, "dup")
	if removalIdentity(globalEntry) == removalIdentity(projectEntry) {
		t.Fatal("global and project entries share a removal identity")
	}

	openRemoveRow(t, p, globalEntry)
	press(t, p, keyEnter) // arms the global copy
	if !p.removeArmed[removalIdentity(globalEntry)] {
		t.Fatal("global remove did not arm")
	}

	// The project copy shares a name but not an identity: opening and
	// confirming it must not inherit the global confirmation.
	openRemoveRow(t, p, projectEntry)
	press(t, p, keyEnter)
	mustExistPath(t, projectDir)
	mustExistPath(t, globalDir)
	mustKeepLockEntry(t, p, scopeProject, "dup")
	mustKeepLockEntry(t, p, scopeGlobal, "dup")
	if !p.removeArmed[removalIdentity(projectEntry)] {
		t.Fatal("project remove armed nothing — it confirmed a deletion never armed")
	}

	press(t, p, keyEnter)
	mustBeGonePath(t, projectDir)
	mustDropLockEntry(t, p, scopeProject, "dup")
	mustExistPath(t, globalDir)
	mustKeepLockEntry(t, p, scopeGlobal, "dup")
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
