package connect

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/presetflow"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
)

// modelValueSep separates provider from model in a picker Value. NUL cannot
// appear in either half, unlike "/" which is common in model IDs
// ("anthropic/claude-sonnet-4").
const modelValueSep = "\x00"

// probeOnlyLimits is passed to provider.NewFromConfig for capability
// probing; limit-table discovery already happened during model-list probe
// and must not block the TUI while the confirmation screen is opening.
const probeOnlyLimits = false

func encodeModelValue(provider, model string) string {
	return provider + modelValueSep + model
}

func decodeModelValue(v string) (provider, model string) {
	if p, m, ok := strings.Cut(v, modelValueSep); ok {
		return p, m
	}
	return "", v
}

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
	stepConfirmLimits
)

type Opts struct {
	Cfg              config.Config
	Discovered       map[string][]schema.ModelInfo
	SkipToIntroModel bool
	ScopedProvider   string
	CfgPath          string
	AllProviders     bool
	// DataDir enables the on-disk limit table during probing (read from
	// cache; refreshed remotely only per privacy.remote_limit_discovery).
	DataDir string
}

type Model struct {
	step             step
	renameReturnStep step
	picker           *picker.Model
	input            textfield.Model
	renameInput      textfield.Model
	title            string
	subtitle         string
	footer           string
	err              string
	template         provider.ProviderTemplate
	providerName     string
	providerCfg      config.ProviderConfig
	models           []schema.ModelInfo
	cfg              config.Config
	discovered       map[string][]schema.ModelInfo
	scopedProvider   string
	cfgPath          string
	width            int
	height           int
	probeStart       int64
	spinner          int
	modelChosen      string
	remoteEnabled    bool
	allProviders     bool
	probeErrs        map[string]error
	dataDir          string
	confirm          *presetflow.ConfirmState
	detectingCaps    bool
	capProbeID       uint64
	cancelCapProbe   context.CancelFunc
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
		allProviders:   opts.AllProviders,
		dataDir:        opts.DataDir,
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
	case presetflow.CapabilityProbedMsg:
		if !m.detectingCaps || msg.RequestID != m.capProbeID ||
			m.step != stepConfirmLimits || msg.Provider != m.providerName || msg.Model != m.modelChosen {
			return m, nil // stale result from a since-abandoned selection
		}
		m.finishCapabilityProbe()
		m.confirm.Limits = m.confirm.Limits.WithProbed(msg.Caps.ToolCalling, msg.Caps.ContextWindow)
		return m, nil
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
		if m.step == stepConfirmLimits {
			if m.detectingCaps || m.confirm == nil || !m.confirm.Editing() {
				return m, nil
			}
			return m, m.confirm.UpdateInput(msg)
		}
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

// Sizing keeps the connect wizard docked under the default height cap.
func (p Panel) Sizing() dock.Sizing { return dock.Docked }

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
	case stepConfirmLimits:
		if m.detectingCaps {
			b.WriteString(hintStyle().Render("detecting model capabilities…"))
		} else {
			b.WriteString(m.confirm.Render(pw))
		}
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(errStyle().Render(m.err))
	}
	footer := m.footer
	if m.step == stepConfirmLimits && m.detectingCaps {
		footer = "please wait…"
	}
	if footer != "" {
		b.WriteString("\n")
		b.WriteString(footerStyle().Render(footer))
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
	Provider        string
	Model           string
	ProviderCfg     config.ProviderConfig
	EnabledRemote   bool
	ContextWindow   int
	MaxOutputTokens int
	ToolCalling     *bool
}

