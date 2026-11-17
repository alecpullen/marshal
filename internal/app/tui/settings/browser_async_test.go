package settings

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
)

// saveOutcome drives a commit's command tree to the ChangedMsg it produces and
// returns the SaveDoneMsg that produced it. The write itself runs off the
// Update goroutine (AUD-10), so the panel applies its own result first and the
// ChangedMsg follows one hop later.
func saveOutcome(t *testing.T, b *BrowserPanel, cmd tea.Cmd) (ChangedMsg, SaveDoneMsg) {
	t.Helper()
	var changed []ChangedMsg
	var done SaveDoneMsg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, inner := range msg {
				walk(inner)
			}
		case SaveDoneMsg:
			done = msg
			walk(b.Update(msg))
		case ChangedMsg:
			changed = append(changed, msg)
		}
	}
	walk(cmd)
	if len(changed) != 1 {
		t.Fatalf("the commit produced %d ChangedMsgs, want one", len(changed))
	}
	return changed[0], done
}

// changedNow is saveOutcome's ChangedMsg alone.
func changedNow(t *testing.T, b *BrowserPanel, cmd tea.Cmd) ChangedMsg {
	t.Helper()
	changed, _ := saveOutcome(t, b, cmd)
	return changed
}

// runBounded returns f's command, failing if f has not returned in two seconds:
// that is what a commit that ran its write on the Update goroutine would cost.
func runBounded(t *testing.T, f func() tea.Cmd) tea.Cmd {
	t.Helper()
	done := make(chan tea.Cmd, 1)
	go func() { done <- f() }()
	select {
	case cmd := <-done:
		return cmd
	case <-time.After(2 * time.Second):
		t.Fatal("the settings commit blocked on the session: a write ran on the Update goroutine")
		return nil
	}
}

// blockingSaveSession parks SaveProjectConfig until the test releases it, or
// until the caller's context is cancelled.
type blockingSaveSession struct {
	acpclient.Session
	started chan struct{}
	release chan struct{}
	sawCtx  chan struct{}
}

func newBlockingSaveSession(sess acpclient.Session) *blockingSaveSession {
	return &blockingSaveSession{
		Session: sess,
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
		sawCtx:  make(chan struct{}, 4),
	}
}

func (s *blockingSaveSession) SaveProjectConfig(ctx context.Context, cfg config.Config) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		s.sawCtx <- struct{}{}
		return ctx.Err()
	}
	return s.Session.SaveProjectConfig(ctx, cfg)
}

func TestSettingsSaveDoesNotBlockTheUpdateGoroutine(t *testing.T) {
	dir := t.TempDir()
	host, sess := testSeam(t, dir, "")
	block := newBlockingSaveSession(sess)
	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(block))

	cmd := runBounded(t, func() tea.Cmd {
		return b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	})
	if cmd == nil {
		t.Fatal("a commit must return a command")
	}
	if !b.saving {
		t.Fatal("the panel does not report a write in flight")
	}
	if !strings.Contains(b.View(120, 30), "saving…") {
		t.Fatal("the panel does not show the saving indicator")
	}
	// The panel is still responsive: navigation is a local no-op, not a wait.
	if next := runBounded(t, func() tea.Cmd { return b.Update(tea.KeyPressMsg{Code: tea.KeyDown}) }); next != nil {
		if _, ok := next().(ChangedMsg); ok {
			t.Fatal("navigation during a save committed again")
		}
	}

	close(block.release)
	changed, _ := saveOutcome(t, b, cmd)
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}
	if b.saving {
		t.Fatal("the saving state survived the result")
	}
}

func TestSettingsSaveResultIsAppliedExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	host, sess := testSeam(t, dir, "")
	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(sess))

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	changed, done := saveOutcome(t, b, cmd)
	if changed.SaveErr != nil {
		t.Fatalf("save failed: %v", changed.SaveErr)
	}
	if len(changed.Receipts) == 0 {
		t.Fatal("a successful save must emit a receipt")
	}
	// Replaying the result changes nothing: the panel has already applied it.
	if next := b.Update(done); next != nil {
		if msg := next(); msg != nil {
			if _, ok := msg.(ChangedMsg); ok {
				t.Fatal("replaying the save result committed a second time")
			}
		}
	}
}

func TestSettingsSaveFailureIsSurfacedOnce(t *testing.T) {
	blockingPath := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(blockingPath, nil, 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	host, sess := testSeam(t, blockingPath, "")
	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(sess))

	cmd := b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	changed := changedNow(t, b, cmd)
	if changed.SaveErr == nil {
		t.Fatal("save should fail")
	}
	if len(changed.Receipts) != 0 {
		t.Fatalf("a failed save must not emit receipts: %v", changed.Receipts)
	}
	if b.saving {
		t.Fatal("the saving state survived the failure")
	}
	if !b.savePending {
		t.Fatal("a failed save must leave savePending set for the retry")
	}
}

func TestSettingsSaveIsCancelledWithThePanelContext(t *testing.T) {
	dir := t.TempDir()
	host, sess := testSeam(t, dir, "")
	block := newBlockingSaveSession(sess)
	ctx, cancel := context.WithCancel(context.Background())
	b := NewBrowser(config.Default(), "shell.allow_network", WithHost(host), WithSession(block), WithContext(ctx))

	cmd := runBounded(t, func() tea.Cmd {
		return b.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	})
	cancel()
	changed := changedNow(t, b, cmd)
	if changed.SaveErr == nil {
		t.Fatal("a cancelled save must report its failure, not a success")
	}
	select {
	case <-block.sawCtx:
	default:
		t.Fatal("the write did not observe the panel's cancelled context")
	}
	if b.saving {
		t.Fatal("the saving state survived the cancellation")
	}
}
