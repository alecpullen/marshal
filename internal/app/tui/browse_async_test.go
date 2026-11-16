package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/tui/inspector"
)

// blockStack stands in for a slow daemon: every StackNode the model makes parks
// until the test releases it, or until the caller's context is cancelled.
type blockStack struct {
	started chan string
	release chan struct{}
	cancel  chan struct{}
	err     error

	mu    sync.Mutex
	calls []string
}

func newBlockStack() *blockStack {
	return &blockStack{
		started: make(chan string, 16),
		release: make(chan struct{}),
		cancel:  make(chan struct{}, 16),
	}
}

// open releases every parked call.
func (b *blockStack) open() { close(b.release) }

func (b *blockStack) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *blockStack) called() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

// blockSession wraps a session whose StackNode parks on the blockStack.
type blockSession struct {
	acpclient.Session
	b *blockStack
}

func (s *blockSession) StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error) {
	s.b.mu.Lock()
	s.b.calls = append(s.b.calls, nodeID)
	s.b.mu.Unlock()
	select {
	case s.b.started <- nodeID:
	default:
	}
	select {
	case <-s.b.release:
	case <-ctx.Done():
		s.b.cancel <- struct{}{}
	}
	if s.b.err != nil {
		return acpwire.StackNodeResult{}, s.b.err
	}
	return s.Session.StackNode(ctx, nodeID, subagentID)
}

// withSlowDaemon replaces m's session with one that parks its StackNode calls.
// The transcript has already been built, so no render-time fetch is in flight
// when it is installed.
func withSlowDaemon(m Model) (Model, *blockStack) {
	b := newBlockStack()
	m.sess = &blockSession{Session: m.sess, b: b}
	// A node the transcript has already drawn has its detail cached, and a cached
	// detail is served without a call. Dropping the cache models a node whose
	// detail was never rendered — the case that needs the session.
	m.details = nil
	m.detailAsked = nil
	return m, b
}

