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
		return nil
	case "sandbox":
		if cfg.Tools.Shell.Sandbox.Backend == "container" && cfg.Tools.Shell.Sandbox.ContainerImage == "" {
			return []string{"container backend set but no image configured — will fall back or error at runtime"}
		}
	}
	return nil
}
