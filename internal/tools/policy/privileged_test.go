// internal/tools/policy/privileged_test.go — shell.sudo command validation.
package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestCheckPrivileged(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})

	rejected := []string{
		"",
		"   ",
		"sudo systemctl restart nginx",
		"su -c id",
		"doas id",
		"pkexec id",
		"run0 id",
		"env sudo id",
		"sh -c 'sudo id'",
	}
	for _, cmd := range rejected {
		t.Run("reject/"+cmd, func(t *testing.T) {
			if err := pe.CheckPrivileged(cmd); err == nil {
				t.Fatalf("CheckPrivileged(%q) = nil, want an error", cmd)
			}
		})
	}

	accepted := []string{
		"systemctl restart nginx",
		"apt-get install -y jq",
		"rm -rf /etc/x",
		"grep sudo /etc/sudoers",
		"cat /var/log/sudo.log",
	}
	for _, cmd := range accepted {
		t.Run("accept/"+cmd, func(t *testing.T) {
			if err := pe.CheckPrivileged(cmd); err != nil {
				t.Fatalf("CheckPrivileged(%q) = %v, want nil", cmd, err)
			}
		})
	}
}

// TestCheckPrivilegedDoesNotReplaceTheFloor pins that the privileged gate
// and the catastrophic floor are independent: CheckPrivileged accepts
// `rm -rf /etc/x`, and the floor is what denies it.
func TestCheckPrivilegedDoesNotReplaceTheFloor(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})
	if err := pe.CheckPrivileged("rm -rf /etc/x"); err != nil {
		t.Fatalf("CheckPrivileged(rm -rf /etc/x) = %v, want nil (the floor is a separate check)", err)
	}
	dec, reason := pe.EvaluateGuardrails("rm -rf /etc/x", "deny", true)
	if dec != DecisionDeny {
		t.Fatalf("EvaluateGuardrails(rm -rf /etc/x, system) = %v, want Deny", dec)
	}
	if !strings.Contains(reason, "guardrail") {
		t.Fatalf("floor reason = %q, want it to name the guardrail", reason)
	}
}