// RefreshMsg is emitted when the user presses ctrl+r in the model
// selection step. It asks the parent to evict the listed providers'
// cached discovery entries and re-probe them.
type RefreshMsg struct {
	Providers []string
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
		switch m.renameReturnStep {
		case stepBaseURL:
			enterBaseURLStep(m)
		case stepRemoteGate:
			m.enterRemoteGate()
		default:
			m.enterSummary()
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
		ti.Placeholder = "$" + m.template.KeyEnv + " · blank uses it · or paste a key"
	} else {
		ti.Placeholder = "$ENV_NAME to read from an env var, or paste a key"
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

func (m *Model) enterConfirmLimits() (*Model, tea.Cmd) {
	m.cancelCapabilityProbe()
	m.step = stepConfirmLimits
	m.title = "Confirm model limits"
	m.subtitle = m.providerName + " · " + m.modelChosen
	m.footer = "[↵] confirm  [c] edit context  [o] edit max output  [Esc] back"
	m.err = ""
	m.picker = nil

	lim := presetflow.Resolve(m.discovered[m.providerName], m.modelChosen)
	if preset, ok := m.presetFor(m.providerName, m.modelChosen); ok {
		lim = lim.WithPreset(preset.ContextWindow, preset.MaxOutputTokens)
	}
	m.confirm = presetflow.NewConfirmState(lim)

	cmd, cancel := m.probeCapabilities(m.capProbeID)
	m.cancelCapProbe = cancel
	m.detectingCaps = cmd != nil
	return m, cmd
}

// presetFor returns the preset already saved for providerName+modelID, if
// any — re-picking a model whose limits came back unknown must not erase
// figures confirmed earlier.
func (m *Model) presetFor(providerName, modelID string) (routing.ModelPreset, bool) {
	for _, p := range m.cfg.Models.Presets {
		if p.Provider == providerName && p.Model == modelID {
			return p, true
		}
	}
	return routing.ModelPreset{}, false
}

// probeCapabilities returns a cmd that probes the chosen model's
// capabilities if the constructed provider supports it (currently: Ollama
// only), or nil if there is nothing to probe — callers must not set
// detectingCaps=true unless this returns non-nil, or the "detecting…" state
// would never clear.
func (m *Model) probeCapabilities(requestID uint64) (tea.Cmd, context.CancelFunc) {
	// Capability probing only needs the provider client. Limit-table discovery
	// already happened during model-list discovery and must not block the TUI
	// while this confirmation screen is opening.
	prov, err := provider.NewFromConfig(m.providerName, m.providerCfg, m.dataDir, probeOnlyLimits)
	if err != nil {
		return nil, nil
	}
	if _, ok := prov.(provider.CapabilityProber); !ok {
		return nil, nil
	}
	return presetflow.ProbeCapabilitiesCmdWithCancel(prov, m.providerName, m.modelChosen, requestID)
}

func (m *Model) cancelCapabilityProbe() {
	if m.cancelCapProbe != nil {
		m.cancelCapProbe()
		m.cancelCapProbe = nil
	}
	m.capProbeID++
	m.detectingCaps = false
}

func (m *Model) finishCapabilityProbe() {
	if m.cancelCapProbe != nil {
		m.cancelCapProbe()
		m.cancelCapProbe = nil
	}
	m.detectingCaps = false
}

func (m *Model) enterSummary() {
	m.step = stepSummary
	m.title = "Review provider"
	m.subtitle = ""
	m.footer = "[↵] confirm  [n] rename provider  [Esc] back"
	m.err = ""
	m.picker = nil
}

func (m *Model) enterRename(returnTo step) {
	m.step = stepRename
	m.renameReturnStep = returnTo
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

func (m *Model) isCustomTemplate() bool {
	return m.template.ID == "custom" || m.template.ID == "openai_compatible"
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
	if m.allProviders {
		m.subtitle = "all configured providers"
	} else {
		m.subtitle = providerName
	}
	m.footer = "[↑↓] move [↵] pick [^r] refresh [Esc] done"
	m.err = ""
	m.providerName = providerName
	if pc, ok := m.cfg.Providers[providerName]; ok {
		m.providerCfg = pc
	}
	m.picker = buildModelPicker(m, providerName)
}

func buildModelPicker(m *Model, providerName string) *picker.Model {
	var items []picker.Item
	if m.allProviders {
		// Collect models from every configured provider, sorted for stable order.
		pnames := make([]string, 0, len(m.cfg.Providers))
		for k := range m.cfg.Providers {
			pnames = append(pnames, k)
		}
		sort.Strings(pnames)
		for _, pn := range pnames {
			candidates := m.discovered[pn]
			if len(candidates) == 0 {
				if tpl, ok := m.providerTemplate(pn); ok {
					candidates = templateModelsToInfo(tpl.Models)
				}
			}
			if len(candidates) == 0 {
				// Keep the provider visible even with nothing discovered, or
				// it silently vanishes from the list. The note explains why.
				items = append(items, picker.Item{
					Label: "Enter model id manually", Detail: pn,
					Badge: m.emptyProviderNote(pn), Group: pn,
					Value: encodeModelValue(pn, "__manual__"),
				})
				continue
			}
			for _, mi := range candidates {
				badge := "◉ discovered"
				if _, ok := m.discovered[pn]; !ok || len(m.discovered[pn]) == 0 {
					badge = "◯ catalog"
				}
				items = append(items, picker.Item{
					Label: mi.ID, Detail: pn, Badge: badge, Group: pn,
					Value: encodeModelValue(pn, mi.ID),
				})
			}
		}
	} else {
		candidates := m.models
		if len(candidates) == 0 {
			if cached, ok := m.discovered[providerName]; ok {
				candidates = cached
			}
		}
		if len(candidates) == 0 {
			if tpl, ok := m.providerTemplate(providerName); ok {
				candidates = templateModelsToInfo(tpl.Models)
			}
		}
		if len(candidates) == 0 {
			candidates = templateModelsToInfo(m.template.Models)
		}
		for _, mi := range candidates {
			badge := "◉ discovered"
			if len(m.models) == 0 {
				badge = "◯ catalog"
			}
			items = append(items, picker.Item{Label: mi.ID, Detail: providerName, Badge: badge, Group: providerName, Value: mi.ID})
		}
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Label: "Enter model id manually", Value: "__manual__", Badge: "custom"})
	}
	sub := "pick a model for " + providerName
	if m.allProviders {
		sub = "pick a model"
	}
	p := picker.New(m.title, sub, items)
	p.SetAllowCustom(true)
	return p
}

// emptyProviderNote explains why a configured provider has no model rows in
// all-providers mode: its probe failed, the privacy gate skipped it, or it
// simply returned nothing.
func (m *Model) emptyProviderNote(providerName string) string {
	if _, failed := m.probeErrs[providerName]; failed {
		return "◯ probe failed — ^r retries"
	}
	if pc, ok := m.cfg.Providers[providerName]; ok &&
		!probe.IsLocalhost(pc.BaseURL) &&
		!m.cfg.Privacy.RemoteProvidersAllowed {
		return "◯ remote discovery disabled (privacy.remote_providers)"
	}
	return "◯ no models discovered"
}

// templateModelsToInfo wraps a template's catalog model IDs in ModelInfo
// records so the picker can treat catalog and discovered models uniformly.
// Templates have no per-model limits, so all fields except ID are zero.
func templateModelsToInfo(ids []string) []schema.ModelInfo {
	out := make([]schema.ModelInfo, len(ids))
	for i, id := range ids {
		out[i] = schema.ModelInfo{ID: id}
	}
	return out
}

func badgeForTemplate(tpl provider.ProviderTemplate) string {
	if tpl.Local {
		return "local"
	}
	return "remote"
}

func (m *Model) handlePickerPicked(value string) (*Model, tea.Cmd) {
	if m.step == stepPickModel {
		// When AllProviders is set, the value is encoded with provider prefix.
		p, mdl := decodeModelValue(value)
		if mdl == "__manual__" {
			// Scope a typed-in model id to this row's provider, then let the
			// user enter the id via the picker's custom-value path.
			if p != "" {
				m.providerName = p
				if pc, ok := m.cfg.Providers[p]; ok {
					m.providerCfg = pc
				}
			}
			return m, nil
		}
		if p != "" {
			m.providerName = p
			if pc, ok := m.cfg.Providers[p]; ok {
				m.providerCfg = pc
			}
			m.modelChosen = mdl
		} else {
			m.modelChosen = value
		}
		// Both flows confirm limits before completing: /models skipped the
		// summary, but it must not skip this.
		return m.enterConfirmLimits()
	}
	tpl, ok := provider.Lookup(value)
	if !ok {
		if value == "custom" || strings.HasPrefix(value, "@ai-sdk/") {
			m.template = provider.ProviderTemplate{ID: "custom", Type: "openai_compatible"}
			m.providerCfg = config.ProviderConfig{Type: "openai_compatible", Template: "custom"}
			enterBaseURLStep(m)
			return m, nil
		}
		m.template = provider.ProviderTemplate{ID: value, Type: "openai_compatible"}
		m.providerCfg = config.ProviderConfig{Type: "openai_compatible", Template: value}
		enterBaseURLStep(m)
		return m, nil
	}
	m.template = tpl
	m.providerCfg = config.ProviderConfig{Type: tpl.Type, BaseURL: tpl.BaseURL, APIKeyEnv: tpl.KeyEnv, ToolCalling: tpl.ToolCalling, Template: tpl.ID}
	if tpl.BaseURL == "" {
		enterBaseURLStep(m)
		return m, nil
	}
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
	name := msg.Provider
	if name == "" {
		name = m.providerName
	}
	if msg.Err != nil {
		if m.allProviders {
			// One provider's failure must not disturb the others; note it on
			// that provider's picker rows instead of the (invisible) probing
			// step's inline error.
			if m.probeErrs == nil {
				m.probeErrs = map[string]error{}
			}
			m.probeErrs[name] = msg.Err
			m.rebuildModelPicker()
			return m, nil
		}
		m.err = "✗ " + strutil.Truncate(msg.Err.Error(), 72, true)
		if hint := probeHint(msg.Err); hint != "" {
			m.err += "\n  ↳ " + hint
		}
		m.footer = "[r] retry  [s] skip  [Esc] cancel"
		return m, nil
	}
	delete(m.probeErrs, name)
	if m.discovered != nil {
		m.discovered[name] = msg.Models
	}
	if m.allProviders {
		// Results arrive interleaved across providers; the scoped m.models
		// belongs to the single-provider flow only. Rebuild rows in place so
		// the user's filter and position survive each delivery.
		m.rebuildModelPicker()
		return m, nil
	}
	m.models = msg.Models
	_, advCmd := m.advanceToPickModel()
	return m, advCmd
}

// rebuildModelPicker refreshes the model rows (e.g. after a probe result)
// while preserving whatever the user has typed into the filter.
func (m *Model) rebuildModelPicker() {
	filter := ""
	if m.picker != nil {
		filter = m.picker.FilterValue()
	}
	m.picker = buildModelPicker(m, m.providerName)
	if filter != "" {
		m.picker.SetFilter(filter)
	}
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
		// ctrl+r is intercepted here (not by the picker) so a bare "r"
		// still falls through to the filter input — every printable key
		// the picker doesn't recognise edits the filter box.
		if m.step == stepPickModel && ks == "ctrl+r" {
			return m, m.refreshCmd()
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
			if m.isCustomTemplate() {
				m.providerName = m.uniqueName()
				m.enterRename(stepRemoteGate)
				return m, nil
			}
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
			m.enterRename(stepSummary)
			return m, nil
		case "esc":
			enterPickModelStep(m, m.providerName)
			return m, nil
		}
		return m, nil
	case stepConfirmLimits:
		if m.detectingCaps {
			if ks == "esc" {
				m.cancelCapabilityProbe()
				m.confirm = nil
				enterPickModelStep(m, m.providerName)
			}
			return m, nil // ignore keys while the capability probe is in flight
		}
		result, cmd := m.confirm.HandleKey(k)
		switch result {
		case presetflow.ConfirmDone:
			if m.scopedProvider != "" {
				m.step = stepDone
				return m, m.done()
			}
			m.enterSummary()
			return m, nil
		case presetflow.ConfirmCancelled:
			m.cancelCapabilityProbe()
			m.confirm = nil
			enterPickModelStep(m, m.providerName)
			return m, nil
		}
		return m, cmd
	case stepRename:
		switch ks {
		case "enter":
			return m.confirmRename()
		case "esc":
			return m, m.back()
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
		if m.isCustomTemplate() {
			m.providerName = m.uniqueName()
			m.enterRename(stepBaseURL)
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
			m.providerCfg.APIKeyEnv = "" // clear template env reference when using a literal key
		} else if m.template.KeyEnv != "" {
			m.providerCfg.APIKeyEnv = m.template.KeyEnv
			m.providerCfg.APIKey = ""
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
	switch m.renameReturnStep {
	case stepBaseURL, stepRemoteGate:
		m.enterAPIKey()
		return m, nil
	default:
		m.enterSummary()
		return m, nil
	}
}

func (m *Model) enterProbing() (*Model, tea.Cmd) {
	m.step = stepProbing
	m.title = "Connecting"
	m.subtitle = m.template.Label
	m.footer = "[r] retry  [s] skip  [Esc] cancel"
	m.err = ""
	m.picker = nil
	if m.providerName == "" {
		m.providerName = m.uniqueName()
	}
	if m.providerCfg.Template == "" && m.template.ID != "" {
		m.providerCfg.Template = m.template.ID
	}
	m.providerCfg.Type = orDefault(m.template.Type, "openai_compatible")
	m.probeStart = time.Now().UnixNano()
	m.spinner = 0
	return m, tea.Batch(m.runProbe(), tick())
}

func (m *Model) skipProbe() (*Model, tea.Cmd) {
	m.models = templateModelsToInfo(m.template.Models)
	return m.advanceToPickModel()
}

func (m *Model) advanceToPickModel() (*Model, tea.Cmd) {
	enterPickModelStep(m, m.providerName)
	return m, nil
}

func (m *Model) runProbe() tea.Cmd {
	return probe.Provider("connect", m.providerName, m.providerCfg, m.dataDir, m.cfg.Privacy.RemoteLimitDiscovery)
}

// refreshCmd emits a RefreshMsg naming the providers whose entries the
// picker is currently showing, so the parent TUI can evict and re-probe
// them. In single-provider mode only the scoped provider is listed; in
// all-providers mode every provider with a populated discovered map (or
// the active providerName) is included.
func (m *Model) refreshCmd() tea.Cmd {
	names := map[string]struct{}{m.providerName: {}}
	if m.allProviders {
		for pn := range m.discovered {
			if len(m.discovered[pn]) > 0 {
				names[pn] = struct{}{}
			}
		}
		for pn := range m.cfg.Providers {
			if tpl, ok := m.providerTemplate(pn); ok && len(tpl.Models) > 0 {
				names[pn] = struct{}{}
			}
		}
	}
	providers := make([]string, 0, len(names))
	for n := range names {
		providers = append(providers, n)
	}
	sort.Strings(providers)
	return func() tea.Msg { return RefreshMsg{Providers: providers} }
}

func (m *Model) done() tea.Cmd {
	return func() tea.Msg {
		return DoneMsg{
			Provider:        m.providerName,
			Model:           m.modelChosen,
			ProviderCfg:     m.providerCfg,
			EnabledRemote:   m.remoteEnabled,
			ContextWindow:   m.confirm.Limits.ContextWindow,
			MaxOutputTokens: m.confirm.Limits.MaxOutputTokens,
			ToolCalling:     m.confirm.Limits.ToolCalling,
		}
	}
}

func (m *Model) uniqueName() string {
	base := nameBase(m.template.ID, m.providerCfg.BaseURL)
	existing := map[string]bool{}
	for k := range m.cfg.Providers {
		existing[k] = true
	}
	return provider.UniqueName(base, existing)
}

// nameBase picks the default provider name base. For known templates it uses
// the template ID; for custom/openai-compatible entries it tries to derive a
// readable name from the base URL host.
func nameBase(templateID, baseURL string) string {
	if templateID != "" && templateID != "custom" && templateID != "openai_compatible" {
		return templateID
	}
	if host := hostBase(baseURL); host != "" {
		return host
	}
	return "custom"
}

// hostBase extracts a clean host from a URL: scheme and path are removed,
// the port is stripped, and leading "api."/"www." prefixes are removed.
// u.Hostname() is used (rather than manual port splitting) so IPv6 literals
// like "[::1]:11434" resolve to "::1" instead of a malformed "[".
func hostBase(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	host = strings.TrimPrefix(host, "api.")
	host = strings.TrimPrefix(host, "www.")
	return host
}

// providerTemplate looks up the catalog template for a configured provider.
// It prefers the stored template ID so renamed providers still resolve to
// their original catalog models.
func (m *Model) providerTemplate(pn string) (provider.ProviderTemplate, bool) {
	pc, ok := m.cfg.Providers[pn]
	if !ok {
		return provider.ProviderTemplate{}, false
	}
	id := pn
	if pc.Template != "" {
		id = pc.Template
	}
	return provider.Lookup(id)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
