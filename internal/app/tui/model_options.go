package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/app/tui/picker"
)

// pendingModelOptionsState tracks a model-options config candidate that was
// saved to disk while the runner was busy and still needs to be reloaded and
// applied when the model becomes idle.
type pendingModelOptionsState struct {
	presetName string
	cfg        config.Config
	retry      bool
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
		m.dock.Open(modeloptions.New(m.pendingModelOptions.cfg, presetName))
		return
	}
	if _, ok := m.state.Config.Models.Presets[presetName]; !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Preset %q is not configured.", presetName), session.ContentTypePlain)
		return
	}
	m.dock.Open(modeloptions.New(m.state.Config, presetName))
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
// busy.
func (m *Model) handleModelOptionsChanged(msg modeloptions.ChangedMsg) tea.Cmd {
	if m.pendingModelOptions != nil && m.pendingModelOptions.presetName == msg.PresetName {
		m.pendingModelOptions.retry = false
	}
	if !m.busy && m.state.RunningJobsCount() == 0 {
		saveErr, reloadErr := m.persistAndReload(msg.Config)
		if saveErr != nil || reloadErr != nil {
			m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Could not apply %s option: save=%v reload=%v", msg.FieldID, saveErr, reloadErr), session.ContentTypePlain)
			return nil
		}
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("%s: %s → %s", fieldLabel(msg.FieldID), msg.OldValue, msg.NewValue), session.ContentTypePlain)
		return nil
	}

	if err := config.SaveProjectConfig(config.ProjectConfigPath(m.workDir), msg.Config); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Saved %s option failed: %v", msg.FieldID, err), session.ContentTypePlain)
		return nil
	}
	if m.trustRefresh != nil {
		m.trustRefresh(m.workDir)
	}
	m.pendingModelOptions = &pendingModelOptionsState{
		presetName: msg.PresetName,
		cfg:        msg.Config,
	}
	m.state.AddMessage(session.RoleSystem, fmt.Sprintf("%s: %s → %s (saved for next idle turn)", fieldLabel(msg.FieldID), msg.OldValue, msg.NewValue), session.ContentTypePlain)
	return nil
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
