//go:build probe

// probe_kimi drives the production subagent dispatch path against the
// kimi/k3-256k preset:
//   1. config.Load (merges HOME + project)
//   2. cfg.RoutingConfig() -> routing.Config
//   3. routing.NewStaticRouter(...) -> StaticRouter
//   4. router.ResolveRole(RoleImplementer) -> Route (the same call agent.Runner makes)
//   5. assert route.Preset.Temperature == 1.0 (the kimi preset's required value)
//   6. provider.NewFromConfig(route.Preset.Provider, ...)
//   7. assert Capabilities().TemperatureLocked matches the config flag (round-trip)
//   8. provider.ChatText(ctx, ChatRequest{Temperature: route.Preset.Temperature, ...})
//
// Run:
//   CGO_ENABLED=1 go run -tags probe ./cmd/marshal/probe_kimi

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func probeKimiMain() int {
	home, _ := os.UserHomeDir()
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get working dir: %v\n", err)
		return 1
	}
	trusted := true
	cfg, err := config.Load(config.LoadOptions{
		HomeDir:    home,
		WorkingDir: workdir,
		Trusted:    &trusted,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return 1
	}

	// Force the active preset to kimi/k3-256k so we test the same route a
	// /sdd or /plan dispatch would resolve.
	presetName := "kimi/k3-256k"
	cfg.Profile.Default = ""
	cfg.Profile.ActivePreset = presetName

	pcfg, ok := cfg.Providers["kimi"]
	if !ok {
		fmt.Fprintln(os.Stderr, "kimi provider not found")
		return 1
	}

	rc := cfg.RoutingConfig()
	router := routing.NewStaticRouter(rc)
	route, err := router.ResolveRole(routing.RoleImplementer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve RoleImplementer: %v\n", err)
		return 1
	}

	fmt.Printf("route: profile=%s preset=%s/%s\n", route.Profile, route.Preset.Provider, route.Preset.Model)
	if route.Preset.Temperature != nil {
		fmt.Printf("route.Preset.Temperature: %v\n", *route.Preset.Temperature)
		if *route.Preset.Temperature != 1.0 {
			fmt.Fprintf(os.Stderr, "FAIL: expected Temperature=1.0, got %v\n", *route.Preset.Temperature)
			return 3
		}
	} else {
		fmt.Fprintln(os.Stderr, "FAIL: route.Preset.Temperature is nil — expected the kimi preset to pin temperature 1.0")
		return 3
	}

	p, err := provider.NewFromConfig(route.Preset.Provider, pcfg, "", false, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new provider: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if caps := p.Capabilities(ctx); caps.TemperatureLocked != pcfg.TemperatureLocked {
		fmt.Fprintf(os.Stderr, "FAIL: Capabilities().TemperatureLocked = %v, want %v (config flag did not round-trip through the factory)\n",
			caps.TemperatureLocked, pcfg.TemperatureLocked)
		return 3
	}
	fmt.Printf("capabilities.TemperatureLocked: %v\n", pcfg.TemperatureLocked)

	req := schema.ChatRequest{
		Model:  route.Preset.Model,
		Stream: false,
		// Exactly what the runner would inject. When the provider config sets
		// temperature_locked, the runner gate suppresses this before the wire;
		// the probe deliberately bypasses that gate to verify raw endpoint behavior.
		Temperature: route.Preset.Temperature,
		Messages: []schema.ChatMessage{
			{
				Role:    schema.RoleSystem,
				Content: "You are a helpful assistant. Be concise.",
			},
			{
				Role:    schema.RoleUser,
				Content: "Pick a random integer between 1 and 100 (inclusive). Reply with only that number, no words.",
			},
		},
	}

	text, err := provider.ChatText(ctx, p, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ChatText error: %v\nreply-so-far=%q\n", err, text)
		return 2
	}
	fmt.Printf("assistant: %q\n", text)
	return 0
}
