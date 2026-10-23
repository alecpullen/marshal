package policy

import "testing"

func TestShellRoutineOperationTable(t *testing.T) {
	cases := []struct {
		cmd  string
		want ShellEffect
	}{
		{"pwd", ShellRead},
		{"cat file", ShellRead},
		{"git status", ShellRead},
		{"git commit -m x", ShellWrite},
		{"go test ./...", ShellWrite},
		{"cargo build", ShellWrite},
		{"make", ShellWrite},
		{"npm run build", ShellWrite},
		{"cp a b", ShellWrite},
		{"tee out", ShellWrite},
	}
	for _, tc := range cases {
		a := analyze(t, tc.cmd)
		if len(a.Operations) == 0 {
			t.Fatalf("%q: no operations", tc.cmd)
		}
		if a.Operations[0].Effects&tc.want == 0 {
			t.Fatalf("%q: effects = %b, want bit %b", tc.cmd, a.Operations[0].Effects, tc.want)
		}
	}
}

func TestShellReadFlagsArePositive(t *testing.T) {
	// rg --pre executes a helper per file: not a proven read.
	a := analyze(t, "rg --pre ./helper pattern")
	if a.Operations[0].Effects&ShellRead != 0 {
		t.Fatalf("rg --pre classified as read: %b", a.Operations[0].Effects)
	}
	// grep -r is a read.
	a = analyze(t, "grep -r pattern .")
	if a.Operations[0].Effects&ShellRead == 0 {
		t.Fatalf("grep -r not classified as read: %b", a.Operations[0].Effects)
	}
}

func TestShellCommitMessagesAreOnlyData(t *testing.T) {
	a := analyze(t, `git commit -m 'sudo reboot mkfs git reset --hard'`)
	if len(a.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(a.Operations))
	}
	op := a.Operations[0]
	if op.Executable.Value != "git" {
		t.Fatalf("executable = %q, want git", op.Executable.Value)
	}
	if op.Effects&ShellWrite == 0 {
		t.Fatalf("commit effects = %b, want a write", op.Effects)
	}
	if op.Effects&ShellDestructive != 0 {
		t.Fatalf("commit message data became destructive: %b", op.Effects)
	}
}

func TestShellLocalInstallTargets(t *testing.T) {
	for _, cmd := range []string{"go get ./...", "go mod tidy", "npm install", "pnpm install"} {
		a := analyze(t, cmd)
		if len(a.Operations) == 0 {
			t.Fatalf("%q: no operations", cmd)
		}
		effects := a.Operations[0].Effects
		if effects&ShellWrite == 0 {
			t.Fatalf("%q: effects = %b, want a routine write", cmd, effects)
		}
		if effects&ShellEnvironment != 0 {
			t.Fatalf("%q: effects = %b, want no environment effect", cmd, effects)
		}
	}
}

func TestShellGlobalAndUncertainInstallTargets(t *testing.T) {
	a := analyze(t, "npm install -g pkg")
	if a.Operations[0].Effects&ShellEnvironment == 0 {
		t.Fatalf("npm install -g effects = %b, want environment", a.Operations[0].Effects)
	}
	a = analyze(t, "pip install pkg")
	if a.Operations[0].Effects&ShellEnvironment == 0 {
		t.Fatalf("pip install effects = %b, want environment", a.Operations[0].Effects)
	}
}

func TestShellOrdinaryOverwriteIsRoutineEdit(t *testing.T) {
	a := analyze(t, "echo x > file")
	if len(a.Operations) == 0 {
		t.Fatal("no operations")
	}
	if a.Operations[0].Effects&ShellWrite == 0 {
		t.Fatalf("redirect effects = %b, want a write", a.Operations[0].Effects)
	}
}
