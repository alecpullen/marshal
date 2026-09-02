package agent

import (
	"fmt"
	"testing"
)

func TestUnverifiedMutation_NoHistory(t *testing.T) {
	tr := newProgressTracker()
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("empty history must not fire the gate")
	}
}

func TestUnverifiedMutation_MutationWithoutVerification(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.read", `{"path":"a.go"}`, hashToolResult("x"), true)
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	last, fire := tr.unverifiedMutation()
	if !fire {
		t.Fatal("patch without verification must fire the gate")
	}
	if last.name != "file.write_patch" {
		t.Fatalf("last mutation = %q, want file.write_patch", last.name)
	}
}

func TestUnverifiedMutation_VerificationAfterMutation(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("verification after the mutation must satisfy the gate")
	}
}

func TestUnverifiedMutation_VerificationBeforeMutationDoesNotCount(t *testing.T) {
	tr := newProgressTracker()
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	if _, fire := tr.unverifiedMutation(); !fire {
		t.Fatal("verification before the last mutation must not satisfy the gate")
	}
}

func TestUnverifiedMutation_TestLikeShellCountsAsVerification(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record("shell.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("shell.run 'go test' must count as verification")
	}
}

func TestUnverifiedMutation_MutatingShellIsMutation(t *testing.T) {
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"rm -rf build"}`, hashToolResult(""), true)
	if _, fire := tr.unverifiedMutation(); !fire {
		t.Fatal("rm is on the mutating allowlist and must fire the gate")
	}
}

func TestUnverifiedMutation_ResearchShellNeverFires(t *testing.T) {
	// Research and unrecognized shell commands are neutral: a turn that
	// only ran them must finish without the verification nudge.
	research := []string{
		"git log --oneline -20", "git status --short", "git diff HEAD",
		"git show abc123", "git branch -a",
		"find . -name '*.go'", "grep -rn TODO .", "ls -la", "pwd",
		"cat README.md", "wc -l main.go", "head -50 go.mod",
		"sed -n '5,10p' main.go", "awk '{print $1}' file.txt",
		"curl -s https://example.com/api", "ps aux", "du -sh .",
		"git commit -m \"make it work\"", "grep '>' README.md",
		"awk '$3 > 5' data.txt", "git log --grep=\"docker build\"",
		"man make", "ls cmake", "git push origin main", "git stash list",
		"ssh-keygen -l -f key", "truncate --help", "git stash --help",
		"make -n test", "make --dry-run build",
		"grep perl -i foo.txt", "rg sed -i f",
		"echo hi 2> err.log 1> out.txt", "cmd 1> out.txt",
		"docker exec c ls /data", "docker exec c cat /etc/hosts",
		"git rm file.go --dry-run",
		"", "not json",
	}
	for _, cmd := range research {
		args := `{}`
		if cmd != "" {
			args = fmt.Sprintf(`{"command":%q}`, cmd)
		}
		tr := newProgressTracker()
		tr.record("shell.run", args, hashToolResult(""), true)
		if _, fire := tr.unverifiedMutation(); fire {
			t.Errorf("research/unknown command %q must not arm the gate", cmd)
		}
	}
}

func TestUnverifiedMutation_FailedCallsDoNotCount(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("err"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("failed mutation must not arm the gate")
	}
	// A red test is a FAILED call (test.run surfaces a non-zero exit as a
	// tool error), but it must still satisfy the gate: the model ran a
	// verification command, which is what the gate checks. Otherwise an
	// honest model that runs tests and reports "tests still fail" would be
	// penalized identically to one that never verified.
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("exit 1"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("a failed verification run must still satisfy the gate")
	}
}

func TestUnverifiedMutation_FailedVerificationStillSatisfiesGate(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr.record("shell.run", `{"command":"go test ./..."}`, hashToolResult("exit 2"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("a failing test-like shell.run must still satisfy the gate")
	}
}

func TestLooksLikeVerificationCommandMatchesCommandFieldOnly(t *testing.T) {
	// A verification keyword in the command field counts.
	if !looksLikeVerificationCommand(`{"command":"go test ./..."}`) {
		t.Fatal("'go test ./...' must count as verification")
	}
	// A verification keyword in an UNRELATED field must not count, or a
	// mutation could masquerade as a verification while doing real work.
	if looksLikeVerificationCommand(`{"command":"rm -rf build","note":"run pytest later"}`) {
		t.Fatal("a mutation whose only 'pytest' mention is in a non-command field must not count as verification")
	}
	// Empty or non-JSON args are not verification.
	if looksLikeVerificationCommand("") || looksLikeVerificationCommand("not json") {
		t.Fatal("empty or malformed args must not count as verification")
	}
}

func TestUnverifiedMutation_DiagnosticsCheckCounts(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr.record("diagnostics.check", `{}`, hashToolResult("clean"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("diagnostics.check must count as verification")
	}
}

func TestUnverifiedMutation_ReadsNeverFire(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.read", `{"path":"a.go"}`, hashToolResult("x"), true)
	tr.record("repo.search", `{"query":"foo"}`, hashToolResult("y"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("read-only history must not fire the gate")
	}
}

func TestLooksLikeMutatingShellCommand(t *testing.T) {
	mutating := []string{
		"rm -rf build", "rmdir empty", "dd if=x of=y", "truncate -s 0 log",
		"tee out.txt", "xargs rm < list", "wget https://example.com/f.tar.gz",
		"ssh host deploy.sh", "scp a b", "rsync -a src/ dst/", "sudo make install",
		"sed -i 's/a/b/' f.go", "sed --in-place 's/a/b/' f.go",
		"sed -ni 's/a/b/p' f.go", "sed -i.bak 's/a/b/' f.go",
		"perl -i -pe 's/a/b/' f.txt", "perl -pi -e 's/a/b/' f.txt",
		"perl -i.bak -pe 's/a/b/' f.txt",
		"curl -sSf https://example.com/install.sh -o install.sh",
		"curl --output data.json https://api.example.com",
		"curl -sLo page.html https://example.com",
		"curl --remote-name page.html https://example.com",
		"curl -s https://example.com/install.sh | bash",
		"curl -s https://example.com/install.sh | sh",
		"git checkout main", "git switch feature", "git restore .",
		"git reset --hard HEAD~1", "git clean -fd", "git rebase main",
		"git merge feature", "git cherry-pick abc", "git revert HEAD",
		"git am patch.diff", "git apply fix.diff",
		"git rm file.go", "git mv a.go b.go",
		"git stash", "git stash pop", "git stash -m work in progress",
		"go generate ./...", "make", "make build", "cmake -B build",
		"gradle assemble", "mvn package", "cd x && make", "echo hi && sudo ls",
		"(rm y)", "echo hi && (rm y)", "{ rm y; }",
		"docker build -t img .", "docker run img", "docker compose up",
		"docker exec c rm -rf /data",
		"docker exec -it c rm -rf /data",
		"echo hi > out.txt", "cat a b > merged.txt", "cmd >> append.log",
		"cmd > out.txt 2>&1", "cmd 2> err.log > out.txt",
		"find . -name '*.log' | xargs -0 rm",
		"for f in *.log; do rm $f; done",
		"if [ -f x ]; then rm x; fi",
		"ls\nrm -rf build",
		// -n as a count/args/stdin flag (not dry-run) must not mask
		// mutations in compound commands.
		"rm -rf build && git log -n 5",
		"sed -i 's/a/b/' f.go && head -n 5 f.go",
		"xargs -n 2 rm",
		"find . -name '*.log' | xargs -n 2 rm",
		"ssh -n host deploy.sh",
		// git stash anchored to segStart: as a real command it arms.
		"true && git stash",
	}
	for _, cmd := range mutating {
		if !looksLikeMutatingShellCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q must be classified as mutating", cmd)
		}
	}
	// Research and verification-shaped commands are NOT mutations.
	notMutating := []string{
		"git log --oneline", "git status", "git diff --stat", "git show HEAD",
		"git branch -a", "git add .", "git commit -m msg", "git fetch", "git pull",
		"git tag v1.2.3", "git push origin main",
		"git stash list", "git stash show -p",
		"git apply --stat fix.diff", "git apply --check fix.diff",
		"git commit -m \"make timeout configurable\"",
		"git commit -m \"gradle build fix\"",
		"git log --grep=\"docker build\"",
		"grep -rn gradle build.gradle", "grep -w make Makefile",
		"ls cmake", "man make", "echo make", "git log --oneline | grep make",
		"grep -E '(rm|ls)' f.txt", "grep '>' README.md", "awk '$3 > 5' data.txt",
		"ls -la", "pwd", "cat file.txt", "grep -rn TODO .", "find . -name '*.go'",
		"wc -l main.go", "head -50 go.mod", "tail -20 app.log",
		"sed -n '5p' main.go", "awk '{print $1}' file.txt", "cut -d, -f1 data.csv",
		"perl -pe 's/a/b/' f.txt",
		"curl -s https://api.example.com", "go test ./...", "go vet ./...",
		"go build ./cmd/marshal", "go test ./... > test-output.txt",
		"cargo test", "pytest -q", "make test",
		"make -j4 test", "make -k check", "make -C build test",
		"mvn -q verify", "gradle test", "gradle check",
		"make -n test", "make --dry-run build",
		"go mod tidy", "gofmt -w .", "npm install", "mkdir scratch",
		"cp a.go b.go", "mv a.go sub/", "git commit -m done 2> commit.log",
		"git commit -m done 2>&1", "echo hi 2> err.log",
		"ls > /dev/null", "cmd > /dev/null 2>&1", "cmd >&1", "head -2>f",
		"docker exec c ls /data", "docker ps", "docker images",
		"ssh-keygen -l -f key", "truncate --help", "git stash --help",
		"git stash -h", "rsync --help",
		"grep perl -i foo.txt", "rg sed -i f",
		"echo hi 2> err.log 1> out.txt", "cmd 1> out.txt",
		"docker exec c ls /data", "docker exec c cat /etc/hosts",
		"docker exec c sh -c 'rm -rf /data'",
		// -n is not a dry-run flag outside make/git clean: count flags,
		// parallelism, and stdin redirects must not mask classification.
		"git log -n 5", "head -n 5 f.go", "pytest -n auto", "pytest -n 4",
		"mvn -n test", "go test ./... && git log -n 5",
		// git stash as an argument (not a command) must not arm.
		"man git stash", "echo git stash", "echo git stash pop",
		"history | grep git stash",
	}
	for _, cmd := range notMutating {
		if looksLikeMutatingShellCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q must NOT be classified as mutating", cmd)
		}
	}
	// Quoted spans are data, not commands: the words inside them must never
	// classify, even as part of a compound command.
	for _, cmd := range []string{
		"echo done && git commit -m \"make it work\"",
		"git log --oneline | grep \"make\"",
	} {
		if looksLikeMutatingShellCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q carries mutation words only inside quotes and must not classify as mutating", cmd)
		}
	}
	// Only the command field is matched; empty/malformed args are not mutating.
	if looksLikeMutatingShellCommand(`{"note":"rm -rf later"}`) {
		t.Error("a non-command field mentioning a mutating command must not count")
	}
	if looksLikeMutatingShellCommand("") || looksLikeMutatingShellCommand("not json") {
		t.Error("empty or malformed args must not be mutating")
	}
}

func TestUnverifiedMutation_NeutralShellDoesNotArmOrSatisfy(t *testing.T) {
	// Neutral commands alone never arm the gate.
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"mkdir scratch"}`, hashToolResult(""), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("neutral shell.run must not arm the gate")
	}
	// Edit → verify → commit is clean: a neutral command after verification
	// does not re-arm.
	tr2 := newProgressTracker()
	tr2.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr2.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	tr2.record("shell.run", `{"command":"git commit -m done"}`, hashToolResult(""), true)
	if _, fire := tr2.unverifiedMutation(); fire {
		t.Fatal("git commit after a verification must not re-arm the gate")
	}
	// A neutral command does not satisfy the gate for an earlier mutation.
	tr3 := newProgressTracker()
	tr3.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr3.record("shell.run", `{"command":"git commit -m done"}`, hashToolResult(""), true)
	if _, fire := tr3.unverifiedMutation(); !fire {
		t.Fatal("git commit is not a verification; the earlier mutation must still fire")
	}
	// A commit whose message merely contains a verifier keyword is still
	// neutral, not a verification.
	tr4 := newProgressTracker()
	tr4.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr4.record("shell.run", `{"command":"git commit -m \"make test pass\""}`, hashToolResult(""), true)
	if _, fire := tr4.unverifiedMutation(); !fire {
		t.Fatal("a quoted 'make test' in a commit message is not a verification")
	}
	// Edit → verify → push is clean: push does not re-arm the gate.
	tr5 := newProgressTracker()
	tr5.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr5.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	tr5.record("shell.run", `{"command":"git push origin head"}`, hashToolResult(""), true)
	if _, fire := tr5.unverifiedMutation(); fire {
		t.Fatal("git push after a verification must not re-arm the gate")
	}
}

