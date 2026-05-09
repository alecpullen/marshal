package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/gateway/detect"
	"github.com/alecpullen/marshal/internal/gateway/integration"
	"github.com/alecpullen/marshal/internal/models"
	"github.com/spf13/cobra"
)

// doctorCmd returns the doctor subcommand.
func doctorCmd(gf *globalFlags) *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Marshal configuration and detect available providers",
		Long: `Diagnose Marshal setup by:
  - Detecting available model providers (cloud APIs and local servers)
  - Checking API key availability
  - Showing recommended profile based on detected providers
  - Displaying current bindings and budgets
  - Suggesting fixes for any issues`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Options{Profile: gf.profile})
			if err != nil {
				// Don't fail on config error - we can still detect providers
				cfg = &config.Config{}
			}

			fmt.Println("🔍 Marshal Doctor")
			fmt.Println(strings.Repeat("=", 50))
			fmt.Println()

			// 1. Detect providers
			fmt.Println("📡 Detecting providers...")
			detector := detect.NewDetector()
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			
			providers := detector.Probe(ctx)
			if len(providers) == 0 {
				fmt.Println("   ❌ No providers detected")
				fmt.Println()
				printProviderSetupHelp()
			} else {
				for _, p := range providers {
					status := "✅"
					if !p.AuthAvailable && p.IsCloud() {
						status = "⚠️  (no API key)"
					}
					fmt.Printf("   %s %s", status, p.Name)
					if verbose && len(p.AvailableModels) > 0 {
						fmt.Printf(" [%d models]", len(p.AvailableModels))
					}
					fmt.Println()
				}
				fmt.Println()
			}

		// 2. Show recommended profile
		result := detector.Analyze(cmd.Context())
		recommended := getRecommendedProfileFromResult(result)
		if recommended != "" {
			fmt.Printf("🎯 Recommended profile: %s\n", recommended)
			fmt.Printf("   Run: marshal profile use %s\n", recommended)
			fmt.Println()
		}

			// 3. Check configuration
			fmt.Println("⚙️  Configuration")
			if cfg.Model.Executor.Model == "" {
				fmt.Println("   ❌ No executor model configured")
			} else {
				fmt.Printf("   ✅ Executor: %s\n", cfg.Model.Executor.Model)
			}

			if cfg.Model.Critic.Model == "" {
				fmt.Println("   ⚠️  No critic model configured (will use executor)")
			} else {
				fmt.Printf("   ✅ Critic: %s\n", cfg.Model.Critic.Model)
			}
			fmt.Println()

			// 4. Show current bindings if using gateway
			if gf.profile == "" || gf.profile == "gateway" {
				fmt.Println("🔗 Gateway Bindings")
				
				modelReg, _ := models.LoadDefault()
				reg, err := integration.NewGatewayRegistry(cfg, modelReg)
				if err != nil {
					fmt.Printf("   ⚠️  Could not initialize gateway: %v\n", err)
				} else {
					router := reg.GetRouter()
					roles := router.ListRoles()
					if len(roles) == 0 {
						fmt.Println("   (none configured - will auto-resolve)")
					} else {
						sort.Strings(roles)
						for _, role := range roles {
							binding, _ := router.GetBinding(role)
							fmt.Printf("   %s → %s\n", role, binding.String())
						}
					}
				}
				fmt.Println()
			}

			// 5. Print summary
			fmt.Println(strings.Repeat("=", 50))
			if len(providers) == 0 {
				fmt.Println("❌ Marshal is not ready - no providers available")
				os.Exit(1)
			} else if cfg.Model.Executor.Model == "" {
				fmt.Println("⚠️  Marshal can run but needs configuration")
				fmt.Println("   Run 'marshal init' to create a sample config")
			} else {
				fmt.Println("✅ Marshal is ready to use!")
				fmt.Println("   Run 'marshal chat' to start an interactive session")
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed information")
	return cmd
}

// profileCmd returns the profile subcommand.
func profileCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage Marshal configuration profiles",
	}

	cmd.AddCommand(
		profileListCmd(),
		profileUseCmd(),
		profileShowCmd(),
	)

	return cmd
}

// profileListCmd lists available profiles.
func profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Available profiles:")
			fmt.Println()
			
			profiles := []struct {
				name        string
				description string
			}{
				{"local-only", "All inference local. No API keys needed. Best for privacy."},
				{"balanced", "Frontier orchestration, local execution. Best cost/quality tradeoff."},
				{"quality", "Frontier models for everything. Best results, highest cost."},
				{"budget", "Cheapest viable. Mix of small frontier models and local."},
			}
			
			for _, p := range profiles {
				fmt.Printf("  %s\n", p.name)
				fmt.Printf("    %s\n", p.description)
				fmt.Println()
			}
			
			fmt.Println("Use 'marshal profile use <name>' to activate a profile")
			return nil
		},
	}
}

