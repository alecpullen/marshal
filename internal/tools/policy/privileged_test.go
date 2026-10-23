// internal/tools/policy/privileged_test.go — shell.sudo command validation.
package policy

import "testing"

func TestCheckPrivileged(t *testing.T) {
	rejected := []string{
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
			a := analyze(t, cmd)
			if !hasEffect(a, ShellPrivilege) {
				t.Fatalf("%q: no privilege effect in %+v", cmd, a.Operations)
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
			a := analyze(t, cmd)
			if hasEffect(a, ShellPrivilege) {
				t.Fatalf("%q: classified as privilege: %+v", cmd, a.Operations)
			}
		})
	}
}

// TestCheckPrivilegedDoesNotReplaceTheFloor pins that the privileged gate
// and the catastrophic floor are independent: a bare destructive command is
// not a privilege wrapper, and the floor is what denies the catastrophic
// shape.
func TestCheckPrivilegedDoesNotReplaceTheFloor(t *testing.T) {
	a := analyze(t, "rm -rf /")
	if hasEffect(a, ShellPrivilege) {
		t.Fatalf("rm -rf / classified as privilege: %+v", a.Operations)
	}
	if !hasEffect(a, ShellCatastrophic) {
		t.Fatalf("rm -rf / not classified catastrophic: %+v", a.Operations)
	}
}
