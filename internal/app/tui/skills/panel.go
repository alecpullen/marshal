package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	installSource string
	installScope  string
	installing    bool
	installErr    string
	removeArmed   map[string]bool
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a skills panel.
func NewPanel(homeDir, workDir string, projectTrusted bool, state *session.State) *Panel {
	p := &Panel{
		homeDir:        homeDir,
		workDir:        workDir,
		projectTrusted: projectTrusted,
		state:          state,
		removeArmed:    make(map[string]bool),
		installScope:   scopeGlobal,
	}
	p.filter = textfield.New()
	p.filter.SetVirtualCursor(true)
	p.filter.Focus()
	p.list = settings.NewFieldList(p.buildFields)
	return p
}

func (p *Panel) activeList() *settings.FieldList {
	if len(p.stack) > 0 {
		return settings.FrameList(p.stack[len(p.stack)-1])
	}
	return p.list
}

func (p *Panel) globalSkillsDir() string {
	home, _ := os.UserHomeDir()
	if p.homeDir != "" {
		home = p.homeDir
	}
	return filepath.Join(home, ".config", "marshal", "skills")
}

func (p *Panel) projectSkillsDir() string {
	return filepath.Join(p.workDir, ".marshal", "skills")
}

func (p *Panel) activeIndex() *skills.Index {
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
		settings.SetFieldAct(install, func() tea.Cmd {
			return p.runInstall()
		})
		fields = append(fields, install)

		return fields
	})
}

func (p *Panel) runInstall() tea.Cmd {
	if p.installSource == "" {
		return nil
	}
	p.installing = true
	p.installErr = ""
	targetDir := skills.ScopeDir(p.homeDir, p.workDir, p.installScope == scopeProject)
	source := p.installSource
	scope := p.installScope
	return func() tea.Msg {
		_, err := skills.Install(context.Background(), source, targetDir, "")
		return installResultMsg{Scope: scope, Err: err}
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
		settings.SetFieldSummary(f, func() string { return s.Scope })
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

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loadResultMsg:
		if msg.Err != nil {
			p.list.ErrMsg = msg.Err.Error()
		}
		return nil
	case installResultMsg:
		p.installing = false
		if msg.Err != nil {
			p.installErr = msg.Err.Error()
			return nil
		}
		p.installSource = ""
		p.installScope = scopeGlobal
		if p.stack != nil && len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		p.list.Refresh()
		return nil
	case removeResultMsg:
		if msg.Err != nil {
			p.list.ErrMsg = msg.Err.Error()
			return nil
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
		l := p.activeList()
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
	}
	return nil
}

func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 4 {
		return ""
	}
	settings.FieldListSetSize(p.list, width-3, maxHeight-4)
	body := "/ " + p.filter.View() + "\n" + p.list.View()
	footer := fmt.Sprintf("%d skills", len(settings.FieldListRows(p.list)))
	content := body + "\n" + lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(footer)
	return content
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
