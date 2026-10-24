//go:build !windows

package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// newSudoAuthorizer builds the REAL authorizer over a test-owned authority, so
// every privileged test below exercises the production admission contract
// rather than a nil-means-trusted stub.
func newSudoAuthorizer(t *testing.T) (*policy.Authorizer, *testAuthority) {
	t.Helper()
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	cfg.Tools.Shell.Confirm.Commands = []string{"git reset --hard", "git push"}
	cfg.Tools.Shell.FloorCommands = []string{"git push"}
	authority := newTestAuthority()
	authority.snap.Access.System = true
	return policy.NewAuthorizer(policy.NewEngine(&cfg, nil), authority), authority
}

func sudoRegistry(t *testing.T, root string, state *session.State, authorizer CommandAuthorizer) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:     root,
		CommandRunner:     &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:      state,
		CommandAuthorizer: authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg
}

func sudoAdmittedToolForTest(t *testing.T) (*registry.Registry, *session.State, *policy.Authorizer, *testAuthority) {
	t.Helper()
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)
	state.SetSecretPromptCapable(true)
	authorizer, authority := newSudoAuthorizer(t)
	return sudoRegistry(t, root, state, authorizer), state, authorizer, authority
}

// TestSudoWithoutCommandAuthorizerFailsClosed pins that a registry with no
// authorizer refuses a privileged command before any prompt is raised and
// before any process runs. There is no unadmitted privileged path: the legacy
// SudoPolicy callback is gone, so a missing authorizer is a refusal rather than
// a trusted bypass.
func TestSudoWithoutCommandAuthorizerFailsClosed(t *testing.T) {
	argvFile, _ := fakeSudo(t, 0, "", true)
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)
	state.SetSecretPromptCapable(true)
	reg := sudoRegistry(t, root, state, nil)

	_, err := invokeTool(t, reg, "shell.sudo", `{"command":"id"}`)
	if err == nil || !strings.Contains(err.Error(), "no command authorizer") {
		t.Fatalf("shell.sudo without an authorizer = %v, want a fail-closed refusal", err)
	}
	if state.PendingSudoAuth() != nil {
		t.Fatal("a refused call must not raise a prompt")
	}
	if _, statErr := os.Stat(argvFile); !os.IsNotExist(statErr) {
		t.Fatal("the privileged runner ran without an authorizer")
	}
}

func TestFullAutoSudoDoesNotAskForPassword(t *testing.T) {
	fakeSudo(t, 1, "sudo: a password is required", false)
	cfg := config.Default()
	cfg.Agent.Auto = true
	state := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	authorizer := policy.NewAuthorizer(policy.NewEngine(&cfg, nil), state.InteractionAuthority())
	reg := sudoRegistry(t, state.WorkingDir, state, authorizer)
	_, err := invokeTool(t, reg, "shell.sudo", `{"command":"id"}`)
	if err == nil || !strings.Contains(err.Error(), "Full Auto cannot prompt") {
		t.Fatalf("sudo = %v, want credential blocker", err)
	}
	if state.PendingSudoAuth() != nil || state.PendingApproval() != nil {
		t.Fatal("Full Auto opened a prompt")
	}
}

func TestEnablingFullAutoClosesPendingCredentialPrompt(t *testing.T) {
	fakeSudo(t, 0, "", false)
	reg, state, _, _ := sudoAdmittedToolForTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	tool, _ := reg.Lookup("shell.sudo")
	go func() {
		_, err := tool.Handler(ctx, registry.ToolCall{ID: "sudo", Name: "shell.sudo", Args: []byte(`{"command":"id"}`)})
		done <- err
	}()
	waitForSudoPrompt(t, state)
	on := true
	if _, err := state.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "Full Auto cannot prompt") {
		t.Fatalf("sudo = %v, want credential blocker", err)
	}
	if state.PendingSudoAuth() != nil {
		t.Fatal("Full Auto left a credential prompt open")
	}
}

