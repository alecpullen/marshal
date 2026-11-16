package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// hooksBrowser builds a browser drilled into the hooks collection, whose rows
// are the deletable entries.
func hooksBrowser(t *testing.T) *BrowserPanel {
	t.Helper()
	// The entry is part of the config the browser opens on, so the baseline
	// snapshot matches it: a delete is the only difference either press could
	// produce, which is what the flush assertion reads.
	cfg := config.Default()
	cfg.Hooks.Entries = []config.HookConfig{{Event: "pre_tool", Command: "echo one"}}
	b := NewBrowser(cfg, "")
	b.stack = newPaneStack(hooksFrame(b.reg.st))
	b.stack.SetSize(80, 24)
	return b
}

// TestRowHintsNameTheDeleteKey pins AUD-13's disclosure half: a row that can be
// deleted says so in the contextual hint line. Before this, d was undocumented
// in every settings list.
func TestRowHintsNameTheDeleteKey(t *testing.T) {
	b := hooksBrowser(t)
	hints := rowHints(b, false)
	if !strings.Contains(hints, "d delete") {
		t.Fatalf("deletable row hint does not name d: %q", hints)
	}
	// Armed, the hint says what the second press does.
	b.Update(kp("d"))
	if armed := rowHints(b, false); !strings.Contains(armed, "d again") {
		t.Fatalf("armed row hint does not say d again: %q", armed)
	}

	// A row with no delete keeps its existing hint text unchanged.
	plain := NewBrowser(config.Default(), "")
	drillBrowserToRow(t, plain, "Theme")
	if got := rowHints(plain, true); strings.Contains(got, "d delete") {
		t.Fatalf("non-deletable row advertises deletion: %q", got)
	}
}

// TestBrowserDeleteFlushesOnlyAfterConfirmation pins AUD-13's safety half: the
// first d changes nothing and persists nothing; the second removes the entry
// and the browser commits it.
func TestBrowserDeleteFlushesOnlyAfterConfirmation(t *testing.T) {
	b := hooksBrowser(t)

	if cmd := b.Update(kp("d")); cmd != nil {
		t.Fatalf("the first d produced a command; nothing was committed: %T", cmd())
	}
	if got := len(b.reg.st.cfg.Hooks.Entries); got != 1 {
		t.Fatalf("the first d deleted: %d entries left", got)
	}

	cmd := b.Update(kp("d"))
	if got := len(b.reg.st.cfg.Hooks.Entries); got != 0 {
		t.Fatalf("the second d did not delete: %d entries left", got)
	}
	if cmd == nil {
		t.Fatal("the confirmed delete did not flush the config change")
	}
	// AUD-10 moved every settings write off the Update goroutine, so the
	// confirmed delete answers with the commit it started (SaveDoneMsg) instead
	// of the applied result; applying that yields the ChangedMsg the model sees.
	// A blocked save still answers synchronously with ChangedMsg.
	switch msg := cmd().(type) {
	case SaveDoneMsg:
		if msg.Err != nil {
			t.Fatalf("the confirmed delete failed to save: %v", msg.Err)
		}
	case ChangedMsg:
	default:
		t.Fatalf("the confirmed delete returned %T, want a commit result", msg)
	}
}
