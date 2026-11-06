package settings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/presetflow"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/theme"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
)

// stripANSI removes ANSI escape sequences from text so test assertions
// can match visible content without lipgloss styling.
func stripANSI(s string) string { return theme.ANSIRe.ReplaceAllString(s, "") }

func TestBrowserFiltersAndRendersRows(t *testing.T) {
	b := NewBrowser(config.Default(), "shell")

	view := b.View(80, 12)
	if !strings.Contains(view, "Shell · Allow network") {
		t.Fatalf("filtered view should list shell keys, got:\n%s", view)
	}
	if !strings.Contains(view, "Settings") {
		t.Errorf("panel title missing")
	}
}

func TestBrowserGroupsAreDividersInOneColumn(t *testing.T) {
	b := NewBrowser(config.Default(), "")
	rows := strings.Split(stripANSI(b.View(80, 40)), "\n")
	if !strings.HasPrefix(rows[0], "╭─") {
		t.Fatalf("first row should open the frame, got %q", rows[0])
	}
	var agentRow string
	for _, r := range rows {
		if strings.HasPrefix(r, "├─ Agent") {
			agentRow = r
			break
		}
	}
	if agentRow == "" {
		t.Fatalf("single-column view should draw the Agent group as a divider, got:\n%s", strings.Join(rows, "\n"))
	}
	last := rows[len(rows)-1]
	if !strings.HasPrefix(last, "╰─ ") || !strings.Contains(last, "settings") {
		t.Fatalf("last row should be the frame's footer carrying the count, got %q", last)
	}

	twoCol := NewBrowser(config.Default(), "")
	for _, r := range strings.Split(stripANSI(twoCol.View(140, 40)), "\n") {
		if strings.HasPrefix(r, "├─ Agent") {
			t.Fatalf("two-column view must not draw group dividers, got:\n%s", twoCol.View(140, 40))
		}
	}
}

func TestBrowserToggleSavesAndEmitsChangedMsg(t *testing.T) {
	dir := t.TempDir()
	host, sess := testSeam(t, dir, "")
	path := filepath.Join(dir, ".marshal", "config.toml")
	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(sess))

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
	host, sess := testSeam(t, blockingPath, "")
	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(sess))

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
	cfgPath := filepath.Join(blockingPath, ".marshal", "config.toml")
	host, sess := testSeam(t, blockingPath, filepath.Join(dir, "user-config.toml"))

	b := NewBrowser(config.Default(), "agent.max_retries",
		WithUserConfigPath(filepath.Join(dir, "user-config.toml")), WithHost(host), WithSession(sess))
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
	cfgPath := filepath.Join(blockingPath, ".marshal", "config.toml")
	host, sess := testSeam(t, blockingPath, "")

	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(sess))
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
	b := NewBrowser(config.Default(), "remote providers")
	view := b.View(80, 24)
	if !strings.Contains(view, "Privacy · Remote providers allowed") {
		t.Fatalf("row label should show section + human title, got:\n%s", view)
	}
	if strings.Contains(view, "▸ privacy.remote_providers") {
		t.Errorf("row label must not show the dotted key as the primary label:\n%s", view)
	}
}

func TestBrowserCursorDescShowsDottedKey(t *testing.T) {
	b := NewBrowser(config.Default(), "remote providers")
	view := b.View(80, 24)
	if !strings.Contains(view, "privacy.remote_providers") {
		t.Fatalf("dotted key should appear in the description line, got:\n%s", view)
	}
}

func TestBrowserCollectionRowShowsSectionTitle(t *testing.T) {
	b := NewBrowser(config.Default(), "providers collection")
	view := b.View(80, 24)
	if !strings.Contains(view, "Providers") {
		t.Fatalf("collection row should show the section title, got:\n%s", view)
	}
}

func TestBrowserPasteRoutesToConfirmLimitInput(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"coding": {Name: "coding", Roles: map[routing.AgentRole]routing.RoleBinding{}},
	}
	b := NewBrowser(cfg, "",
		WithUserConfigPath(filepath.Join(t.TempDir(), "user-config.toml")))
	b.confirm = presetflow.NewConfirmState(presetflow.Limits{ContextSource: presetflow.SourceUnknown})
	b.confirmTarget = &pendingMaterialization{
		Profile: "coding", Role: routing.RoleImplementer,
		ProviderName: "openai", ModelID: "unknown-model",
	}

	b.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	b.Update(tea.PasteMsg{Content: "65536"})
	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	preset, ok := b.reg.Config().Models.Presets["openai/unknown-model"]
	if !ok {
		t.Fatal("confirming the overlay should materialize the selected model")
	}
	if preset.ContextWindow != 65536 {
		t.Fatalf("pasted context window = %d, want 65536", preset.ContextWindow)
	}
}