// TestSudoFreshPromptCombinesExactConsent pins the combined prompt: one dialog
// discloses the exact command and the caller's reason, carries the
// command-approval role, and its approval is what admits the attempt.
func TestSudoFreshPromptCombinesExactConsent(t *testing.T) {
	argvFile, _ := fakeSudo(t, 0, "", true)
	reg, state, _, authority := sudoAdmittedToolForTest(t)

	done := make(chan error, 1)
	go func() {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":"git reset --hard","reason":"recover the working tree"}`)
		done <- err
	}()
	sa := waitForSudoPrompt(t, state)
	if !sa.CommandApproval {
		t.Fatal("the fresh prompt must carry the command-approval role")
	}
	if sa.Command != "git reset --hard" {
		t.Fatalf("prompt command = %q, want the exact command", sa.Command)
	}
	if !strings.Contains(sa.Reason, "recover the working tree") {
		t.Fatalf("prompt reason = %q, want the caller's reason", sa.Reason)
	}
	sa.Respond(session.SudoAuthDecision{OK: true, Password: []byte("pw")})
	if err := <-done; err != nil {
		t.Fatalf("shell.sudo = %v, want success", err)
	}
	if _, err := os.Stat(argvFile); err != nil {
		t.Fatalf("the privileged runner did not run: %v", err)
	}
	leases, releases := authority.counts()
	if leases != 1 || releases != 1 {
		t.Fatalf("admissions = %d leases / %d releases, want one admitted and released attempt", leases, releases)
	}
}

// TestSudoWarmUnlockStillRequiresCommandConsent pins that a warm unlock grant is
// a credential, never consent: a command that needs specific approval is still
// prompted for, before the warm attempt runs.
func TestSudoWarmUnlockStillRequiresCommandConsent(t *testing.T) {
	fakeSudo(t, 0, "", false) // a warm grant: exits 0 without reading stdin
	reg, state, _, _ := sudoAdmittedToolForTest(t)
	state.SetSudoUnlockEnabled(true)

	done := make(chan error, 1)
	go func() {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":"git reset --hard"}`)
		done <- err
	}()
	sa := waitForSudoPrompt(t, state)
	if !sa.CommandApproval {
		t.Fatal("a warm credential is not command consent: the prompt must still collect it")
	}
	sa.Respond(session.SudoAuthDecision{OK: true, Password: []byte("pw")})
	if err := <-done; err != nil {
		t.Fatalf("shell.sudo = %v, want success after the combined approval", err)
	}
}

// TestSudoPayloadFloorAndPublishingUseSharedAnalysis pins that the bare payload
// goes through the same analysis ordinary commands use: the catastrophic floor
// and the privilege-wrapper refusal deny it before any prompt or process, and
// publishing still retains exact approval.
func TestSudoPayloadFloorAndPublishingUseSharedAnalysis(t *testing.T) {
	argvFile, _ := fakeSudo(t, 0, "", true)
	reg, state, _, _ := sudoAdmittedToolForTest(t)

	for _, command := range []string{"mkfs.ext4 /dev/sda", "shutdown -h now", "sudo id"} {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":`+strconv.Quote(command)+`}`)
		if err == nil {
			t.Fatalf("shell.sudo %q = nil error, want the shared analysis to refuse it", command)
		}
		if !strings.Contains(err.Error(), "denied by policy") {
			t.Fatalf("shell.sudo %q = %v, want a policy denial", command, err)
		}
		if state.PendingSudoAuth() != nil {
			t.Fatalf("shell.sudo %q raised a prompt; a Deny must refuse before asking", command)
		}
	}
	if _, err := os.Stat(argvFile); !os.IsNotExist(err) {
		t.Fatal("a denied privileged payload reached the runner")
	}

	// Publishing keeps exact approval: it prompts with the command role and
	// runs once that prompt has approved the exact command.
	done := make(chan error, 1)
	go func() {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":"git push origin main"}`)
		done <- err
	}()
	sa := waitForSudoPrompt(t, state)
	if !sa.CommandApproval {
		t.Fatal("publishing must retain exact command approval")
	}
	sa.Respond(session.SudoAuthDecision{OK: true, Password: []byte("pw")})
	if err := <-done; err != nil {
		t.Fatalf("publishing payload = %v, want success after approval", err)
	}
}

