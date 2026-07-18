package settings

import (
	"fmt"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
)

func settingsTheme() theme.Theme { return theme.Current() }

const (
	sidebarWidth      = 20
	sidebarBreakpoint = 70
	maxFrameWidth     = 100
	maxFrameHeight    = 32
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlaySearch
	overlayHelp
	overlayPicker
	overlayDiff
)

type Model struct {
	state          *state
	specs          []sectionSpec
	panes          []*paneStack
	cursor         int
	paneFocused    bool
	overlay        overlayKind
	search         searchState // zero value until Task 11 wires it
	pickerModel    *picker.Model
	pickerOnPick   func(string) error
	pickerFieldID  string
	diffLines      []diffLine
	pendingCancel  bool
	savedFlash     bool
	footerMsg      string // error/status text; cleared on next keypress
	saveBlocked    string // non-empty when the parent forbids saving
	workingDir     string
	projectCfgPath string
	width          int
	height         int
	sidebarHidden  bool
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	st := newState(cfg)
	specs := sectionList()
	panes := make([]*paneStack, len(specs))
	for i, sp := range specs {
		panes[i] = newPaneStack(withResetRow(st, sp.id, sp.title, sp.root(st)))
	}
	return Model{
		state:          st,
		specs:          specs,
		panes:          panes,
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}
}

func (m Model) Init() tea.Cmd { return nil }

// frameSize returns the outer settings frame dimensions.
func (m Model) frameSize() (int, int) {
	w := min(m.width-2, maxFrameWidth)
	h := min(m.height-1, maxFrameHeight)
	if w < 40 {
		w = max(m.width, 40)
	}
	if h < 10 {
		h = max(m.height, 10)
	}
	return w, h
}

// State returns the model's working state. Exposed for parent-package tests
// that need to mutate cfg before driving Update.
func (m *Model) State() *state { return m.state }

// SetWorkingConfig replaces the working copy of the config. Used by
// parent-package tests that need to dirty the diff overlay.
func (m *Model) SetWorkingConfig(cfg config.Config) { m.state.cfg = cfg }

func (m *Model) SetSize(width, height int) {
	m.width, m.height = width, height
	m.sidebarHidden = width > 0 && width < sidebarBreakpoint
	fw, fh := m.frameSize()
	pw := fw - 2 // detail panel interior width
	if !m.sidebarHidden {
		pw = fw - sidebarWidth - 2
	}
	ph := fh - 4 // borders + title/warning line + footer
	for _, p := range m.panes {
		p.SetSize(pw-2, ph)
	}
}

func (m Model) dirty() bool { return !reflect.DeepEqual(m.state.cfg, m.state.snapshot) }

func (m *Model) activePane() *paneStack    { return m.panes[m.cursor] }
func (m Model) activeSectionTitle() string { return m.specs[m.cursor].title }