func TestBrowserEscCancelsCapabilityProbeAndRejectsLateResult(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	b := NewBrowser(cfg, "", WithHost(testHost(t, nil)))
	b.reg.st.discovered["ollama"] = []schema.ModelInfo{{ID: "qwen3"}}
	target := pendingMaterialization{
		Profile: "coding", Role: routing.RoleImplementer,
		ProviderName: "ollama", ModelID: "qwen3",
	}
	check := b.startConfirm(target)
	if check == nil || b.detectingCaps {
		t.Fatal("Ollama confirm should check probeability off the Update goroutine")
	}
	if cmd := b.Update(check()); cmd == nil || !b.detectingCaps {
		t.Fatal("Ollama confirm should start capability detection")
	}
	oldRequestID := b.capProbeID
	b.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if b.confirm != nil || b.detectingCaps {
		t.Fatal("Esc should cancel the active capability probe")
	}

	check = b.startConfirm(target)
	if check == nil || b.detectingCaps {
		t.Fatal("restarting confirm should check probeability again")
	}
	if cmd := b.Update(check()); cmd == nil || !b.detectingCaps {
		t.Fatal("restarting confirm should start a new capability probe")
	}
	b.Update(presetflow.CapabilityProbedMsg{Provider: "ollama", Model: "qwen3", RequestID: oldRequestID})
	if !b.detectingCaps {
		t.Fatal("late result from the canceled request must not clear new detection")
	}
}

// canProbeGateHost answers CanProbeModel only once release is closed, and
// records the goroutine the call ran on. Every other Host method is unused by
// these tests, so the embedded interface stays nil.
type canProbeGateHost struct {
	acpclient.Host

	started chan struct{}
	release chan struct{}
	answer  bool

	once sync.Once
	gid  uint64
}

func newCanProbeGateHost(answer bool) *canProbeGateHost {
	return &canProbeGateHost{started: make(chan struct{}), release: make(chan struct{}), answer: answer}
}

func (h *canProbeGateHost) CanProbeModel(context.Context, string, config.ProviderConfig) bool {
	h.once.Do(func() {
		h.gid = currentGoroutineID()
		close(h.started)
	})
	<-h.release
	return h.answer
}

// canProbeGoroutine is the goroutine the gated check ran on. Only read it after
// started is closed.
func (h *canProbeGateHost) canProbeGoroutine() uint64 { return h.gid }