// TestSudoModeChangeBeforeAttemptRejects pins that admission revalidates the
// live authority immediately before the process starts: an approval granted
// under one mode cannot dispatch after the owner changes it.
func TestSudoModeChangeBeforeAttemptRejects(t *testing.T) {
	argvFile, _ := fakeSudo(t, 0, "", true)
	reg, state, _, authority := sudoAdmittedToolForTest(t)

	done := make(chan error, 1)
	go func() {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":"git reset --hard"}`)
		done <- err
	}()
	sa := waitForSudoPrompt(t, state)
	// The owner switches to Ask between the approval and the attempt.
	authority.setMode(interaction.ModeAsk)
	sa.Respond(session.SudoAuthDecision{OK: true, Password: []byte("pw")})
	err := <-done
	if !errors.Is(err, policy.ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired after the mode change", err)
	}
	if _, statErr := os.Stat(argvFile); !os.IsNotExist(statErr) {
		t.Fatal("the privileged runner ran after the mode changed")
	}
}

// TestSudoPasswordRetryCannotReuseStaleAuthority pins that a retry re-evaluates
// the current policy instead of renewing the previous proof: once the owner
// stops admitting the command, the retry is refused with no second prompt and
// no second dispatch.
func TestSudoPasswordRetryCannotReuseStaleAuthority(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := "#!/bin/sh\n" +
		"echo run >> " + runs + "\n" +
		"echo 'sudo: Sorry, try again.' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake privileged binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	state.SetSystemAccess(true)
	state.SetSecretPromptCapable(true)
	base, authority := newSudoAuthorizer(t)
	// The flip is armed by the test once the first prompt has been answered, so
	// it lands on the retry's evaluation rather than on the prompt path.
	var flipMode atomic.Bool
	flipping := &flippingAuthorizer{Authorizer: base, onEvaluation: func(int) {
		if flipMode.Load() {
			authority.setMode(interaction.ModeAsk)
		}
	}}
	reg := sudoRegistry(t, root, state, flipping)

	done := make(chan error, 1)
	go func() {
		_, err := invokeTool(t, reg, "shell.sudo", `{"command":"git reset --hard"}`)
		done <- err
	}()
	// A refusal that arrives before the prompt is a different failure from a
	// missing prompt; report the handler's own error instead of timing out.
	select {
	case err := <-done:
		t.Fatalf("shell.sudo failed before prompting: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	first := waitForSudoPrompt(t, state)
	flipMode.Store(true)
	first.Respond(session.SudoAuthDecision{OK: true, Password: []byte("bad")})

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the retry did not settle; it may have prompted again")
	}
	if err == nil || !strings.Contains(err.Error(), "denied by policy") {
		t.Fatalf("err = %v, want the retry refused by the current policy", err)
	}
	if state.PendingSudoAuth() != nil {
		t.Fatal("a second prompt was raised; a stale proof must not be renewed")
	}
	if got := countFileLines(t, runs); got != 1 {
		t.Fatalf("privileged runs = %d, want exactly 1 (the retry must not dispatch)", got)
	}
}

// flippingAuthorizer flips the owner's mode on a chosen evaluation, which is how
// these tests place a policy change between a settled prompt and the next
// attempt without racing the handler.
type flippingAuthorizer struct {
	*policy.Authorizer
	calls        int
	onEvaluation func(int)
}

func (f *flippingAuthorizer) Evaluate(ctx context.Context, call policy.Call) (*policy.Authorization, policy.Evaluation, error) {
	f.calls++
	if f.onEvaluation != nil {
		f.onEvaluation(f.calls)
	}
	return f.Authorizer.Evaluate(ctx, call)
}

func countFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}
