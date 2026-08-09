package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/skills"
)

const scopeGlobal = "global"
const scopeProject = "project"

// Panel is the TUI management surface for installed skills.
type Panel struct {
	homeDir        string
	workDir        string
	projectTrusted bool
	state          *session.State
	skillIndex     *skills.Index

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	installSource string
	installScope  string
	installing    bool
	installErr    string
	// status is a transient confirmation line ("installed foo (global)")
	// rendered in the footer, so a completed action is never silent.
	status      string
	removeArmed map[string]bool
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a skills panel. skillIndex is the runtime skill index
// (may be nil); when present it is the source of truth for loading skills
// so the panel reflects skills the runtime has already loaded.
func NewPanel(homeDir, workDir string, projectTrusted bool, state *session.State, skillIndex *skills.Index) *Panel {
	p := &Panel{
		homeDir:        homeDir,
		workDir:        workDir,
		projectTrusted: projectTrusted,
		state:          state,
		skillIndex:     skillIndex,
		removeArmed:    make(map[string]bool),
		installScope:   scopeGlobal,
	}
	p.filter = textfield.New()
	p.filter.SetVirtualCursor(true)
	p.filter.Focus()
	p.list = settings.NewFieldList(p.buildFields)
	return p
}

// ActiveList returns the FieldList for the current stack depth.
func (p *Panel) ActiveList() *settings.FieldList {
	if len(p.stack) > 0 {
		return settings.FrameList(p.stack[len(p.stack)-1])
	}
	return p.list
}

// FilterValue returns the current filter text.
func (p *Panel) FilterValue() string { return p.filter.Value() }

// homeRoot is the home directory the panel resolves user-level paths
// against: the injected one when tests supply it, the real one otherwise.
func (p *Panel) homeRoot() string {
	if p.homeDir != "" {
		return p.homeDir
	}
	home, _ := os.UserHomeDir()
	return home
}

func (p *Panel) globalSkillsDir() string {
	return filepath.Join(config.UserDir(p.homeRoot()), "skills")
}

// isAutoloaded reports whether name is in the effective skills.autoload list.
func (p *Panel) isAutoloaded(name string) bool {
	if p.state == nil {
		return false
	}
	for _, n := range p.state.Config.Skills.Autoload {
		if n == name {
			return true
		}
	}
	return false
}

// setAutoload adds or removes name from skills.autoload and persists it.
//
// Autoload always writes to the user config, never the project one, even
// for a project-scoped skill. It is a preference about how you want the
// agent to behave, not a property of the repository, and writing it
// project-side would commit one developer's always-on skills into a shared
// tree. SaveUserConfigValue writes just this key, so the rest of the user
// config — and the project layer — are left alone.
func (p *Panel) setAutoload(name string, on bool) {
	if p.state == nil {
		p.ActiveList().ErrMsg = "no session available"
		return
	}
	current := p.state.Config.Skills.Autoload
	next := make([]string, 0, len(current)+1)
	for _, n := range current {
		if n != name {
			next = append(next, n)
		}
	}
	if on {
		next = append(next, name)
	}
	path := config.UserConfigPath(p.homeRoot())
	if err := config.SaveUserConfigValue(path, "skills.autoload", next); err != nil {
		p.ActiveList().ErrMsg = fmt.Sprintf("save autoload: %v", err)
		return
	}
	// Mirror the write in memory so the toggle reflects immediately; the
	// injection itself is a startup step, so it takes effect next session.
	p.state.Config.Skills.Autoload = next
	p.ActiveList().ErrMsg = ""
	if on {
		p.status = fmt.Sprintf("%s autoloads next session", name)
	} else {
		p.status = fmt.Sprintf("%s no longer autoloads", name)
	}
}

func (p *Panel) projectSkillsDir() string {
	return filepath.Join(p.workDir, ".marshal", "skills")
}

func (p *Panel) activeIndex() *skills.Index {
	if p.skillIndex != nil {
		return p.skillIndex
	}
	idx, _ := skills.LoadSkills(p.globalSkillsDir(), p.projectSkillsDir())
	if idx == nil {
		idx = skills.NewIndex()
	}
	return idx
}

func (p *Panel) detailFrame(s skills.ScopedSkill) *settings.Frame {
	return settings.NewFrame(s.Skill.Name, func() []*settings.Field {
		var fields []*settings.Field

		info := settings.NewField("detail.name", "Name", settings.KindScalar)
		settings.SetFieldGetStr(info, func() string { return s.Skill.Name })
		fields = append(fields, info)

		desc := settings.NewField("detail.desc", "Description", settings.KindScalar)
		settings.SetFieldGetStr(desc, func() string { return s.Skill.Description })
		fields = append(fields, desc)

		scope := settings.NewField("detail.scope", "Scope", settings.KindScalar)
		settings.SetFieldGetStr(scope, func() string { return s.Scope })
		fields = append(fields, scope)

		auto := settings.NewField("detail.autoload", "Autoload", settings.KindToggle)
		settings.SetFieldDesc(auto, "inject into every session at startup · saved to the global config")
		settings.SetFieldGetBool(auto, func() bool { return p.isAutoloaded(s.Skill.Name) })
		settings.SetFieldSetBool(auto, func(v bool) { p.setAutoload(s.Skill.Name, v) })
		fields = append(fields, auto)

		load := settings.NewField("action.load", "Load now", settings.KindAction)
		settings.SetFieldDesc(load, "inject this skill into the current session")
		settings.SetFieldAct(load, func() tea.Cmd {
			return func() tea.Msg {
				if p.state == nil {
					return loadResultMsg{Err: fmt.Errorf("no session available")}
				}
				idx := p.activeIndex()
				if err := skills.LoadSkillIntoSession(idx, p.state, s.Skill.Name); err != nil {
					return loadResultMsg{Err: err}
				}
				return loadResultMsg{Name: s.Skill.Name}
			}
		})
		fields = append(fields, load)

		remove := settings.NewField("action.remove", "Remove", settings.KindAction)
		settings.SetFieldDesc(remove, "delete this skill from disk")
		settings.SetFieldActLabel(remove, func() string {
			if p.removeArmed[s.Skill.Name] {
				return "↵ confirm remove"
			}
			return "↵ remove"
		})
		settings.SetFieldAct(remove, func() tea.Cmd {
			if !p.removeArmed[s.Skill.Name] {
				p.removeArmed[s.Skill.Name] = true
				return nil
			}
			p.removeArmed[s.Skill.Name] = false
			return p.runRemove(s)
		})
		fields = append(fields, remove)

		return fields
	})
}

func (p *Panel) installFrame() *settings.Frame {
	return settings.NewFrame("Install skill", func() []*settings.Field {
		var fields []*settings.Field

		source := settings.NewField("install.source", "Source", settings.KindScalar)
		settings.SetFieldDesc(source, "github:owner/repo, https://..., or local path")
		settings.SetFieldGetStr(source, func() string { return p.installSource })
		settings.SetFieldSetStr(source, func(v string) error {
			p.installSource = v
			return nil
		})
		fields = append(fields, source)

		scope := settings.NewField("install.scope", "Scope", settings.KindEnum)
		settings.SetFieldDesc(scope, "global or project")
		options := []string{scopeGlobal}
		if p.projectTrusted {
			options = append(options, scopeProject)
		}
		settings.SetFieldOptions(scope, func() []string { return options })
		settings.SetFieldGetStr(scope, func() string { return p.installScope })
		settings.SetFieldSetStr(scope, func(v string) error {
			p.installScope = v
			return nil
		})
		fields = append(fields, scope)

		install := settings.NewField("install.confirm", "Install", settings.KindAction)
		settings.SetFieldDesc(install, "clone or copy the skill into the selected scope")
		settings.SetFieldActLabel(install, func() string {
			if p.installing {
				return "installing…"
			}
			return "↵ install"
		})
		settings.SetFieldAct(install, func() tea.Cmd {
			return p.runInstall()
		})
		fields = append(fields, install)

		return fields
	})
}

func (p *Panel) runInstall() tea.Cmd {
	if p.installing {
		return nil
	}
	if strings.TrimSpace(p.installSource) == "" {
		// Returning nil here silently did nothing, which is indistinguishable
		// from a broken key binding. Surface it on the list's error row.
		p.installErr = "enter a source first"
		p.ActiveList().ErrMsg = p.installErr
		return nil
	}
	p.installing = true
	p.installErr = ""
	targetDir := skills.ScopeDir(p.homeDir, p.workDir, p.installScope == scopeProject)
	source := p.installSource
	scope := p.installScope
	return func() tea.Msg {
		name, err := skills.Install(context.Background(), source, targetDir, "")
		return installResultMsg{Name: name, Scope: scope, Err: err}
	}
}

func (p *Panel) runRemove(s skills.ScopedSkill) tea.Cmd {
	return func() tea.Msg {
		var path string
		if s.Scope == scopeGlobal {
			path = filepath.Join(p.globalSkillsDir(), s.Skill.Name)
		} else {
			path = filepath.Join(p.projectSkillsDir(), s.Skill.Name)
		}
		err := os.RemoveAll(path)
		return removeResultMsg{Name: s.Skill.Name, Scope: s.Scope, Err: err}
	}
}

func (p *Panel) buildFields() []*settings.Field {
	query := strings.TrimSpace(p.filter.Value())
	scoped, _ := skills.ListScopes(p.globalSkillsDir(), p.projectSkillsDir())

	fields := []*settings.Field{
		settings.NewField("header.installed", "Installed skills", settings.KindHeader),
	}

	for _, s := range scoped {
		s := s
		name := s.Skill.Name
		f := settings.NewField("skill."+name, name, settings.KindDrill)
		settings.SetFieldDesc(f, fmt.Sprintf("%s · %s", s.Scope, s.Skill.Description))
		// Which skills are always-on is otherwise invisible without opening
		// the config file, so it belongs in the row summary.
		settings.SetFieldSummary(f, func() string {
			if p.isAutoloaded(name) {
				return s.Scope + " · autoload"
			}
			return s.Scope
		})
		settings.SetFieldBuild(f, func() *settings.Frame { return p.detailFrame(s) })
		fields = append(fields, f)
	}

	if len(scoped) == 0 {
		none := settings.NewField("none", "No skills installed", settings.KindHeader)
		settings.SetFieldTitle(none, "(none)")
		fields = append(fields, none)
	}

	install := settings.NewField("action.install", "＋ Install skill", settings.KindAction)
	settings.SetFieldDesc(install, "install a skill from a git URL or local path")
	settings.SetFieldAct(install, func() tea.Cmd {
		p.installSource = ""
		p.installScope = scopeGlobal
		p.installErr = ""
		p.stack = append(p.stack, p.installFrame())
		return nil
	})
	fields = append(fields, install)

	if query == "" {
		return fields
	}

	filtered := []*settings.Field{}
	for _, f := range fields {
		if f.Kind == settings.KindHeader {
			continue
		}
		hay := settings.FieldID(f) + " " + settings.FieldTitle(f) + " " + settings.FieldDesc(f)
		if len(fuzzy.Rank(query, []string{hay})) > 0 {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

type loadResultMsg struct {
	Name string
	Err  error
}

type installResultMsg struct {
	Name  string
	Scope string
	Err   error
}

type removeResultMsg struct {
	Name  string
	Scope string
	Err   error
}

// OwnsMsg claims this panel's async result messages so the dock host
// delivers them. Without it the model cannot name these unexported types
// and drops them, so an install or remove completes on disk while the UI
// shows nothing at all. See dock.MessageOwner.
func (p *Panel) OwnsMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case loadResultMsg, installResultMsg, removeResultMsg:
		return true
	}
	return false
}

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loadResultMsg:
		if msg.Err != nil {
			p.ActiveList().ErrMsg = msg.Err.Error()
			return nil
		}
		p.status = fmt.Sprintf("loaded %s", msg.Name)
		p.ActiveList().ErrMsg = ""
		return nil
	case installResultMsg:
		p.installing = false
		if msg.Err != nil {
			p.installErr = msg.Err.Error()
			p.ActiveList().ErrMsg = p.installErr
			return nil
		}
		p.installSource = ""
		p.installScope = scopeGlobal
		p.status = fmt.Sprintf("installed %s (%s)", msg.Name, msg.Scope)
		if len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		p.list.Refresh()
		return nil
	case removeResultMsg:
		if msg.Err != nil {
			p.ActiveList().ErrMsg = msg.Err.Error()
			return nil
		}
		p.status = fmt.Sprintf("removed %s (%s)", msg.Name, msg.Scope)
		// A remove drills in from the detail frame; pop back so the refreshed
		// list is what the user lands on.
		if len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		p.list.Refresh()
		return nil
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyEscape {
			if len(p.stack) > 0 {
				p.stack = p.stack[:len(p.stack)-1]
				return nil
			}
			return func() tea.Msg { return settings.BrowserClosedMsg{} }
		}
		l := p.ActiveList()
		switch msg.String() {
		case "up", "down":
			return settings.FieldListUpdate(l, msg)
		case "enter":
			cmd := settings.FieldListUpdate(l, msg)
			if f := settings.FieldListTakePushRequest(l); f != nil {
				p.stack = append(p.stack, f)
			}
			return cmd
		default:
			if len(p.stack) > 0 || l.Editing() {
				return settings.FieldListUpdate(l, msg)
			}
			var cmd tea.Cmd
			p.filter, cmd = p.filter.Update(msg)
			p.list.Refresh()
			return cmd
		}
	case tea.PasteMsg:
		// Forward paste to the drilled frame or the field being edited;
		// otherwise deliver it to the flat filter input.
		if len(p.stack) > 0 || p.ActiveList().Editing() {
			return settings.FieldListUpdate(p.ActiveList(), msg)
		}
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(msg)
		p.list.Refresh()
		return cmd
	}
	return nil
}

