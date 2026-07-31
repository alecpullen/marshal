package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// TestIsGitPushFloor_Variants pins S-05: the non-bypassable git-push floor
// must recognize git push in common invocation forms, not just a literal
// argv0 "git" with first arg "push".
func TestIsGitPushFloor_Variants(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Baseline
		{"git push", true},
		{"git push origin main", true},
		{"git push --force", true},
		{"git push -f", true},
		{"git push --tags", true},
		{"git push --force-with-lease", true},

		// Path-prefixed git binary
		{"/usr/bin/git push", true},
		{"/usr/local/bin/git push origin main", true},

		// Git global options before the subcommand
		{"git -c core.pager=cat push origin main", true},
		{"git --config core.pager=cat push", true},
		{"git -C /some/path push", true},
		{"git -c user.name=test -c user.email=test@example.com push", true},

		// Common prefix wrappers
		{"env git push", true},
		{"env -i git push", true},
		{"env FOO=bar git push origin main", true},
		{"env -u PATH git push", true},
		{"nice git push", true},
		{"nice -n 10 git push", true},
		{"nice --adjustment=10 git push", true},

		// Negative cases
		{"git commit -m foo", false},
		{"git status", false},
		{"echo git push", false},
		{"git pushd", false},
		{"env echo git push", false},
		{"nice echo git push", false},
	}

	for _, tc := range cases {
		got := isGitPushFloor(tc.cmd)
		if got != tc.want {
			t.Errorf("isGitPushFloor(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// TestGitPushFloorAlwaysConfirms_Variants pins S-05 end-to-end: even in auto
// and copilot modes, every recognized git push variant must require approval.
func TestGitPushFloorAlwaysConfirms_Variants(t *testing.T) {
	variants := []string{
		"git push origin main",
		"/usr/bin/git push",
		"git -c core.pager=cat push origin main",
		"env git push",
		"nice -n 10 git push",
	}

	for _, mode := range []ApprovalMode{ModeEdit, ModeCopilot, ModeAuto} {
		for _, cmd := range variants {
			pe := NewEngine(&config.Config{}, nil)
			pe.SetApprovalMode(mode)
			dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": cmd})
			if err != nil {
				t.Fatalf("mode %s cmd %q: Evaluate error: %v", mode, cmd, err)
			}
			if dec != DecisionConfirm {
				t.Errorf("mode %s cmd %q: got %v, want Confirm; reason=%q", mode, cmd, dec, reason)
			}
			if !strings.Contains(reason, "git push") {
				t.Errorf("mode %s cmd %q: reason %q should mention git push", mode, cmd, reason)
			}
		}
	}
}
