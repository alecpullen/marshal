package tui

import (
	"context"
	"fmt"
	"os"
	"sort"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/presetflow"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
)

// pendingModelOptionsState tracks a model-options config candidate that was
// saved to disk while the runner was busy and still needs to be reloaded and
// applied when the model becomes idle.
type pendingModelOptionsState struct {
	presetName string
	cfg        config.Config
	retry      bool
}

// resolveReasoningSupport reports whether the preset's model is known to
// accept a thinking-effort control, so the model-options panel can hide the
// row when it would be a no-op. Any resolution failure leaves the row
// visible: hiding a working control is worse than showing a dead one.
// Limit discovery is cache-only — a keypress must never trigger a remote
// limits refresh — and the capability probe is bounded by
// presetflow.CapabilityProbeTimeout.
//
// Caveat: the limits table is a unified on-disk cache fed by OpenRouter and
// LiteLLM, keyed by upstream provider/model ids. Lookup accepts variant and
// unambiguous-prefix matches, so a marshal provider name (e.g. "litellm")
// serving a fine-tuned non-reasoning variant of a model may inherit the
// upstream's Reasoning verdict. The panel's "hide only when known" policy
// makes this self-consistent — the row is hidden only when the table reports
// a definite yes/no — but a false "no" is possible. The fail-open direction
// is to show the row, which a variant match cannot override.
func (m *Model) resolveReasoningSupport(presetName string) bool {
	preset, ok := m.state.Config.Models.Presets[presetName]
	if !ok {
		return true
	}
	pc, ok := m.state.Config.Providers[preset.Provider]
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), presetflow.CapabilityProbeTimeout)
	defer cancel()
	prov, err := provider.NewFromConfig(preset.Provider, pc, m.dataDir, false, m.state.Config.Agent.ThinkingBudgetMargin)
	if err != nil {
		return true
	}
	var table *limits.Table
	if cache, err := limits.Load(m.dataDir); err == nil && len(cache.Table) > 0 {
		t := limits.NewTable(cache.Table)
		table = &t
	}
	supported, _ := provider.ResolveReasoningSupport(ctx, prov, preset.Provider, preset.Model, table)
	return supported
}

// openModelOptions opens the model-options panel for the active route's preset.
func (m *Model) openModelOptions() {
	route := m.state.ActiveRoute()
	if !route.Active || route.Preset == "" {
		m.state.AddMessage(session.RoleSystem, "No active model preset. Use /models to pick one first.", session.ContentTypePlain)
		return
	}
	presetName := route.Preset
	if m.pendingModelOptions != nil && m.pendingModelOptions.presetName == presetName {
		m.dock.Open(modeloptions.New(m.pendingModelOptions.cfg, presetName, m.resolveReasoningSupport(presetName)))
		return
	}
	if _, ok := m.state.Config.Models.Presets[presetName]; !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Preset %q is not configured.", presetName), session.ContentTypePlain)
		return
	}
	m.dock.Open(modeloptions.New(m.state.Config, presetName, m.resolveReasoningSupport(presetName)))
}

// openModelOptionsForProvider opens a picker listing the model pairs that use
// the named provider, then opens the model-options editor for the selected
// pair. It is wired to the "Model options" action in the provider drill-in.
func (m *Model) openModelOptionsForProvider(providerName string) {
	var items []picker.Item
	for name, preset := range m.state.Config.Models.Presets {
		if preset.Provider != providerName {
			continue
		}
		items = append(items, picker.Item{Label: preset.Model, Value: name})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	if len(items) == 0 {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("No model pairs configured for %q yet. Use /connect or /models to pick one.", providerName), session.ContentTypePlain)
		return
	}
	p := picker.New("Pick a model pair", "override context/max-output", items)
	m.dock.Open(p)
	m.pickerCommand = "model-options"
}

