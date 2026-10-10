//go:build !windows

package native

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// fakeSudo writes a `sudo` shell script into a temp dir and prepends it to
// PATH for the duration of the test. The script records its argv and stdin
// so the test can assert the exact invocation shape, then exits with the
// requested code and stderr.
func fakeSudo(t *testing.T, exitCode int, stderr string, readStdin bool) (argvFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	stdinFile = filepath.Join(dir, "stdin")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argvFile + "\n"
	if readStdin {
		script += "cat > " + stdinFile + "\n"
	}
	script += "echo " + shellQuote(stderr) + " >&2\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvFile, stdinFile
}

func TestSudoArgvShape(t *testing.T) {
	argvFile, _ := fakeSudo(t, 0, "", true)
	runner := sudoRunner{fresh: true}
	_, err := runner.Run(context.Background(), CommandRequest{
		Command: "systemctl restart nginx",
		Dir:     t.TempDir(),
		Timeout: 10 * time.Second,
		Stdin:   []byte("hunter2\n"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(argvFile)
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	want := []string{"-k", "-S", "-p", "", "--"}
	for i, w := range want {
		if i >= len(lines) || lines[i] != w {
			t.Fatalf("argv[%d] = %q, want %q (full argv: %q)", i, lines[i], w, lines)
		}
	}
	// The command must be a SINGLE argv element after -c, prefixed with the
	// stdin redirect that stops the payload reading the password.
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "-c exec </dev/null; systemctl restart nginx") {
		t.Fatalf("argv = %q, want the command as one element after -c", lines)
	}
}

// TestSudoPayloadCannotReadThePassword pins the stdin-inheritance fix: sudo
// and the command it execs share fd 0, so a command that reads stdin would
// otherwise print the password into the transcript and the audit ledger.
func TestSudoPayloadCannotReadThePassword(t *testing.T) {
	dir := t.TempDir()
	// A fake sudo that does NOT read stdin, then runs the payload exactly as
	// the real one would: `sh -c <payload>` with the inherited fd 0.
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --) shift; break;; esac; shift; done\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := sudoRunner{fresh: true}
	result, err := runner.Run(context.Background(), CommandRequest{
		Command: "cat", Dir: t.TempDir(), Timeout: 10 * time.Second,
		Stdin: []byte("s3cret\n"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(result.Stdout, "s3cret") {
		t.Fatalf("the payload read the password from stdin: stdout = %q", result.Stdout)
	}
}

func TestSudoPasswordGoesToStdinNotArgv(t *testing.T) {
	argvFile, stdinFile := fakeSudo(t, 0, "", true)
	runner := sudoRunner{fresh: true}
	if _, err := runner.Run(context.Background(), CommandRequest{
		Command: "id", Dir: t.TempDir(), Timeout: 10 * time.Second,
		Stdin: []byte("s3cret\n"),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv, _ := os.ReadFile(argvFile)
	if strings.Contains(string(argv), "s3cret") {
		t.Fatalf("password leaked into argv: %q", argv)
	}
	stdin, _ := os.ReadFile(stdinFile)
	if string(stdin) != "s3cret\n" {
		t.Fatalf("stdin = %q, want the password", stdin)
	}
}

func TestSudoFreshTogglesK(t *testing.T) {
	argvFile, _ := fakeSudo(t, 0, "", true)
	runner := sudoRunner{fresh: false}
	if _, err := runner.Run(context.Background(), CommandRequest{
		Command: "id", Dir: t.TempDir(), Timeout: 10 * time.Second, Stdin: []byte("x\n"),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv, _ := os.ReadFile(argvFile)
	if strings.Contains(string(argv), "-k") {
		t.Fatalf("unlock path must omit -k; argv = %q", argv)
	}
}

func TestSudoEnvIsAllowlistedAndPinned(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	script := "#!/bin/sh\nenv > " + envFile + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")

	runner := sudoRunner{fresh: true}
	if _, err := runner.Run(context.Background(), CommandRequest{
		Command: "id", Dir: t.TempDir(), Timeout: 10 * time.Second, Stdin: []byte("x\n"),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env, _ := os.ReadFile(envFile)
	if strings.Contains(string(env), "LD_PRELOAD") {
		t.Fatalf("LD_PRELOAD reached the privileged child: %q", env)
	}
	if !strings.Contains(string(env), "LC_ALL=C") || !strings.Contains(string(env), "LANG=C") {
		t.Fatalf("locale not pinned to C: %q", env)
	}
}

func TestSudoSignatureMatching(t *testing.T) {
	cases := []struct {
		stderr string
		wrong  bool
		tty    bool
		needs  bool
	}{
		{"sudo: Sorry, try again.", true, false, false},
		{"sudo: no tty present and no askpass program specified", false, true, false},
		{"sudo: a terminal is required to read the password", false, true, false},
		{"sudo: no password was provided", false, false, true},
		{"sudo: a password is required", false, false, true},
		{"systemctl: command not found", false, false, false},
	}
	for _, tc := range cases {
		if got := sudoWrongPassword(tc.stderr); got != tc.wrong {
			t.Errorf("sudoWrongPassword(%q) = %v, want %v", tc.stderr, got, tc.wrong)
		}
		if got := sudoRequiresTTY(tc.stderr); got != tc.tty {
			t.Errorf("sudoRequiresTTY(%q) = %v, want %v", tc.stderr, got, tc.tty)
		}
		if got := sudoNeedsPassword(tc.stderr); got != tc.needs {
			t.Errorf("sudoNeedsPassword(%q) = %v, want %v", tc.stderr, got, tc.needs)
		}
	}
}

func TestSudoToolRefusesWithoutSystemAccess(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:  state,
		SudoPolicy:    func(string) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "shell.sudo", `{"command":"id"}`)
	if err == nil || !strings.Contains(err.Error(), "system access") {
		t.Fatalf("shell.sudo without system access = %v, want a system-access error", err)
	}
}

func TestSudoToolRefusesWithoutSecretPrompt(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true) // SecretPromptCapable stays false (ACP/web default)
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:  state,
		SudoPolicy:    func(string) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "shell.sudo", `{"command":"id"}`)
	if err == nil || !strings.Contains(err.Error(), "cannot prompt") {
		t.Fatalf("shell.sudo without a secret prompt = %v, want a refusal", err)
	}
	if state.PendingSudoAuth() != nil {
		t.Fatal("a refused call must not raise a prompt")
	}
}

func TestSudoToolRefusesPrivilegeWrapper(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)
	state.SetSecretPromptCapable(true)
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:  state,
		SudoPolicy:    func(cmd string) error { return policy.NewEngine(&config.Config{}, nil).CheckPrivileged(cmd) },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "shell.sudo", `{"command":"sudo id"}`)
	if err == nil || !strings.Contains(err.Error(), "privilege wrapper") {
		t.Fatalf("shell.sudo with a wrapped command = %v, want a refusal", err)
	}
}

// TestSudoToolAppliesTheCatastrophicFloor pins the floor-on-payload fix:
// CheckPrivileged deliberately does not reject floor commands, so without an
// explicit GuardrailCheck the tool would be a way to run `rm -rf /etc` as
// root past the floor that shell.run obeys.
func TestSudoToolAppliesTheCatastrophicFloor(t *testing.T) {
	// The privileged runner is hardcoded (not the injected CommandRunner), so
	// this test must put a fake sudo on PATH or it would invoke the real one.
	fakeSudo(t, 0, "", true)
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)
	state.SetSecretPromptCapable(true)
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:  state,
		SudoPolicy:    func(string) error { return nil },
		Guardrail: func(cmd string, system bool) error {
			return policy.NewEngine(&config.Config{}, nil).GuardrailCheck(cmd, system)
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	for _, cmd := range []string{"rm -rf /etc", "mkfs /dev/sda", "shutdown -h now"} {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":`+strconv.Quote(cmd)+`}`)
		if err == nil {
			t.Fatalf("shell.sudo %q = nil error, want the floor to deny it", cmd)
		}
		if !strings.Contains(err.Error(), "guardrail") {
			t.Fatalf("shell.sudo %q = %v, want a guardrail denial", cmd, err)
		}
	}
	// The floor must not reject an ordinary admin command. That call gets
	// past the floor and blocks on the password prompt, so answer it the way
	// the TUI would.
	done := make(chan error, 1)
	go func() {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":"systemctl restart nginx"}`)
		done <- err
	}()
	waitForSudoPrompt(t, state)
	state.PendingSudoAuth().Respond(session.SudoAuthDecision{OK: true, Password: []byte("pw")})
	if err := <-done; err != nil {
		t.Fatalf("shell.sudo with an ordinary command = %v, want it past the floor", err)
	}
}

// waitForSudoPrompt blocks until the pending privileged-auth prompt appears.
func waitForSudoPrompt(t *testing.T, state *session.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if state.PendingSudoAuth() != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no privileged-auth prompt was raised")
}

// TestSudoRunnerEnforcesTimeout pins the timeout fix: sudoRunner bypasses
// execRunner, which is what installs the deadline, so it must install its own
// or a hung privileged command holds the single-flight lock forever.
func TestSudoRunnerEnforcesTimeout(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := sudoRunner{fresh: true}
	start := time.Now()
	result, err := runner.Run(context.Background(), CommandRequest{
		Command: "id", Dir: t.TempDir(), Timeout: 1 * time.Second,
	})
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("Run took %v, want the 1s timeout enforced", elapsed)
	}
	// A killed process stays an error (only a non-zero exit is cleared), so
	// the caller learns the command timed out rather than reading a partial
	// result as success.
	if err == nil {
		t.Fatal("Run = nil error, want the kill to surface as an error")
	}
	if result.Meta.KilledReason != "timeout" {
		t.Fatalf("KilledReason = %q, want \"timeout\"", result.Meta.KilledReason)
	}
}

func TestSudoToolSingleFlight(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)
	state.SetSecretPromptCapable(true)
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:  state,
		SudoPolicy:    func(string) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if !state.TryLockSudo() {
		t.Fatal("TryLockSudo() = false, want true")
	}
	defer state.UnlockSudo()
	_, err := invokeTool(t, reg, "shell.sudo", `{"command":"id"}`)
	if err == nil || !strings.Contains(err.Error(), "another privileged command") {
		t.Fatalf("shell.sudo while the lock is held = %v, want the retry error", err)
	}
}
