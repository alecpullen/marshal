// internal/tools/policy/system_floor_test.go — canonical floor and privilege matrix.
package policy

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// TestGitPushFloorSurvivesPrivilegeWrapper pins that the non-bypassable push
// floor cannot be skipped by prefixing a privilege wrapper, and that the
// privilege deny runs BEFORE the floor: a privilege-prefixed push is a Deny
// (steering to shell.sudo), not the floor's Confirm.
func TestGitPushFloorSurvivesPrivilegeWrapper(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		system bool
		want   Decision
	}{
		{name: "plain push system", cmd: "git push", system: true, want: DecisionConfirm},
		// Privilege wrappers deny before the floor under system access.
		{name: "privilege push system", cmd: "sudo git push", system: true, want: DecisionDeny},
		{name: "privilege push with remote system", cmd: "sudo git push origin main", system: true, want: DecisionDeny},
		{name: "privilege push path-prefixed git system", cmd: "sudo /usr/bin/git push", system: true, want: DecisionDeny},
		{name: "env privilege push system", cmd: "env sudo git push", system: true, want: DecisionDeny},
		// Flag-off keeps the stricter guardrail Deny.
		{name: "privilege push flag off", cmd: "sudo git push", want: DecisionDeny},
		// A non-push sudo command is a privilege deny under system access.
		{name: "privilege non-push system", cmd: "sudo git status", system: true, want: DecisionDeny},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
			ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: true, System: tc.system, Revision: 1}, tc.cmd)
			if ev.Decision != tc.want {
				t.Fatalf("Evaluate(%q, system=%v) = %v, want %v", tc.cmd, tc.system, ev.Decision, tc.want)
			}
			if tc.want == DecisionConfirm && !ev.ExactCommandApproval {
				t.Fatalf("Evaluate(%q) did not require exact approval", tc.cmd)
			}
			if tc.want == DecisionDeny && tc.system && !hasReasonCode(ev, ReasonPrivilegeWrapper) {
				t.Fatalf("Evaluate(%q) reasons = %+v, want the privilege-wrapper reason", tc.cmd, ev.Reasons)
			}
		})
	}
}

// TestSystemFloorFailsClosed pins the fail-closed contract of the
// catastrophic floor (spec §4/§13). Catastrophic shapes and privilege
// wrappers are denied; destructive operations outside the workspace retain
// exact approval and require System; a relative destructive operation is
// preauthorized only by Auto.
func TestSystemFloorFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		system bool
		auto   bool
		want   Decision
	}{
		{name: "rm absolute", cmd: "rm -rf /etc", system: true, want: DecisionConfirm},
		{name: "rm quoted absolute", cmd: `rm -rf "/etc"`, system: true, want: DecisionConfirm},
		{name: "rm mixed operands", cmd: "rm -rf /etc ./notes", system: true, want: DecisionConfirm},
		{name: "rm relative then absolute", cmd: "rm -rf ./notes /etc", system: true, want: DecisionConfirm},
		{name: "rm glob", cmd: "rm -rf /etc/*", system: true, want: DecisionConfirm},
		{name: "rm home expansion", cmd: "rm -rf $HOME/project", system: true, want: DecisionConfirm},
		{name: "rm quoted home expansion", cmd: `rm -rf "$HOME"`, system: true, want: DecisionConfirm},
		{name: "rm tilde", cmd: "rm -rf ~", system: true, want: DecisionDeny},
		{name: "rm command substitution", cmd: "rm -rf $(pwd)/src", system: true, want: DecisionConfirm},
		{name: "sudo rm absolute", cmd: "sudo rm -rf /etc", system: true, want: DecisionDeny},
		{name: "sudo with flag then rm", cmd: "sudo -n rm -rf /etc", system: true, want: DecisionDeny},
		{name: "sudo with user value then rm", cmd: "sudo -u root rm -rf /etc", system: true, want: DecisionDeny},
		{name: "env sudo rm absolute", cmd: "env sudo rm -rf /etc", system: true, want: DecisionDeny},
		{name: "sudo recursive chmod", cmd: "sudo chmod -R 000 /etc", system: true, want: DecisionDeny},
		{name: "recursive chmod absolute", cmd: "chmod -R 000 /etc", system: true, want: DecisionConfirm},
		{name: "sudo find delete", cmd: "sudo find / -delete", system: true, want: DecisionDeny},
		{name: "sudo dd to device", cmd: "sudo dd if=/dev/zero of=/dev/sda", system: true, want: DecisionDeny},
		{name: "find piped to xargs rm", cmd: "find / | xargs rm -rf", system: true, want: DecisionConfirm},
		{name: "xargs rm from stdin", cmd: "xargs -0 rm -rf < /tmp/list", system: true, want: DecisionConfirm},
		{name: "find exec rm", cmd: "find /etc -exec rm -rf {} +", system: true, want: DecisionConfirm},
		{name: "sh -c rm absolute", cmd: "sh -c 'rm -rf /etc'", system: true, want: DecisionConfirm},
		{name: "bash -c rm absolute", cmd: "bash -c 'rm -rf /etc'", system: true, want: DecisionConfirm},
		{name: "mkfs", cmd: "mkfs.ext4 /dev/sda1", system: true, want: DecisionDeny},
		{name: "shutdown", cmd: "shutdown -h now", system: true, want: DecisionDeny},
		{name: "reboot via sudo", cmd: "sudo reboot", system: true, want: DecisionDeny},

		{name: "rm relative", cmd: "rm -rf rel/x", system: true, want: DecisionConfirm},
		{name: "rm relative auto", cmd: "rm -rf rel/x", system: true, auto: true, want: DecisionAllow},
		// Privilege wrappers are Deny under system access (spec §1): the
		// previously-Allowed plain sudo shapes are the accepted cost of
		// making privileged execution expressible only through shell.sudo.
		{name: "rm sudo relative", cmd: "sudo rm -rf rel/x", system: true, want: DecisionDeny},
		{name: "sudo ls", cmd: "sudo ls", system: true, want: DecisionDeny},
		{name: "sudo find without delete", cmd: "sudo find / -name x", system: true, want: DecisionDeny},
		{name: "rm relative without system", cmd: "rm -rf rel/x", want: DecisionConfirm},
		{name: "rm absolute without system", cmd: "rm -rf /etc", want: DecisionConfirm},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
			ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: tc.auto, System: tc.system, Revision: 1}, tc.cmd)
			if ev.Decision != tc.want {
				t.Fatalf("Evaluate(%q, system=%v, auto=%v) = %v, want %v", tc.cmd, tc.system, tc.auto, ev.Decision, tc.want)
			}
			if tc.want == DecisionConfirm && !ev.ExactCommandApproval {
				t.Fatalf("Evaluate(%q) did not require exact approval", tc.cmd)
			}
		})
	}
}