// profileUseCmd activates a profile.
func profileUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <profile-name>",
		Short: "Activate a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileName := args[0]
			
			validProfiles := map[string]bool{
				"local-only": true,
				"balanced":   true,
				"quality":    true,
				"budget":     true,
			}
			
			if !validProfiles[profileName] {
				return fmt.Errorf("unknown profile: %s (valid: local-only, balanced, quality, budget)", profileName)
			}
			
			// Create profile configuration
			cfg := createProfileConfig(profileName)
			
			// Write to marshal.toml
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			
			configPath := filepath.Join(cwd, "marshal.toml")
			
			// Check if file exists
			if _, err := os.Stat(configPath); err == nil {
				// File exists - append profile section
				f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0644)
				if err != nil {
					return fmt.Errorf("opening config: %w", err)
				}
				defer f.Close()
				
				fmt.Fprintf(f, "\n# Activated profile: %s\n", profileName)
				fmt.Fprintf(f, "profile = \"%s\"\n", profileName)
			} else {
				// Create new file with full profile
				if err := writeFullProfile(configPath, profileName, cfg); err != nil {
					return fmt.Errorf("writing config: %w", err)
				}
			}
			
			fmt.Printf("✅ Activated profile: %s\n", profileName)
			fmt.Printf("   Configuration written to: %s\n", configPath)
			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Println("  1. Review and edit marshal.toml to add your API keys")
			fmt.Println("  2. Run 'marshal doctor' to verify setup")
			fmt.Println("  3. Run 'marshal chat' to start coding")
			
			return nil
		},
	}
}

// profileShowCmd shows the current profile.
func profileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current active profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Options{})
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			
			// Try to detect active profile from config
			profile := detectActiveProfile(cfg)
			
			fmt.Printf("Active profile: %s\n", profile)
			fmt.Println()
			
			fmt.Println("Model bindings:")
			fmt.Printf("  Executor:  %s/%s\n", cfg.Model.Executor.Provider, cfg.Model.Executor.Model)
			fmt.Printf("  Critic:    %s/%s\n", cfg.Model.Critic.Provider, cfg.Model.Critic.Model)
			fmt.Printf("  Marshal:   %s/%s\n", cfg.Model.Marshal.Provider, cfg.Model.Marshal.Model)
			fmt.Printf("  Compactor: %s/%s\n", cfg.Model.Compactor.Provider, cfg.Model.Compactor.Model)
			
			return nil
		},
	}
}

// getRecommendedProfileFromResult returns the recommended profile based on detection.
func getRecommendedProfileFromResult(result detect.ProbeResult) string {
	hasAnthropic := false
	hasLocal := false
	for _, p := range result.Providers {
		if p.Name == "anthropic" {
			hasAnthropic = true
		}
		if !p.IsCloud() {
			hasLocal = true
		}
	}

	switch {
	case hasAnthropic && hasLocal:
		return "balanced"
	case hasAnthropic:
		return "quality"
	case hasLocal:
		return "local-only"
	default:
		return ""
	}
}

// printProviderSetupHelp prints help for setting up providers.
func printProviderSetupHelp() {
	fmt.Println("To get started, you need at least one model provider:")
	fmt.Println()
	fmt.Println("Cloud providers (require API key):")
	fmt.Println("  • Anthropic: Set ANTHROPIC_API_KEY for Claude models")
	fmt.Println("  • OpenAI:    Set OPENAI_API_KEY for GPT models")
	fmt.Println("  • Others:    See documentation for OpenRouter, Fireworks, etc.")
	fmt.Println()
	fmt.Println("Local providers (free, runs on your machine):")
	fmt.Println("  • Ollama:    Install from ollama.com, run 'ollama serve'")
	fmt.Println("  • LM Studio: Download from lmstudio.ai, start local server")
	fmt.Println("  • vLLM:      For advanced users with GPU infrastructure")
	fmt.Println()
}