func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case probe.ResultMsg:
		label := fmt.Sprintf("\u2713 ok (%d models)", len(msg.Models))
		if msg.Err != nil {
			label = "\u2717 " + truncateErr(msg.Err.Error())
		}
		m.state.applyActionResult(msg.FieldID, label)
		if msg.Err == nil && msg.Provider != "" {
			m.state.discovered[msg.Provider] = msg.Models
		}
		return *m, nil
	case actionResultMsg:
		m.state.applyActionResult(msg.FieldID, msg.Label)
		return *m, nil
	case picker.PickedMsg:
		return m.handlePickerPicked(msg.Value)
	case picker.CancelledMsg:
		m.closePicker()
		return *m, nil
	}

	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		return *m, nil
	}
	ks := k.String()
	if ks != "esc" {
		m.pendingCancel = false
	}
	m.savedFlash = false
	if m.footerMsg != "" && ks != "ctrl+s" {
		m.footerMsg = ""
	}

	// Overlays capture everything (Task 11 wires search; Task 12 help).
	if m.overlay == overlayHelp {
		if ks == "esc" || ks == "?" {
			m.overlay = overlayNone
		}
		return *m, nil
	}
	if m.overlay == overlaySearch {
		cmd := m.updateSearch(k)
		return *m, cmd
	}
	if m.overlay == overlayPicker {
		if m.pickerModel == nil {
			m.overlay = overlayNone
			return *m, nil
		}
		cmd := m.pickerModel.Update(msg)
		return *m, cmd
	}
	if m.overlay == overlayDiff {
		return m.updateDiffOverlay(k)
	}

	editing := m.activePane().top().list.Editing()

	// Global keys (never while an inline edit wants the characters).
	switch ks {
	case "ctrl+s":
		cmd := m.openDiff()
		return *m, cmd
	case "ctrl+o": // parent toggle key behaves like Esc-at-top: close request
		cmd := m.requestClose()
		return *m, cmd
	}
	if !editing {
		switch ks {
		case "/":
			m.openSearch()
			return *m, nil
		case "?":
			m.overlay = overlayHelp
			return *m, nil
		}
	}

	// Esc: up one level, always.
	if ks == "esc" {
		if editing {
			m.activePane().top().list.CancelEdit()
			return *m, nil
		}
		m.activePane().top().list.DisarmCurrent()
		if m.activePane().pop() {
			return *m, nil
		}
		if m.paneFocused && !m.sidebarHidden {
			m.paneFocused = false
			return *m, nil
		}
		cmd := m.requestClose()
		return *m, cmd
	}

	if m.sidebarHidden {
		// Narrow mode: pane always focused; h/l page sections at root.
		m.paneFocused = true
		if !editing && m.activePane().atRoot() {
			switch ks {
			case "l":
				m.cursor = (m.cursor + 1) % len(m.specs)
				return *m, nil
			case "h":
				m.cursor = (m.cursor - 1 + len(m.specs)) % len(m.specs)
				return *m, nil
			}
		}
		cmd := m.activePane().Update(msg)
		if req := m.activePane().top().list.TakePushPicker(); req != nil {
			m.openPicker(req)
		}
		return *m, cmd
	}

	if !m.paneFocused {
		switch ks {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.specs)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.specs) - 1
		case "enter", "l", "right", "tab":
			m.paneFocused = true
		}
		return *m, nil
	}

	// Pane focused: h / shift+tab return to the sidebar unless the cursor
	// row is an enum (which consumes ←/→ but not h) or an edit is open.
	if !editing {
		switch ks {
		case "h", "shift+tab":
			m.paneFocused = false
			return *m, nil
		}
	}
	cmd := m.activePane().Update(msg)
	if req := m.activePane().top().list.TakePushPicker(); req != nil {
		m.openPicker(req)
	}
	return *m, cmd
}

// requestClose is the top-level Esc: confirm when dirty, else cancel out.
func (m *Model) requestClose() tea.Cmd {
	if m.dirty() && !m.pendingCancel {
		m.pendingCancel = true
		return nil
	}
	m.pendingCancel = false
	return func() tea.Msg { return CancelledMsg{} }
}

// SetSaveBlocked sets (or clears) the reason settings cannot be saved.
// When non-empty, saveCmd returns nil and sets footerMsg instead of
// writing or reloading config.
func (m *Model) SetSaveBlocked(reason string) {
	m.saveBlocked = reason
}

// saveView returns the rendered save control for the diff overlay footer.
// When save is blocked the control is dimmed with the reason appended inline.
func (m *Model) saveView() string {
	if m.saveBlocked != "" {
		return mutedStyle().Render("Save (blocked: " + m.saveBlocked + ")")
	}
	return keyStyle().Render("Ctrl+S") + " save"
}

func (m *Model) saveCmd() tea.Cmd {
	if m.saveBlocked != "" {
		m.footerMsg = m.saveBlocked
		return nil
	}
	if err := config.SaveProjectConfig(m.projectCfgPath, m.state.cfg); err != nil {
		m.footerMsg = fmt.Sprintf("Save failed: %v", err)
		return nil
	}
	loaded, err := config.Load(config.LoadOptions{WorkingDir: m.workingDir})
	if err != nil {
		m.footerMsg = fmt.Sprintf("Reload failed: %v", err)
		return nil
	}
	m.pendingCancel = false
	m.savedFlash = true
	return func() tea.Msg { return SavedMsg{Cfg: loaded} }
}

func (m *Model) openPicker(req *pickerRequest) {
	p := picker.New(req.title, req.footer, req.items)
	p.SetAllowCustom(req.allowCustom)
	m.pickerModel = p
	m.pickerOnPick = req.onPick
	m.pickerFieldID = req.fieldID
	m.overlay = overlayPicker
}

func (m *Model) closePicker() {
	m.overlay = overlayNone
	m.pickerModel = nil
	m.pickerOnPick = nil
	m.pickerFieldID = ""
}

func (m *Model) openDiff() tea.Cmd {
	m.diffLines = configDiff(m.state.snapshot, m.state.cfg)
	m.overlay = overlayDiff
	return nil
}