// runCmdsOrNone runs a command tree and returns the messages that arrive before
// the deadline. It runs each member of a batch on its own, so a member that
// never returns — a clipboard write with no terminal on the other end, a flash's
// clear timer — costs one short wait and not the messages of its siblings.
func runCmdsOrNone(cmd tea.Cmd, deadline time.Time) []tea.Msg {
	if cmd == nil {
		return nil
	}
	wait := time.Until(deadline)
	if wait > 150*time.Millisecond {
		wait = 150 * time.Millisecond
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-out:
	case <-time.After(wait):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, inner := range batch {
			msgs = append(msgs, runCmdsOrNone(inner, deadline)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

// updateBounded runs one Update and fails if it has not returned in two
// seconds, which is what a call on the Update goroutine would cost.
func updateBounded(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	type result struct {
		m   Model
		cmd tea.Cmd
	}
	done := make(chan result, 1)
	go func() {
		updated, cmd := m.Update(msg)
		next, ok := updated.(Model)
		if !ok {
			next = *updated.(*Model)
		}
		done <- result{m: next, cmd: cmd}
	}()
	select {
	case r := <-done:
		return r.m, r.cmd
	case <-time.After(2 * time.Second):
		t.Fatal("Update blocked on the session: a browse action ran its RPC on the Update goroutine")
		return m, nil
	}
}

// runBrowseAction drives one browse action and everything it starts, feeding
// each message back through Update, and returns the model and every message the
// action produced. A detail a render asked for is not fed back: the driver's
// own re-render would ask again, which is the render path's business and not
// this action's. A command that does not finish in time (a flash clear timer, a
// clipboard write with no terminal) is skipped rather than waited for.
func runBrowseAction(t *testing.T, m Model, cmds ...tea.Cmd) (Model, []tea.Msg) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var seen []tea.Msg
	pending := append([]tea.Cmd(nil), cmds...)
	pending = append(pending, m.serveCalls())
	for round := 0; len(pending) > 0 && round < 200; round++ {
		if !time.Now().Before(deadline) {
			break
		}
		next := pending[0]
		pending = pending[1:]
		for _, msg := range runCmdsOrNone(next, deadline) {
			seen = append(seen, msg)
			if _, isDetail := msg.(detailMsg); isDetail {
				continue
			}
			updated, follow := m.Update(msg)
			m = mustModel(t, updated)
			pending = append(pending, follow)
		}
		pending = append(pending, m.serveCalls())
	}
	return m, seen
}

// warmDetail fetches ids' details through the call queue, so a test that reads a
// node's copy text, file or inspector detail reads the same cache a browse
// action fills.
func warmDetail(t *testing.T, m *Model, ids ...nodeID) {
	t.Helper()
	cmd := m.queueBrowse("", ids, func(*Model) tea.Cmd { return nil })
	*m = settleCalls(t, *m, cmd)
}

// sawText reports whether any collected message carries text.
func sawText(msgs []tea.Msg, text string) bool {
	for _, msg := range msgs {
		if strings.Contains(fmt.Sprintf("%v", msg), text) {
			return true
		}
	}
	return false
}

// browseOnAFailingToolRow is a browse model whose cursor sits on the failing
// test's tool row.
func browseOnAFailingToolRow(t *testing.T) Model {
	t.Helper()
	m := browseFixture(t)
	return pressKeys(m, "esc", "j")
}

func TestBrowseInspectDoesNotBlockTheUpdateLoop(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)

	m, cmd := updateBounded(t, m, keyOf("i"))
	if m.dock.IsOpen() {
		t.Fatal("the inspector opened before the detail landed")
	}
	if m.browsePending == "" {
		t.Fatal("an outstanding inspect must show a pending indicator")
	}
	if cmd == nil {
		t.Fatal("the queued fetch must be a command")
	}

	// The update loop is still live: resize, a key press and a quit all route.
	m, _ = updateBounded(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.rawWidth != 100 {
		t.Fatalf("rawWidth = %d after a resize during an outstanding inspect, want 100", m.rawWidth)
	}
	m, _ = updateBounded(t, m, keyOf("k"))
	if m.browsePending == "" {
		t.Fatal("the resize or the key press cleared the outstanding inspect")
	}

	block.open()
	m, _ = runBrowseAction(t, m, cmd)
	if _, ok := m.dock.Panel().(*inspector.Panel); !ok {
		t.Fatal("the inspector did not open when the detail landed")
	}
	if m.browsePending != "" {
		t.Fatalf("the pending indicator survived the result: %q", m.browsePending)
	}
}

func TestBrowseCopyDoesNotBlockTheUpdateLoop(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)

	m, cmd := updateBounded(t, m, keyOf("y"))
	if !strings.Contains(m.sessionRightSegment(), "copying") {
		t.Fatalf("no pending indicator while the copy waits: %q", m.sessionRightSegment())
	}
	if cmd == nil {
		t.Fatal("the queued fetch must be a command")
	}
	block.open()
	m, msgs := runBrowseAction(t, m, cmd)
	if !sawText(msgs, "FAIL pkg/foo main.go:12: boom") {
		t.Fatal("the copy did not carry the row's output once the detail landed")
	}
	if m.browsePending != "" {
		t.Fatalf("the pending indicator survived the copy: %q", m.browsePending)
	}
}

func TestBrowseOpenDoesNotBlockTheUpdateLoop(t *testing.T) {
	t.Setenv("EDITOR", "true")
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)

	m, cmd := updateBounded(t, m, keyOf("o"))
	if !strings.Contains(m.sessionRightSegment(), "opening") {
		t.Fatalf("no pending indicator while the open waits: %q", m.sessionRightSegment())
	}
	var editorRan bool
	for _, msg := range flattenCmd(cmd) {
		if _, ok := msg.(editorDoneMsg); ok {
			editorRan = true
		}
	}
	if editorRan {
		t.Fatal("the editor ran before the detail landed")
	}
	block.open()
	m, _ = runBrowseAction(t, m, cmd)
	if m.browsePending != "" {
		t.Fatalf("the pending indicator survived the open: %q", m.browsePending)
	}
}

func TestBrowseDetailResultsAreAppliedExactlyOnce(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)

	m, cmd := updateBounded(t, m, keyOf("y"))
	block.open()
	callMsg := callDoneFrom(t, cmd)
	updated, follow := m.Update(callMsg)
	m = mustModel(t, updated)
	msgs := flattenCmd(follow)
	if !sawText(msgs, "FAIL pkg/foo main.go:12: boom") {
		t.Fatal("the copy did not land")
	}
	// The same result fed again changes nothing: no second copy message.
	updated, again := m.Update(callMsg)
	m = mustModel(t, updated)
	if sawText(flattenCmd(again), "FAIL pkg/foo main.go:12: boom") {
		t.Fatal("replaying the result copied a second time")
	}
	_ = m
}

