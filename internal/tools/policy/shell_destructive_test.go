package policy

import "testing"

func hasEffect(a ShellAnalysis, want ShellEffect) bool {
	for _, op := range a.Operations {
		if op.Effects&want != 0 {
			return true
		}
	}
	return false
}

func TestShellDestructiveEquivalentForms(t *testing.T) {
	cmds := []string{
		"rm -rf x", "rm -fr x", "rm -r x", "rm --recursive x",
		"git reset --hard", "git clean -f", "git restore x", "git checkout -- x",
		"git branch -D x", "git worktree remove x",
		"find . -delete", "chmod -R 777 x", "chown -R u x",
		"truncate -s 0 x", "shred x",
	}
	for _, cmd := range cmds {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellDestructive) {
			t.Fatalf("%q: no destructive effect in %+v", cmd, a.Operations)
		}
	}
}

func TestShellDestructiveDryRunsAndSingleRemoval(t *testing.T) {
	// A dry-run clean is not destructive.
	a := analyze(t, "git clean -n")
	if hasEffect(a, ShellDestructive) {
		t.Fatalf("git clean -n classified destructive: %+v", a.Operations)
	}
	// A single-file removal is not destructive.
	a = analyze(t, "rm x")
	if hasEffect(a, ShellDestructive) {
		t.Fatalf("rm x classified destructive: %+v", a.Operations)
	}
}

func TestShellPublishingEveryWrapper(t *testing.T) {
	cmds := []string{
		"git push", "git push origin main", "git push --force",
		"sh -c 'git push'", "bash -lc 'git push'",
	}
	for _, cmd := range cmds {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellPublish) {
			t.Fatalf("%q: no publishing effect in %+v", cmd, a.Operations)
		}
	}
}

func TestShellFloorOperationTable(t *testing.T) {
	cmds := []string{
		"mkfs.ext4 /dev/sda", "mke2fs /dev/sda", "newfs /dev/sda",
		"diskutil eraseDisk x", "shutdown -h now", "reboot",
		"dd if=/dev/zero of=/dev/sda",
	}
	for _, cmd := range cmds {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellCatastrophic) {
			t.Fatalf("%q: no catastrophic effect in %+v", cmd, a.Operations)
		}
	}
}

func TestShellFloorWholeContentsEquivalents(t *testing.T) {
	for _, cmd := range []string{"rm -rf /", "rm -rf ~", "rm -rf /*"} {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellCatastrophic) {
			t.Fatalf("%q: no catastrophic effect in %+v", cmd, a.Operations)
		}
	}
}

func TestShellPrivilegeWordsVersusWrappers(t *testing.T) {
	a := analyze(t, "sudo id")
	if !hasEffect(a, ShellPrivilege) {
		t.Fatalf("sudo id not classified as privilege: %+v", a.Operations)
	}
	a = analyze(t, "echo sudo")
	if hasEffect(a, ShellPrivilege) {
		t.Fatalf("echo sudo classified as privilege: %+v", a.Operations)
	}
}
