package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/trust"
)

// trustUserConfig gives the daemon's runtime a configured model. A trust
// applies in place by reloading the runtime, whose dry run needs a route, and
// a project child inherits this process's environment, so the child's home and
// config directory are the test's. MARSHAL_CONFIG_DIR is per-test, so the
// package's other cases keep their empty user config.
func trustUserConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", dir)
	body := `[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://127.0.0.1:1/v1"
api_key = "mock-key"

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
}

// writeTrustProjectConfig gives the project the config whose trust these tests
// ask about. Its name is what proves the project layer applied.
func writeTrustProjectConfig(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, ".marshal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	body := "[project]\nname = \"" + name + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// waitForTrusted waits until the attachment's session reports the project
// trusted and its config mirror carries the project's name.
func waitForTrusted(t *testing.T, a *attachment, name string) {
	t.Helper()
	waitFor(t, func() error {
		if !a.session.Status().Trusted {
			return errors.New("the status is not trusted yet")
		}
		if got := a.session.Config().Project.Name; got != name {
			return fmt.Errorf("the config mirror's project name is %q, want %q", got, name)
		}
		return nil
	})
}

// waitForNote waits until the session's transcript holds text.
func waitForNote(t *testing.T, ctx context.Context, a *attachment, text string) {
	t.Helper()
	waitFor(t, func() error {
		page, err := a.session.Messages(ctx, acpwire.MessageQuery{Last: 10})
		if err != nil {
			return err
		}
		if !carries(page, text) {
			return fmt.Errorf("the transcript does not hold %q yet", text)
		}
		return nil
	})
}

// evaluate asks the trust store this process's data directory holds whether
// the project still needs a question.
func evaluate(t *testing.T, root string) (needsPrompt bool) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("find home directory: %v", err)
	}
	_, needsPrompt, err = trust.Evaluate(trust.NewStore(config.DataDir(home)), root)
	if err != nil {
		t.Fatalf("evaluate trust: %v", err)
	}
	return needsPrompt
}

// TestAnUntrustedDaemonSessionAsksItsClient is W11's core: a daemon session on
// an untrusted project starts without the project config, asks its client, and
// a permanent trust applies to the running session, which stays attached.
func TestAnUntrustedDaemonSessionAsksItsClient(t *testing.T) {
	trustUserConfig(t)
	h := newDaemonHarness(t)
	writeTrustProjectConfig(t, h.root, "daemon-trust")

	a := h.attach("")
	defer a.close()

	if got := a.host.TrustPrompt(); got.Dir != h.root || !got.InPlace {
		t.Fatalf("trust/prompt = %+v, want %q in place", got, h.root)
	}
	if a.session.Status().Trusted {
		t.Fatal("the session started trusted")
	}
	if got := a.session.Config().Project.Name; got == "daemon-trust" {
		t.Fatal("the untrusted project config was applied before the answer")
	}

	ctx := callCtx(t)
	if err := a.host.DecideTrust(ctx, h.root, acpwire.TrustPermanent); err != nil {
		t.Fatalf("trust/decide: %v", err)
	}
	waitForTrusted(t, a, "daemon-trust")
	waitForNote(t, ctx, a, "Project config trusted; it is in force now.")

	// The attachment is still open: a note still round trips on it.
	const after = "still attached after the trust"
	if err := a.session.AddNote(ctx, acpwire.Note{Text: after, Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("add a note after the trust: %v", err)
	}
	waitForNote(t, ctx, a, after)

	if needsPrompt := evaluate(t, h.root); needsPrompt {
		t.Fatal("the permanent trust was not recorded")
	}

	// A new session on the same project starts trusted, with no prompt.
	b := h.attach("")
	defer b.close()
	if got := b.host.TrustPrompt(); got.Dir != "" {
		t.Fatalf("the second session's prompt = %+v, want none", got)
	}
	if !b.session.Status().Trusted {
		t.Fatal("the second session started untrusted")
	}
}

// TestASessionTrustOverTheDaemonRecordsNothing: a session trust applies to the
// running session and records nothing, so the next session is asked again.
func TestASessionTrustOverTheDaemonRecordsNothing(t *testing.T) {
	trustUserConfig(t)
	h := newDaemonHarness(t)
	writeTrustProjectConfig(t, h.root, "daemon-trust")

	a := h.attach("")
	defer a.close()

	ctx := callCtx(t)
	if err := a.host.DecideTrust(ctx, h.root, acpwire.TrustSession); err != nil {
		t.Fatalf("trust/decide: %v", err)
	}
	waitForTrusted(t, a, "daemon-trust")
	waitForNote(t, ctx, a, "Project config trusted for this session; it is in force now.")

	if needsPrompt := evaluate(t, h.root); !needsPrompt {
		t.Fatal("a session trust recorded a permanent trust")
	}

	b := h.attach("")
	defer b.close()
	if got := b.host.TrustPrompt(); got.Dir != h.root || !got.InPlace {
		t.Fatalf("the second session's prompt = %+v, want it asked again at %q", got, h.root)
	}
}

// TestTwoViewersShareOneTrustAnswer: the service host records the answer, so a
// second viewer's decision succeeds without effect and its status is trusted
// too.
func TestTwoViewersShareOneTrustAnswer(t *testing.T) {
	trustUserConfig(t)
	h := newDaemonHarness(t)
	writeTrustProjectConfig(t, h.root, "daemon-trust")

	first := h.attach("")
	defer first.close()
	second := h.attach(first.conn.SessionID)
	defer second.close()
	if second.conn.SessionID != first.conn.SessionID {
		t.Fatalf("the second viewer attached to %q, want %q", second.conn.SessionID, first.conn.SessionID)
	}

	ctx := callCtx(t)
	if err := first.host.DecideTrust(ctx, h.root, acpwire.TrustPermanent); err != nil {
		t.Fatalf("the first viewer's trust/decide: %v", err)
	}
	waitForTrusted(t, first, "daemon-trust")

	// One answer wins: the second viewer's decision for the same directory
	// succeeds and applies nothing.
	if err := second.host.DecideTrust(ctx, h.root, acpwire.TrustSession); err != nil {
		t.Fatalf("the second viewer's trust/decide: %v", err)
	}
	waitFor(t, func() error {
		if !second.session.Status().Trusted {
			return errors.New("the second viewer's status is not trusted")
		}
		return nil
	})
}