func TestBrowseStaleDetailResultIsDropped(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	first := m.cursor
	m, block := withSlowDaemon(m)

	m, cmdA := updateBounded(t, m, keyOf("i"))
	m, _ = updateBounded(t, m, keyOf("k"))
	second := m.cursor
	if second == first {
		t.Fatal("the cursor did not move")
	}
	m, cmdB := updateBounded(t, m, keyOf("i"))
	block.open()
	m, _ = runBrowseAction(t, m, cmdA, cmdB)

	if _, ok := m.dock.Panel().(*inspector.Panel); !ok {
		t.Fatal("the inspector did not open for the newest action")
	}
	// The newest action is the one that applied; the older result was dropped
	// rather than opening its own node.
	if m.browseApplied != m.browseSeq {
		t.Fatalf("applied action = %d, newest = %d", m.browseApplied, m.browseSeq)
	}
	if _, ok := m.nodeDetail(second); !ok {
		t.Fatal("the newest node's detail was not cached by its action")
	}
}

func TestBrowseDetailFailureIsSurfaced(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)
	block.err = errors.New("daemon gone")

	m, cmd := updateBounded(t, m, keyOf("i"))
	block.open()
	m, _ = runBrowseAction(t, m, cmd)
	if m.dock.IsOpen() {
		t.Fatal("a failed detail must not open the inspector")
	}
	if !m.flashActive() || !strings.Contains(m.flash, "daemon gone") {
		t.Fatalf("the failure was not surfaced, flash = %q", m.flash)
	}
	if m.browsePending != "" {
		t.Fatalf("the pending indicator survived the failure: %q", m.browsePending)
	}
}

func TestBrowsePendingIndicatorClearsWhenTheResultLands(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)

	m, cmd := updateBounded(t, m, keyOf("i"))
	if !strings.Contains(m.sessionRightSegment(), "inspecting") {
		t.Fatalf("the status line does not show the pending inspect: %q", m.sessionRightSegment())
	}
	block.open()
	m, _ = runBrowseAction(t, m, cmd)
	if strings.Contains(m.sessionRightSegment(), "inspecting") {
		t.Fatalf("the pending indicator is still shown: %q", m.sessionRightSegment())
	}
}

func TestBrowseDetailIsCancelledOnQuit(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)

	m, cmd := updateBounded(t, m, keyOf("i"))
	go func() { _ = cmd() }()
	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the browse call never started")
	}
	quit := m.finishQuit()
	if quit != nil {
		t.Fatal("a quit with a call in flight must wait for it, not quit at once")
	}
	select {
	case <-block.cancel:
	case <-time.After(2 * time.Second):
		t.Fatal("the quit did not cancel the outstanding browse call")
	}
}

func TestBrowseInspectorNavigationDoesNotBlockTheUpdateLoop(t *testing.T) {
	m := browseOnAFailingToolRow(t)
	m, block := withSlowDaemon(m)
	m.dock.Open(inspector.New(inspector.Detail{Title: "step", Subject: "x"}))

	m, cmd := updateBounded(t, m, inspector.NavigateMsg{Delta: 1})
	block.open()
	m, _ = runBrowseAction(t, m, cmd)
	if _, ok := m.dock.Panel().(*inspector.Panel); !ok {
		t.Fatal("the inspector closed instead of re-pointing")
	}
}

// callDoneFrom runs cmd until the converted call's result message comes back,
// so a test can feed the same result twice.
func callDoneFrom(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msgs := runCmdsBounded(t, cmd, time.Now().Add(5*time.Second))
	for _, msg := range msgs {
		if _, ok := msg.(callDoneMsg); ok {
			return msg
		}
	}
	t.Fatalf("the command produced no call result: %v", msgs)
	return nil
}
