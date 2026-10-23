package policy

import (
	"strings"
	"testing"
)

// A shell command holding a character the terminal acts on, or a bidi
// override, could show in the approval prompt as something other than what
// runs. The canonical analyzer refuses it before any prompt, in every mode.
func TestEvaluateRefusesCommandsWithDisplayControls(t *testing.T) {
	spoofs := []string{
		"rm -rf ~/work #\x1b[2K\rapt-get update",
		"ls\rrm -rf ~",
		"echo \xe2\x80\xaehello", // U+202E RIGHT-TO-LEFT OVERRIDE
		"echo \xc2\x9b2K",        // U+009B, the C1 CSI introducer
	}
	for _, cmd := range spoofs {
		_, err := AnalyzeShell(ShellInput{Command: cmd})
		if err == nil || !strings.Contains(err.Error(), "control character") {
			t.Errorf("AnalyzeShell(%q) = %v, want an error naming the control", cmd, err)
		}
	}
}

// Newlines and tabs are layout, not control: heredocs and multi-line
// scripts still reach the usual rules.
func TestEvaluateAllowsMultilineCommands(t *testing.T) {
	if _, err := AnalyzeShell(ShellInput{Command: "cat <<EOF\n\tok\nEOF"}); err != nil {
		t.Fatalf("multi-line command rejected: %v", err)
	}
}

// The password prompt is the last thing read before root runs the command,
// so the sudo tool's own check refuses it too. That check is the same
// display-control validation the analyzer applies.
func TestCheckPrivilegedRefusesDisplayControls(t *testing.T) {
	if err := checkCommandControls("apt-get update #\x1b[2K\r"); err == nil || !strings.Contains(err.Error(), "control character U+001B") {
		t.Fatalf("checkCommandControls = %v, want the control named", err)
	}
	if err := checkCommandControls("apt-get update"); err != nil {
		t.Fatalf("checkCommandControls(plain) = %v", err)
	}
}