// currentGoroutineID parses the id out of the runtime's stack header
// ("goroutine 18 [running]:").
func currentGoroutineID() uint64 {
	buf := make([]byte, 64)
	fields := strings.Fields(string(buf[:runtime.Stack(buf, false)]))
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestTheProbeCheckRunsOffTheUpdateGoroutine pins startConfirm's CanProbeModel
// check to its own command: opening the confirm screen must not wait on the
// host, "detecting…" must appear only once a yes answer arrives, and an answer
// for a screen Esc abandoned in the meantime is dropped.
func TestTheProbeCheckRunsOffTheUpdateGoroutine(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	host := newCanProbeGateHost(true)
	b := NewBrowser(cfg, "", WithHost(host))
	b.reg.st.discovered["ollama"] = []schema.ModelInfo{{ID: "qwen3"}}
	target := pendingMaterialization{
		Profile: "coding", Role: routing.RoleImplementer,
		ProviderName: "ollama", ModelID: "qwen3",
	}

	updateGID := currentGoroutineID()
	startConfirmCmd := make(chan tea.Cmd, 1)
	go func() { startConfirmCmd <- b.startConfirm(target) }()
	var check tea.Cmd
	select {
	case check = <-startConfirmCmd:
	case <-time.After(2 * time.Second):
		t.Fatal("startConfirm blocked: the can-probe check must not run inline")
	}
	if check == nil {
		t.Fatal("a browser with a host should ask it whether the model can be probed")
	}
	if b.detectingCaps {
		t.Fatal(`"detecting…" must not appear before the host answers`)
	}
	if view := stripANSI(b.View(80, 24)); strings.Contains(view, "detecting") {
		t.Fatalf(`confirm screen should still render its limits form, got:\n%s`, view)
	}
	select {
	case <-host.started:
		t.Fatal("startConfirm called CanProbeModel on the Update goroutine")
	default:
	}

	answer := make(chan tea.Msg, 1)
	go func() { answer <- check() }()
	awaitSignal(t, host.started, "the can-probe check to run")
	if gid := host.canProbeGoroutine(); gid == updateGID {
		t.Fatalf("CanProbeModel ran on the Update goroutine (%d)", gid)
	}

	// Esc abandons the screen while the host is still thinking.
	b.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if b.confirm != nil || b.detectingCaps {
		t.Fatal("Esc should close the confirm screen")
	}
	close(host.release)
	select {
	case stale := <-answer:
		if next := b.Update(stale); next != nil || b.detectingCaps {
			t.Fatal("a can-probe answer for an abandoned confirm must not start a probe")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the can-probe answer")
	}

	// A confirm that is still open sees the yes answer, and only then detects.
	openHost := newCanProbeGateHost(true)
	close(openHost.release)
	b.reg.st.host = openHost
	check = b.startConfirm(target)
	if check == nil || b.detectingCaps {
		t.Fatal("reopened confirm should check probeability off the Update goroutine")
	}
	if cmd := b.Update(check()); cmd == nil || !b.detectingCaps {
		t.Fatal("a yes answer should start capability detection")
	}
	if view := stripANSI(b.View(80, 24)); !strings.Contains(view, "detecting") {
		t.Fatalf(`"detecting…" should appear once the host answered yes, got:\n%s`, view)
	}
}

func TestBrowserViewHonorsMaxHeight(t *testing.T) {
	for _, pickerOpen := range []bool{false, true} {
		b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "providers")
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
	b := NewBrowser(config.Default(), "providers")
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
	b := NewBrowser(config.Default(), "")

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc at root must emit close")
	}
	if _, ok := cmd().(BrowserClosedMsg); !ok {
		t.Fatal("want BrowserClosedMsg")
	}
}

func TestUnfilteredBrowserGroupsBySection(t *testing.T) {
	b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "shell")
	// When filtered, the matchedFields path should not produce kindHeader rows.
	for _, row := range b.list.Rows() {
		if row.Kind == kindHeader {
			t.Fatalf("filtered browser should not have header rows, got id=%q", row.ID)
		}
	}
}

func TestCursorSkipsHeaders(t *testing.T) {
	b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "")
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
	b := NewBrowser(config.Default(), "")
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

// TestGlobalRowWithoutHostFallsThroughToProjectSave pins userPath()'s host-less
// rule: a browser built with a session but no host has nowhere to write the
// user config, so a global-target row falls through to the project save rather
// than failing or silently doing nothing.
func TestGlobalRowWithoutHostFallsThroughToProjectSave(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ".marshal", "config.toml")
	_, sess := testSeam(t, dir, "")
	b := NewBrowser(config.Default(), "", WithSession(sess))

	drillBrowserToRow(t, b, "Theme")
	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd == nil {
		t.Fatal("cycling an enum must produce a command")
	}
	changed, ok := cmd().(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", cmd())
	}
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}
	if changed.GlobalTarget {
		t.Fatal("a global row without a host must take the project path")
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("project config was not written: %v", err)
	}
	if !strings.Contains(string(data), "theme") {
		t.Fatalf("theme change missing from project config:\n%s", data)
	}
}

func TestGlobalTargetRowWritesUserConfigOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath := config.UserConfigPath(home)
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ".marshal", "config.toml")
	host, sess := testSeam(t, dir, userPath)
	b := NewBrowser(config.Default(), "", WithUserConfigPath(userPath), WithHost(host), WithSession(sess))

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
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ".marshal", "config.toml")
	host, sess := testSeam(t, dir, userPath)
	b := NewBrowser(config.Default(), "", WithUserConfigPath(userPath), WithHost(host), WithSession(sess))

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

