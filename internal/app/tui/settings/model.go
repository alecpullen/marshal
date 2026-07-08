package settings

import (
	"fmt"
	"sort"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/huhtheme"
	"marshal/internal/llm/routing"
)

// Model is a wrapper around a *huh.Form that edits a project config in
// place. The type name and method signatures are preserved so the parent
// TUI model can keep using `settings.New`, `settings.Model.Update`,
// `settings.Model.SetSize`, and `settings.Model.View` unchanged.
//
// Because the parent reassigns the returned Model value
// (`m.settingsModel, cmd = m.settingsModel.Update(msg)`), all mutable state
// that the embedded *huh.Form binds to via pointers (the editable field
// values and the cfg) lives in a heap-allocated state struct. Model copies
// share the same *state pointer, so the form's accessors keep pointing at
// stable storage no matter how many times the value receiver is copied.
type Model struct {
	form           *huh.Form
	state          *state
	workingDir     string
	projectCfgPath string
	footer         string
	width          int
	height         int
	completed      bool
}

// state holds the editable field values the huh form binds to by pointer,
// plus the working copy of the config that field callbacks mutate. It is
// always heap-allocated (Model stores a *state) so pointer bindings survive
// Model value copies.
type state struct {
	cfg            config.Config
	provider       string
	model          string
	localOnly      bool
	presetOpt      string
	maxToolIter    string
	maxRetries     string
	shellTimeout   string
	maxShellOutput string
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	if m.form != nil {
		m.form.WithWidth(frameWidth(width))
	}
}

func frameWidth(width int) int {
	fw := 57
	if width > 0 {
		fw = min(60, width-4)
	}
	if fw < 30 {
		fw = 30
	}
	return fw
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	m := Model{
		state:          &state{cfg: cfg},
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}
	s := m.state

	profileNames := make([]string, 0, len(cfg.AgentProfiles))
	for name := range cfg.AgentProfiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	if len(profileNames) == 0 {
		profileNames = []string{""}
	}

	activePreset := activePresetFromConfig(cfg)

	provider := cfg.Agent.Provider
	model := cfg.Agent.Model
	localOnly := false
	if activePreset.Name != "" {
		provider = activePreset.Provider
		model = activePreset.Model
		localOnly = activePreset.LocalOnly
	}

	s.provider = provider
	s.model = model
	s.localOnly = localOnly
	s.presetOpt = activePreset.Name
	s.maxToolIter = strconv.Itoa(cfg.Agent.MaxToolIterations)
	s.maxRetries = strconv.Itoa(cfg.Agent.MaxRetries)
	s.shellTimeout = strconv.Itoa(cfg.Tools.Shell.DefaultTimeoutSeconds)
	s.maxShellOutput = strconv.Itoa(cfg.Tools.Shell.MaxOutputBytes)

	profileOpts := make([]huh.Option[string], 0, len(profileNames))
	for _, name := range profileNames {
		profileOpts = append(profileOpts, huh.NewOption(name, name))
	}

	// Numeric field builder: validates digits + min bound, writes back the
	// parsed value to the supplied setter. Replicates the old intField clamp
	// behaviour (min only is enforced here; the original Max tool iterations
	// field had a min of 1 and no upper bound).
	numField := func(label string, value *string, min int, set func(int)) *huh.Input {
		return huh.NewInput().
			Key(label).
			Title(label).
			Value(value).
			Validate(func(s string) error {
				v, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("must be a number")
				}
				if min != 0 && v < min {
					v = min
				}
				*value = strconv.Itoa(v)
				set(v)
				return nil
			})
	}

	fields := []huh.Field{
		huh.NewSelect[string]().
			Key("Default profile").
			Title("Default profile").
			Options(profileOpts...).
			Value(&s.cfg.Profile.Default),

		// Preset is read-only display — a single-option Select keeps the user
		// from editing it while still showing it inline.
		huh.NewSelect[string]().
			Key("Preset").
			Title("Preset").
			Options(huh.NewOption(activePreset.Name, activePreset.Name)).
			Value(&s.presetOpt),

		huh.NewInput().
			Key("Provider").
			Title("Provider").
			Value(&s.provider).
			Validate(func(val string) error {
				if name := m.activePresetName(); name != "" {
					if p, ok := s.cfg.Models.Presets[name]; ok {
						p.Provider = val
						s.cfg.Models.Presets[name] = p
					}
				} else {
					s.cfg.Agent.Provider = val
				}
				return nil
			}),

		huh.NewInput().
			Key("Model").
			Title("Model").
			Value(&s.model).
			Validate(func(val string) error {
				if name := m.activePresetName(); name != "" {
					if p, ok := s.cfg.Models.Presets[name]; ok {
						p.Model = val
						s.cfg.Models.Presets[name] = p
					}
				} else {
					s.cfg.Agent.Model = val
				}
				return nil
			}),

		huh.NewConfirm().
			Key("Local only").
			Title("Local only").
			Description("block remote providers for this preset").
			Value(&s.localOnly).
			Validate(func(ok bool) error {
				if name := m.activePresetName(); name != "" {
					if p, ok := s.cfg.Models.Presets[name]; ok {
						p.LocalOnly = ok
						s.cfg.Models.Presets[name] = p
					}
				}
				return nil
			}),

		huh.NewConfirm().
			Key("Remote providers allowed").
			Title("Remote providers allowed").
			Description("allow remote providers globally").
			Value(&s.cfg.Privacy.RemoteProvidersAllowed),

		numField("Max tool iterations", &s.maxToolIter, 1, func(v int) { s.cfg.Agent.MaxToolIterations = v }),
		numField("Max retries", &s.maxRetries, 0, func(v int) { s.cfg.Agent.MaxRetries = v }),
		numField("Shell timeout", &s.shellTimeout, 0, func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v }),
		numField("Max shell output", &s.maxShellOutput, 0, func(v int) { s.cfg.Tools.Shell.MaxOutputBytes = v }),

		huh.NewConfirm().
			Key("Allow network").
			Title("Allow network").
			Description("shell commands may use network").
			Value(&s.cfg.Tools.Shell.AllowNetwork),

		huh.NewConfirm().
			Key("Allow sudo").
			Title("Allow sudo").
			Description("shell commands may use sudo").
			Value(&s.cfg.Tools.Shell.AllowSudo),

		huh.NewConfirm().
			Key("Allow destructive").
			Title("Allow destructive").
			Description("shell commands may be destructive").
			Value(&s.cfg.Tools.Shell.AllowDestructive),

		huh.NewConfirm().
			Key("Auto-approve shell").
			Title("Auto-approve shell").
			Description("skip shell confirmations").
			Value(&s.cfg.Tools.Shell.AutoApprove),
	}

	group := huh.NewGroup(fields...).Title("Settings")

	km := huh.NewDefaultKeyMap()
	// Keep Ctrl+C as the parent's global quit (handled above the form
	// routing) and Esc for the form's own cancel path.
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c"))
	// Ctrl+S submits the form; Enter navigates fields and submits on the
	// last field, matching the previous Ctrl+S-save UX.
	km.Input.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Confirm.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Select.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	// Preserve the old Space-to-toggle behaviour for boolean settings.
	km.Confirm.Toggle = key.NewBinding(key.WithKeys("h", "l", "right", "left", "space"))

	m.form = huh.NewForm(group).
		WithTheme(huhtheme.WarmSunset()).
		WithWidth(frameWidth(0)).
		WithShowHelp(true).
		WithKeyMap(km)

	// Eagerly run the form's Init so the first field is focused and the view
	// is laid out before the parent ever renders. The parent TUI never calls
	// settings.Init(); executing the returned command tree here mirrors what
	// the bubbletea runtime would do on the program's first tick. The cmds
	// are non-blocking (focus setup + a window-size request that is a no-op
	// outside a real program), so we drain them synchronously.
	if initCmd := m.form.Init(); initCmd != nil {
		_ = initCmd()
	}

	return m
}