// handleModelOptionsChanged persists the candidate config and either reloads
// it immediately when idle, or defers the reload until the runner is no longer
// busy. Presets are user-global, so both paths write the user config; the
// project file carries nothing for this change.
func (m *Model) handleModelOptionsChanged(msg modeloptions.ChangedMsg) tea.Cmd {
	if m.pendingModelOptions != nil && m.pendingModelOptions.presetName == msg.PresetName {
		m.pendingModelOptions.retry = false
	}
	home, homeErr := os.UserHomeDir()
	if homeErr != nil || home == "" {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Could not apply %s option: locate home directory: %v", msg.FieldID, homeErr), session.ContentTypePlain)
		return nil
	}
	userPath := config.UserConfigPath(home)
	if !m.busy && m.state.RunningJobsCount() == 0 {
		saveErr, reloadErr := m.savePresetsAndReload(userPath, msg.Config)
		if saveErr != nil || reloadErr != nil {
			m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Could not apply %s option: save=%v reload=%v", msg.FieldID, saveErr, reloadErr), session.ContentTypePlain)
			return nil
		}
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("%s: %s → %s", fieldLabel(msg.FieldID), msg.OldValue, msg.NewValue), session.ContentTypePlain)
		return nil
	}

	if err := config.SaveUserConfigPresets(userPath, msg.Config.Models.Presets); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Saved %s option failed: %v", msg.FieldID, err), session.ContentTypePlain)
		return nil
	}
	m.pendingModelOptions = &pendingModelOptionsState{
		presetName: msg.PresetName,
		cfg:        msg.Config,
	}
	m.state.AddMessage(session.RoleSystem, fmt.Sprintf("%s: %s → %s (saved for next idle turn)", fieldLabel(msg.FieldID), msg.OldValue, msg.NewValue), session.ContentTypePlain)
	return nil
}

// savePresetsAndReload persists the presets of cfg to the user-global config
// and reloads the runtime, mirroring persistAndReload minus the project
// write (SaveProjectConfig never emits presets). It returns the save error
// or, when saving succeeded, the reload error (nil on full success).
func (m *Model) savePresetsAndReload(userPath string, cfg config.Config) (saveErr, reloadErr error) {
	if err := config.SaveUserConfigPresets(userPath, cfg.Models.Presets); err != nil {
		m.applyNewConfig(cfg)
		m.configSavePending = true
		return err, nil
	}
	m.configSavePending = false
	// The user file changed on disk; refresh the load-time snapshots before
	// rebuilding the runtime so the next save diffs against fresh layers.
	m.reloadLayers()
	if m.configReloader != nil {
		// reloadAgentRuntime may install cfg before reporting a cleanup
		// error; invalidate config-derived state before attempting it.
		m.setReg = nil
		if err := m.configReloader(cfg); err != nil {
			m.applyNewConfig(cfg)
			return nil, err
		}
		m.afterRuntimeReload()
	}
	m.applyNewConfig(cfg)
	m.refreshDiagnostics()
	return nil, nil
}

// flushPendingModelOptions applies a pending options candidate if the model is
// now idle. Returns a command that emits the result, or nil if no work is
// needed.
func (m *Model) flushPendingModelOptions() tea.Cmd {
	if m.pendingModelOptions == nil {
		return nil
	}
	if m.busy || m.state.RunningJobsCount() > 0 {
		return nil
	}
	pending := m.pendingModelOptions
	reloadErr := m.configReloader(pending.cfg)
	if reloadErr != nil {
		pending.retry = true
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Could not activate %s options: %v", pending.presetName, reloadErr), session.ContentTypePlain)
		return nil
	}
	m.applyNewConfig(pending.cfg)
	m.pendingModelOptions = nil
	m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Activated %s options", pending.presetName), session.ContentTypePlain)
	return nil
}

func fieldLabel(id string) string {
	switch id {
	case "context_window":
		return "Context window"
	case "max_output_tokens":
		return "Max output tokens"
	case "tool_calling":
		return "Tool calling"
	case "local_only":
		return "Local only"
	}
	return id
}
