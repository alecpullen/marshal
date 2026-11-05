package tui

import (
	"fmt"
	"sort"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// pendingModelOptionsState tracks a model-options config candidate that was
// saved to disk while the runner was busy and still needs to be reloaded and
// applied when the model becomes idle.
type pendingModelOptionsState struct {
	presetName string
	cfg        config.Config
	retry      bool
}

// resolveReasoningSupport controls visibility from resolved provider metadata.
// Unknown models retain a provider-default row; saved values remain editable.
func (m *Model) resolveReasoningSupport(presetName string) bool {
	options := m.modelThinkingOptions(presetName)
	return options == nil || len(options.Levels) > 0 || m.sess.Config().Models.Presets[presetName].Thinking != ""
}

// openModelOptions opens the model-options panel for the active route's preset.
func (m *Model) openModelOptions() tea.Cmd {
	route := m.sess.Status().ActiveRoute
	if !route.Active || route.Preset == "" {
		m.note("No active model preset. Use /models to pick one first.")
		return nil
	}
	presetName := route.Preset
	if m.pendingModelOptions != nil && m.pendingModelOptions.presetName == presetName {
		m.dock.Open(modeloptions.New(m.pendingModelOptions.cfg, presetName, m.resolveReasoningSupport(presetName), m.modelThinkingOptions(presetName)))
		return m.probeThinkingOptions(presetName)
	}
	if _, ok := m.sess.Config().Models.Presets[presetName]; !ok {
		m.note(fmt.Sprintf("Preset %q is not configured.", presetName))
		return nil
	}
	m.dock.Open(modeloptions.New(m.sess.Config(), presetName, m.resolveReasoningSupport(presetName), m.modelThinkingOptions(presetName)))
	return m.probeThinkingOptions(presetName)
}

// openModelOptionsForProvider opens a picker listing the model pairs that use
// the named provider, then opens the model-options editor for the selected
// pair. It is wired to the "Model options" action in the provider drill-in.
func (m *Model) openModelOptionsForProvider(providerName string) {
	var items []picker.Item
	for name, preset := range m.sess.Config().Models.Presets {
		if preset.Provider != providerName {
			continue
		}
		items = append(items, picker.Item{Label: preset.Model, Value: name})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	if len(items) == 0 {
		m.note(fmt.Sprintf("No model pairs configured for %q yet. Use /connect or /models to pick one.", providerName))
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
	// One read of the session config: the panel's candidate is msg.Config, so
	// this snapshot is the baseline both save paths diff against.
	cfg := m.sess.Config()
	if m.pendingModelOptions != nil && m.pendingModelOptions.presetName == msg.PresetName {
		m.pendingModelOptions.retry = false
	}
	if _, homeErr := m.userHome(); homeErr != nil {
		m.note(fmt.Sprintf("Could not apply %s option: %v", msg.FieldID, homeErr))
		return nil
	}
	// Baseline: the session's config, read once above, which seeded the
	// panel. The panel emitted a mutated clone, so that snapshot carries the
	// pre-edit membership — presets another marshal process persisted after
	// this process loaded survive the merge.
	//
	// A running background command does not defer the reload: command
	// ownership survives a reload, so a job admitted before it keeps its
	// captured runner, cwd, output policy and wait, and only future launches
	// resolve the new defaults.
	if !m.busy {
		saveErr, reloadErr := m.savePresetsAndReload(msg.Config, cfg.Models.Presets)
		if saveErr != nil || reloadErr != nil {
			m.note(fmt.Sprintf("Could not apply %s option: save=%v reload=%v", msg.FieldID, saveErr, reloadErr))
			return nil
		}
		m.note(fmt.Sprintf("%s: %s → %s", fieldLabel(msg.FieldID), msg.OldValue, msg.NewValue))
		return nil
	}

	if err := m.host.SaveUserPresets(m.ctx, msg.Config.Models.Presets, cfg.Models.Presets); err != nil {
		m.note(fmt.Sprintf("Saved %s option failed: %v", msg.FieldID, err))
		return nil
	}
	m.pendingModelOptions = &pendingModelOptionsState{
		presetName: msg.PresetName,
		cfg:        msg.Config,
	}
	m.note(fmt.Sprintf("%s: %s → %s (saved for next idle turn)", fieldLabel(msg.FieldID), msg.OldValue, msg.NewValue))
	return nil
}

// savePresetsAndReload persists the presets of cfg to the user-global config
// and reloads the runtime, mirroring persistAndReload minus the project
// write (SaveProjectConfig never emits presets). baseline is this process's
// load-time preset map — the merge keeps presets other marshal processes
// wrote after it was captured. It returns the save error or, when saving
// succeeded, the reload error (nil on full success).
func (m *Model) savePresetsAndReload(cfg config.Config, baseline map[string]routing.ModelPreset) (saveErr, reloadErr error) {
	if err := m.host.SaveUserPresets(m.ctx, cfg.Models.Presets, baseline); err != nil {
		m.applyNewConfig(cfg)
		m.configSavePending = true
		return err, nil
	}
	m.configSavePending = false
	// reloadAgentRuntime may install cfg before reporting a cleanup
	// error; invalidate config-derived state before attempting it.
	m.setReg = nil
	reloaded, err := m.sess.ReloadConfig(m.ctx, cfg)
	if err != nil {
		m.applyNewConfig(cfg)
		return nil, err
	}
	if reloaded {
		m.afterRuntimeReload()
	}
	m.applyNewConfig(cfg)
	m.refreshDiagnostics()
	return nil, nil
}

// flushPendingModelOptions applies a pending options candidate if the model is
// now idle. Returns a command that emits the result, or nil if no work is
// needed.
//
// Only an active TURN defers the reload. Background commands are not a reason
// to wait: the conversation's command service survives a reload, so applying
// the candidate changes future launches without disturbing a command that is
// already running.
func (m *Model) flushPendingModelOptions() tea.Cmd {
	if m.pendingModelOptions == nil {
		return nil
	}
	if m.busy {
		return nil
	}
	pending := m.pendingModelOptions
	// A session with no reload path reports (false, nil): the change is
	// activated all the same, which is what the pending entry was for.
	_, reloadErr := m.sess.ReloadConfig(m.ctx, pending.cfg)
	if reloadErr != nil {
		pending.retry = true
		m.note(fmt.Sprintf("Could not activate %s options: %v", pending.presetName, reloadErr))
		return nil
	}
	m.applyNewConfig(pending.cfg)
	m.pendingModelOptions = nil
	m.note(fmt.Sprintf("Activated %s options", pending.presetName))
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

func (m *Model) modelThinkingOptions(presetName string) *schema.ThinkingOptions {
	cfg := m.sess.Config()
	preset := cfg.Models.Presets[presetName]
	pc := cfg.Providers[preset.Provider]
	return schema.ResolveThinkingOptions(pc.Type, preset.Model, preset.ThinkingOptions, m.discovered[preset.Provider])
}

func (m *Model) probeThinkingOptions(presetName string) tea.Cmd {
	cfg := m.sess.Config()
	preset := cfg.Models.Presets[presetName]
	pc, ok := cfg.Providers[preset.Provider]
	if !ok || (!probe.IsLocalhost(pc.BaseURL) && !cfg.Privacy.RemoteProvidersAllowed) {
		return nil
	}
	return probe.Provider(m.host, "thinking-options", preset.Provider, pc)
}
