package connect

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/provider"
	"marshal/internal/strutil"
)

func titleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(theme.Current().FGDefault)
}
func mutedStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }
func hintStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(theme.Current().StatusInfo) }
func errStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(theme.Current().StatusError) }
func footerStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }

type step int

const (
	stepPickTemplate step = iota
	stepBaseURL
	stepAPIKey
	stepProbing
	stepPickModel
	stepSummary
	stepRename
	stepDone
	stepCancelled
	stepRemoteGate
)

type Opts struct {
	Cfg              config.Config
	Discovered       map[string][]string
	SkipToIntroModel bool
	ScopedProvider   string
	CfgPath          string
}

type Model struct {
	step           step
	picker         *picker.Model
	input          textfield.Model
	renameInput    textfield.Model
	title          string
	subtitle       string
	footer         string
	err            string
	template       provider.ProviderTemplate
	providerName   string
	providerCfg    config.ProviderConfig
	models         []string
	cfg            config.Config
	discovered     map[string][]string
	scopedProvider string
	cfgPath        string
	width          int
	height         int
	probeStart     int64
	spinner        int
	modelChosen    string
	remoteEnabled  bool
}

func New(opts Opts) *Model {
	ti := textfield.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	ri := textfield.New()
	ri.SetVirtualCursor(true)
	ri.Focus()
	m := &Model{
		cfg:            opts.Cfg,
		discovered:     opts.Discovered,
		input:          ti,
		renameInput:    ri,
		scopedProvider: opts.ScopedProvider,
		cfgPath:        opts.CfgPath,
	}
	if opts.SkipToIntroModel {
		enterPickModelStep(m, opts.ScopedProvider)
		return m
	}
	m.enterPickTemplate()
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// InputValue returns the active text input's current text.
func (m *Model) InputValue() string { return m.input.Value() }

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
	case tea.PasteMsg:
		switch m.step {
		case stepBaseURL, stepAPIKey:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		case stepRename:
			var cmd tea.Cmd
			m.renameInput, cmd = m.renameInput.Update(msg)
			return m, cmd
		case stepPickTemplate, stepPickModel:
			if m.picker == nil {
				return m, nil
			}
			cmd := m.picker.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

// Panel adapts Model to the dock.Panel interface: Model.Update returns
// (*Model, tea.Cmd) for historical reasons but mutates in place.
type Panel struct{ *Model }

func (p Panel) Update(msg tea.Msg) tea.Cmd {
	_, cmd := p.Model.Update(msg)
	return cmd
}

func (m *Model) View(maxW, maxH int) string {
	// The picker already renders its own gutter-framed panel with a
	// title, hints, and footer; wrapping it in a second chrome.Panel here
	// would stack two gutters and duplicate the footer text.
	if m.picker != nil {
		return m.picker.View(maxW, maxH)
	}
	pw := layout.PanelWidth(maxW)
	var b strings.Builder
	b.WriteString(titleStyle().Render(m.title))
	b.WriteString("\n")
	if m.subtitle != "" {
		b.WriteString(hintStyle().Render(m.subtitle))
		b.WriteString("\n")
	}
	switch m.step {
	case stepProbing:
		b.WriteString(m.renderProbing(pw))
	case stepBaseURL, stepAPIKey:
		b.WriteString(m.renderInput(pw))
	case stepSummary:
		b.WriteString(m.renderSummary(pw))
	case stepRename:
		b.WriteString(m.renderRenameInput(pw))
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(errStyle().Render(m.err))
	}
	if m.footer != "" {
		b.WriteString("\n")
		b.WriteString(footerStyle().Render(m.footer))
	}
	return chrome.Panel("connect", b.String(), pw, min(lipgloss.Height(b.String())+2, maxH), true, theme.Current())
}

func (m *Model) renderInput(pw int) string {
	inner := pw - 2
	line := m.input.View()
	if len(line) > inner {
		line = line[:inner]
	}
	return line
}

func (m *Model) renderRenameInput(pw int) string {
	inner := pw - 2
	line := m.renameInput.View()
	if len(line) > inner {
		line = line[:inner]
	}
	return line
}

func (m *Model) renderProbing(pw int) string {
	if m.probeStart > 0 && time.Now().Sub(time.Unix(0, m.probeStart)) > 200*time.Millisecond {
		frame := spinnerFrames[m.spinner%len(spinnerFrames)]
		return mutedStyle().Render(frame + " connecting…")
	}
	return mutedStyle().Render("… connecting")
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type DoneMsg struct {
	Provider      string
	Model         string
	ProviderCfg   config.ProviderConfig
	EnabledRemote bool
}

type CancelledMsg struct{}

type TickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return TickMsg{} })
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
	case stepSummary:
		m.enterPickTemplate()
	case stepRename:
		m.enterSummary()
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
	p := picker.New(m.title, "pick a template", items)
	p.SetAllowCustom(true)
	m.picker = p
}

