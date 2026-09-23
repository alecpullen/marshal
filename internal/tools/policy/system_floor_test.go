// internal/tools/policy/system_floor_test.go — TestSystemFloorFailsClosed
package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// TestSystemFloorFailsClosed pins the fail-closed contract of the catastrophic
// floor (spec §4/§13). The floor releases the recursive-delete deny only for
// operands the parser can prove are relative literals; every other shape —
// absolute paths, mixed operands, quoting, shell expansion, globs, sudo
// wrapping, xargs payloads, and inline shell payloads — must keep the deny.
//
// The table below is the regression net for the bypasses found in branch
// review; each entry previously reached Allow under system access in an
// auto-approving mode with no human gate.
func TestSystemFloorFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		system bool
		want   Decision
	}{
		{name: "rm absolute", cmd: "rm -rf /etc", system: true, want: DecisionDeny},
		{name: "rm quoted absolute", cmd: `rm -rf "/etc"`, system: true, want: DecisionDeny},
		{name: "rm mixed operands", cmd: "rm -rf /etc ./notes", system: true, want: DecisionDeny},
		{name: "rm relative then absolute", cmd: "rm -rf ./notes /etc", system: true, want: DecisionDeny},
		{name: "rm glob", cmd: "rm -rf /etc/*", system: true, want: DecisionDeny},
		{name: "rm home expansion", cmd: "rm -rf $HOME/project", system: true, want: DecisionDeny},
		{name: "rm quoted home expansion", cmd: `rm -rf "$HOME"`, system: true, want: DecisionDeny},
		{name: "rm tilde", cmd: "rm -rf ~", system: true, want: DecisionDeny},
		{name: "rm command substitution", cmd: "rm -rf $(pwd)/src", system: true, want: DecisionDeny},
		{name: "sudo rm absolute", cmd: "sudo rm -rf /etc", system: true, want: DecisionDeny},
		{name: "sudo with flag then rm", cmd: "sudo -n rm -rf /etc", system: true, want: DecisionDeny},
		{name: "sudo with user value then rm", cmd: "sudo -u root rm -rf /etc", system: true, want: DecisionDeny},
		{name: "env sudo rm absolute", cmd: "env sudo rm -rf /etc", system: true, want: DecisionDeny},
		{name: "sudo recursive chmod", cmd: "sudo chmod -R 000 /etc", system: true, want: DecisionDeny},
		{name: "recursive chmod absolute", cmd: "chmod -R 000 /etc", system: true, want: DecisionDeny},
		{name: "sudo find delete", cmd: "sudo find / -delete", system: true, want: DecisionDeny},
		{name: "sudo dd to device", cmd: "sudo dd if=/dev/zero of=/dev/sda", system: true, want: DecisionDeny},
		{name: "find piped to xargs rm", cmd: "find / | xargs rm -rf", system: true, want: DecisionDeny},
		{name: "xargs rm from stdin", cmd: "xargs -0 rm -rf < /tmp/list", system: true, want: DecisionDeny},
		{name: "find exec rm", cmd: "find /etc -exec rm -rf {} +", system: true, want: DecisionDeny},
		{name: "sh -c rm absolute", cmd: "sh -c 'rm -rf /etc'", system: true, want: DecisionDeny},
		{name: "bash -c rm absolute", cmd: "bash -c 'rm -rf /etc'", system: true, want: DecisionDeny},
		{name: "mkfs", cmd: "mkfs.ext4 /dev/sda1", system: true, want: DecisionDeny},
		{name: "shutdown", cmd: "shutdown -h now", system: true, want: DecisionDeny},
		{name: "reboot via sudo", cmd: "sudo reboot", system: true, want: DecisionDeny},

		{name: "rm relative", cmd: "rm -rf rel/x", system: true, want: DecisionAllow},
		{name: "rm sudo relative", cmd: "sudo rm -rf rel/x", system: true, want: DecisionAllow},
		{name: "sudo ls", cmd: "sudo ls", system: true, want: DecisionAllow},
		{name: "sudo find without delete", cmd: "sudo find / -name x", system: true, want: DecisionAllow},
		{name: "rm relative without system", cmd: "rm -rf rel/x", want: DecisionDeny},
		{name: "rm absolute without system", cmd: "rm -rf /etc", want: DecisionDeny},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
			// Auto mode is the strongest approval posture: if the floor holds
			// here it holds under every mode, and a floor failure here is the
			// no-human-gate case the review demonstrated.
			pe.SetApprovalMode(ModeAuto)

			var opts []EvaluateOption
			if tc.system {
				opts = append(opts, WithSystem(true))
			}
			dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": tc.cmd}, opts...)
			if err != nil {
				t.Fatalf("Evaluate(%q) error: %v", tc.cmd, err)
			}
			if dec != tc.want {
				t.Fatalf("Evaluate(%q, system=%v) = %v (%s), want %v", tc.cmd, tc.system, dec, reason, tc.want)
			}
			if tc.want == DecisionDeny && !strings.Contains(reason, "blocked by conservative guardrail") {
				t.Fatalf("Evaluate(%q, system=%v) deny reason = %q, want the conservative-guardrail wording", tc.cmd, tc.system, reason)
			}
		})
	}
}
