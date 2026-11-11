package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/trust"
)

// writeClientTrustProject writes a project config naming projectName under
// dir.
func writeClientTrustProject(t *testing.T, dir, projectName string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[project]\nname = \"" + projectName + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeClientTrustUserConfig writes the user config the reload path needs: a
// provider, a preset and a profile, so a config reload can dry-run.
func writeClientTrustUserConfig(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(config.UserDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(config.UserConfigPath(home), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startClientTrustRuntime starts a runtime on a project whose config names
// "client-trust", with the headless resolver over the test home's store, as a
// project child passes it. clientTrust adds WithClientTrust.
func startClientTrustRuntime(t *testing.T, clientTrust bool) (*Runtime, string) {
	t.Helper()
	home := t.TempDir()
	dir := t.TempDir()
	writeClientTrustProject(t, dir, "client-trust")
	writeClientTrustUserConfig(t, home)

	opts := []Option{
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithHomeDir(home),
		WithWorkingDir(dir),
		WithTrustResolver(trust.NewHeadlessResolver(
			trust.NewStore(config.DataDir(home)),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		)),
	}
	if clientTrust {
		opts = append(opts, WithClientTrust())
	}
	rt, err := StartRuntime(context.Background(), opts...)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	return rt, home
}

// lastMessage returns the transcript's last message.
func lastMessage(t *testing.T, rt *Runtime) (string, string) {
	t.Helper()
	messages := rt.State.Messages()
	if len(messages) == 0 {
		t.Fatal("the transcript is empty")
	}
	last := messages[len(messages)-1]
	return string(last.Role), last.Content
}

// TestAClientTrustRuntimeLeavesThePromptPending: WithClientTrust evaluates
// trust as embedded mode does, so an untrusted project starts without its
// config, names the prompt and carries the in-place apply.
func TestAClientTrustRuntimeLeavesThePromptPending(t *testing.T) {
	rt, _ := startClientTrustRuntime(t, true)

	if !rt.TrustPromptPending {
		t.Fatal("TrustPromptPending = false, want the client to be asked")
	}
	if got := rt.State.Config.Project.Name; got == "client-trust" {
		t.Fatal("the project config was applied before the trust answer")
	}
	if rt.State.Trusted() {
		t.Fatal("the session started trusted")
	}

	deps := rt.ServiceDeps(context.Background())
	if deps.TrustPrompt != rt.WorkingDir {
		t.Errorf("ServiceDeps.TrustPrompt = %q, want the runtime's working directory %q", deps.TrustPrompt, rt.WorkingDir)
	}
	if deps.ApplyTrust == nil {
		t.Error("ServiceDeps.ApplyTrust = nil, want the in-place apply")
	}
}

// TestAStoredTrustAsksNothing: a matching permanent record applies the project
// config at startup, so nothing is pending and the deps carry no prompt.
func TestAStoredTrustAsksNothing(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	writeClientTrustProject(t, dir, "client-trust")
	writeClientTrustUserConfig(t, home)
	store := trust.NewStore(config.DataDir(home))
	hash, err := trust.ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("ConfigHashFor: %v", err)
	}
	if err := store.SetTrust(trust.Canonicalize(dir), true, hash); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}

	rt, err := StartRuntime(context.Background(),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithHomeDir(home),
		WithWorkingDir(dir),
		WithTrustResolver(trust.NewHeadlessResolver(store, slog.New(slog.NewTextHandler(io.Discard, nil)))),
		WithClientTrust(),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.TrustPromptPending {
		t.Fatal("TrustPromptPending = true with a matching stored trust")
	}
	if got := rt.State.Config.Project.Name; got != "client-trust" {
		t.Fatalf("project name = %q, want the stored trust to apply it", got)
	}
	deps := rt.ServiceDeps(context.Background())
	if deps.TrustPrompt != "" || deps.ApplyTrust != nil {
		t.Fatalf("ServiceDeps carries prompt %q and apply %v, want neither", deps.TrustPrompt, deps.ApplyTrust != nil)
	}
}

// TestWithoutClientTrustTheHeadlessRuleStays: without the option the headless
// resolver fails closed, so nothing pends and nothing is applied.
func TestWithoutClientTrustTheHeadlessRuleStays(t *testing.T) {
	rt, _ := startClientTrustRuntime(t, false)

	if rt.TrustPromptPending {
		t.Fatal("TrustPromptPending = true without WithClientTrust")
	}
	if got := rt.State.Config.Project.Name; got == "client-trust" {
		t.Fatal("the headless rule applied the project config without a stored trust")
	}
	deps := rt.ServiceDeps(context.Background())
	if deps.TrustPrompt != "" || deps.ApplyTrust != nil {
		t.Fatalf("ServiceDeps carries prompt %q and apply %v, want neither", deps.TrustPrompt, deps.ApplyTrust != nil)
	}
}

// TestAPermanentTrustAppliesInPlace: a permanent trust marks the session
// trusted, applies the name, records the trust and writes the note — all
// without ending the runtime.
func TestAPermanentTrustAppliesInPlace(t *testing.T) {
	rt, home := startClientTrustRuntime(t, true)
	deps := rt.ServiceDeps(context.Background())
	if deps.ApplyTrust == nil {
		t.Fatal("ServiceDeps.ApplyTrust is nil")
	}

	if err := deps.ApplyTrust(context.Background(), trust.DecisionTrustPermanent); err != nil {
		t.Fatalf("ApplyTrust: %v", err)
	}
	if !rt.State.Trusted() {
		t.Fatal("the session is not trusted after a permanent trust")
	}
	if got := rt.State.Config.Project.Name; got != "client-trust" {
		t.Fatalf("project name = %q, want the trusted config applied in place", got)
	}
	if got := rt.State.Layers().Merged.Project.Name; got != "client-trust" {
		t.Fatalf("merged layers' project name = %q, want the trusted layers kept", got)
	}
	_, needsPrompt, err := trust.Evaluate(trust.NewStore(config.DataDir(home)), rt.WorkingDir)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if needsPrompt {
		t.Fatal("Evaluate still needs a question after a permanent trust")
	}
	role, note := lastMessage(t, rt)
	if role != "system" || note != "Project config trusted; it is in force now." {
		t.Fatalf("last message = (%s) %q, want the permanent trust's note", role, note)
	}
}

// TestASessionTrustRecordsNothing: a session trust applies the project config
// but records no permanent trust, so the next session asks again.
func TestASessionTrustRecordsNothing(t *testing.T) {
	rt, home := startClientTrustRuntime(t, true)
	deps := rt.ServiceDeps(context.Background())

	if err := deps.ApplyTrust(context.Background(), trust.DecisionTrustSession); err != nil {
		t.Fatalf("ApplyTrust: %v", err)
	}
	if got := rt.State.Config.Project.Name; got != "client-trust" {
		t.Fatalf("project name = %q, want the name applied", got)
	}
	_, needsPrompt, err := trust.Evaluate(trust.NewStore(config.DataDir(home)), rt.WorkingDir)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !needsPrompt {
		t.Fatal("Evaluate needs no question after a session trust, want it to still ask")
	}
	role, note := lastMessage(t, rt)
	if role != "system" || note != "Project config trusted for this session; it is in force now." {
		t.Fatalf("last message = (%s) %q, want the session trust's note", role, note)
	}
}

// TestAConfigThatDoesNotLoadIsRefused: a project config that no longer parses
// fails the apply before anything is recorded, and the failure note says so.
func TestAConfigThatDoesNotLoadIsRefused(t *testing.T) {
	rt, home := startClientTrustRuntime(t, true)
	deps := rt.ServiceDeps(context.Background())
	if deps.ApplyTrust == nil {
		t.Fatal("ServiceDeps.ApplyTrust is nil")
	}
	if err := os.WriteFile(filepath.Join(rt.WorkingDir, ".marshal", "config.toml"),
		[]byte("this is not toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := deps.ApplyTrust(context.Background(), trust.DecisionTrustPermanent); err == nil {
		t.Fatal("ApplyTrust succeeded with a project config that does not parse")
	}
	if rt.State.Trusted() {
		t.Fatal("the session was marked trusted by a failed apply")
	}
	trusted, err := trust.NewStore(config.DataDir(home)).IsTrusted(trust.Canonicalize(rt.WorkingDir))
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("the failed apply recorded a permanent trust")
	}
	role, note := lastMessage(t, rt)
	if role != "system" || !strings.HasPrefix(note, "✗ Trust not applied: ") {
		t.Fatalf("last message = (%s) %q, want the failure note", role, note)
	}
}
