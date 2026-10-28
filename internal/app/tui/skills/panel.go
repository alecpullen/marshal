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
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
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
	removeArmed map[removeIdentity]bool
}

// removeIdentity is the confirmation key for a removal: the scope plus the
// discovered filesystem source. Skill names are frontmatter content and may
// repeat — two entries in one store can share a name, and the same name can
// exist in both scopes — so a name-only key could confirm a deletion the user
// never armed, on an entry they were not looking at.
type removeIdentity struct {
	scope  string
	source string
}

// removalIdentity derives the confirmation key from filesystem provenance,
// never from Skill.Name.
func removalIdentity(s skills.ScopedSkill) removeIdentity {
	return removeIdentity{scope: s.Scope, source: s.SourceName}
}

// skillRowID is the stable identity of one discovered skill row: the scope
// plus the store child discovery recorded, never the frontmatter name alone.
// A name can exist in both scopes and twice in one store, so a name-only ID
// let two different entries share a row. Length-prefixing each component
// makes the encoding unambiguous — no (scope, source) pair can produce
// another pair's ID — while the "skill." prefix stays what countLabel and
// the filter match on.
func skillRowID(scope, source string) string {
	return fmt.Sprintf("skill.%d:%s.%d:%s", len(scope), scope, len(source), source)
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
		removeArmed:    make(map[removeIdentity]bool),
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

// activeIndex is the session's shared runtime index, or a freshly built one
// when the panel has none. It answers "what does this session know", so it is
// not a load decision: loadableSelection compares against p.skillIndex
// directly and refuses when the session has no index, because loading from a
// just-constructed index would silently disagree with the session.
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

// loadableSelection returns the exact current-session value to load, or a
// user-facing reason why this selected on-disk entry cannot be loaded now.
//
// The index has no source provenance, so the only honest claim is that the
// selected content equals what this session holds — never that this scope is
// the runtime's origin, since a matching body from another source is the same
// value. The stores are re-read here rather than trusting the snapshot the
// detail frame was built from: the file, or the shared index, can change while
// the frame is open. A directory read error is not an empty valid index, so it
// fails closed instead of reporting "nothing installed".
func (p *Panel) loadableSelection(s skills.ScopedSkill) (skills.Skill, string) {
	if p.state == nil {
		return skills.Skill{}, "no session available"
	}
	if p.skillIndex == nil {
		return skills.Skill{}, "this session has no skill index — restart Marshal to build one"
	}

	list, err := skills.ListScopes(p.globalSkillsDir(), p.projectSkillsDir())
	if err != nil {
		return skills.Skill{}, fmt.Sprintf("cannot read the skills directories: %v", err)
	}
	var found *skills.ScopedSkill
	for i := range list {
		if list[i].Scope == s.Scope && list[i].SourceName == s.SourceName {
			found = &list[i]
			break
		}
	}
	if found == nil {
		return skills.Skill{}, fmt.Sprintf("%s is no longer in the %s store — reopen the panel", s.SourceName, s.Scope)
	}
	if s.SourceInfo != nil && found.SourceInfo != nil && !os.SameFile(s.SourceInfo, found.SourceInfo) {
		return skills.Skill{}, fmt.Sprintf("%s was replaced on disk — reopen the panel", s.SourceName)
	}
	if found.Skill != s.Skill {
		return skills.Skill{}, fmt.Sprintf("%s changed on disk — reopen the panel to pick up the new content", s.SourceName)
	}

	indexed, ok := p.skillIndex.Load(s.Skill.Name)
	if !ok {
		return skills.Skill{}, fmt.Sprintf("the current session has no %s — restart Marshal to pick it up", s.Skill.Name)
	}
	if indexed != found.Skill {
		return skills.Skill{}, fmt.Sprintf("the current session holds a different %s — another source may take precedence; restart Marshal to apply disk changes", s.Skill.Name)
	}
	if p.state.HasActiveSkill(s.Skill.Name) {
		return skills.Skill{}, fmt.Sprintf("%s is already active — its current body is kept until unloaded; unload it before loading another version", s.Skill.Name)
	}
	return found.Skill, ""
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
		// Derive the label and description from the same check the action
		// runs, so the row never promises a load the session cannot honour.
		// What is verified is content equality with the current session, not
		// which scope the runtime actually loaded from.
		if _, reason := p.loadableSelection(s); reason != "" {
			settings.SetFieldDesc(load, "unavailable — "+reason)
			settings.SetFieldActLabel(load, func() string { return "↵ load unavailable" })
		} else {
			settings.SetFieldDesc(load, "content matches the current session — inject it now")
			settings.SetFieldActLabel(load, func() string { return "↵ load now" })
		}
		settings.SetFieldAct(load, func() tea.Cmd {
			return func() tea.Msg {
				// Revalidate here, not only when the row was drawn: the
				// source file and the shared index can both change while
				// the frame is open.
				exact, reason := p.loadableSelection(s)
				if reason != "" {
					return loadResultMsg{Err: fmt.Errorf("%s", reason)}
				}
				// Load from a one-entry index built from the value just
				// checked, so a concurrent update to the shared index cannot
				// swap the body between validation and lookup. The session's
				// shared index is never mutated from here.
				idx := skills.NewIndex()
				idx.Set(exact.Name, exact)
				if err := skills.LoadSkillIntoSession(idx, p.state, exact.Name); err != nil {
					return loadResultMsg{Err: err}
				}
				return loadResultMsg{Name: exact.Name}
			}
		})
		fields = append(fields, load)

		remove := settings.NewField("action.remove", "Remove", settings.KindAction)
		settings.SetFieldDesc(remove, "delete this skill from disk")
		id := removalIdentity(s)
		settings.SetFieldActLabel(remove, func() string {
			if p.removeArmed[id] {
				return "↵ confirm remove"
			}
			return "↵ remove"
		})
		// Leaving the row (arrow keys call the row's disarm) or the detail
		// frame must drop the armed confirmation, so it can never be carried
		// into a different selection or a later visit.
		settings.SetFieldDisarm(remove, func() { delete(p.removeArmed, id) })
		settings.SetFieldAct(remove, func() tea.Cmd {
			if !p.removeArmed[id] {
				p.removeArmed[id] = true
				return nil
			}
			delete(p.removeArmed, id)
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
	// Target the same directory the panel and the runtime read. Never
	// skills.ScopeDir: its global path is hardcoded to ~/.config, so an
	// install under MARSHAL_CONFIG_DIR or XDG_CONFIG_HOME would land
	// somewhere neither the panel nor the loader ever looks.
	var targetDir string
	switch p.installScope {
	case scopeGlobal:
		targetDir = p.globalSkillsDir()
	case scopeProject:
		if !p.projectTrusted {
			p.installErr = "project scope needs a trusted folder"
			p.ActiveList().ErrMsg = p.installErr
			return nil
		}
		targetDir = p.projectSkillsDir()
	default:
		p.installErr = fmt.Sprintf("unknown skill scope %q", p.installScope)
		p.ActiveList().ErrMsg = p.installErr
		return nil
	}
	p.installing = true
	p.installErr = ""
	source := p.installSource
	scope := p.installScope
	return func() tea.Msg {
		name, err := skills.Install(context.Background(), source, targetDir, "")
		return installResultMsg{Name: name, Scope: scope, Err: err}
	}
}

// runRemove deletes the discovered source of s from its own store. The root is
// chosen from the scope alone, and the removal itself is confined to that
// store by skills.RemoveScopedSkill — the target is never rebuilt from
// s.Skill.Name, which is untrusted frontmatter content.
func (p *Panel) runRemove(s skills.ScopedSkill) tea.Cmd {
	return func() tea.Msg {
		var root string
		switch s.Scope {
		case scopeGlobal:
			root = p.globalSkillsDir()
		case scopeProject:
			root = p.projectSkillsDir()
		default:
			return removeResultMsg{
				Name:  s.Skill.Name,
				Scope: s.Scope,
				Err:   fmt.Errorf("cannot remove skill: unknown scope %q", s.Scope),
			}
		}
		return removeResultMsg{
			Name:  s.Skill.Name,
			Scope: s.Scope,
			Err:   skills.RemoveScopedSkill(root, s),
		}
	}
}

func (p *Panel) buildFields() []*settings.Field {
	query := strings.TrimSpace(p.filter.Value())
	scoped, _ := skills.ListScopes(p.globalSkillsDir(), p.projectSkillsDir())

	fields := []*settings.Field{
		settings.NewField("header.installed", "Installed skills", settings.KindHeader),
		// Disk changes never reach the shared runtime index: it is built at
		// startup and also carries verified plugin skills. Say so up front,
		// not only after an action, and admit that precedence can still pick
		// another value for a shadowed name.
		settings.NewField("note.restart", "disk changes apply after a restart — the index loads at startup", settings.KindHeader),
		settings.NewField("note.precedence", "project and plugin skills can shadow a global entry", settings.KindHeader),
	}

	// The gate is a session-scope control: flipping it here never edits the
	// config, it only overrides this session (and re-enabling clears the
	// sticky decisions with it). Guarded on state so nil-state tests and
	// stateless panels still render.
	if p.state != nil {
		gate := settings.NewField("action.gate", "Skill load gate", settings.KindAction)
		if p.state.SkillGateEnabled() {
			if p.state.Config.Skills.LoadGateThresholdTokens <= 0 {
				// Threshold 0 disables the gate in the runner even while
				// the session switch is on; say so instead of "≤ 0 tokens".
				settings.SetFieldDesc(gate, "on (inert — load_gate_threshold_tokens is 0, which disables the gate) · enter to turn off")
			} else {
				settings.SetFieldDesc(gate, fmt.Sprintf("on — prompts when the context window is ≤ %d tokens · enter to turn off", p.state.Config.Skills.LoadGateThresholdTokens))
			}
		} else {
			settings.SetFieldDesc(gate, "off — skill.load runs without prompting · enter to turn on (clears decisions)")
		}
		settings.SetFieldAct(gate, func() tea.Cmd {
			p.state.SkillGateSetEnabled(!p.state.SkillGateEnabled())
			p.status = "skill load gate " + map[bool]string{true: "on", false: "off"}[p.state.SkillGateEnabled()]
			p.list.Refresh()
			return nil
		})
		fields = append(fields, gate)
	}

	for _, s := range scoped {
		s := s
		name := s.Skill.Name
		// Identify the row by discovered provenance, not the frontmatter
		// name: two entries can share a name, and an ID built from the name
		// made them one row.
		f := settings.NewField(skillRowID(s.Scope, s.SourceName), name, settings.KindDrill)
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

	// One row per sticky session-scope decision, so a deny that has gone
	// stale (or an allow that outlived its welcome) can be flipped without
	// restarting the session. Clearing never edits the config either.
	if p.state != nil {
		for _, d := range p.state.SkillGateDecisions() {
			d := d
			title := "✓ " + d.Skill + " (allowed)"
			if !d.Allowed {
				title = fmt.Sprintf("✕ %s (denied ×%d)", d.Skill, d.Count)
			}
			dec := settings.NewField("gate."+d.Skill, title, settings.KindAction)
			settings.SetFieldDesc(dec, "enter to clear — the next load re-prompts")
			settings.SetFieldAct(dec, func() tea.Cmd {
				p.state.SkillGateClearDecision(d.Skill)
				p.status = "cleared " + d.Skill + " — next load re-prompts"
				p.list.Refresh()
				return nil
			})
			fields = append(fields, dec)
		}
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
		// Say what was actually verified: the loaded content equals this
		// session's value for the name. The index carries no provenance, so
		// this is not a claim about which scope the runtime loaded from.
		p.status = fmt.Sprintf("loaded %s — content matched the current session", msg.Name)
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
		p.status = fmt.Sprintf("installed %s (%s) — restart Marshal to apply it to the runtime", msg.Name, msg.Scope)
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
		p.status = fmt.Sprintf("removed %s (%s) — restart Marshal to drop it from the runtime", msg.Name, msg.Scope)
		// A remove drills in from the detail frame; pop back so the refreshed
		// list is what the user lands on.
		if len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		p.list.Refresh()
		return nil
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyEscape {
			// Leaving the frame — or the panel — drops any armed removal, so
			// a confirmation cannot carry over to another selection or a
			// later visit.
			p.ActiveList().DisarmCurrent()
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
	pw := layout.PanelWidth(width)
	l := p.ActiveList()
	settings.FieldListSetSize(l, pw-3, maxHeight-4)
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
	content := body + "\n" + chrome.TertiaryStyle().Render(footer)
	return chrome.PanelWithHints("Skills", "", content, pw, min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