func TestFlushChangesWritesProvidersAndPresetsGlobally(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath := config.UserConfigPath(home)
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ".marshal", "config.toml")
	host, sess := testSeam(t, dir, userPath)
	b := NewBrowser(config.Default(), "", WithUserConfigPath(userPath), WithHost(host), WithSession(sess))

	// Simulate a providers-drill edit: mutate the working config, then flush.
	cfg := b.reg.Config()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen3": {Name: "ollama/qwen3", Provider: "ollama", Model: "qwen3"},
	}
	b.reg.st.cfg = cfg

	cmd := b.flushChanges(nil, true)
	if cmd == nil {
		t.Fatal("flush should produce a ChangedMsg command")
	}
	changed, ok := cmd().(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", cmd())
	}
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}

	userData, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userData), "localhost:11434") || !strings.Contains(string(userData), "qwen3") {
		t.Fatalf("provider/preset missing from user config:\n%s", userData)
	}
	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectData), "localhost:11434") || strings.Contains(string(projectData), "qwen3") {
		t.Fatalf("provider/preset leaked into project config:\n%s", projectData)
	}
}

func TestFlushChangesWithoutProviderChangesSkipsUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath := config.UserConfigPath(home)
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ".marshal", "config.toml")
	host, sess := testSeam(t, dir, userPath)
	b := NewBrowser(config.Default(), "", WithUserConfigPath(userPath), WithHost(host), WithSession(sess))

	// A benign project-scope edit must not rewrite the user config.
	b.reg.st.cfg.Agent.PlanFirst = true
	cmd := b.flushChanges(nil, true)
	if cmd == nil {
		t.Fatal("flush should produce a ChangedMsg command")
	}
	changed, ok := cmd().(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", cmd())
	}
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}
	if _, err := os.Stat(userPath); !os.IsNotExist(err) {
		t.Fatalf("user config must not be written for a project-only change: %v", err)
	}
	if _, err := os.Stat(projectPath); err != nil {
		t.Fatalf("project config not written: %v", err)
	}
}

// TestBrowserRenameKeepsTheProvidersKey pins that a provider renamed in the
// browser takes its stored key with it: the browser opens on the session's
// masked config, so the mask under the new name is only restorable if the
// save is told which name it stood for. The old entry must not survive.
func TestBrowserRenameKeepsTheProvidersKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath := config.UserConfigPath(home)
	dir := t.TempDir()
	// A production seam refreshes the layers after every user-config write, so
	// the project save that follows a rename resolves the key by its new name.
	// Without that refresh a rename would leave the project save with a mask
	// nothing stored can answer — an artifact of the fixture, not the flow.
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	host, sess := inproc.New(inproc.Deps{
		State:          state,
		Auth:           newMemAuth(),
		UserConfigPath: userPath,
		ReloadLayers: func() (config.Layers, bool) {
			loaded, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: dir})
			if err != nil {
				return config.Layers{}, false
			}
			return config.Layers{Default: config.Default(), User: loaded, Merged: loaded}, true
		},
	})
	const literalKey = "sk-literal-old-key-1234"
	// A second provider whose key shares the last four characters makes the
	// mask ambiguous on its own: passing no name map then fails the write with
	// the pinned error, so this test pins the plumbing, not just the restore's
	// unique-suffix fallback.
	const otherKey = "sk-other-old-key-1234"
	stored := config.Default()
	stored.Providers = map[string]config.ProviderConfig{
		"old":   {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: literalKey},
		"other": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: otherKey},
	}
	if err := sess.ApplyConfig(context.Background(), stored); err != nil {
		t.Fatalf("apply config: %v", err)
	}

	// The browser opens on the masked copy the session reports, exactly as a
	// real one does.
	b := NewBrowser(sess.Config(), "", WithUserConfigPath(userPath), WithHost(host), WithSession(sess))

	var nameRow *field
	for _, r := range providersFrame(b.reg.st).List.Rows()[0].Build().List.Rows() {
		if r.Title == "Name" {
			nameRow = r
			break
		}
	}
	if nameRow == nil {
		t.Fatal("provider detail must have a Name row")
	}
	if err := nameRow.SetStr("new"); err != nil {
		t.Fatalf("rename err = %v", err)
	}

	cmd := b.flushChanges(nil, true)
	if cmd == nil {
		t.Fatal("flush should produce a ChangedMsg command")
	}
	changed, ok := cmd().(ChangedMsg)
	if !ok {
		t.Fatalf("want ChangedMsg, got %T", cmd())
	}
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}

	cfg, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["new"].APIKey; got != literalKey {
		t.Fatalf("providers.new.api_key = %q, want the literal key %q", got, literalKey)
	}
	if got := cfg.Providers["other"].APIKey; got != otherKey {
		t.Fatalf("providers.other.api_key = %q, want the untouched key %q", got, otherKey)
	}
	if _, ok := cfg.Providers["old"]; ok {
		t.Fatal("the old provider name survived the rename")
	}
}