// countLabel is the footer's skill count.
//
// It counts skill rows specifically rather than every row in the active
// list. The list also carries a header and the install action, and a
// drilled-in detail frame carries none of them — so counting rows reported
// "3 skills" for a single installed skill and "6 skills" while viewing one
// skill's detail. Counting `skill.` rows in the root list keeps the number
// honest at any stack depth and still tracks the filter, since filtering
// rebuilds that list.
func (p *Panel) countLabel() string {
	n := 0
	for _, f := range settings.FieldListRows(p.list) {
		if strings.HasPrefix(settings.FieldID(f), "skill.") {
			n++
		}
	}
	if n == 1 {
		return "1 skill"
	}
	return fmt.Sprintf("%d skills", n)
}

func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 4 {
		return ""
	}
	l := p.ActiveList()
	settings.FieldListSetSize(l, width-3, maxHeight-4)
	header := "/ " + p.filter.View()
	if len(p.stack) > 0 {
		header = settings.FrameTitle(p.stack[len(p.stack)-1])
	}
	body := header + "\n" + l.View()
	footer := p.countLabel()
	// An in-flight install (a git clone can take seconds) and a completed one
	// both used to render nothing, making a working action look broken.
	switch {
	case p.installing:
		footer += " · installing…"
	case p.status != "":
		footer += " · " + p.status
	}
	content := body + "\n" + lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(footer)
	return content
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
