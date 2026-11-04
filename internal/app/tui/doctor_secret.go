package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/textfield"
)

// doctorSecret is the isolated, masked field that collects an API key for one
// /doctor repair. It owns its own text input, so opening, typing in, saving or
// abandoning a repair never shares text, cursor, placeholder or paste
// attachments with the chat composer: the draft underneath survives a repair
// untouched, and a key typed for a provider is never submitted as a prompt.
// It exists for exactly one repair and is cleared on success or cancel.
type doctorSecret struct {
	provider string
	input    textfield.Model
}

// newDoctorSecret builds the masked field for provider. The character limit is
// unlimited so a long key is never silently truncated, and the field is masked
// from the first byte (textinput.EchoPassword, the same primitive the sudo
// prompt and the provider connect flow use).
func newDoctorSecret(provider string, width int) *doctorSecret {
	ti := textfield.New()
	ti.EchoMode = textinput.EchoPassword
	ti.CharLimit = 0
	ti.SetVirtualCursor(true)
	ti.Focus()
	ti.Placeholder = "Enter API key for " + provider
	ti.SetWidth(max(width, 1))
	return &doctorSecret{provider: provider, input: ti}
}

// doctorSecretLines is the console body of the masked field: the field's own
// row plus the key hint. inputSections and inputChromeRows both read it, so the
// painted console and the row budget cannot disagree.
func (m Model) doctorSecretLines() []string {
	if m.doctorSecret == nil {
		return nil
	}
	lines := append([]string{}, consoleLines(m.doctorSecret.input.View())...)
	return append(lines, mutedStyle().Render("Enter saves · Esc cancels"))
}

// doctorActionable reports whether the masked key field is on screen and can
// accept a keystroke: the terminal is at least the supported minimum and no
// docked panel owns the frame. Mirroring sudoActionable/promptFits keeps a
// field the user cannot see from reading — or saving — a key byte.
func (m Model) doctorActionable() bool {
	if m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight {
		return false
	}
	return !m.dock.IsOpen()
}

// handleDoctorSecret routes one keypress or paste to the masked field. It runs
// before the dock, the prompts, the composer and the large-paste branch, so
// ordinary typing, "?", completion, history recall and the paste attachment
// chip never see a byte of the key.
func (m Model) handleDoctorSecret(msg tea.Msg) (tea.Model, tea.Cmd) {
	ds := m.doctorSecret
	if ds == nil {
		return m, nil
	}
	if !m.doctorActionable() {
		// A field the user cannot see must never read or save a key: below the
		// supported minimum, or behind an open panel, only Escape reaches it so
		// cancelling still works. Every other key and every paste is dropped.
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
			m.clearDoctorSecret()
			m.refreshViewport()
		}
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			m.clearDoctorSecret()
			m.refreshViewport()
			return m, nil
		case "enter":
			return m.submitDoctorSecret()
		}
	}
	var cmd tea.Cmd
	ds.input, cmd = ds.input.Update(msg)
	return m, cmd
}

// submitDoctorSecret saves the typed key to the user-global config, reloads the
// runtime and refreshes /doctor. A failed save keeps the masked field open so
// the value can be retried without retyping — an error message never carries
// key material.
func (m Model) submitDoctorSecret() (tea.Model, tea.Cmd) {
	ds := m.doctorSecret
	if ds == nil {
		return m, nil
	}
	provider := ds.provider
	value := strings.TrimSpace(ds.input.Value())
	if value == "" {
		return m, nil
	}
	home, err := m.userHome()
	if err != nil {
		m.note(fmt.Sprintf("✗ Failed to locate home directory: %v", err))
		m.refreshViewport()
		return m, nil
	}
	if err := config.SaveUserConfigProviderAPIKey(config.UserConfigPath(home), provider,
		config.ProviderConfig{APIKey: value}); err != nil {
		// Nothing was written: the field stays up for a retry.
		m.note(fmt.Sprintf("✗ Failed to save API key: %v", err))
		m.refreshViewport()
		return m, nil
	}
	// The key is on disk from here on. A later failure is a reload problem and
	// is reported as such: it must never read as the key having been lost.
	newCfg := m.state.Config
	if newCfg.Providers != nil {
		copied := make(map[string]config.ProviderConfig, len(newCfg.Providers))
		for k, v := range newCfg.Providers {
			copied[k] = v
		}
		newCfg.Providers = copied
	}
	if pc, ok := newCfg.Providers[provider]; ok {
		pc.APIKey = value
		pc.APIKeyEnv = ""
		newCfg.Providers[provider] = pc
	}
	if saveErr, reloadErr := m.persistAndReload(newCfg); saveErr != nil {
		m.note(fmt.Sprintf("✗ Saved key, but failed to persist project config: %v", saveErr))
	} else if reloadErr != nil {
		m.note(fmt.Sprintf("✗ Saved key, but failed to reload runtime: %v", reloadErr))
	} else {
		m.note(fmt.Sprintf("✓ Saved API key for %s", provider))
	}
	// The key is saved, so the field has done its job: clear it and leave the
	// draft exactly as it was. The diagnostics badge is recomputed here so the
	// fixed problem stops counting even on the reload-failure paths.
	//
	// The /doctor panel itself is not re-run or re-opened: it is closed for the
	// whole time a prompt owns the keys (see doctorActionable), so the user is
	// returned to the chat and the system message above is the receipt. Re-run
	// /doctor to see the refreshed diagnosis.
	m.clearDoctorSecret()
	m.refreshDiagnostics()
	m.refreshViewport()
	return m, nil
}

// clearDoctorSecret drops the masked field and gives the console back to the
// chat composer. Nothing about the draft is restored because nothing about it
// was taken: text, cursor, placeholder, focus and paste attachments are exactly
// as the repair found them.
func (m *Model) clearDoctorSecret() {
	m.doctorSecret = nil
	m.updateViewportHeight()
	m.invalidateTranscript()
}
