package help

import (
	"strings"
	"testing"
)

// TestMouseHintStaysHonestWhenCaptureIsDisabled pins AUD-14's footer half: when
// the config disables mouse capture, the terminal owns the mouse for the whole
// session and Ctrl+S does nothing. Advertising either "drag copy" (as if the
// TUI captured the mouse) or "Ctrl+S scroll" (as if the key worked) would be a
// promise the TUI cannot keep, so both drop out.
func TestMouseHintStaysHonestWhenCaptureIsDisabled(t *testing.T) {
	out := stripANSI(Footer(FooterHints{MouseCaptureDisabled: true}))
	for _, want := range []string{"drag copy", "Ctrl+S scroll"} {
		if strings.Contains(out, want) {
			t.Fatalf("capture is off in config but the footer still advertises %q: %q", want, out)
		}
	}
	// The rest of the idle cluster survives: this drops one pair, not the row.
	for _, want := range []string{"Tab mode", "/ cmd", "? help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("capture-disabled footer dropped %q: %q", want, out)
		}
	}
}
