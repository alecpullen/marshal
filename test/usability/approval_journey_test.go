package usability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/trust"
	"marshal/test/usability/harness"
	"marshal/test/usability/screen"
)

// approvalCommand is the exact command both subtests script the agent to run.
// It matches the workspace's confirmation rule character for character, so the
// rule — not a glob or a prefix — is what the policy engine sees.
const approvalCommand = "rm -f approval-marker.txt"

const (
	approvalMarkerName    = "approval-marker.txt"
	approvalMarkerContent = "keep me\n"
)

// TestScriptedApprovalDetailsAndDecision drives a real 80x24 approval twice:
// once refusing it and once accepting it. The two outcomes are asserted from
// independent evidence — the marker file's bytes on disk and the tool-role
// message the agent sent back to the provider — never from the model's final
// text, which is only a completion signal.
//
// The approval screen identifies a shell call as "Agent wants to run:" plus the
// command; "shell.run" is its internal name (DisplayToolName maps it to "Run
// command" in the transcript). The screen assertions therefore target the
// identity the user is actually shown.
func TestScriptedApprovalDetailsAndDecision(t *testing.T) {
	bin := requireBinary(t)

	for _, tc := range []struct {
		name string
		deny bool
	}{
		{name: "deny", deny: true},
		{name: "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			marker := filepath.Join(workDir, approvalMarkerName)
			writeMarker(t, marker, approvalMarkerContent)
			// Test-only shell policy: an exact confirmation rule for this one
			// command and passthrough execution, so the command runs on the
			// host instead of inside the restricted sandbox. The workspace is
			// trusted below, which is what makes this project layer apply.
			writeApprovalPolicy(t, workDir)

			p := newFixtureProvider(t, []scriptedReply{
				{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand})},
				{Text: "finished"},
			})
			env := isolatedAgentEnv(t, p.URL())
			trustTestWorkspace(t, env.HomeDir, workDir)

			sess, err := harness.New(harness.Config{
				BinaryPath: bin,
				Width:      80,
				Height:     24,
				WorkDir:    workDir,
				Env:        env.Env,
			})
			if err != nil {
				t.Fatalf("harness.New: %v", err)
			}
			defer sess.Close()

			waitForViewport(t, sess, 60*time.Second, "idle composer", func(scr screen.Screen) bool {
				return strings.Contains(scr.Content, "❯")
			})

			if err := sess.Send("Run the command we agreed on."); err != nil {
				t.Fatalf("Send(goal): %v", err)
			}
			if err := sess.SendKey("enter"); err != nil {
				t.Fatalf("SendKey(enter): %v", err)
			}

			// The live approval must name the operation and show the exact
			// command being authorized, at 80x24, before any decision. The
			// docked panel titles itself "Approval" and reports why it fired:
			// "confirmed by rule" is what proves the workspace's exact
			// [tools.shell.confirm] entry matched, rather than rm's destructive
			// default.
			waitForViewport(t, sess, 90*time.Second, "approval screen", func(scr screen.Screen) bool {
				return strings.Contains(scr.Content, "Approval") &&
					strings.Contains(scr.Content, "Agent wants to run:") &&
					strings.Contains(scr.Content, approvalCommand) &&
					strings.Contains(scr.Content, "Reason: confirmed by rule")
			})

			if tc.deny {
				// Escape twice inside the panel's 1.5s re-press window. The two
				// presses are separated just enough that the terminal decoder
				// cannot fuse them into one escape sequence, and well inside the
				// window.
				if err := sess.SendKey("esc"); err != nil {
					t.Fatalf("SendKey(esc) first: %v", err)
				}
				time.Sleep(100 * time.Millisecond)
				if err := sess.SendKey("esc"); err != nil {
					t.Fatalf("SendKey(esc) second: %v", err)
				}
			} else {
				// The real confirmation is two Enters: the first arms, the
				// second executes. Waiting for the armed prompt is what proves
				// the first press landed instead of racing the second.
				if err := sess.SendKey("enter"); err != nil {
					t.Fatalf("SendKey(enter) arm: %v", err)
				}
				waitForViewport(t, sess, 30*time.Second, "armed approval", func(scr screen.Screen) bool {
					return strings.Contains(scr.Content, "Press Enter again to confirm")
				})
				if err := sess.SendKey("enter"); err != nil {
					t.Fatalf("SendKey(enter) confirm: %v", err)
				}
			}

			waitForViewport(t, sess, 60*time.Second, "approval closed", func(scr screen.Screen) bool {
				return !strings.Contains(scr.Content, "Agent wants to run:")
			})

			// The turn settles only when the scripted final reply is on screen:
			// the tool result was delivered and the model answered.
			waitForViewport(t, sess, 60*time.Second, "turn settled", func(scr screen.Screen) bool {
				return strings.Contains(scr.Content, "finished")
			})

			// The provider's record is the independent oracle for what the tool
			// reported: a denial and a success must not look alike.
			msgs := waitForToolMessages(t, p, 1, 60*time.Second, "tool result")
			if len(msgs) != 1 {
				t.Fatalf("provider recorded %d tool messages, want 1: %+v", len(msgs), msgs)
			}
			// The agent echoes the provider's call id, so the recorded result
			// really answers the scripted call.
			if msgs[0].CallID != "call_1" {
				t.Fatalf("tool result call id = %q, want call_1", msgs[0].CallID)
			}

			if tc.deny {
				if !strings.Contains(msgs[0].Content, "denied by user") {
					t.Fatalf("denied tool result = %q, want a denial", msgs[0].Content)
				}
				// The command must never have run: not a byte changed.
				got, err := os.ReadFile(marker)
				if err != nil {
					t.Fatalf("marker file is gone after a denied approval: %v", err)
				}
				if string(got) != approvalMarkerContent {
					t.Fatalf("marker content = %q after a denied approval, want %q", got, approvalMarkerContent)
				}
			} else {
				if !strings.HasPrefix(msgs[0].Content, "Tool shell.run result:") {
					t.Fatalf("allowed tool result = %q, want a successful shell.run result", msgs[0].Content)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("marker file still exists after an approved command: stat error %v", err)
				}
			}

			// An extra completion, a stray endpoint or an unreadable body means
			// the turn did something the script does not describe.
			p.AssertNoUnexpectedRequests(t)
		})
	}
}