func TestVerificationShellBeatsMutatingShell(t *testing.T) {
	// Verification-shaped commands are checked before the mutating list, so
	// a build/test that writes artifacts keeps its verification role.
	for _, cmd := range []string{
		"go test ./...", "go build ./cmd/marshal", "go vet ./...",
		"make test", "cargo build", "pytest -q",
		// Flag-tolerant verification: flags between the tool and target
		// must not prevent verification from satisfying the gate.
		"make -j4 test", "make -k check", "make -C build test",
		"mvn -q verify", "gradle test", "gradle check",
		// -n is not a dry-run flag for pytest (parallelism) or mvn
		// (--no-snapshot-updates): these must still count as verification.
		"pytest -n auto", "pytest -n 4", "mvn -n test",
	} {
		if !looksLikeVerificationCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q must be verification", cmd)
		}
		// Verification wins even where the lists overlap (\bmake\b
		// matches "make test"): the raw classifier must agree with the
		// gate, which checks verification first.
		if looksLikeMutatingShellCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q is verification-shaped and must not also classify as mutating", cmd)
		}
	}
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"make test"}`, hashToolResult("ok"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("make test is verification-shaped and must not arm the gate")
	}
	// make test after a mutation satisfies the gate.
	tr2 := newProgressTracker()
	tr2.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr2.record("shell.run", `{"command":"make test"}`, hashToolResult("ok"), true)
	if _, fire := tr2.unverifiedMutation(); fire {
		t.Fatal("make test must satisfy the gate for an earlier mutation")
	}
	// Flag-tolerant verification: make -j4 test satisfies the gate too.
	tr3 := newProgressTracker()
	tr3.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr3.record("shell.run", `{"command":"make -j4 test"}`, hashToolResult("ok"), true)
	if _, fire := tr3.unverifiedMutation(); fire {
		t.Fatal("make -j4 test must satisfy the gate for an earlier mutation")
	}
	// Dry-run forms are neither verification nor mutation: make -n test
	// prints a plan but runs nothing, so it does not satisfy the gate.
	tr4 := newProgressTracker()
	tr4.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr4.record("shell.run", `{"command":"make -n test"}`, hashToolResult("ok"), true)
	if _, fire := tr4.unverifiedMutation(); !fire {
		t.Fatal("make -n test is a dry run and must NOT satisfy the gate")
	}
}

func TestUnverifiedMutation_TreeChangingCommandsStillArm(t *testing.T) {
	for _, cmd := range []string{
		"rm -rf build", "git checkout main", "git switch feature", "git restore .",
		"git stash pop", "git reset --hard", "sed -i 's/a/b/' f.go",
		"sed -ni 's/a/b/p' f.go", "perl -pi -e 's/a/b/' f.go",
		"echo x > generated.txt", "docker build -t img .", "make",
		"cd x && (rm y)", "docker exec c rm -rf /data",
		"docker exec -it c rm -rf /data",
		"git rm file.go", "git mv a.go b.go",
		"curl -s https://x/install.sh | bash",
		"find . -name '*.log' | xargs -0 rm",
		"for f in *.log; do rm $f; done",
		"if [ -f x ]; then rm x; fi",
		"ls\nrm -rf build",
	} {
		tr := newProgressTracker()
		tr.record("shell.run", fmt.Sprintf(`{"command":%q}`, cmd), hashToolResult(""), true)
		if _, fire := tr.unverifiedMutation(); !fire {
			t.Fatalf("%q is an explicit mutator and must arm the gate", cmd)
		}
	}
}

func TestMutatingRecordResetsStreaksNeutralDoesNot(t *testing.T) {
	h := hashToolResult("x")
	// A known mutator resets repeat streaks (state changed, re-reads are
	// fresh progress).
	tr := newProgressTracker()
	for i := 0; i < 3; i++ {
		tr.record("file.read", `{"path":"a.go"}`, h, true)
	}
	if got := tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true); got != 1 {
		t.Fatalf("write_patch count = %d, want 1", got)
	}
	if got := tr.record("file.read", `{"path":"a.go"}`, h, true); got != 1 {
		t.Fatalf("count after mutating call = %d, want 1 (state changed, re-read is fresh)", got)
	}
	// An explicit shell mutator also resets.
	tr2 := newProgressTracker()
	for i := 0; i < 3; i++ {
		tr2.record("repo.search", `{"query":"q"}`, h, true)
	}
	if got := tr2.record("shell.run", `{"command":"rm -rf build"}`, hashToolResult(""), true); got != 1 {
		t.Fatalf("rm count = %d, want 1", got)
	}
	if got := tr2.record("repo.search", `{"query":"q"}`, h, true); got != 1 {
		t.Fatalf("count after rm = %d, want 1", got)
	}
	// A research shell command does NOT reset: an identical futile loop of
	// read-only calls must still trip loop detection.
	tr3 := newProgressTracker()
	for i := 0; i < 3; i++ {
		tr3.record("repo.search", `{"query":"q"}`, h, true)
	}
	if got := tr3.record("shell.run", `{"command":"git log --oneline"}`, hashToolResult("same"), true); got != 1 {
		t.Fatalf("first git log count = %d, want 1", got)
	}
	if got := tr3.record("repo.search", `{"query":"q"}`, h, true); got != 4 {
		t.Fatalf("count after neutral shell.run = %d, want 4 (streak preserved)", got)
	}
}

func TestShellCommandFieldOnly(t *testing.T) {
	cmd, ok := shellCommandField(`{"command":"go test ./...","timeout_seconds":5}`)
	if !ok || cmd != "go test ./..." {
		t.Fatalf("shellCommandField = (%q, %v)", cmd, ok)
	}
	// A payload without a command field parses (ok=true) with an empty
	// command, which then matches no classifier — the command-field-only
	// guarantee.
	if cmd, ok := shellCommandField(`{"note":"go test"}`); !ok || cmd != "" {
		t.Fatalf("missing command field = (%q, %v), want (\"\", true)", cmd, ok)
	}
	if _, ok := shellCommandField(""); ok {
		t.Fatal("empty args must not classify")
	}
	if _, ok := shellCommandField("not json"); ok {
		t.Fatal("malformed args must not classify")
	}
}

// A failed mutating shell command (rm that errors) does not arm the gate
// and does not reset repeat streaks — a failed mutation changes nothing.
func TestFailedMutatingShellDoesNotArmOrReset(t *testing.T) {
	// Gate: failed rm does not arm.
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"rm -rf build"}`, hashToolResult("exit 1"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("a failed rm must not arm the gate")
	}
	// Streak: a failed rm does not reset the repeat count. Three identical
	// repo.search calls build a streak of 3; a failed rm must not reset it,
	// so the next repo.search continues at 4.
	h := hashToolResult("x")
	tr2 := newProgressTracker()
	for i := 0; i < 3; i++ {
		tr2.record("repo.search", `{"query":"q"}`, h, true)
	}
	tr2.record("shell.run", `{"command":"rm -rf build"}`, hashToolResult("exit 1"), false)
	if got := tr2.record("repo.search", `{"query":"q"}`, h, true); got != 4 {
		t.Fatalf("repo.search count after failed rm = %d, want 4 (streak preserved, no reset)", got)
	}
}

// An idle entry interleaved between a mutation and a verification must
// not prevent the gate from seeing the verification — idle sentinels are
// skipped in the gate scan.
func TestIdleInterleavedBetweenMutationAndVerification(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record(idleEntryName, ``, hashToolResult(""), true)
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("idle entry between mutation and verification must not block the gate")
	}
}