func activePresetFromConfig(cfg config.Config) routing.ModelPreset {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return routing.ModelPreset{}
	}
	name, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return routing.ModelPreset{}
	}
	if preset, ok := cfg.Models.Presets[name]; ok {
		return preset
	}
	return routing.ModelPreset{Name: name}
}

func (m *Model) activePresetName() string {
	profile, ok := m.state.cfg.AgentProfiles[m.state.cfg.Profile.Default]
	if !ok {
		return ""
	}
	name, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return ""
	}
	return name
}

func (m Model) Init() tea.Cmd {
	if m.form == nil {
		return nil
	}
	return m.form.Init()
}

func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.form == nil {
		return *m, nil
	}
	// Esc cancels the form (preserving the previous Esc-to-cancel behaviour).
	// Ctrl+C is intercepted by the parent before reaching here.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		return *m, func() tea.Msg { return CancelledMsg{} }
	}

	updated, cmd := m.form.Update(msg)
	if f, ok := updated.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateCompleted {
		if m.completed {
			return *m, nil
		}
		m.completed = true
		return *m, m.saveCmd()
	}
	if m.form.State == huh.StateAborted {
		return *m, func() tea.Msg { return CancelledMsg{} }
	}
	return *m, cmd
}

func (m *Model) saveCmd() tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveProjectConfig(m.projectCfgPath, m.state.cfg); err != nil {
			m.footer = fmt.Sprintf("Save failed: %v", err)
			return nil
		}
		loaded, err := config.Load(config.LoadOptions{WorkingDir: m.workingDir})
		if err != nil {
			m.footer = fmt.Sprintf("Reload failed: %v", err)
			return nil
		}
		return SavedMsg{Cfg: loaded}
	}
}

func (m Model) View() string {
	if m.form == nil {
		return ""
	}
	view := m.form.View()
	if m.footer != "" {
		view += "\n" + m.footer
	}
	return view
}

func (m Model) FocusedFieldTitle() string {
	if m.form == nil {
		return ""
	}
	f := m.form.GetFocusedField()
	if f == nil {
		return ""
	}
	return f.GetKey()
}

func (m Model) Footer() string {
	return m.footer
}

// BoolValue returns the current value of a named boolean settings field. It
// is a convenience for tests that need to observe confirm-field state without
// parsing the rendered view.
func (m Model) BoolValue(title string) bool {
	switch title {
	case "Local only":
		return m.state.localOnly
	case "Remote providers allowed":
		return m.state.cfg.Privacy.RemoteProvidersAllowed
	case "Allow network":
		return m.state.cfg.Tools.Shell.AllowNetwork
	case "Allow sudo":
		return m.state.cfg.Tools.Shell.AllowSudo
	case "Allow destructive":
		return m.state.cfg.Tools.Shell.AllowDestructive
	case "Auto-approve shell":
		return m.state.cfg.Tools.Shell.AutoApprove
	}
	return false
}