// writeMarker creates the file the scripted command targets.
func writeMarker(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write marker %s: %v", path, err)
	}
}

// writeApprovalPolicy writes the test-only shell policy into the workspace's
// project config. It carries no provider or preset entries: those are
// user-global, and a trusted project file that still had them would be hoisted
// on load.
func writeApprovalPolicy(t *testing.T, workDir string) {
	t.Helper()
	dir := filepath.Join(workDir, ".marshal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	policy := `[tools.shell.confirm]
commands = ["` + approvalCommand + `"]

[tools.shell.sandbox]
backend = "passthrough"
unsafe_passthrough = true
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(policy), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// trustTestWorkspace seeds the child's own trust store so the workspace's
// project config is merged. The store lives under the isolated home's
// MARSHAL_DATA_DIR, so the developer's real trust store is never read or
// written; policy itself is untouched.
func trustTestWorkspace(t *testing.T, homeDir, workDir string) {
	t.Helper()
	hash, err := trust.ConfigHashFor(workDir)
	if err != nil {
		t.Fatalf("hash project config: %v", err)
	}
	if hash == "" {
		t.Fatalf("workspace has no project config for the trust record to cover")
	}
	store := trust.NewStore(filepath.Join(homeDir, ".local", "share", "marshal"))
	if err := store.SetTrust(trust.Canonicalize(workDir), true, hash); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}
}

// mustJSON marshals a scripted tool's arguments, failing the test rather than
// sending the provider an invalid argument blob.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	return data
}

// waitForToolMessages blocks until the provider has recorded at least want
// tool-role messages, returning what it has. The bound is what keeps a turn
// that never reports back from hanging the suite.
func waitForToolMessages(t *testing.T, p *fixtureProvider, want int, timeout time.Duration, what string) []capturedToolMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if msgs := p.ToolMessages(); len(msgs) >= want {
			return msgs
		}
		if time.Now().After(deadline) {
			msgs := p.ToolMessages()
			t.Fatalf("%s: provider recorded %d tool messages in %s, want %d", what, len(msgs), timeout, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
