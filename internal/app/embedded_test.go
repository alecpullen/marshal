package app

import (
	"context"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/trust"
)

func TestNewEmbeddedResolvesOptionsOnce(t *testing.T) {
	// Regression test for F-BUG-154: options must be applied exactly once, by
	// NewEmbedded, not once there and again in each Start.
	applyCount := 0
	customOpt := func(opts *options) {
		applyCount++
	}

	embedded, err := NewEmbedded(context.Background(),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		customOpt,
	)
	if err != nil {
		t.Fatalf("NewEmbedded() error = %v", err)
	}
	defer embedded.Close(context.Background())
	if applyCount != 1 {
		t.Fatalf("option applied %d times, want 1", applyCount)
	}

	ctx := context.Background()
	rt, err := embedded.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	embedded.Resume(rt.SessionID)
	rt, err = embedded.Start(ctx)
	if err != nil {
		t.Fatalf("Start() after Resume() error = %v", err)
	}
	if err := embedded.Finish(nil); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if applyCount != 1 {
		t.Fatalf("option applied %d times over start/resume/start, want 1", applyCount)
	}
}

// TestServiceDepsServesTheRuntime pins what the runtime hands the client
// seam's service: the runtime's state, tools, reload path and work, and no
// trust prompt of its own (P3 R1).
func TestServiceDepsServesTheRuntime(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("service-deps")
	// The default autoload injects a skill body into a fresh session; opt out
	// so the fixture stays about the service's deps.
	cfg.Skills.Autoload = nil

	rt, err := StartRuntime(ctx,
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)
	if rt.Runner == nil {
		t.Fatal("the fixture provider was not built")
	}

	deps := rt.ServiceDeps(ctx)
	if deps.State != rt.State {
		t.Fatal("the deps do not serve the runtime's state")
	}
	if names, ok := deps.Tools(); !ok || len(names) == 0 {
		t.Fatalf("Tools() = %v, %v, want the runtime's tool names", names, ok)
	}
	if deps.ReloadConfig == nil || deps.ReloadLayers == nil {
		t.Fatal("the deps carry no reload path")
	}
	if deps.TrustRefresh == nil {
		t.Fatal("the deps carry no trust refresh")
	}
	if deps.Work == nil {
		t.Fatal("the deps serve no work for a running provider")
	}
	// The startup trust prompt and its decision belong to the embedding.
	if deps.TrustPrompt != "" {
		t.Fatalf("ServiceDeps set the trust prompt to %q", deps.TrustPrompt)
	}
	if deps.DecideTrust != nil {
		t.Fatal("ServiceDeps set the trust decision")
	}
	host, sess := service.New(deps)
	if host == nil || sess == nil || sess.State() != rt.State {
		t.Fatal("the service over the runtime's deps does not serve its state")
	}

	// A runtime whose provider build failed serves no work, as providerReady
	// decides.
	deadRT, err := StartRuntime(ctx,
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
	)
	if err != nil {
		t.Fatalf("StartRuntime without a provider: %v", err)
	}
	defer deadRT.Close(ctx)
	if n, ok := deadRT.State.Notice(); !ok || n.Category != session.NoticeProvider {
		t.Fatalf("the provider-less runtime recorded notice %+v, want a provider notice", n)
	}
	if deadDeps := deadRT.ServiceDeps(ctx); deadDeps.Work != nil {
		t.Fatal("the deps serve work without a provider")
	}
}
