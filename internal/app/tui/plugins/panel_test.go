package plugins

import (
	"context"
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
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/theme"
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
// be assigned and never read, so failures showed nothing at all. The failure is
// driven through the panel's real scan path, so it also proves the error result
// belongs to the current frame and that a failed scan keeps no directory.
func TestScanErrorIsVisible(t *testing.T) {
	deps := fakeScanDeps(nil)
	deps.Clone = func(context.Context, *plugins.Installer, string, string, string) (string, error) {
		return "", errBoom{}
	}
	p := newPanelWithScanDeps(t.TempDir(), t.TempDir(), true, nil, deps)
	openInstallFrame(t, p)
	setInstallField(t, p, "install.source", "https://example.com/demo.git")
	p.Update(scanMsg(t, p))

	if p.busy != "" {
		t.Error("busy state not cleared after a failed scan")
	}
	if !strings.Contains(p.activeList().ErrMsg, "boom") {
		t.Errorf("scan error not surfaced: %q", p.activeList().ErrMsg)
	}
	if p.scanLease != nil || p.scanParent != "" {
		t.Error("a failed scan kept ownership of a directory")
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

	parent := filepath.Join(t.TempDir(), "scan")
	if err := os.MkdirAll(filepath.Join(parent, "clone"), 0755); err != nil {
		t.Fatal(err)
	}
	p.scanParent = parent
	p.scannedContents = &plugins.Contents{}

	p.resetInstall()

	if p.scanParent != "" {
		t.Error("scanParent not cleared")
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
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
	keyEnter  = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyUp     = tea.KeyPressMsg{Code: tea.KeyUp}
	keyDown   = tea.KeyPressMsg{Code: tea.KeyDown}
	keyEsc    = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyPgDown = tea.KeyPressMsg{Code: tea.KeyPgDown}
	keyPgUp   = tea.KeyPressMsg{Code: tea.KeyPgUp}
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

// --- scan lifecycle fixtures ---------------------------------------------
//
// These fixtures drive the real install frame: open it from the root list, set
// the source through the frame's own setter, press Scan, and deliver the
// worker's message. Only the clone is faked, so no test needs the network, git,
// or a package-global hook.

// fakeScanDeps fakes the clone, writing one hook into dest, and records that it
// ran. Scan and Hash stay real: the review's contents and hash are the ones the
// user decides on.
func fakeScanDeps(cloned *bool) scanDeps {
	return scanDeps{
		NewInstaller: func() (*plugins.Installer, error) { return &plugins.Installer{}, nil },
		Clone: func(_ context.Context, _ *plugins.Installer, _, _, dest string) (string, error) {
			if cloned != nil {
				*cloned = true
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", err
			}
			hook := "[hooks]\n[[hooks.entries]]\nevent = \"pre_tool_use\"\ncommand = \"echo reviewed\"\n"
			if err := os.WriteFile(filepath.Join(dest, "hooks.toml"), []byte(hook), 0o644); err != nil {
				return "", err
			}
			return "abc1234", nil
		},
		Scan: plugins.ScanPlugin,
		Hash: plugins.HashDir,
	}
}

// hasRow reports whether the active list has a row with this field ID.
func hasRow(p *Panel, id string) bool {
	for _, f := range settings.FieldListRows(p.activeList()) {
		if settings.FieldID(f) == id {
			return true
		}
	}
	return false
}

// openInstallFrame opens the install frame from the root list, the way the
// Install action does.
func openInstallFrame(t *testing.T, p *Panel) {
	t.Helper()
	p.Update(keyEnter)
	if len(p.stack) == 0 {
		t.Fatal("the install frame did not open")
	}
}

// setInstallField writes an install-frame value through the row's own setter,
// then rebuilds the frame, so the invalidation hooks run exactly as they do for
// a committed edit.
func setInstallField(t *testing.T, p *Panel, id, value string) {
	t.Helper()
	for _, f := range settings.FieldListRows(p.activeList()) {
		if settings.FieldID(f) != id {
			continue
		}
		if f.SetStr == nil {
			t.Fatalf("row %q is not editable", id)
		}
		if err := f.SetStr(value); err != nil {
			t.Fatalf("set %q: %v", id, err)
		}
		p.activeList().Refresh()
		return
	}
	t.Fatalf("no row %q in the install frame", id)
}

// scanMsg runs the Scan action and returns the worker's message without
// delivering it, so a test can act while the result is still undelivered.
func scanMsg(t *testing.T, p *Panel) scanResultMsg {
	t.Helper()
	focusRow(t, p, "install.scan")
	cmd := p.Update(keyEnter)
	if cmd == nil {
		t.Fatal("Scan produced no command")
	}
	t.Cleanup(func() {
		if p.scanLease != nil || p.scanParent != "" {
			t.Errorf("scan state outlived the test: lease=%v parent=%q", p.scanLease != nil, p.scanParent)
		}
	})
	msg, ok := cmd().(scanResultMsg)
	if !ok {
		t.Fatalf("scan produced %T, want a scan result", msg)
	}
	return msg
}

// scanReview performs a full scan through the panel and returns the temporary
// parent the review now owns.
func scanReview(t *testing.T, p *Panel, source string) string {
	t.Helper()
	openInstallFrame(t, p)
	setInstallField(t, p, "install.source", source)
	p.Update(scanMsg(t, p))
	if p.scanParent == "" {
		t.Fatalf("the scan claimed no clone; error %q", p.activeList().ErrMsg)
	}
	return p.scanParent
}

// replacementPanel is any other dock panel: the test needs a second panel to
// replace the plugins panel with.
type replacementPanel struct{}

func (replacementPanel) Update(tea.Msg) tea.Cmd { return nil }
func (replacementPanel) View(int, int) string   { return "" }
func (replacementPanel) Sizing() dock.Sizing    { return dock.Docked }

// TestScanReviewInvalidatedBySourceOrScope pins that a review belongs to the
// exact source and scope that were scanned: editing either drops the review,
// deletes its clone, and removes Confirm, so the user is never asked to confirm
// an install they did not review.
func TestScanReviewInvalidatedBySourceOrScope(t *testing.T) {
	for _, tc := range []struct{ name, id, value string }{
		{"source", "install.source", "https://example.com/other.git"},
		{"scope", "install.scope", scopeProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPanelWithScanDeps(t.TempDir(), t.TempDir(), true, nil, fakeScanDeps(nil))
			parent := scanReview(t, p, "https://example.com/demo.git")
			if !hasRow(p, "install.confirm") {
				t.Fatal("a successful scan did not offer Confirm")
			}

			setInstallField(t, p, tc.id, tc.value)

			if p.scannedContents != nil || p.scanParent != "" {
				t.Fatal("the review survived an edit to the source or scope")
			}
			if hasRow(p, "install.confirm") {
				t.Fatal("Confirm survived an invalidated review")
			}
			mustBeGonePath(t, parent)

			if cmd := p.runInstallFromScan(); cmd != nil {
				t.Fatal("an invalidated review still produced an install command")
			}
		})
	}
}

// TestAbandonedScanCannotAttachToReopenedFrame pins that a scan result which
// arrives after its frame was left and reopened cannot become the new frame's
// review, and that the clone it published is deleted without waiting for that
// result.
func TestAbandonedScanCannotAttachToReopenedFrame(t *testing.T) {
	cloned := false
	p := newPanelWithScanDeps(t.TempDir(), t.TempDir(), true, nil, fakeScanDeps(&cloned))

	openInstallFrame(t, p)
	setInstallField(t, p, "install.source", "https://example.com/demo.git")
	msg := scanMsg(t, p) // the scan ran; its result is still undelivered
	if !cloned {
		t.Fatal("the fake clone never ran")
	}

	// Leave the install frame — abandoning the scan — then come back: the
	// reopened frame is a different review with a later generation.
	press(t, p, keyEsc)
	if len(p.stack) != 0 {
		t.Fatal("escape did not leave the install frame")
	}
	openInstallFrame(t, p)

	p.Update(msg)
	if p.scannedContents != nil || p.scanParent != "" {
		t.Fatal("an abandoned scan attached to the reopened frame")
	}
	if hasRow(p, "install.confirm") {
		t.Fatal("an abandoned scan offered Confirm in the reopened frame")
	}
	if p.busy != "" {
		t.Fatalf("the panel is still %q after an abandoned scan", p.busy)
	}
	// The lease owned the clone, so it is gone whether or not a message
	// arrived.
	mustBeGonePath(t, filepath.Dir(msg.CloneDir))
}

// TestPanelReplacementCleansUndeliveredScan pins the dock lifecycle contract:
// replacing the panel notifies it even though its scan result is still in
// flight, so the clone is deleted instead of leaking with the message.
func TestPanelReplacementCleansUndeliveredScan(t *testing.T) {
	p := newPanelWithScanDeps(t.TempDir(), t.TempDir(), true, nil, fakeScanDeps(nil))
	openInstallFrame(t, p)
	setInstallField(t, p, "install.source", "https://example.com/demo.git")
	msg := scanMsg(t, p) // never delivered to the panel

	parent := filepath.Dir(msg.CloneDir)
	mustExistPath(t, parent)

	var h dock.Host
	h.Open(p)
	h.Open(replacementPanel{})
	mustBeGonePath(t, parent)
	if p.scanLease != nil || p.scanParent != "" {
		t.Fatal("the released panel kept ownership of its scan")
	}

	// A late delivery finds no frame to attach to and deletes nothing twice.
	p.Update(msg)
	mustBeGonePath(t, parent)
}

// TestInstallOwnsCloneAfterConfirm pins the ownership handover: once the user
// confirms, the worker owns the clone, so closing or replacing the panel must
// not delete files the worker is still copying — and the worker cleans them
// itself when it finishes.
func TestInstallOwnsCloneAfterConfirm(t *testing.T) {
	home := t.TempDir()
	p := newPanelWithScanDeps(home, t.TempDir(), true, nil, fakeScanDeps(nil))
	parent := scanReview(t, p, "https://example.com/demo.git")
	cloneDir := filepath.Join(parent, "clone")

	focusRow(t, p, "install.confirm")
	cmd := p.Update(keyEnter)
	if cmd == nil {
		t.Fatal("Confirm produced no command")
	}
	if p.scanParent != "" {
		t.Fatal("the panel still references the clone it handed to the install worker")
	}

	// The panel is released while the install is still running: the worker's
	// input is not the panel's to delete any more.
	var h dock.Host
	h.Open(p)
	h.Open(replacementPanel{})
	mustExistPath(t, cloneDir)

	msg, ok := cmd().(installResultMsg)
	if !ok {
		t.Fatalf("install produced %T, want an install result", msg)
	}
	if msg.Err != nil {
		t.Fatalf("install failed: %v", msg.Err)
	}
	// The worker cleaned its own clone and committed the plugin.
	mustBeGonePath(t, cloneDir)
	mustExistPath(t, filepath.Join(plugins.GlobalStoreDir(home), "demo"))
}

// reviewPages renders the open detail at the given size, paging to the end, and
// returns every page, both plain and raw.
func reviewPages(t *testing.T, p *Panel, width, height int) (plain, raw []string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		view := p.View(width, height)
		raw = append(raw, view)
		plain = append(plain, ansi.Strip(view))
		before := p.reviewDetail.offset
		press(t, p, keyPgDown)
		if p.reviewDetail.offset == before {
			break
		}
	}
	return plain, raw
}

// TestReviewDetailShowsCompleteExecutableContent pins the review contract: the
// confirmation screen used to ask the user to authorise executable content it
// rendered truncated to one row, with MCP arguments joined by a space so their
// boundaries were invisible. Every executable entry now opens a wrapped,
// pageable view of its complete text, and terminal controls in that text are
// shown rather than executed.
func TestReviewDetailShowsCompleteExecutableContent(t *testing.T) {
	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.stack = append(p.stack, p.installFrame())

	p.scannedContents = &plugins.Contents{
		Hooks: []config.HookConfig{{
			Event:   "pre_tool_use",
			Command: "echo " + strings.Repeat("h", 90) + " MARKER_HOOK_END \x1b[2Kwipe",
		}},
		MCPServers: map[string]config.MCPServerConfig{
			"srv": {Command: "server-bin", Args: []string{
				"--flag",
				"has space",
				`has"quote`,
				"\x1b[31mevil",
			}},
		},
		MCPPolicies: map[string]string{
			"net-policy": "deny " + strings.Repeat("p", 100) + " MARKER_POLICY_END",
		},
	}
	p.scannedName = "demo"
	p.scannedRawSource = "https://example.com/demo.git"
	p.scannedSource = "https://example.com/demo.git"
	p.scannedScope = scopeGlobal
	p.installSource = "https://example.com/demo.git"
	p.scanParent = filepath.Join(t.TempDir(), "scan")

	// The confirmation states the source and scope the review belongs to, as
	// the normalized source the scan fetched rather than the typed text.
	var gotSource, gotScope string
	for _, f := range settings.FieldListRows(p.activeList()) {
		switch settings.FieldID(f) {
		case "confirm.reviewed_source":
			gotSource = settings.FieldGetStr(f)
		case "confirm.reviewed_scope":
			gotScope = settings.FieldGetStr(f)
		}
	}
	if gotSource != p.scannedSource || gotScope != scopeGlobal {
		t.Fatalf("review rows show source=%q scope=%q, want %q/%q",
			gotSource, gotScope, p.scannedSource, scopeGlobal)
	}

	// The hook's complete command, including its tail, is reachable.
	focusRow(t, p, "confirm.hook.pre_tool_use")
	press(t, p, keyEnter)
	if p.reviewDetail == nil {
		t.Fatal("entering the hook row did not open its full content")
	}

	// Enter inside the detail must never install.
	press(t, p, keyEnter)
	if p.busy != "" {
		t.Fatalf("Enter inside the detail started %q", p.busy)
	}

	pages, raw := reviewPages(t, p, 80, 24)
	joined := strings.Join(pages, "\n")
	if !strings.Contains(joined, "MARKER_HOOK_END") {
		t.Fatalf("the hook command's tail never rendered across %d pages:\n%s", len(pages), joined)
	}
	if !strings.Contains(joined, `\x1b[2K`) {
		t.Errorf("the hook command's control character was not shown as an escape:\n%s", joined)
	}
	for _, view := range raw {
		if strings.Contains(view, "\x1b[2K") {
			t.Errorf("the hook command's erase-line sequence reached the terminal raw:\n%q", view)
		}
	}

	// Esc returns to the entry the detail was opened from.
	press(t, p, keyEsc)
	if p.reviewDetail != nil {
		t.Fatal("esc did not return to the review")
	}
	if cur := settings.FieldListCursorRow(p.activeList()); cur == nil || settings.FieldID(cur) != "confirm.hook.pre_tool_use" {
		t.Fatalf("esc landed on %v, want the hook row it came from", cur)
	}

	// MCP arguments keep their boundaries and their escapes.
	focusRow(t, p, "confirm.mcp.srv")
	press(t, p, keyEnter)
	if p.reviewDetail == nil {
		t.Fatal("entering the MCP row did not open its full content")
	}
	pages, _ = reviewPages(t, p, 80, 24)
	joined = strings.Join(pages, "\n")
	for _, want := range []string{
		`[0] "--flag"`,
		`[1] "has space"`,
		`[2] "has\"quote"`,
		`[3] "\x1b[31mevil"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argument %s not distinct in the detail:\n%s", want, joined)
		}
	}
	press(t, p, keyEsc)

	// A long policy is readable to its end.
	focusRow(t, p, "confirm.mcppolicy.net-policy")
	press(t, p, keyEnter)
	if p.reviewDetail == nil {
		t.Fatal("entering the policy row did not open its full content")
	}
	pages, _ = reviewPages(t, p, 80, 24)
	joined = strings.Join(pages, "\n")
	if !strings.Contains(joined, "MARKER_POLICY_END") {
		t.Fatalf("the policy's tail never rendered across %d pages:\n%s", len(pages), joined)
	}
	press(t, p, keyEsc)

	// The Confirm row is still the only way to install.
	focusRow(t, p, "install.confirm")
	if cmd := p.Update(keyEnter); cmd == nil {
		t.Fatal("confirm produced no command")
	}
	if p.busy != "installing" {
		t.Fatalf("confirm left busy=%q, want installing", p.busy)
	}
}

// TestReviewDetailResizeAndNoColor pins that the detail survives a resize and
// stays readable with colour stripped: the entry title, the page hint and any
// overflow indicator are plain text, and no colour escape is emitted under
// NO_COLOR.
func TestReviewDetailResizeAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	prev := theme.Current()
	theme.Reload(theme.Load())
	t.Cleanup(func() { theme.Reload(prev) })

	p := NewPanel(t.TempDir(), t.TempDir(), true, nil)
	p.stack = append(p.stack, p.installFrame())
	p.scannedContents = &plugins.Contents{
		Hooks:       []config.HookConfig{{Event: "pre_tool_use", Command: "echo " + strings.Repeat("z", 240)}},
		MCPPolicies: map[string]string{"pol": "deny"},
	}
	p.scannedSource = "file:///tmp/plugin"
	p.scannedScope = scopeGlobal

	focusRow(t, p, "confirm.hook.pre_tool_use")
	press(t, p, keyEnter)
	if p.reviewDetail == nil {
		t.Fatal("entering the hook row did not open its full content")
	}

	for _, size := range [][2]int{{80, 24}, {40, 12}, {60, 8}} {
		view := p.View(size[0], size[1])
		plain := ansi.Strip(view)
		if lines := strings.Split(plain, "\n"); len(lines) > size[1] {
			t.Fatalf("%dx%d: detail rendered %d rows, past its budget", size[0], size[1], len(lines))
		}
		for _, want := range []string{"esc back", "PgUp/PgDn", "hook · pre_tool_use"} {
			if !strings.Contains(plain, want) {
				t.Errorf("%dx%d: %q missing from the detail:\n%s", size[0], size[1], want, plain)
			}
		}
		if p.reviewDetail == nil {
			t.Fatalf("%dx%d: the detail closed on resize", size[0], size[1])
		}
		if p.reviewDetail.offset != 0 {
			t.Fatalf("%dx%d: resize left offset %d, want the re-wrapped body pinned to the top",
				size[0], size[1], p.reviewDetail.offset)
		}
		// Only bold/faint attributes may survive NO_COLOR, the same allowance
		// the settings panel makes; any other escape is a colour the user
		// asked not to see.
		stripped := strings.NewReplacer("\x1b[1m", "", "\x1b[2m", "", "\x1b[m", "").Replace(view)
		if strings.Contains(stripped, "\x1b[") {
			t.Errorf("%dx%d: monochrome detail emitted a colour escape:\n%q", size[0], size[1], stripped)
		}
		if rows := p.reviewDetail.rows(p.reviewDetail.width); len(rows) > p.reviewDetail.height {
			if !strings.Contains(plain, "rows ") {
				t.Errorf("%dx%d: overflowing detail hid its page indicator:\n%s", size[0], size[1], plain)
			}
		}
	}

	// Paging still works after the last resize, in both directions.
	press(t, p, keyPgDown)
	paged := p.reviewDetail.offset
	if paged == 0 {
		t.Fatal("pgdown did not page the re-wrapped detail")
	}
	press(t, p, keyPgUp)
	if p.reviewDetail.offset >= paged {
		t.Fatalf("pgup left offset %d, want less than %d", p.reviewDetail.offset, paged)
	}

	press(t, p, keyEsc)
	if p.reviewDetail != nil {
		t.Fatal("esc did not return to the review")
	}
	if cur := settings.FieldListCursorRow(p.activeList()); cur == nil || settings.FieldID(cur) != "confirm.hook.pre_tool_use" {
		t.Fatalf("esc landed on %v, want the hook row it came from", cur)
	}
}