// createProfileConfig creates a config for a named profile.
func createProfileConfig(profile string) *config.Config {
	cfg := &config.Config{}
	
	switch profile {
	case "local-only":
		cfg.Model.Executor = config.ModelConfig{
			Provider: "ollama",
			Model:    "qwen2.5-coder:14b",
			BaseURL:  "http://localhost:11434/v1",
		}
		cfg.Model.Critic = cfg.Model.Executor
		cfg.Model.Marshal = cfg.Model.Executor
		cfg.Model.Compactor = config.ModelConfig{
			Provider: "ollama",
			Model:    "qwen2.5:7b",
			BaseURL:  "http://localhost:11434/v1",
		}
		
	case "balanced":
		cfg.Model.Executor = config.ModelConfig{
			Provider: "ollama",
			Model:    "qwen2.5-coder:14b",
			BaseURL:  "http://localhost:11434/v1",
		}
		cfg.Model.Marshal = config.ModelConfig{
			Provider: "openai-compat",
			Model:    "claude-sonnet-4-7",
			BaseURL:  "https://api.anthropic.com/v1",
		}
		cfg.Model.Critic = cfg.Model.Marshal
		cfg.Model.Compactor = config.ModelConfig{
			Provider: "ollama",
			Model:    "qwen2.5:7b",
			BaseURL:  "http://localhost:11434/v1",
		}
		
	case "quality":
		cfg.Model.Marshal = config.ModelConfig{
			Provider: "openai-compat",
			Model:    "claude-opus-4-7",
			BaseURL:  "https://api.anthropic.com/v1",
		}
		cfg.Model.Executor = config.ModelConfig{
			Provider: "openai-compat",
			Model:    "claude-sonnet-4-7",
			BaseURL:  "https://api.anthropic.com/v1",
		}
		cfg.Model.Critic = cfg.Model.Marshal
		cfg.Model.Compactor = config.ModelConfig{
			Provider: "openai-compat",
			Model:    "claude-haiku-4-5",
			BaseURL:  "https://api.anthropic.com/v1",
		}
		
	case "budget":
		cfg.Model.Executor = config.ModelConfig{
			Provider: "ollama",
			Model:    "qwen2.5-coder:14b",
			BaseURL:  "http://localhost:11434/v1",
		}
		cfg.Model.Marshal = config.ModelConfig{
			Provider: "openai-compat",
			Model:    "gpt-4o-mini",
			BaseURL:  "https://api.openai.com/v1",
		}
		cfg.Model.Critic = config.ModelConfig{
			Provider: "openai-compat",
			Model:    "gpt-4o-mini",
			BaseURL:  "https://api.openai.com/v1",
		}
		cfg.Model.Compactor = config.ModelConfig{
			Provider: "ollama",
			Model:    "qwen2.5:7b",
			BaseURL:  "http://localhost:11434/v1",
		}
	}
	
	return cfg
}

// detectActiveProfile tries to detect which profile is active based on config.
func detectActiveProfile(cfg *config.Config) string {
	// Check if it's using local-only pattern
	if cfg.Model.Executor.BaseURL == "http://localhost:11434/v1" &&
		cfg.Model.Marshal.BaseURL == "" {
		return "local-only"
	}
	
	// Check for quality pattern (all Anthropic)
	if strings.Contains(cfg.Model.Executor.BaseURL, "anthropic") &&
		strings.Contains(cfg.Model.Marshal.BaseURL, "anthropic") {
		if cfg.Model.Marshal.Model == "claude-opus-4-7" {
			return "quality"
		}
	}
	
	// Check for balanced pattern (mix of cloud and local)
	if (strings.Contains(cfg.Model.Marshal.BaseURL, "anthropic") ||
		strings.Contains(cfg.Model.Marshal.BaseURL, "openai")) &&
		strings.Contains(cfg.Model.Executor.BaseURL, "localhost") {
		return "balanced"
	}
	
	// Check for budget pattern (mostly local/GPT-mini)
	if strings.Contains(cfg.Model.Marshal.Model, "mini") ||
		strings.Contains(cfg.Model.Marshal.Model, "haiku") {
		return "budget"
	}
	
	return "custom"
}

// writeFullProfile writes a complete profile configuration file.
func writeFullProfile(path, profileName string, cfg *config.Config) error {
	content := fmt.Sprintf(`# Marshal Configuration
# Profile: %s
# Generated by 'marshal profile use %s'

[model.executor]
provider = "%s"
model = "%s"
base_url = "%s"
api_key = "${%s_API_KEY}"

[model.critic]
provider = "%s"
model = "%s"
base_url = "%s"
api_key = "${%s_API_KEY}"

[model.marshal]
provider = "%s"
model = "%s"
base_url = "%s"
api_key = "${%s_API_KEY}"

[model.compactor]
provider = "%s"
model = "%s"
base_url = "%s"
api_key = "${%s_API_KEY}"

[loop]
max_rounds = 3
`,
		profileName, profileName,
		cfg.Model.Executor.Provider, cfg.Model.Executor.Model, cfg.Model.Executor.BaseURL,
		strings.ToUpper(cfg.Model.Executor.Provider),
		cfg.Model.Critic.Provider, cfg.Model.Critic.Model, cfg.Model.Critic.BaseURL,
		strings.ToUpper(cfg.Model.Critic.Provider),
		cfg.Model.Marshal.Provider, cfg.Model.Marshal.Model, cfg.Model.Marshal.BaseURL,
		strings.ToUpper(cfg.Model.Marshal.Provider),
		cfg.Model.Compactor.Provider, cfg.Model.Compactor.Model, cfg.Model.Compactor.BaseURL,
		strings.ToUpper(cfg.Model.Compactor.Provider),
	)
	
	return os.WriteFile(path, []byte(content), 0644)
}
