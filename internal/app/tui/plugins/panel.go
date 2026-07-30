package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/plugins"
)

const scopeGlobal = "global"
const scopeProject = "project"

// Panel is the TUI management surface for installed plugins.
type Panel struct {
	homeDir        string
	workDir        string
	projectTrusted bool

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	installSource   string
	installScope    string
	scannedContents *plugins.Contents
	scannedName     string
	scannedSource   string
	scanDir         string
	removeArmed     map[string]bool
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a plugins panel.
func NewPanel(homeDir, workDir string, projectTrusted bool) *Panel {
	p := &Panel{
		homeDir:        homeDir,
		workDir:        workDir,
		projectTrusted: projectTrusted,
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

func (p *Panel) globalStoreDir() string {
	home, _ := os.UserHomeDir()
	if p.homeDir != "" {
		home = p.homeDir
	}
	return plugins.GlobalStoreDir(home)
}

func (p *Panel) projectStoreDir() string {
	return plugins.ProjectStoreDir(p.workDir)
}

func (p *Panel) globalLockPath() string {
	home, _ := os.UserHomeDir()
	if p.homeDir != "" {
		home = p.homeDir
	}
	return plugins.GlobalLockPath(home)
}

func (p *Panel) projectLockPath() string {
	return plugins.ProjectLockPath(p.workDir)
}

type scopedEntry struct {
	plugins.LockEntry
	Scope string
}

func (p *Panel) lockEntries() []scopedEntry {
	var entries []scopedEntry
	if lf, err := plugins.ReadLockfile(p.globalLockPath()); err == nil {
		for _, e := range lf.Plugins {
			e := e
			entries = append(entries, scopedEntry{LockEntry: e, Scope: scopeGlobal})
		}
	}
	if p.projectTrusted {
		if lf, err := plugins.ReadLockfile(p.projectLockPath()); err == nil {
			for _, e := range lf.Plugins {
				e := e
				entries = append(entries, scopedEntry{LockEntry: e, Scope: scopeProject})
			}
		}
	}
	return entries
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func (p *Panel) detailFrame(e scopedEntry) *settings.Frame {
	return settings.NewFrame(e.Name, func() []*settings.Field {
		var fields []*settings.Field

		name := settings.NewField("detail.name", "Name", settings.KindScalar)
		settings.SetFieldGetStr(name, func() string { return e.Name })
		fields = append(fields, name)

		src := settings.NewField("detail.source", "Source", settings.KindScalar)
		settings.SetFieldGetStr(src, func() string { return e.Source })
		fields = append(fields, src)

		commit := settings.NewField("detail.commit", "Commit", settings.KindScalar)
		settings.SetFieldGetStr(commit, func() string { return e.Commit })
		fields = append(fields, commit)

		scope := settings.NewField("detail.scope", "Scope", settings.KindScalar)
		settings.SetFieldGetStr(scope, func() string { return e.Scope })
		fields = append(fields, scope)

		update := settings.NewField("action.update", "Update", settings.KindAction)
		settings.SetFieldDesc(update, "check for a newer version")
		settings.SetFieldAct(update, func() tea.Cmd {
			return nil // body added in Task 15
		})
		fields = append(fields, update)

		remove := settings.NewField("action.remove", "Remove", settings.KindAction)
		settings.SetFieldDesc(remove, "delete this plugin from disk")
		settings.SetFieldActLabel(remove, func() string {
			if p.removeArmed[e.Name] {
				return "↵ confirm remove"
			}
			return "↵ remove"
		})
		settings.SetFieldAct(remove, func() tea.Cmd {
			if !p.removeArmed[e.Name] {
				p.removeArmed[e.Name] = true
				return nil
			}
			p.removeArmed[e.Name] = false
			return p.runRemove(e)
		})
		fields = append(fields, remove)

		return fields
	})
}

type removeResultMsg struct {
	Name  string
	Scope string
	Err   error
}

func (p *Panel) runRemove(e scopedEntry) tea.Cmd {
	return func() tea.Msg {
		var storeDir, lockPath string
		if e.Scope == scopeGlobal {
			storeDir = p.globalStoreDir()
			lockPath = p.globalLockPath()
		} else {
			storeDir = p.projectStoreDir()
			lockPath = p.projectLockPath()
		}
		if err := os.RemoveAll(filepath.Join(storeDir, e.Name)); err != nil {
			return removeResultMsg{Err: err}
		}
		lf, err := plugins.ReadLockfile(lockPath)
		if err != nil {
			return removeResultMsg{Err: err}
		}
		lf.Remove(e.Name)
		if err := lf.Write(lockPath); err != nil {
			return removeResultMsg{Err: err}
		}
		return removeResultMsg{Name: e.Name, Scope: e.Scope}
	}
}

func (p *Panel) buildFields() []*settings.Field {
	query := strings.TrimSpace(p.filter.Value())
	entries := p.lockEntries()

	fields := []*settings.Field{
		settings.NewField("header.installed", "Installed plugins", settings.KindHeader),
	}

	for _, e := range entries {
		e := e
		f := settings.NewField("plugin."+e.Name, e.Name, settings.KindDrill)
		settings.SetFieldDesc(f, fmt.Sprintf("%s · %s", e.Scope, e.Source))
		settings.SetFieldSummary(f, func() string { return shortCommit(e.Commit) })
		settings.SetFieldBuild(f, func() *settings.Frame { return p.detailFrame(e) })
		fields = append(fields, f)
	}

	if len(entries) == 0 {
		none := settings.NewField("none", "No plugins installed", settings.KindHeader)
		settings.SetFieldTitle(none, "(none)")
		fields = append(fields, none)
	}

	install := settings.NewField("action.install", "＋ Install plugin", settings.KindAction)
	settings.SetFieldDesc(install, "install a plugin from a git URL or local path")
	settings.SetFieldAct(install, func() tea.Cmd {
		return nil // body added in Task 15
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

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
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
	case removeResultMsg:
		if msg.Err != nil {
			p.activeList().ErrMsg = msg.Err.Error()
			return nil
		}
		// Pop detail frame and refresh the list.
		if len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		p.list.Refresh()
		return nil
	}
	return nil
}

func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 4 {
		return ""
	}
	settings.FieldListSetSize(p.list, width-3, maxHeight-4)
	body := "/ " + p.filter.View() + "\n" + p.list.View()
	footer := fmt.Sprintf("%d plugins", len(settings.FieldListRows(p.list)))
	content := body + "\n" + lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(footer)
	return content
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
