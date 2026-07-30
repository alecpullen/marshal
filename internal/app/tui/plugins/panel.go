package plugins

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
	"marshal/internal/plugins"
)

const scopeGlobal = "global"
const scopeProject = "project"

// Panel is the TUI management surface for installed plugins.
type Panel struct {
	homeDir        string
	workDir        string
	projectTrusted bool
	state          *session.State

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	installSource   string
	installScope    string
	installErr      string
	scannedContents *plugins.Contents
	scannedName     string
	scannedSource   string
	scanDir         string
	removeArmed     map[string]bool
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a plugins panel.
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
			p.installSource = e.Source
			p.installScope = e.Scope
			p.stack = append(p.stack, p.installFrame())
			return nil
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

type installResultMsg struct {
	Name  string
	Scope string
	Err   error
}

type scanResultMsg struct {
	Name     string
	Source   string
	Scope    string
	Contents plugins.Contents
	ScanDir  string
	Err      error
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

func (p *Panel) installFrame() *settings.Frame {
	return settings.NewFrame("Install plugin", func() []*settings.Field {
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

		scan := settings.NewField("install.scan", "Scan", settings.KindAction)
		settings.SetFieldDesc(scan, "fetch and review plugin contents")
		settings.SetFieldAct(scan, func() tea.Cmd {
			return p.runScan()
		})
		fields = append(fields, scan)

		if p.scannedContents != nil {
			// Passive content header and rows.
			fields = append(fields, settings.NewField("confirm.passive", "Passive content", settings.KindHeader))
			if len(p.scannedContents.Skills) == 0 && len(p.scannedContents.Commands) == 0 {
				fields = append(fields, settings.NewField("confirm.passive.none", "(none)", settings.KindScalar))
			}
			for _, s := range p.scannedContents.Skills {
				f := settings.NewField("confirm.skill."+s.Name, s.Name, settings.KindScalar)
				settings.SetFieldGetStr(f, func() string { return s.Description })
				fields = append(fields, f)
			}
			for _, c := range p.scannedContents.Commands {
				f := settings.NewField("confirm.cmd."+c.Name, "/"+c.Name, settings.KindScalar)
				settings.SetFieldGetStr(f, func() string { return c.Description })
				fields = append(fields, f)
			}

			// Executable content header and rows.
			fields = append(fields, settings.NewField("confirm.exec", "Executable content", settings.KindHeader))
			if len(p.scannedContents.Hooks) == 0 && len(p.scannedContents.MCPServers) == 0 && len(p.scannedContents.MCPPolicies) == 0 {
				fields = append(fields, settings.NewField("confirm.exec.none", "(none)", settings.KindScalar))
			}
			for _, h := range p.scannedContents.Hooks {
				f := settings.NewField("confirm.hook."+h.Event, h.Event, settings.KindScalar)
				settings.SetFieldGetStr(f, func() string { return h.Command })
				fields = append(fields, f)
			}
			for name, srv := range p.scannedContents.MCPServers {
				n, s := name, srv
				f := settings.NewField("confirm.mcp."+n, n, settings.KindScalar)
				settings.SetFieldGetStr(f, func() string { return s.Command + " " + strings.Join(s.Args, " ") })
				fields = append(fields, f)
			}

			confirm := settings.NewField("install.confirm", "Confirm install", settings.KindAction)
			settings.SetFieldDesc(confirm, "write plugin to disk and update lockfile")
			settings.SetFieldAct(confirm, func() tea.Cmd {
				return p.runInstallFromScan()
			})
			fields = append(fields, confirm)
		}

		return fields
	})
}

func (p *Panel) runScan() tea.Cmd {
	if p.installSource == "" {
		return nil
	}
	ctx := context.Background()
	source := p.installSource
	scope := p.installScope
	return func() tea.Msg {
		cloneURL, name, err := plugins.NormalizeSource(source)
		if err != nil {
			return scanResultMsg{Err: err}
		}
		if name == "" {
			name = "unknown"
		}
		inst, err := plugins.NewInstaller()
		if err != nil {
			return scanResultMsg{Err: err}
		}
		tmpDir, err := os.MkdirTemp("", "marshal-plugin-tui-*")
		if err != nil {
			return scanResultMsg{Err: err}
		}
		cloneDest := filepath.Join(tmpDir, "clone")
		_, err = inst.Clone(ctx, cloneURL, "", cloneDest)
		if err != nil {
			os.RemoveAll(tmpDir)
			return scanResultMsg{Err: err}
		}
		_ = os.RemoveAll(filepath.Join(cloneDest, ".git"))
		contents, err := plugins.ScanPlugin(cloneDest)
		if err != nil {
			os.RemoveAll(tmpDir)
			return scanResultMsg{Err: err}
		}
		return scanResultMsg{Name: name, Source: cloneURL, Scope: scope, Contents: contents, ScanDir: cloneDest}
	}
}

func (p *Panel) resetInstall() {
	p.installSource = ""
	p.installScope = scopeGlobal
	p.scannedContents = nil
	p.scannedName = ""
	p.scannedSource = ""
	p.scanDir = ""
}

func (p *Panel) runInstallFromScan() tea.Cmd {
	if p.scannedContents == nil || p.scanDir == "" {
		return nil
	}
	name := p.scannedName
	scope := p.installScope
	source := p.scannedSource
	cloneDest := p.scanDir
	return func() tea.Msg {
		var storeDir, lockPath string
		if scope == scopeGlobal {
			storeDir = p.globalStoreDir()
			lockPath = p.globalLockPath()
		} else {
			storeDir = p.projectStoreDir()
			lockPath = p.projectLockPath()
		}
		dest := filepath.Join(storeDir, name)
		if err := os.MkdirAll(storeDir, 0755); err != nil {
			os.RemoveAll(filepath.Dir(cloneDest))
			return installResultMsg{Err: err}
		}
		if err := os.RemoveAll(dest); err != nil {
			os.RemoveAll(filepath.Dir(cloneDest))
			return installResultMsg{Err: err}
		}
		if err := plugins.CopyDir(cloneDest, dest); err != nil {
			os.RemoveAll(filepath.Dir(cloneDest))
			return installResultMsg{Err: err}
		}
		os.RemoveAll(filepath.Dir(cloneDest))
		hash, err := plugins.HashDir(dest)
		if err != nil {
			return installResultMsg{Err: err}
		}
		commit := "unknown"
		lf, err := plugins.ReadLockfile(lockPath)
		if err != nil {
			return installResultMsg{Err: err}
		}
		lf.Upsert(plugins.LockEntry{
			Name:        name,
			Source:      source,
			Commit:      commit,
			ContentHash: hash,
		})
		if err := lf.Write(lockPath); err != nil {
			return installResultMsg{Err: err}
		}
		return installResultMsg{Name: name, Scope: scope}
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
		p.resetInstall()
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
	case scanResultMsg:
		if msg.Err != nil {
			p.installErr = msg.Err.Error()
			return nil
		}
		p.scannedContents = &msg.Contents
		p.scannedName = msg.Name
		p.scannedSource = msg.Source
		p.scanDir = msg.ScanDir
		p.activeList().Refresh()
		return nil
	case installResultMsg:
		if msg.Err != nil {
			p.installErr = msg.Err.Error()
			return nil
		}
		p.resetInstall()
		settings.FieldListRefresh(p.list)
		if p.state != nil {
			p.state.AddMessage(session.RoleSystem, "Restart Marshal to activate the plugin change.", session.ContentTypePlain)
		}
		return nil
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
		if p.state != nil {
			p.state.AddMessage(session.RoleSystem, "Restart Marshal to activate the plugin change.", session.ContentTypePlain)
		}
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