// TestGitPushFloorBypassShapes pins that the wrapper/payload shapes from
// docs/system-access-mode-followups.md items 1 & 2 cannot bypass the
// non-bypassable push floor.
func TestGitPushFloorBypassShapes(t *testing.T) {
	cmds := []string{
		"sh -c 'git push'",
		"bash -c 'git push'",
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
		// Combined single-dash shell flags still carry the payload.
		"bash -lc 'git push'",
		"bash -ic 'git push'",
		"sh -oc 'git push'",
		"zsh -lc 'git push'",
		// Nested payloads must recurse, not be mangled by quote trimming.
		`sh -c 'sh -c "git push"'`,
		// Wrapper flags that take a value must not desync the operand skip.
		"timeout -k 5 5 git push",
		"timeout -s KILL 5 git push",
		"xargs -a list git push",
		"xargs --arg-file list git push",
		// Option words between -c and the payload, and deeper nesting.
		"bash -c -l 'git push'",
		"bash -c -- 'git push'",
		"timeout 5 bash -c -l 'git push'",
		`sh -c "sh -c 'sh -c \"git push\"'"`,
		// Escaped and ANSI-C quoted payloads.
		`sh -c git\ push`,
		"bash -c $'git push'",
		// Escaped-quote nesting: the printed form is not valid shell, so the
		// floor must decode the argument rather than re-parse the printer text.
		`sh -c 'sh -c '\''git push'\'''`,
		`bash -c "bash -c '\''git push'\''"`,
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			a := analyze(t, cmd)
			if !hasEffect(a, ShellPublish) {
				t.Fatalf("%q: no publishing effect in %+v", cmd, a.Operations)
			}
		})
	}
}

// TestGitPushFloorBypassShapesFlagOff pins the flag-off half of the same
// contract: without system access the floor must still never auto-approve a
// wrapped push.
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
		"bash -lc 'git push'",
		"bash -ic 'git push'",
		"sh -oc 'git push'",
		"zsh -lc 'git push'",
		`sh -c 'sh -c "git push"'`,
		"timeout -k 5 5 git push",
		"timeout -s KILL 5 git push",
		"xargs -a list git push",
		"xargs --arg-file list git push",
		"bash -c -l 'git push'",
		"bash -c -- 'git push'",
		"timeout 5 bash -c -l 'git push'",
		`sh -c "sh -c 'sh -c \"git push\"'"`,
		`sh -c git\ push`,
		"bash -c $'git push'",
		`sh -c 'sh -c '\''git push'\'''`,
		`bash -c "bash -c '\''git push'\''"`,
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			pe := NewEngine(&config.Config{}, []string{})
			ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: true, Revision: 1}, cmd)
			if ev.Decision == DecisionAllow {
				t.Fatalf("Evaluate(%q, flag off) = Allow, want a human gate", cmd)
			}
		})
	}
}

