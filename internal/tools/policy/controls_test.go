package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// A shell command holding a character the terminal acts on, or a bidi
// override, could show in the approval prompt as something other than what
// runs. It is refused before any prompt, in every mode, auto included.
func TestEvaluateRefusesCommandsWithDisplayControls(t *testing.T) {
	spoofs := []string{
		"rm -rf ~/work #\x1b[2K\rapt-get update",
		"ls\rrm -rf ~",
		"echo \xe2\x80\xaehello", // U+202E RIGHT-TO-LEFT OVERRIDE
		"echo \xc2\x9b2K",        // U+009B, the C1 CSI introducer
	}
	for _, mode := range []ApprovalMode{ModeEdit, ModeAuto} {
		pe := NewEngine(&config.Config{}, []string{})
		pe.SetApprovalMode(mode)
		for _, tool := range []string{"shell.run", "test.run", "shell.sudo"} {
			for _, cmd := range spoofs {
				dec, reason, err := pe.Evaluate(tool, map[string]interface{}{"command": cmd})
				if err != nil || dec != DecisionDeny || !strings.Contains(reason, "control character") {
					t.Errorf("%s %s(%q) = %s %q %v, want a deny naming the control", mode, tool, cmd, dec, reason, err)
				}
			}
		}
	}
}

// Newlines and tabs are layout, not control: heredocs and multi-line
// scripts still reach the usual rules.
func TestEvaluateAllowsMultilineCommands(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})
	pe.SetApprovalMode(ModeAuto)
	dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "cat <<EOF\n\tok\nEOF"})
	if err != nil || strings.Contains(reason, "control character") {
		t.Fatalf("multi-line command = %s %q %v, want it past the control check", dec, reason, err)
	}
}

// The password prompt is the last thing read before root runs the command,
// so the sudo tool's own check refuses it too.
func TestCheckPrivilegedRefusesDisplayControls(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})
	if err := pe.CheckPrivileged("apt-get update #\x1b[2K\r"); err == nil || !strings.Contains(err.Error(), "control character U+001B") {
		t.Fatalf("CheckPrivileged = %v, want the control named", err)
	}
	if err := pe.CheckPrivileged("apt-get update"); err != nil {
		t.Fatalf("CheckPrivileged(plain) = %v", err)
	}
}