func (m *Model) enterAPIKey() {
	m.step = stepAPIKey
	m.title = "API key"
	m.subtitle = m.template.KeyHint
	m.footer = "[↵] save  [Esc] back"
	m.err = ""
	ti := textfield.New()
	ti.EchoMode = textinput.EchoPassword
	ti.SetVirtualCursor(true)
	ti.Focus()
	if m.template.KeyEnv != "" {
		ti.Placeholder = "paste key · $ENV_NAME · blank uses $" + m.template.KeyEnv
	} else {
		ti.Placeholder = "paste key, or $ENV_NAME to read from an env var"
	}
	m.input = ti
	m.picker = nil
}

func (m *Model) remoteBlocked(baseURL string) bool {
	return !probe.IsLocalhost(baseURL) &&
		!m.cfg.Privacy.RemoteProvidersAllowed &&
		!m.remoteEnabled
}

func (m *Model) enterRemoteGate() {
	m.step = stepRemoteGate
	m.title = "Remote providers are disabled"
	m.subtitle = "this endpoint is not on localhost (privacy.remote_providers)"
	m.footer = "[y] enable remote providers and continue  [Esc] back"
	m.err = ""
	m.picker = nil
}

func (m *Model) enterSummary() {
	m.step = stepSummary
	m.title = "Review provider"
	m.subtitle = ""
	m.footer = "[↵] confirm  [n] rename  [Esc] back"
	m.err = ""
	m.picker = nil
}

func (m *Model) enterRename() {
	m.step = stepRename
	m.title = "Rename provider"
	m.subtitle = "enter a new name for this provider"
	m.footer = "[↵] save  [Esc] back"
	m.err = ""
	ri := textfield.New()
	ri.SetVirtualCursor(true)
	ri.Focus()
	ri.SetValue(m.providerName)
	m.renameInput = ri
	m.picker = nil
}

func (m *Model) keySourceLabel() string {
	if m.providerCfg.APIKey != "" {
		return "key stored in config"
	}
	if m.providerCfg.APIKeyEnv != "" {
		return "$" + m.providerCfg.APIKeyEnv + " (env)"
	}
	return "none"
}

func (m *Model) renderSummary(pw int) string {
	var b strings.Builder
	b.WriteString(mutedStyle().Render("provider: ") + titleStyle().Render(m.providerName) + "\n")
	b.WriteString(mutedStyle().Render("endpoint: ") + titleStyle().Render(strutil.Truncate(m.providerCfg.BaseURL, pw-12, true)) + "\n")
	b.WriteString(mutedStyle().Render("key:      ") + titleStyle().Render(m.keySourceLabel()) + "\n")
	b.WriteString(mutedStyle().Render("model:    ") + titleStyle().Render(m.modelChosen) + "\n")
	if m.cfgPath != "" {
		b.WriteString(mutedStyle().Render("save to:  ") + titleStyle().Render(strutil.Truncate(m.cfgPath, pw-12, true)) + "\n")
	}
	return b.String()
}