// TestBrowserRenameChainsCollapse pins the map's chain semantics: a → b → c
// records {c: a}, while a → b → a records nothing.
func TestBrowserRenameChainsCollapse(t *testing.T) {
	newNameRow := func(t *testing.T) (*state, *field) {
		t.Helper()
		cfg := config.Default()
		cfg.Providers = map[string]config.ProviderConfig{
			"a": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		}
		st := newState(cfg)
		var nameRow *field
		for _, r := range providersFrame(st).List.Rows()[0].Build().List.Rows() {
			if r.Title == "Name" {
				nameRow = r
				break
			}
		}
		if nameRow == nil {
			t.Fatal("provider detail must have a Name row")
		}
		return st, nameRow
	}

	t.Run("a to b to c records c from a", func(t *testing.T) {
		st, nameRow := newNameRow(t)
		for _, to := range []string{"b", "c"} {
			if err := nameRow.SetStr(to); err != nil {
				t.Fatalf("rename to %q: %v", to, err)
			}
		}
		if len(st.renamed) != 1 || st.renamed["c"] != "a" {
			t.Fatalf("renamed = %v, want {c: a}", st.renamed)
		}
	})

	t.Run("a to b to a records nothing", func(t *testing.T) {
		st, nameRow := newNameRow(t)
		for _, to := range []string{"b", "a"} {
			if err := nameRow.SetStr(to); err != nil {
				t.Fatalf("rename to %q: %v", to, err)
			}
		}
		if len(st.renamed) != 0 {
			t.Fatalf("renamed = %v, want an empty map", st.renamed)
		}
	})
}

func TestHintsShowWriteTarget(t *testing.T) {
	b := NewBrowser(config.Default(), "")
	drillBrowserToRow(t, b, "Theme")
	hints := rowHints(b, b.stack == nil)
	if !strings.Contains(hints, "user config") {
		t.Fatalf("hints should name the write target, got %q", hints)
	}
}

