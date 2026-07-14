package connect

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/provider"
)

var th = theme.Load()

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(th.FGDefault)
	mutedStyle   = lipgloss.NewStyle().Foreground(th.FGMuted)
	hintStyle    = lipgloss.NewStyle().Foreground(th.StatusInfo)
	errStyle     = lipgloss.NewStyle().Foreground(th.StatusError)
	footerStyle  = lipgloss.NewStyle().Foreground(th.FGMuted)
	successStyle = lipgloss.NewStyle().Foreground(th.StatusSuccess)
)

type step int

const (
	stepPickTemplate step = iota
	stepBaseURL
	stepAPIKey
	stepProbing
	stepPickModel
	stepDone
	stepCancelled
)

type Opts struct {
	Cfg              config.Config
	Discovered       map[string][]string
	SkipToIntroModel bool
	ScopedProvider   string
}

type Model struct {
	step           step
	picker         *picker.Model
	input          textinput.Model
	title          string
	subtitle       string
	footer         string
	err            string
	template       provider.ProviderTemplate
	providerName   string
	providerCfg    config.ProviderConfig
	models         []string
	probeErr       error
	cfg            config.Config
	discovered     map[string][]string
	scopedProvider string
	width          int
	height         int
	probeStart     int64
	spinner        int
	pendingSave    bool
	modelChosen    string
}

func New(opts Opts) *Model {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	m := &Model{
		cfg:            opts.Cfg,
		discovered:     opts.Discovered,
		input:          ti,
		scopedProvider: opts.ScopedProvider,
	}
	if opts.SkipToIntroModel {
		m.enterPickModel(opts.ScopedProvider)
		return m
	}
	m.enterPickTemplate()
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case probe.ResultMsg:
		return m.handleProbeResult(msg)
	case picker.PickedMsg:
		return m.handlePickerPicked(msg.Value)
	case picker.CancelledMsg:
		if m.step == stepPickTemplate || m.step == stepPickModel {
			return m, m.cancel()
		}
		m.err = ""
		return m, m.back()
	case TickMsg:
		m.spinner++
		if m.step == stepProbing {
			return m, tick()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) View(maxW, maxH int) string {
	pw := min(64, maxW-8)
	if pw < 40 {
		pw = max(maxW-2, 40)
	}
	ph := min(14, maxH)
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n")
	if m.subtitle != "" {
		b.WriteString(hintStyle.Render(m.subtitle))
		b.WriteString("\n")
	}
	if m.picker != nil {
		b.WriteString(m.picker.View(pw, ph-4))
	} else if m.step == stepProbing {
		b.WriteString(m.renderProbing(pw))
	} else if m.step == stepBaseURL || m.step == stepAPIKey {
		b.WriteString(m.renderInput(pw))
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(errStyle.Render(m.err))
	}
	if m.footer != "" {
		b.WriteString("\n")
		b.WriteString(footerStyle.Render(m.footer))
	}
	return chrome.Panel("connect", b.String(), pw, min(lipgloss.Height(b.String())+2, maxH), true, th)
}

func (m *Model) renderInput(pw int) string {
	inner := pw - 2
	line := m.input.View()
	if len(line) > inner {
		line = line[:inner]
	}
	return line
}

func (m *Model) renderProbing(pw int) string {
	frame := "…"
	if m.probeStart > 0 {
		frame = spinnerFrames[m.spinner%len(spinnerFrames)]
	}
	return mutedStyle.Render(frame + " connecting…")
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type DoneMsg struct {
	Provider string
	Model    string
}

type CancelledMsg struct{}

type TickMsg struct{}

func tick() tea.Cmd {
	return func() tea.Msg {
		return TickMsg{}
	}
}

func (m *Model) cancel() tea.Cmd {
	m.step = stepCancelled
	return func() tea.Msg { return CancelledMsg{} }
}

func (m *Model) back() tea.Cmd {
	switch m.step {
	case stepAPIKey:
		m.enterPickTemplate()
	case stepBaseURL:
		m.enterPickTemplate()
	case stepPickModel:
		if m.providerName != "" && m.template.ID != "" {
			m.step = stepAPIKey
			m.enterAPIKey()
		} else {
			m.enterPickTemplate()
		}
	default:
		return m.cancel()
	}
	return nil
}

func (m *Model) enterPickTemplate() {
	m.step = stepPickTemplate
	m.title = "Connect a provider"
	m.subtitle = ""
	m.footer = "[↑↓] move [↵] pick [Esc] cancel"
	m.err = ""
	all := provider.All()
	items := make([]picker.Item, 0, len(all))
	for _, tpl := range all {
		items = append(items, picker.Item{
			Label:  tpl.Label,
			Detail: tpl.BaseURL,
			Badge:  badgeForTemplate(tpl),
			Value:  tpl.ID,
		})
	}
	p := picker.New("Add provider", "pick a template", items)
	p.SetAllowCustom(true)
	m.picker = p
}

func (m *Model) enterAPIKey() {
	m.step = stepAPIKey
	m.title = "API key"
	m.subtitle = m.template.KeyHint
	m.footer = "[↵] save  [Esc] back"
	m.err = ""
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	if m.template.KeyEnv != "" {
		ti.Placeholder = "paste key, or leave blank to use $" + m.template.KeyEnv
	} else {
		ti.Placeholder = "paste key"
	}
	m.input = ti
	m.picker = nil
}

func enterBaseURLStep(m *Model) {
	m.step = stepBaseURL
	m.title = "Base URL"
	m.subtitle = "OpenAI-compatible endpoint, e.g. https://host/v1"
	m.footer = "[↵] next  [Esc] back"
	m.err = ""
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	ti.Placeholder = "https://…/v1"
	m.input = ti
	m.picker = nil
}

func enterPickModelStep(m *Model, providerName string) {
	m.step = stepPickModel
	m.title = "Select model"
	m.subtitle = providerName
	m.footer = "[↑↓] move [↵] pick [Esc] done"
	m.err = ""
	m.picker = buildModelPicker(m, providerName)
}

func buildModelPicker(m *Model, providerName string) *picker.Model {
	var items []picker.Item
	if cached, ok := m.discovered[providerName]; ok && len(cached) > 0 {
		for _, mid := range cached {
			items = append(items, picker.Item{Label: mid, Detail: providerName, Badge: "◉ discovered", Group: providerName, Value: mid})
		}
	} else if tpl, ok := provider.Lookup(strings.TrimSuffix(strings.TrimPrefix(providerName, m.template.ID), "-2")); ok && len(tpl.Models) > 0 {
		for _, mid := range tpl.Models {
			items = append(items, picker.Item{Label: mid, Detail: providerName, Badge: "◯ catalog", Group: providerName, Value: mid})
		}
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Label: "Enter model id manually", Value: "__manual__", Badge: "custom"})
	}
	p := picker.New("Select model", "pick a model", items)
	p.SetAllowCustom(true)
	return p
}

func badgeForTemplate(tpl provider.ProviderTemplate) string {
	if tpl.Local {
		return "local"
	}
	return "remote"
}

func (m *Model) handlePickerPicked(value string) (*Model, tea.Cmd) {
	m.modelChosen = value
	m.step = stepDone
	return m, func() tea.Msg { return CancelledMsg{} }
}

func (m *Model) handleProbeResult(msg probe.ResultMsg) (*Model, tea.Cmd) { return m, nil }

func (m *Model) handleKey(k tea.KeyPressMsg) (*Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		return m, m.cancel()
	}
	return m, nil
}

func (m *Model) enterPickModel(providerName string) {
	enterPickModelStep(m, providerName)
}