func enterBaseURLStep(m *Model) {
	m.step = stepBaseURL
	m.title = "Base URL"
	m.subtitle = "OpenAI-compatible endpoint, e.g. https://host/v1"
	m.footer = "[↵] next  [Esc] back"
	m.err = ""
	ti := textfield.New()
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
	m.providerName = providerName
	if pc, ok := m.cfg.Providers[providerName]; ok {
		m.providerCfg = pc
	}
	m.picker = buildModelPicker(m, providerName)
}

func buildModelPicker(m *Model, providerName string) *picker.Model {
	var items []picker.Item
	candidates := m.models
	if len(candidates) == 0 {
		if cached, ok := m.discovered[providerName]; ok {
			candidates = cached
		}
	}
	if len(candidates) == 0 {
		candidates = m.template.Models
	}
	for _, mid := range candidates {
		badge := "◉ discovered"
		if len(m.models) == 0 {
			badge = "◯ catalog"
		}
		items = append(items, picker.Item{Label: mid, Detail: providerName, Badge: badge, Group: providerName, Value: mid})
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Label: "Enter model id manually", Value: "__manual__", Badge: "custom"})
	}
	p := picker.New(m.title, "pick a model for "+providerName, items)
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
	if m.step == stepPickModel {
		if value == "__manual__" {
			return m, nil
		}
		m.modelChosen = value
		// Scoped /models flow skips summary; new-provider flow shows summary.
		if m.scopedProvider != "" {
			m.step = stepDone
			return m, m.done()
		}
		m.enterSummary()
		return m, nil
	}
	tpl, ok := provider.Lookup(value)
	if !ok {
		if value == "custom" || strings.HasPrefix(value, "@ai-sdk/") {
			m.template = provider.ProviderTemplate{ID: "custom", Type: "openai_compatible"}
			enterBaseURLStep(m)
			return m, nil
		}
		m.template = provider.ProviderTemplate{ID: value, Type: "openai_compatible"}
		enterBaseURLStep(m)
		return m, nil
	}
	m.template = tpl
	m.providerCfg = config.ProviderConfig{Type: tpl.Type, BaseURL: tpl.BaseURL, APIKeyEnv: tpl.KeyEnv, ToolCalling: tpl.ToolCalling}
	if tpl.Local {
		return m.enterProbing()
	}
	if m.remoteBlocked(tpl.BaseURL) {
		m.enterRemoteGate()
		return m, nil
	}
	m.enterAPIKey()
	return m, nil
}

func (m *Model) handleProbeResult(msg probe.ResultMsg) (*Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = "✗ " + strutil.Truncate(msg.Err.Error(), 72, true)
		if hint := probeHint(msg.Err); hint != "" {
			m.err += "\n  ↳ " + hint
		}
		m.footer = "[r] retry  [s] skip  [Esc] cancel"
		return m, nil
	}
	m.models = msg.Models
	if m.discovered != nil {
		m.discovered[m.providerName] = msg.Models
	}
	_, advCmd := m.advanceToPickModel()
	return m, advCmd
}

// probeHint maps common connection failures to a one-line remediation.
func probeHint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"):
		return "is the server running at this URL?"
	case strings.Contains(msg, "no such host"):
		return "hostname not found — check the base URL"
	case strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized"):
		return "key rejected — check the API key or env var"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timed out — server unreachable or slow"
	case strings.Contains(msg, "certificate"):
		return "TLS problem — check https vs http in the base URL"
	}
	return ""
}

