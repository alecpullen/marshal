// internal/tools/policy/system_floor_test.go — TestSystemFloorFailsClosed
package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// TestGitPushFloorSurvivesPrivilegeWrapper pins that the non-bypassable push
// floor cannot be skipped by prefixing a privilege wrapper. Under system
// access the `sudo` substring guardrail is gone, so without the floor seeing
// through the wrapper a `sudo git push` auto-approved in auto mode. Flag-off
// behavior must stay a guardrail Deny, not become the floor's Confirm — that
// is why the wrapper stripping is scoped to the system floor.
func TestGitPushFloorSurvivesPrivilegeWrapper(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		system bool
		want   Decision
	}{
		{name: "plain push system", cmd: "git push", system: true, want: DecisionConfirm},
		{name: "privilege push system", cmd: "sudo git push", system: true, want: DecisionConfirm},
		{name: "privilege push with remote system", cmd: "sudo git push origin main", system: true, want: DecisionConfirm},
		{name: "privilege push path-prefixed git system", cmd: "sudo /usr/bin/git push", system: true, want: DecisionConfirm},
		{name: "env privilege push system", cmd: "env sudo git push", system: true, want: DecisionConfirm},
		// Flag-off keeps the stricter guardrail Deny.
		{name: "privilege push flag off", cmd: "sudo git push", want: DecisionDeny},
		// A non-push sudo command is not the floor either way.
		{name: "privilege non-push system", cmd: "sudo git status", system: true, want: DecisionAllow},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
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
			if tc.want == DecisionConfirm && !strings.Contains(reason, "non-bypassable floor") {
				t.Fatalf("Evaluate(%q) reason = %q, want the non-bypassable push floor", tc.cmd, reason)
			}
		})
	}
}

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

// TestGitPushFloorBypassShapes pins that the wrapper/payload shapes from
// docs/system-access-mode-followups.md items 1 & 2 cannot bypass the
// non-bypassable push floor. Each entry previously reached Allow under auto
// mode with no human gate.
func TestGitPushFloorBypassShapes(t *testing.T) {
	cmds := []string{
		"sh -c 'git push'",
		"bash -c 'git push'",
		"su -c 'git push'",
		"su root -c 'git push'",
		"exec git push",
		"command git push",
		"builtin git push",
		"nohup git push",
		"setsid git push",
		"timeout 5 git push",
		"watch -n 5 git push",
		"xargs git push <<< 'origin main'",
		"eval 'git push origin main'",
		"exec sh -c 'git push'",
		"nohup bash -c 'git push origin main'",
		"timeout 5 su -c 'git push'",
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
			pe.SetApprovalMode(ModeAuto)
			dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": cmd}, WithSystem(true))
			if err != nil {
				t.Fatalf("Evaluate(%q) error: %v", cmd, err)
			}
			if dec != DecisionConfirm {
				t.Fatalf("Evaluate(%q, system) = %v (%s), want Confirm (non-bypassable floor)", cmd, dec, reason)
			}
			if !strings.Contains(reason, "non-bypassable floor") {
				t.Fatalf("Evaluate(%q) reason = %q, want the floor reason", cmd, reason)
			}
		})
	}
}

// TestGitPushFloorBypassShapesFlagOff pins the flag-off half of the same
// contract: without system access the floor must still never auto-approve a
// wrapped push. Some shapes (sudo/su) are caught earlier by the flag-off
// guardrail and come back Deny; the contract is only that none of them is
// silently allowed.
func TestGitPushFloorBypassShapesFlagOff(t *testing.T) {
	cmds := []string{
		"sh -c 'git push'",
		"bash -c 'git push'",
		"su -c 'git push'",
		"su root -c 'git push'",
		"exec git push",
		"command git push",
		"builtin git push",
		"nohup git push",
		"setsid git push",
		"timeout 5 git push",
		"watch -n 5 git push",
		"xargs git push <<< 'origin main'",
		"eval 'git push origin main'",
		"exec sh -c 'git push'",
		"nohup bash -c 'git push origin main'",
		"timeout 5 su -c 'git push'",
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
			pe.SetApprovalMode(ModeAuto)
			dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": cmd})
			if err != nil {
				t.Fatalf("Evaluate(%q) error: %v", cmd, err)
			}
			if dec == DecisionAllow {
				t.Fatalf("Evaluate(%q, flag off) = Allow (%s), want a human gate", cmd, reason)
			}
		})
	}
}

// TestConfigurableFloorCommands pins the [tools.shell] floor_commands
// contract: configured prefixes are floored exactly like the built-in push
// floor (wrapper and payload blindness included), the built-in push floor
// survives an empty list, and a prefix never matches a longer command.
func TestConfigurableFloorCommands(t *testing.T) {
	newEngine := func(floors []string) *PolicyEngine {
		cfg := &config.Config{}
		cfg.Tools.Shell.FloorCommands = floors
		pe := NewEngine(cfg, []string{})
		pe.SetApprovalMode(ModeAuto)
		return pe
	}

	floored := []string{
		"npm publish",
		"exec npm publish",
		"sh -c 'npm publish'",
		"nohup npm publish --tag x",
	}
	for _, cmd := range floored {
		t.Run("floored/"+cmd, func(t *testing.T) {
			pe := newEngine([]string{"npm publish"})
			dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": cmd}, WithSystem(true))
			if err != nil {
				t.Fatalf("Evaluate(%q) error: %v", cmd, err)
			}
			if dec != DecisionConfirm {
				t.Fatalf("Evaluate(%q) = %v (%s), want Confirm (configured floor)", cmd, dec, reason)
			}
			if !strings.Contains(reason, "configured floor") {
				t.Fatalf("Evaluate(%q) reason = %q, want the configured-floor reason", cmd, reason)
			}
		})
	}

	// An empty configured list must not remove the built-in push floor.
	t.Run("empty list keeps push floor", func(t *testing.T) {
		pe := newEngine(nil)
		dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "git push"}, WithSystem(true))
		if err != nil {
			t.Fatalf("Evaluate error: %v", err)
		}
		if dec != DecisionConfirm || !strings.Contains(reason, "non-bypassable floor") {
			t.Fatalf("Evaluate(git push) = %v (%s), want the non-bypassable push floor", dec, reason)
		}
	})

	// No false positives: a longer command sharing a prefix is not floored.
	for _, cmd := range []string{"git pushd", "npm publishd"} {
		t.Run("not floored/"+cmd, func(t *testing.T) {
			pe := newEngine([]string{"npm publish"})
			dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": cmd}, WithSystem(true))
			if err != nil {
				t.Fatalf("Evaluate(%q) error: %v", cmd, err)
			}
			if dec == DecisionConfirm && strings.Contains(reason, "floor") {
				t.Fatalf("Evaluate(%q) = %v (%s), want no floor", cmd, dec, reason)
			}
		})
	}
}