// TestPrivilegeWrappersDenyUnderSystemAccess pins the inversion: under
// system access every privilege-wrapper shape is Deny in shell.run, at any
// depth, and the reason steers the agent to shell.sudo.
func TestPrivilegeWrappersDenyUnderSystemAccess(t *testing.T) {
	denied := []string{
		"sudo ls",
		"sudo rm -rf rel/x",
		"sudo find / -name x",
		"sudo git status",
		"sudo git push",
		"sudo sh -c 'git push'",
		"sudo su -c 'git push'",
		"sudo bash -c 'rm -rf /'",
		"sudo bash -lc 'git push'",
		"eval 'sudo id'",
		"nohup sudo id &",
		"xargs -n1 sudo rm -rf",
		"env sudo id",
		"command sudo id",
		"exec sudo id",
		"timeout 5 sudo id",
		`sh -c "sudo id"`,
		"sudo -u root id",
		"su -c id",
		"su root -c id",
		// su-wrapped pushes are privilege denies, not the floor's Confirm:
		// the privilege check runs first.
		"su -c 'git push'",
		"su root -c 'git push'",
		"timeout 5 su -c 'git push'",
		"doas id",
		"pkexec id",
		"run0 id",
		"sudo",
		// Quoted/escaped wrapper names: the printer re-emits quoting, so
		// argv0 must be decoded before the wrapper lookup or a single quote
		// character evades the deny.
		`'sudo' id`,
		`"sudo" id`,
		`\sudo id`,
		`s'u'd'o' id`,
		`'su' -c id`,
		// sudoedit is a real setuid sudo front-end.
		"sudoedit /etc/fstab",
		// Multi-call and transparent-prefix wrappers.
		"busybox sudo id",
		"busybox su -c id",
		"time sudo id",
		"time -p sudo id",
		// find's -exec payload is a payload carrier, like xargs.
		`find / -exec sudo id \;`,
		`find / -exec sh -c 'sudo id' \;`,
		`find / -execdir sudo id +`,
	}
	for _, cmd := range denied {
		t.Run(cmd, func(t *testing.T) {
			a := analyze(t, cmd)
			if !hasEffect(a, ShellPrivilege) {
				t.Fatalf("%q: no privilege effect in %+v", cmd, a.Operations)
			}
		})
	}

	// The false-positive boundary: a command that merely MENTIONS sudo is
	// not a privilege wrapper. This is why the check is argv-aware rather
	// than a substring match.
	allowed := []string{
		"grep sudo /etc/sudoers",
		"cat /var/log/sudo.log",
		"echo sudo",
	}
	for _, cmd := range allowed {
		t.Run("not-a-wrapper/"+cmd, func(t *testing.T) {
			a := analyze(t, cmd)
			if hasEffect(a, ShellPrivilege) {
				t.Fatalf("%q: classified as privilege: %+v", cmd, a.Operations)
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
		return NewEngine(cfg, []string{})
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
			ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 1}, cmd)
			if ev.Decision != DecisionConfirm {
				t.Fatalf("Evaluate(%q) = %v, want Confirm (configured floor)", cmd, ev.Decision)
			}
			if !ev.ExactCommandApproval {
				t.Fatalf("Evaluate(%q) did not require exact approval", cmd)
			}
		})
	}

	// An empty configured list must not remove the built-in push floor.
	t.Run("empty list keeps push floor", func(t *testing.T) {
		pe := newEngine(nil)
		ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 1}, "git push")
		if ev.Decision != DecisionConfirm || !ev.ExactCommandApproval {
			t.Fatalf("Evaluate(git push) = %v, want the non-bypassable push floor", ev.Decision)
		}
	})

	// No false positives: a longer command sharing a prefix is not floored.
	for _, cmd := range []string{"git pushd", "npm publishd"} {
		t.Run("not floored/"+cmd, func(t *testing.T) {
			pe := newEngine([]string{"npm publish"})
			ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 1}, cmd)
			if hasReasonCode(ev, ReasonPublishing) {
				t.Fatalf("Evaluate(%q) = %v, want no floor", cmd, ev.Decision)
			}
		})
	}
}

// hasReasonCode reports whether an evaluation carries the given reason code.
func hasReasonCode(ev Evaluation, code ReasonCode) bool {
	for _, r := range ev.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}