func (m *Model) handleKey(k tea.KeyPressMsg) (*Model, tea.Cmd) {
	ks := k.String()
	switch m.step {
	case stepBaseURL, stepAPIKey:
		switch ks {
		case "esc":
			return m, m.back()
		case "enter":
			return m.confirmInput()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
	case stepPickTemplate, stepPickModel:
		if ks == "esc" {
			return m, m.cancel()
		}
		if m.picker == nil {
			return m, nil
		}
		cmd := m.picker.Update(k)
		return m, cmd
	case stepProbing:
		if ks == "esc" {
			return m, m.cancel()
		}
		if ks == "r" {
			return m.enterProbing()
		}
		if ks == "s" {
			return m.skipProbe()
		}
		return m, nil
	case stepRemoteGate:
		switch ks {
		case "y":
			m.remoteEnabled = true
			m.enterAPIKey()
			return m, nil
		case "esc":
			m.enterPickTemplate()
			return m, nil
		}
		return m, nil
	case stepSummary:
		switch ks {
		case "enter":
			m.step = stepDone
			return m, m.done()
		case "n":
			m.enterRename()
			return m, nil
		case "esc":
			enterPickModelStep(m, m.providerName)
			return m, nil
		}
		return m, nil
	case stepRename:
		switch ks {
		case "enter":
			return m.confirmRename()
		case "esc":
			m.enterSummary()
			return m, nil
		default:
			var cmd tea.Cmd
			m.renameInput, cmd = m.renameInput.Update(k)
			return m, cmd
		}
	}
	if ks == "esc" {
		return m, m.cancel()
	}
	return m, nil
}

func (m *Model) confirmInput() (*Model, tea.Cmd) {
	v := strings.TrimSpace(m.input.Value())
	switch m.step {
	case stepBaseURL:
		if v == "" {
			m.err = "base URL cannot be empty"
			return m, nil
		}
		m.providerCfg.BaseURL = v
		if m.remoteBlocked(v) {
			m.enterRemoteGate()
			return m, nil
		}
		m.enterAPIKey()
		return m, nil
	case stepAPIKey:
		if strings.HasPrefix(v, "$") {
			name := strings.TrimPrefix(v, "$")
			if name == "" {
				m.err = "env var name cannot be empty"
				return m, nil
			}
			m.providerCfg.APIKeyEnv = name
			m.providerCfg.APIKey = ""
		} else if v != "" {
			m.providerCfg.APIKey = v
		}
		return m.enterProbing()
	}
	return m, nil
}

func (m *Model) confirmRename() (*Model, tea.Cmd) {
	v := strings.TrimSpace(m.renameInput.Value())
	if v == "" {
		m.err = "name cannot be empty"
		return m, nil
	}
	if _, exists := m.cfg.Providers[v]; exists && v != m.providerName {
		m.err = "a provider with that name already exists"
		return m, nil
	}
	m.providerName = v
	m.enterSummary()
	return m, nil
}

func (m *Model) enterProbing() (*Model, tea.Cmd) {
	m.step = stepProbing
	m.title = "Connecting"
	m.subtitle = m.template.Label
	m.footer = "[r] retry  [s] skip  [Esc] cancel"
	m.err = ""
	m.picker = nil
	m.providerName = m.uniqueName()
	m.providerCfg.Type = orDefault(m.template.Type, "openai_compatible")
	m.probeStart = time.Now().UnixNano()
	m.spinner = 0
	return m, tea.Batch(m.runProbe(), tick())
}

func (m *Model) skipProbe() (*Model, tea.Cmd) {
	m.models = m.template.Models
	return m.advanceToPickModel()
}

func (m *Model) advanceToPickModel() (*Model, tea.Cmd) {
	enterPickModelStep(m, m.providerName)
	return m, nil
}

func (m *Model) runProbe() tea.Cmd {
	return probe.Provider("connect", m.providerName, m.providerCfg)
}

func (m *Model) done() tea.Cmd {
	return func() tea.Msg {
		return DoneMsg{
			Provider:      m.providerName,
			Model:         m.modelChosen,
			ProviderCfg:   m.providerCfg,
			EnabledRemote: m.remoteEnabled,
		}
	}
}

func (m *Model) uniqueName() string {
	if m.template.ID == "custom" || m.template.ID == "" {
		return "custom"
	}
	existing := map[string]bool{}
	for k := range m.cfg.Providers {
		existing[k] = true
	}
	return provider.UniqueName(m.template.ID, existing)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
