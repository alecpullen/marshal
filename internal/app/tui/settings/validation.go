package settings

import (
	"fmt"

	"marshal/internal/app/config"
)

func warningsFor(sectionID string, cfg config.Config) []string {
	switch sectionID {
	case "agent", "providers":
		var ws []string
		if cfg.Privacy.RemoteProvidersAllowed && len(cfg.Providers) == 0 {
			ws = append(ws, "remote providers allowed but none configured")
		}
		if sectionID == "providers" {
			for name, p := range cfg.Providers {
				if p.APIKey != "" {
					ws = append(ws, fmt.Sprintf("provider %q stores an API key in plaintext — prefer api_key_env", name))
				}
			}
		}
		return ws
	case "shell":
		var ws []string
		if cfg.Tools.Shell.AllowSudo && cfg.Tools.Shell.AutoApprove {
			ws = append(ws, "sudo runs without confirmation (auto-approve on)")
		}
		if cfg.Tools.Shell.AllowDestructive && cfg.Tools.Shell.AutoApprove {
			ws = append(ws, "destructive commands run without confirmation (auto-approve on)")
		}
		return ws
	case "sandbox":
		if cfg.Tools.Shell.Sandbox.Backend == "container" && cfg.Tools.Shell.Sandbox.ContainerImage == "" {
			return []string{"container backend set but no image configured — will fall back or error at runtime"}
		}
	}
	return nil
}