func TestDetailPaneShowsProvenance(t *testing.T) {
	b := NewBrowser(config.Default(), "",
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

func TestUnfilteredBrowserHidesAdvancedSections(t *testing.T) {
	b := NewBrowser(config.Default(), "")
	out := stripANSI(b.View(100, 40))
	if strings.Contains(out, "Profiles") {
		t.Fatalf("advanced Profiles section should not be listed:\n%s", out)
	}
}

func TestSearchReachesAdvancedSections(t *testing.T) {
	b := NewBrowser(config.Default(), "profile")
	out := stripANSI(b.View(100, 40))
	if !strings.Contains(out, "Profiles") {
		t.Fatalf("search should still reach the advanced Profiles section:\n%s", out)
	}
}

func TestBrowserTwoColumnShowsDescInDetailPaneWhileDrilled(t *testing.T) {
	b := NewBrowser(config.Default(), "providers")
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

// ── Auth dependency propagation ──────────────────────────────────────
//
// A settings probe must use the credential/OAuth dependency the panel was
// built with. Otherwise a probe from an isolated app (tests, an embedded
// runtime) would silently reach the developer's real keychain.

// memAuth is a test AuthResolver. It records every engine request so a test
// can prove which dependency a probe used.
type memAuth struct {
	store   *credentials.MemStore
	engines []string
	// engineErr, when set, fails CodexOAuthEngine — standing in for an OS
	// keychain that cannot be opened.
	engineErr error
}

func newMemAuth() *memAuth { return &memAuth{store: credentials.NewMemStore()} }

// testHost builds a client host over a fresh test state whose credential/OAuth
// dependency is auth, or a memory resolver when auth is nil. Probes launched
// through it never reach the developer's keychain.
func testHost(t *testing.T, auth provider.AuthResolver) acpclient.Host {
	t.Helper()
	if auth == nil {
		auth = newMemAuth()
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	host, _ := inproc.New(inproc.Deps{State: state, Auth: auth})
	return host
}

// testSeam builds the host and session a panel writes through. The session's
// working directory is workDir, so a project save lands in
// workDir/.marshal/config.toml; userConfigPath is the host's user config file.
// The host carries the same memory resolver testHost uses, so nothing reaches
// the developer's keychain.
func testSeam(t *testing.T, workDir, userConfigPath string) (acpclient.Host, acpclient.Session) {
	t.Helper()
	state := session.New(config.Default(), workDir, time.Unix(100, 0), session.Persistence{})
	host, sess := inproc.New(inproc.Deps{
		State:          state,
		Auth:           newMemAuth(),
		UserConfigPath: userConfigPath,
	})
	return host, sess
}

func (a *memAuth) OpenStore(string) (credentials.Store, error) {
	if a.store == nil {
		return nil, credentials.ErrKeyringUnavailable
	}
	return a.store, nil
}

func (a *memAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.engines = append(a.engines, name)
	if a.engineErr != nil {
		return nil, a.engineErr
	}
	return provider.NewCodexOAuthEngineWithStore(name, a.store)
}

// probeCmdResult executes cmd and returns the probe result it produced,
// unwrapping the tea.Batch the frame helpers may wrap it in.
func probeCmdResult(t *testing.T, cmd tea.Cmd) probe.ResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a probe command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		if len(batch) != 1 {
			t.Fatalf("batch size = %d, want 1", len(batch))
		}
		msg = batch[0]()
	}
	res, ok := msg.(probe.ResultMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want probe.ResultMsg", msg)
	}
	return res
}

// TestBrowserAuthResolverReachesFrameProbes pins step 4: a probe launched from
// a settings frame must build its provider through the panel's resolver, not
// the process-wide one.
func TestBrowserAuthResolverReachesFrameProbes(t *testing.T) {
	auth := newMemAuth()
	auth.engineErr = fmt.Errorf("injected: %w", credentials.ErrKeyringUnavailable)

	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"codex": {
			Type:     "openai_codex",
			Template: "openai-codex",
			BaseURL:  "https://chatgpt.com/backend-api",
			Auth:     "oauth",
		},
	}
	b := NewBrowser(cfg, "", WithHost(testHost(t, auth)))

	res := probeCmdResult(t, b.reg.st.probeCmd("test.connection", "codex", cfg.Providers["codex"]))
	if res.Err == nil {
		t.Fatal("the injected resolver failure must reach the probe result")
	}
	if !errors.Is(res.Err, credentials.ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want the injected keychain failure", res.Err)
	}
	if len(auth.engines) != 1 || auth.engines[0] != "codex" {
		t.Fatalf("engine requests = %v, want one for codex", auth.engines)
	}
}

// TestProbeAllProvidersUsesInjectedResolver: the profiles frame's bulk probe
// goes through the same state, so the injected dependency must cover it too.
func TestProbeAllProvidersUsesInjectedResolver(t *testing.T) {
	auth := newMemAuth()
	auth.engineErr = fmt.Errorf("injected: %w", credentials.ErrKeyringUnavailable)

	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"codex": {Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api"},
	}
	s := newState(cfg)
	s.host = testHost(t, auth)

	probeAllProviders(s, false)

	cmd := s.takePendingCmd()
	if cmd == nil {
		t.Fatal("expected a queued probe command")
	}
	// Executing the probe is what constructs the provider; only then can the
	// resolver's engine be observed.
	res := probeCmdResult(t, cmd)
	if res.Err == nil {
		t.Fatal("the injected resolver failure must reach the probe result")
	}
	if !errors.Is(res.Err, credentials.ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want the injected keychain failure", res.Err)
	}
	if len(auth.engines) != 1 {
		t.Fatalf("engine requests = %v, want the profile probe to use the injected resolver", auth.engines)
	}
}

// TestRebuiltBrowserKeepsTheHost: refreshOpenSettingsBrowser replaces the
// whole BrowserPanel, so the replacement must be built with the same host —
// otherwise a probe after /set would ask nothing.
func TestRebuiltBrowserKeepsTheHost(t *testing.T) {
	cfg := config.Default()
	host := testHost(t, nil)
	first := NewBrowser(cfg, "", WithHost(host))

	// Stand in for the model's refresh: rebuild the panel the way the model
	// does, then confirm the dependency survived.
	second := NewBrowser(cfg, first.FilterValue(), WithHost(host))
	if second.reg.st.host != first.reg.st.host {
		t.Fatal("a rebuilt browser must carry the same host")
	}
	if second.reg.st.host != host {
		t.Fatal("a rebuilt browser must carry the host it was built with")
	}
}
