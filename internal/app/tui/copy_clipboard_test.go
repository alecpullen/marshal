package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeClipboard records local clipboard writes.
type fakeClipboard struct {
	mu        sync.Mutex
	writes    []string
	err       error
	available bool
}

func (f *fakeClipboard) Write(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, text)
	return f.err
}

func (f *fakeClipboard) Available() bool { return f.available }

// runCopy copies the cursor node and feeds every resulting message back
// through Update, returning the model and the messages the copy produced.
func runCopy(t *testing.T, m Model) (Model, []string) {
	t.Helper()
	cmd := m.copyCursorNode()
	if cmd == nil {
		t.Fatal("no copy command")
	}
	var seen []string
	for _, msg := range flattenCmd(cmd) {
		seen = append(seen, fmt.Sprintf("%T %v", msg, msg))
		next, follow := m.Update(msg)
		m = next.(Model)
		for _, f := range flattenCmd(follow) {
			seen = append(seen, fmt.Sprintf("%T %v", f, f))
		}
	}
	return m, seen
}

func sawOSC52(seen []string, text string) bool {
	for _, s := range seen {
		if strings.Contains(strings.ToLower(s), "clipboard") && strings.Contains(s, text) {
			return true
		}
	}
	return false
}

const failingOutput = "FAIL pkg/foo main.go:12: boom"

func TestCopyUsesTheLocalClipboardWhenOneIsAvailable(t *testing.T) {
	m := browseFixture(t)
	clip := &fakeClipboard{available: true}
	m.copyWriter = clip
	m.copyRemote = func() bool { return false }
	m = pressKeys(m, "esc", "j")

	m, seen := runCopy(t, m)

	if len(clip.writes) != 1 || clip.writes[0] != failingOutput {
		t.Fatalf("local writes = %q, want the row's output", clip.writes)
	}
	if sawOSC52(seen, failingOutput) {
		t.Fatalf("a confirmed local write must not also emit OSC 52: %v", seen)
	}
	if m.flash != "Copied 1 line" {
		t.Fatalf("flash = %q", m.flash)
	}
}

func TestCopyFallsBackToOSC52WhenTheLocalWriteFails(t *testing.T) {
	m := browseFixture(t)
	clip := &fakeClipboard{available: true, err: errors.New("pbcopy: exit 1")}
	m.copyWriter = clip
	m = pressKeys(m, "esc", "j")

	m, seen := runCopy(t, m)

	if !sawOSC52(seen, failingOutput) {
		t.Fatalf("a failed local write must fall back to OSC 52: %v", seen)
	}
	if !strings.Contains(m.flash, "OSC 52") || !strings.Contains(m.flash, "pbcopy: exit 1") {
		t.Fatalf("flash should say the copy went over OSC 52 and why: %q", m.flash)
	}
}

func TestCopyOverSSHSkipsTheLocalClipboard(t *testing.T) {
	m := browseFixture(t)
	clip := &fakeClipboard{available: true}
	m.copyWriter = clip
	m.copyRemote = func() bool { return true }
	m = pressKeys(m, "esc", "j")

	_, seen := runCopy(t, m)

	if len(clip.writes) != 0 {
		t.Fatalf("over SSH the local helper writes the remote machine's clipboard: %q", clip.writes)
	}
	if !sawOSC52(seen, failingOutput) {
		t.Fatalf("over SSH the copy must go to the terminal: %v", seen)
	}
}

func TestCopyWithNoLocalHelperUsesOSC52(t *testing.T) {
	m := browseFixture(t)
	clip := &fakeClipboard{available: false}
	m.copyWriter = clip
	m = pressKeys(m, "esc", "j")

	_, seen := runCopy(t, m)

	if len(clip.writes) != 0 {
		t.Fatalf("an unavailable helper must not be run: %q", clip.writes)
	}
	if !sawOSC52(seen, failingOutput) {
		t.Fatalf("no helper: the copy must go to the terminal: %v", seen)
	}
}
