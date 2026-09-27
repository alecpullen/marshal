package provider

import (
	"marshal/internal/app/config"
)

// RebindTemplateDefaults re-applies a provider template's capability
// defaults to any configured [providers.<name>] entry that declares the
// template (via its Template field) or whose key matches the template ID.
//
// This exists because [providers] merge by whole-entry overwrite with
// credential-only inheritance: an entry saved before a template learned a
// capability (e.g. opencode-go gained TemperatureLocked after users had
// already /connect-ed) keeps the old zero value permanently, and there is
// no user-facing path to repair it — config.providers.set deliberately
// refuses to write temperature_locked, tool_calling, or structured_output.
// The template is the authoritative statement of what the endpoint
// actually accepts, so at load the template's capability flags win.
//
// Only true values are applied. A template that leaves a capability off
// never forces it off on the user's entry — that would silently break
// endpoints the user has proven work. The merge is one-directional:
// template true  -> entry true (repair stale false),
// template false -> entry unchanged (preserve explicit user true).
func RebindTemplateDefaults(cfg *config.Config) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return
	}
	for name, pc := range cfg.Providers {
		id := pc.Template
		if id == "" {
			id = name
		}
		tpl, ok := Lookup(id)
		if !ok {
			continue
		}
		if tpl.TemperatureLocked {
			pc.TemperatureLocked = true
		}
		if tpl.ToolCalling {
			pc.ToolCalling = true
		}
		if tpl.StructuredOutput {
			pc.StructuredOutput = true
		}
		cfg.Providers[name] = pc
	}
}