func (m *Model) updateDiffOverlay(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.overlay = overlayNone
		return *m, nil
	case "enter":
		if len(m.diffLines) == 0 {
			return *m, nil
		}
		m.overlay = overlayNone
		cmd := m.saveCmd()
		return *m, cmd
	}
	return *m, nil
}

func (m *Model) handlePickerPicked(value string) (Model, tea.Cmd) {
	if m.pickerOnPick != nil {
		if err := m.pickerOnPick(value); err != nil {
			m.footerMsg = err.Error()
			m.closePicker()
			return *m, nil
		}
	}
	if m.pickerFieldID == wizardFieldID {
		m.drillIntoNewestProvider()
	}
	m.closePicker()
	return *m, nil
}

func (m *Model) drillIntoNewestProvider() {
	name := m.state.wizardCreatedProvider
	m.state.wizardCreatedProvider = "" // consumed
	if name == "" {
		return
	}
	pane := m.activePane()
	for pane.pop() {
	}
	rows := pane.top().list.Rows()
	for i, row := range rows {
		if row != nil && row.id == "providers."+name && row.kind == kindDrill {
			pane.top().list.SetCursor(i)
			_ = pane.top().list.openRow(row)
			if f := pane.top().list.TakePushRequest(); f != nil {
				pane.push(f)
			}
			return
		}
	}
}

func truncateErr(s string) string {
	runes := []rune(s)
	if len(runes) > 40 {
		return string(runes[:37]) + "\u2026"
	}
	return s
}

func sidebarItemStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(settingsTheme().FGDefault)
}
func sidebarActiveStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Background(settingsTheme().BGSelection)
}
func warnStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(settingsTheme().StatusWarning) }
func successStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(settingsTheme().StatusSuccess)
}
func errStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(settingsTheme().StatusError) }
func footerKeyStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(settingsTheme().AccentPrimary)
}
func footerTextStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(settingsTheme().FGMuted) }
func mutedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Faint(true).Foreground(settingsTheme().FGMuted)
}
func keyStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(settingsTheme().AccentPrimary) }

func (m Model) View() string {
	fw, fh := m.frameSize()
	body := m.renderBody(fw, fh-1)
	footer := m.renderFooter(fw)
	out := body + "\n" + footer
	if m.overlay == overlayHelp {
		return m.helpOverlay(fw, fh)
	}
	if m.overlay == overlaySearch {
		return m.searchOverlay(fw, fh)
	}
	if m.overlay == overlayPicker && m.pickerModel != nil {
		return m.pickerModel.View(fw, fh)
	}
	if m.overlay == overlayDiff {
		return m.diffOverlay(fw, fh)
	}
	return out
}

func (m Model) renderBody(fw, fh int) string {
	pane := m.activePane()
	title := pane.breadcrumb(m.activeSectionTitle())
	if m.sidebarHidden {
		title = "‹ " + title + " ›"
	}
	var content strings.Builder
	if warns := warningsFor(m.specs[m.cursor].id, m.state.cfg); len(warns) > 0 {
		content.WriteString(warnStyle().Render("⚠ "+strings.Join(warns, " · ")) + "\n")
	}
	content.WriteString(pane.top().list.View())

	if m.sidebarHidden {
		return renderPanel(title, content.String(), fw, fh, true)
	}

	sidebarTitle := "Settings"
	if m.dirty() {
		sidebarTitle = "Settings " + warnStyle().Render("●")
	}
	var sb strings.Builder
	for i, sp := range m.specs {
		label := "  " + sp.title
		if i == m.cursor {
			label = sidebarActiveStyle().Render("▸ " + sp.title)
		} else {
			label = sidebarItemStyle().Render(label)
		}
		sb.WriteString(label + "\n")
	}
	sidebar := renderPanel(sidebarTitle, strings.TrimRight(sb.String(), "\n"),
		sidebarWidth, fh, !m.paneFocused)
	detail := renderPanel(title, content.String(), fw-sidebarWidth, fh, m.paneFocused)
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, detail)
}

