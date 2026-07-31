package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// TestGuardrailBlocksWrappedRM pins S-06: destructive rm invocations must be
// blocked even when the top-level argv0 is a wrapper or the rm appears as an
// xargs payload. ClassifyCommand only inspects the top-level argv0, so these
// variants previously evaded the guardrail.
func TestGuardrailBlocksWrappedRM(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)

	cases := []struct {
		cmd          string
		wantInReason string
	}{
		{"env rm -fr /", "rm -r -f"},
		{"env rm -f -r /tmp/x", "rm -r -f"},
		{"env -i rm --recursive --force /tmp/x", "rm -r -f"},
		{"env FOO=bar rm -fr /tmp/x", "rm -r -f"},
		{"nice rm -fr /tmp/x", "rm -r -f"},
		{"nice -n 10 rm -f -r ~", "rm -r -f"},
		{"find . -type f | xargs rm -fr", "rm -r -f"},
		{"find . -type f | xargs -I {} rm -fr {}", "rm -r -f"},
		{"find . -type f | xargs -n 1 rm -f -r", "rm -r -f"},
		{"echo hi | rm -fr /tmp", "rm -r -f"},
	}

	for _, tc := range cases {
		dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": tc.cmd})
		if err != nil {
			t.Fatalf("Evaluate(%q) error: %v", tc.cmd, err)
		}
		if dec != DecisionDeny {
			t.Errorf("Evaluate(%q) = %v, want Deny", tc.cmd, dec)
		}
		if !strings.Contains(reason, tc.wantInReason) {
			t.Errorf("Evaluate(%q) reason = %q, want it to contain %q", tc.cmd, reason, tc.wantInReason)
		}
	}
}

// TestGuardrailDoesNotFalsePositiveOnEchoRM verifies that a harmless echo of
// an rm-looking string is not blocked just because the tokens appear in the
// stage.
func TestGuardrailDoesNotFalsePositiveOnEchoRM(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)

	cases := []string{
		"echo rm -fr /",
		"echo rm -rf /tmp",
		"printf 'rm -fr /'",
	}

	for _, cmd := range cases {
		dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": cmd})
		if err != nil {
			t.Fatalf("Evaluate(%q) error: %v", cmd, err)
		}
		if dec == DecisionDeny {
			t.Errorf("Evaluate(%q) should not be denied; reason=%q", cmd, reason)
		}
	}
}
