package skills

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
)

const scopeGlobal = "global"
const scopeProject = "project"

// Service is what the panel asks the session for. acpclient.Session satisfies
// it. The panel keeps no store paths, no skill index and no session state of
// its own: the agent side lists both stores, loads, installs, removes, writes
// the autoload preference and drives the skill gate.
type Service interface {
	Config() config.Config
	Status() acpwire.SessionStatus
	Skills(ctx context.Context) ([]acpwire.SkillEntry, error)
	LoadSkill(ctx context.Context, scope, source string) (string, error)
	InstallSkill(ctx context.Context, source, scope string) (string, error)
	RemoveSkill(ctx context.Context, scope, source string) error
	SetSkillAutoload(ctx context.Context, name string, on bool) error
	SetSkillGate(ctx context.Context, on bool) error
	ClearSkillGateDecision(ctx context.Context, skill string) error
}

// Panel is the TUI management surface for installed skills.
type Panel struct {
	svc            Service
	projectTrusted bool

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	// entries is the last listing the session returned. The panel reads it
	// when it opens and again after each action that changes it, never on
	// render.
	entries []acpwire.SkillEntry

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
func removalIdentity(s acpwire.SkillEntry) removeIdentity {
	return removeIdentity{scope: s.Scope, source: s.Source}
}

// skillRowID is the stable identity of one listed skill row: the scope plus
// the store child discovery recorded, never the frontmatter name alone. A name
// can exist in both scopes and twice in one store, so a name-only ID let two
// different entries share a row. Length-prefixing each component makes the
// encoding unambiguous — no (scope, source) pair can produce another pair's ID
// — while the "skill." prefix stays what countLabel and the filter match on.
func skillRowID(scope, source string) string {
	return fmt.Sprintf("skill.%d:%s.%d:%s", len(scope), scope, len(source), source)
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a skills panel over svc, the session the panel asks for its
// listing and its actions. The listing is read once here, so the first render
// shows what is installed.
func NewPanel(svc Service, projectTrusted bool) *Panel {
	p := &Panel{
		svc:            svc,
		projectTrusted: projectTrusted,
		removeArmed:    make(map[removeIdentity]bool),
		installScope:   scopeGlobal,
	}
	p.filter = textfield.New()
	p.filter.SetVirtualCursor(true)
	p.filter.Focus()
	p.list = settings.NewFieldList(p.buildFields)
	p.reload()
	return p
}

// reload replaces the listing with the session's. A failed read shows an empty
// list, as an ignored directory read did.
func (p *Panel) reload() {
	entries, err := p.svc.Skills(context.Background())
	if err != nil {
		p.entries = nil
		return
	}
	p.entries = entries
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

// isAutoloaded reports whether name is in the effective skills.autoload list.
func (p *Panel) isAutoloaded(name string) bool {
	for _, n := range p.svc.Config().Skills.Autoload {
		if n == name {
			return true
		}
	}
	return false
}

// setAutoload adds or removes name from skills.autoload and persists it.
//
// The write belongs to the session, not the panel: autoload always goes to the
// user config, never the project one, even for a project-scoped skill, because
// it is a preference about how the agent behaves rather than a property of the
// repository.
func (p *Panel) setAutoload(name string, on bool) {
	if err := p.svc.SetSkillAutoload(context.Background(), name, on); err != nil {
		p.ActiveList().ErrMsg = fmt.Sprintf("save autoload: %v", err)
		return
	}
	p.ActiveList().ErrMsg = ""
	if on {
		p.status = fmt.Sprintf("%s autoloads next session", name)
	} else {
		p.status = fmt.Sprintf("%s no longer autoloads", name)
	}
}

func (p *Panel) detailFrame(s acpwire.SkillEntry) *settings.Frame {
	return settings.NewFrame(s.Name, func() []*settings.Field {
		var fields []*settings.Field

		info := settings.NewField("detail.name", "Name", settings.KindScalar)
		settings.SetFieldGetStr(info, func() string { return s.Name })
		fields = append(fields, info)

		desc := settings.NewField("detail.desc", "Description", settings.KindScalar)
		settings.SetFieldGetStr(desc, func() string { return s.Description })
		fields = append(fields, desc)

		scope := settings.NewField("detail.scope", "Scope", settings.KindScalar)
		settings.SetFieldGetStr(scope, func() string { return s.Scope })
		fields = append(fields, scope)

		auto := settings.NewField("detail.autoload", "Autoload", settings.KindToggle)
		settings.SetFieldDesc(auto, "inject into every session at startup · saved to the global config")
		settings.SetFieldGetBool(auto, func() bool { return p.isAutoloaded(s.Name) })
		settings.SetFieldSetBool(auto, func(v bool) { p.setAutoload(s.Name, v) })
		fields = append(fields, auto)

		load := settings.NewField("action.load", "Load now", settings.KindAction)
		// The row's label and description come from the listing's own verdict,
		// so the row never promises a load the session cannot honour.
		if s.LoadBlocked != "" {
			settings.SetFieldDesc(load, "unavailable — "+s.LoadBlocked)
			settings.SetFieldActLabel(load, func() string { return "↵ load unavailable" })
		} else {
			settings.SetFieldDesc(load, "content matches the current session — inject it now")
			settings.SetFieldActLabel(load, func() string { return "↵ load now" })
		}
		settings.SetFieldAct(load, func() tea.Cmd {
			scope, source := s.Scope, s.Source
			return func() tea.Msg {
				// The session revalidates against the store and its own index
				// before it injects anything: both can change while the frame
				// is open.
				name, err := p.svc.LoadSkill(context.Background(), scope, source)
				return loadResultMsg{Name: name, Err: err}
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
	switch p.installScope {
	case scopeGlobal:
	case scopeProject:
		if !p.projectTrusted {
			p.installErr = "project scope needs a trusted folder"
			p.ActiveList().ErrMsg = p.installErr
			return nil
		}
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
		name, err := p.svc.InstallSkill(context.Background(), source, scope)
		return installResultMsg{Name: name, Scope: scope, Err: err}
	}
}

// runRemove asks the session to delete the listed entry's source from its own
// store. The session confines the removal to that store; the target is never
// rebuilt from the frontmatter name, which is untrusted content.
func (p *Panel) runRemove(s acpwire.SkillEntry) tea.Cmd {
	return func() tea.Msg {
		scope, source := s.Scope, s.Source
		err := p.svc.RemoveSkill(context.Background(), scope, source)
		return removeResultMsg{Name: s.Name, Scope: scope, Err: err}
	}
}

// setGate turns the session's skill load gate on or off.
func (p *Panel) setGate(on bool) {
	if err := p.svc.SetSkillGate(context.Background(), on); err != nil {
		p.ActiveList().ErrMsg = err.Error()
		return
	}
	p.status = "skill load gate " + map[bool]string{true: "on", false: "off"}[on]
	p.list.Refresh()
}

// clearDecision clears one sticky gate decision, so its next load re-prompts.
func (p *Panel) clearDecision(skill string) {
	if err := p.svc.ClearSkillGateDecision(context.Background(), skill); err != nil {
		p.ActiveList().ErrMsg = err.Error()
		return
	}
	p.status = "cleared " + skill + " — next load re-prompts"
	p.list.Refresh()
}

func (p *Panel) buildFields() []*settings.Field {
	query := strings.TrimSpace(p.filter.Value())
	entries := p.entries
	status := p.svc.Status()
	cfg := p.svc.Config()

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
	// sticky decisions with it).
	gate := settings.NewField("action.gate", "Skill load gate", settings.KindAction)
	if status.SkillGate.Enabled {
		if cfg.Skills.LoadGateThresholdTokens <= 0 {
			// Threshold 0 disables the gate in the runner even while the
			// session switch is on; say so instead of "≤ 0 tokens".
			settings.SetFieldDesc(gate, "on (inert — load_gate_threshold_tokens is 0, which disables the gate) · enter to turn off")
		} else {
			settings.SetFieldDesc(gate, fmt.Sprintf("on — prompts when the context window is ≤ %d tokens · enter to turn off", cfg.Skills.LoadGateThresholdTokens))
		}
	} else {
		settings.SetFieldDesc(gate, "off — skill.load runs without prompting · enter to turn on (clears decisions)")
	}
	enabled := status.SkillGate.Enabled
	settings.SetFieldAct(gate, func() tea.Cmd {
		p.setGate(!enabled)
		return nil
	})
	fields = append(fields, gate)

	for _, s := range entries {
		s := s
		// Identify the row by discovered provenance, not the frontmatter
		// name: two entries can share a name, and an ID built from the name
		// made them one row.
		f := settings.NewField(skillRowID(s.Scope, s.Source), s.Name, settings.KindDrill)
		settings.SetFieldDesc(f, fmt.Sprintf("%s · %s", s.Scope, s.Description))
		// Which skills are always-on is otherwise invisible without opening
		// the config file, so it belongs in the row summary.
		settings.SetFieldSummary(f, func() string {
			if p.isAutoloaded(s.Name) {
				return s.Scope + " · autoload"
			}
			return s.Scope
		})
		settings.SetFieldBuild(f, func() *settings.Frame { return p.detailFrame(s) })
		fields = append(fields, f)
	}

	if len(entries) == 0 {
		none := settings.NewField("none", "No skills installed", settings.KindHeader)
		settings.SetFieldTitle(none, "(none)")
		fields = append(fields, none)
	}

	// One row per sticky session-scope decision, so a deny that has gone
	// stale (or an allow that outlived its welcome) can be flipped without
	// restarting the session. Clearing never edits the config either.
	for _, d := range status.SkillGate.Decisions {
		d := d
		title := "✓ " + d.Skill + " (allowed)"
		if !d.Allowed {
			title = fmt.Sprintf("✕ %s (denied ×%d)", d.Skill, d.Count)
		}
		dec := settings.NewField("gate."+d.Skill, title, settings.KindAction)
		settings.SetFieldDesc(dec, "enter to clear — the next load re-prompts")
		settings.SetFieldAct(dec, func() tea.Cmd {
			p.clearDecision(d.Skill)
			return nil
		})
		fields = append(fields, dec)
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
		// A loaded skill is active now, so its row's load verdict changed.
		p.reload()
		p.list.Refresh()
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
		p.reload()
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
		p.reload()
		p.list.Refresh()
		return nil
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyEscape {
			// An in-progress edit owns the first escape — a scalar editor, an
			// enum picker or a collection-add prompt all count: cancel that
			// and keep the frame open, so abandoning a value is never the
			// same keystroke as leaving the panel.
			if l := p.ActiveList(); l.Editing() {
				return settings.FieldListUpdate(l, msg)
			}
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