// renderFooter shows only what is actionable right now.
func (m Model) renderFooter(fw int) string {
	seg := func(k, label string) string {
		return footerKeyStyle().Render("["+k+"]") + footerTextStyle().Render(label)
	}
	var parts []string
	switch {
	case m.pendingCancel:
		return ansi.Cut(warnStyle().Render("⚠ unsaved changes — Esc again to discard, Ctrl+S to save"), 0, max(fw, 1))
	case m.footerMsg != "":
		return ansi.Cut(errStyle().Render(m.footerMsg), 0, max(fw, 1))
	case m.savedFlash:
		return ansi.Cut(successStyle().Render("✓ saved"), 0, max(fw, 1))
	}
	fl := m.activePane().top().list
	switch {
	case fl.adding || fl.editing:
		parts = []string{seg("↵", "apply"), seg("Esc", "cancel")}
	case fl.picking:
		parts = []string{seg("j/k", "choose"), seg("↵", "apply"), seg("Esc", "cancel")}
	case !m.paneFocused && !m.sidebarHidden:
		parts = []string{seg("j/k", "move"), seg("↵", "open"), seg("/", "search"), seg("^S", "save"), seg("Esc", "close"), seg("?", "help")}
	default:
		parts = []string{seg("j/k", "move")}
		if row := fl.CursorRow(); row != nil {
			switch row.kind {
			case kindToggle:
				parts = append(parts, seg("Space", "toggle"))
			case kindScalar:
				if row.setStr != nil {
					parts = append(parts, seg("↵", "edit"))
				}
				if row.masked {
					parts = append(parts, seg("d", "clear"))
				}
			case kindEnum:
				parts = append(parts, seg("←/→", "cycle"), seg("↵", "pick"))
			case kindDrill:
				parts = append(parts, seg("↵", "open"))
				if row.del != nil {
					parts = append(parts, seg("d", "delete"))
				}
			case kindAction:
				parts = append(parts, seg("\u21b5", "run"))
			case kindPicker:
				parts = append(parts, seg("\u21b5", "pick"))
			}
			if row.yank != nil {
				parts = append(parts, seg("y", "yank"))
			}
			if row.paste != nil && fl.yankedData != nil {
				parts = append(parts, seg("p", "paste"))
			}
			if row.moveUp != nil || row.moveDown != nil {
				parts = append(parts, seg("shift↑↓", "move"))
			}
		}
		if fl.onAdd != nil {
			parts = append(parts, seg("a", "add"))
		}
		if m.sidebarHidden && m.activePane().atRoot() {
			parts = append(parts, seg("h/l", "section"))
		} else if !m.sidebarHidden {
			parts = append(parts, seg("h", "sidebar"))
		}
		parts = append(parts, seg("/", "search"), seg("^S", "save"), seg("?", "help"))
	}
	if m.dirty() {
		parts = append([]string{warnStyle().Render("● unsaved")}, parts...)
	}
	return ansi.Cut(" "+strings.Join(parts, " "), 0, max(fw, 1))
}

func (m Model) diffOverlay(fw, fh int) string {
	var b strings.Builder
	if len(m.diffLines) == 0 {
		b.WriteString(flDescStyle().Render("no changes"))
		b.WriteString("\n")
	} else {
		for _, d := range m.diffLines {
			line := d.Prefix + " " + d.Path + d.Detail
			switch d.Prefix {
			case "+":
				line = flOnStyle().Render(line)
			case "-":
				line = flErrStyle().Render(line)
			default:
				line = flDescStyle().Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	var footer string
	if len(m.diffLines) == 0 {
		footer = "[Esc] close"
	} else {
		footer = m.saveView() + "  [Esc] cancel"
	}
	content := strings.TrimRight(b.String(), "\n") + "\n" + footerTextStyle().Render(footer)
	h := min(fh, len(m.diffLines)+5)
	if h < 6 {
		h = 6
	}
	return renderPanel("Save changes?", content, fw, h, true)
}

func (m Model) FocusedFieldTitle() string {
	if m.paneFocused || m.sidebarHidden {
		if row := m.activePane().top().list.CursorRow(); row != nil {
			return row.title
		}
	}
	return m.activeSectionTitle()
}

func (m Model) Footer() string { return m.footerMsg }

// BoolValue returns the current value of a named boolean settings field,
// read straight from the working copy. Convenience for tests and the parent
// status line.
func (m Model) BoolValue(title string) bool {
	switch title {
	case "Local only":
		if p, ok := m.state.cfg.Models.Presets[activePresetNameFor(m.state.cfg)]; ok {
			return p.LocalOnly
		}
		return false
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

// activePresetNameFor resolves the implementer preset of the default profile
// (same rule as config.activePresetName, duplicated here because that helper
// is package-private to config).
func activePresetNameFor(cfg config.Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	return profile.Roles[routing.RoleImplementer]
}
