package settings

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
)

// BrowserOption customizes a BrowserPanel.
type BrowserOption func(*BrowserPanel)

// WithProvenance attaches a provenance lookup for row TOML paths. It is
// rendered under the cursor row's description in the detail pane (wide) or
// appended to the inline desc (narrow).
func WithProvenance(fn func(tomlPath string) string) BrowserOption {
	return func(b *BrowserPanel) { b.provenance = fn }
}

// WithUserConfigPath overrides the user config path used for global-target
// writes. When not set, the browser derives it from os.UserHomeDir.
func WithUserConfigPath(path string) BrowserOption {
	return func(b *BrowserPanel) { b.userConfigPath = path }
}

// WithDataDir sets the data directory used to resolve context and output
// limits for probed models. Without it, discovered models carry zero limits
// and presets created from them save context_window = 0.
func WithDataDir(dir string) BrowserOption {
	return func(b *BrowserPanel) { b.reg.st.dataDir = dir }
}

// BrowserPanel is the docked settings browser. It presents a filterable flat
// registry while reusing the existing field list and collection drill frames.
// Every config mutation is persisted immediately.
type BrowserPanel struct {
	reg      *Registry
	cfgPath  string
	filter   textfield.Model
	list     *fieldList
	stack    *paneStack
	baseline config.Config

	pickerModel  *picker.Model
	pickerOnPick func(string) error
	pickerField  string

	pendingKey  string
	saveBlocked string

	// savePending mirrors Model's configSavePending: set when
	// config.SaveProjectConfig fails, cleared on the next successful save.
	// While true, an explicit commit gesture that reproduces the same
	// (already in-memory, still-unsaved) value — which diffs empty against
	// baseline — still retries persistence instead of silently no-op'ing.
	savePending bool

	// provenance looks up a one-line provenance summary for a TOML path.
	provenance func(tomlPath string) string

	// userConfigPath is the path to the user-global config file. Global-target
	// rows write here instead of the project config. When empty, global-target
	// writes fall back to the project config path.
	userConfigPath string

	// flipTarget inverts the write target for the next commit gesture. Set by
	// shift+enter and cleared after each flushChanges call.
	flipTarget bool
}

var _ dock.Panel = (*BrowserPanel)(nil)

func settingsTheme() theme.Theme { return theme.Current() }

// NewBrowser creates a docked browser pre-filtered by query.
func NewBrowser(cfg config.Config, cfgPath, query string, opts ...BrowserOption) *BrowserPanel {
	filter := textfield.New()
	filter.SetVirtualCursor(true)
	filter.Focus()
	filter.SetValue(query)
	filter.CursorEnd()
	if _, ok := settingsTheme().FGDefault.(lipgloss.NoColor); ok {
		styles := textinput.DefaultDarkStyles()
		styles.Focused.Prompt = lipgloss.NewStyle()
		styles.Focused.Text = lipgloss.NewStyle()
		styles.Focused.Placeholder = lipgloss.NewStyle()
		styles.Focused.Suggestion = lipgloss.NewStyle()
		styles.Blurred.Prompt = lipgloss.NewStyle()
		styles.Blurred.Text = lipgloss.NewStyle()
		styles.Blurred.Placeholder = lipgloss.NewStyle()
		styles.Blurred.Suggestion = lipgloss.NewStyle()
		styles.Cursor = textinput.CursorStyle{}
		filter.SetStyles(styles)
		filter.SetVirtualCursor(false)
	}

	browser := &BrowserPanel{
		reg:      BuildRegistry(cfg),
		cfgPath:  cfgPath,
		filter:   filter,
		baseline: cloneConfig(cfg),
	}
	// Resolve the user config path from the home directory, tolerating
	// errors by leaving it empty (global-target writes fall back to project).
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		browser.userConfigPath = config.UserConfigPath(home)
	}
	browser.list = newFieldList(browser.matchedFields)
	for _, opt := range opts {
		opt(browser)
	}
	return browser
}

// FilterValue returns the current browser filter text so callers can reopen
// the browser with the same query after an external config change.
func (b *BrowserPanel) FilterValue() string { return b.filter.Value() }

// SetSaveBlocked prevents immediate settings writes while runtime work or a
// decision is active. The parent updates this before forwarding a key.
// When the reason transitions from non-empty to empty and a save is still
// pending, it returns a command that retries flushing the pending change.
func (b *BrowserPanel) SetSaveBlocked(reason string) tea.Cmd {
	wasBlocked := b.saveBlocked != ""
	b.saveBlocked = reason
	if wasBlocked && reason == "" && b.savePending {
		return b.flushChanges(nil, true)
	}
	return nil
}

// SetSavePending seeds the browser's retry-on-repeated-commit state (see
// flushChanges) for a freshly constructed panel. It exists because the
// browser's own baseline/savePending pair only tracks failures that happen
// during its own lifetime: cfg passed to NewBrowser may already carry an
// unsaved change left behind by a previous, now-discarded BrowserPanel (or by
// /set) — applyNewConfig keeps the session's live config advanced to that
// value even on save failure so the edit isn't lost. Without this, baseline
// is cloned from that already-advanced cfg, diffs empty immediately, and
// savePending starts false, so a commit that repeats the same value silently
// no-ops instead of retrying the save — the caller must call this right
// after NewBrowser whenever it knows persistence is still pending.
func (b *BrowserPanel) SetSavePending(pending bool) { b.savePending = pending }

func (b *BrowserPanel) matchedFields() []*field {
	query := strings.TrimSpace(b.filter.Value())
	fields := make([]*field, 0)
	modified := make(map[string]bool, len(b.reg.Modified()))
	for _, id := range b.reg.Modified() {
		modified[id] = true
	}

	if query == "" {
		// Unfiltered: group by section with headers.
		sectionFields := make(map[string][]*field)
		for _, key := range b.reg.MatchKeys("") {
			f, ok := b.reg.Lookup(key)
			if !ok {
				continue
			}
			sec := b.reg.SectionOf(key)
			sectionFields[sec] = append(sectionFields[sec], browserField(f, sec, modified[key]))
		}
		for _, spec := range sectionList() {
			if spec.advanced && query == "" {
				continue // advanced sections are searchable but not listed
			}
			ff := sectionFields[spec.Title]
			if len(ff) == 0 {
				continue
			}
			fields = append(fields, &field{
				ID:    "header." + spec.ID,
				Title: spec.Title,
				Kind:  kindHeader,
			})
			fields = append(fields, ff...)
		}
		fields = append(fields, b.collectionFields("")...)
		fields = append(fields, b.resetFields("")...)
	} else {
		for _, key := range b.reg.MatchKeys(query) {
			f, ok := b.reg.Lookup(key)
			if !ok {
				continue
			}
			fields = append(fields, browserField(f, b.reg.SectionOf(key), modified[key]))
		}
		fields = append(fields, b.collectionFields(query)...)
		fields = append(fields, b.resetFields(query)...)
		sort.SliceStable(fields, func(i, j int) bool {
			leftDirect := browserDirectMatch(fields[i], query)
			rightDirect := browserDirectMatch(fields[j], query)
			if leftDirect != rightDirect {
				return leftDirect
			}
			return fields[i].ID < fields[j].ID
		})
	}
	return fields
}

// browserField renders a registry field for the flat browser: the human
// title (prefixed with its owning section) is the row label, and the
// canonical dotted key — the /set address — moves into the description.
// When modified is true, a "● " marker is prepended to the title.
// When the field has a tomlPath that differs from its id, the TOML path is
// shown as an alias in the description.
func browserField(field *field, section string, modified bool) *field {
	copy := *field
	if copy.Title == "" {
		copy.Title = field.ID
	}
	if section != "" {
		copy.Title = section + " · " + copy.Title
	}
	if modified {
		copy.Title = "● " + copy.Title
	}
	if copy.Desc == "" {
		copy.Desc = field.ID
	} else {
		copy.Desc = field.ID + " · " + copy.Desc
	}
	if field.TomlPath != "" && field.TomlPath != field.ID {
		copy.Desc += " [toml: " + field.TomlPath + "]"
	}
	return &copy
}

// collectionFields exposes collection-root frames that otherwise have no
// addressable root row when empty (providers, presets, hooks, and similar).
func (b *BrowserPanel) collectionFields(query string) []*field {
	var fields []*field
	for _, spec := range sectionList() {
		root := spec.Root(b.reg.st)
		if root.List.OnAdd == nil && root.List.OnAddMsg == nil {
			continue
		}
		haystack := spec.ID + " " + spec.Title + " collection"
		if !browserMatches(query, haystack) {
			continue
		}
		spec := spec
		fields = append(fields, &field{
			ID:    "section." + spec.ID,
			Title: spec.Title,
			Desc:  "section." + spec.ID + " · collection — ↵ to browse entries",
			Kind:  kindDrill,
			Summary: func() string {
				return fmt.Sprintf("%d entries", len(spec.Root(b.reg.st).List.Rows()))
			},
			Build: func() *Frame {
				return withResetRow(b.reg.st, spec.ID, spec.Title, spec.Root(b.reg.st))
			},
		})
	}
	return fields
}

// resetFields keep the existing two-press reset action discoverable without
// adding every reset row to the browser's normal flat registry.
func (b *BrowserPanel) resetFields(query string) []*field {
	var fields []*field
	for _, spec := range sectionList() {
		if !browserMatches(query, spec.ID+" "+spec.Title+" reset defaults") {
			continue
		}
		fields = append(fields, browserField(resetField(b.reg.st, spec.ID, spec.Title), "", false))
	}
	return fields
}

func browserMatches(query, haystack string) bool {
	if query == "" {
		return true
	}
	return len(fuzzy.Rank(query, []string{haystack})) > 0
}

func browserDirectMatch(field *field, query string) bool {
	return query == "" || strings.Contains(
		strings.ToLower(field.ID+" "+field.Title+" "+field.Desc),
		strings.ToLower(query),
	)
}

func (b *BrowserPanel) activeList() *fieldList {
	if b.stack != nil {
		return b.stack.Top().List
	}
	return b.list
}

// Update handles flat filtering, field editing, collection drills, and
// picker/action messages from the existing field machinery.
func (b *BrowserPanel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case picker.PickedMsg:
		return b.handlePickerPicked(msg.Value)
	case picker.CancelledMsg:
		b.closePicker()
		return nil
	case probe.ResultMsg:
		label := fmt.Sprintf("✓ ok (%d models)", len(msg.Models))
		if msg.Err != nil {
			label = "✗ " + strutil.Truncate(msg.Err.Error(), 37, true)
		}
		b.reg.st.applyActionResult(msg.FieldID, label)
		if msg.Err == nil && msg.Provider != "" {
			b.reg.st.discovered[msg.Provider] = msg.Models
		}
		return nil
	case actionResultMsg:
		b.reg.st.applyActionResult(msg.FieldID, msg.Label)
		return nil
	}

	// Forward paste to the active picker, the drilled pane stack, the field
	// being edited, or the flat filter input.
	if paste, ok := msg.(tea.PasteMsg); ok {
		if b.pickerModel != nil {
			return b.pickerModel.Update(paste)
		}
		list := b.activeList()
		if b.stack != nil {
			return b.flushChanges(b.stack.Update(paste), false)
		}
		if list.Editing() {
			return b.flushChanges(list.Update(paste), false)
		}
		var cmd tea.Cmd
		b.filter, cmd = b.filter.Update(paste)
		b.list.Refresh()
		b.list.SetCursor(0)
		return b.flushChanges(cmd, false)
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if b.pickerModel != nil {
		return b.pickerModel.Update(key)
	}

	// shift+enter flips the write target for the next commit gesture.
	if key.String() == "shift+enter" {
		b.flipTarget = true
		key = tea.KeyPressMsg{Code: tea.KeyEnter}
	}

	list := b.activeList()
	b.pendingKey = ""
	if row := list.CursorRow(); row != nil {
		b.pendingKey = row.ID
	}

	var cmd tea.Cmd
	committed := false
	switch {
	case b.stack != nil:
		if list.Editing() {
			cmd = b.stack.Update(key)
			committed = list.Committed()
		} else if key.Code == tea.KeyEscape {
			list.DisarmCurrent()
			if b.stack.Pop() {
				break
			}
			b.stack = nil
		} else {
			cmd = b.stack.Update(key)
			committed = list.Committed()
			b.takePicker(list)
		}
	case list.Editing():
		cmd = list.Update(key)
		committed = list.Committed()
		b.takePicker(list)
	case key.Code == tea.KeyEscape:
		return func() tea.Msg { return BrowserClosedMsg{} }
	case key.Code == tea.KeyUp || key.Code == tea.KeyDown ||
		key.Code == tea.KeyEnter || key.Code == tea.KeySpace ||
		key.Code == tea.KeyRight || key.Code == tea.KeyLeft:
		cmd = list.Update(key)
		committed = list.Committed()
		if frame := list.TakePushRequest(); frame != nil {
			b.stack = newPaneStack(frame)
		}
		b.takePicker(list)
	default:
		b.filter, cmd = b.filter.Update(key)
		b.list.Refresh()
		b.list.SetCursor(0)
	}
	// Flush queued commands (e.g. model probes queued while building a
	// picker's options) on every pass, not only after a pick. takePendingCmd
	// clearing the slot makes this idempotent with the flush inside
	// handlePickerPicked.
	if queued := b.reg.st.takePendingCmd(); queued != nil {
		return tea.Batch(b.flushChanges(cmd, committed), queued)
	}
	return b.flushChanges(cmd, committed)
}

func (b *BrowserPanel) takePicker(list *fieldList) {
	request := FieldListTakePushPicker(list)
	if request == nil {
		return
	}
	b.pickerModel = picker.New(request.Title, request.Footer, request.Items)
	b.pickerModel.SetAllowCustom(request.AllowCustom)
	b.pickerOnPick = request.OnPick
	b.pickerField = request.FieldID
}

func (b *BrowserPanel) handlePickerPicked(value string) tea.Cmd {
	b.pendingKey = b.pickerField
	if b.pickerOnPick != nil {
		if err := b.pickerOnPick(value); err != nil {
			b.activeList().ErrMsg = err.Error()
			b.closePicker()
			return nil
		}
	}
	b.closePicker()
	if b.reg.st.takeConnectRequested() {
		return func() tea.Msg { return OpenConnectMsg{} }
	}
	if probeCmd := b.reg.st.takePendingCmd(); probeCmd != nil {
		return probeCmd
	}
	// A pick is always an explicit commit gesture (even if it reproduces the
	// value already held in memory from a previously failed save).
	return b.flushChanges(nil, true)
}

func (b *BrowserPanel) closePicker() {
	b.pickerModel = nil
	b.pickerOnPick = nil
	b.pickerField = ""
}

// flushChanges persists mutations and turns the reflected config diff into
// transcript-ready receipts. The working config stays applied on save error
// or when a write is temporarily blocked, matching /set's immediate-apply
// contract.
//
// commitAttempted reports whether this call was triggered by an explicit
// commit gesture (toggle, inline edit confirm, enum cycle/pick, collection
// add/paste, or an external picker pick) rather than pure cursor navigation
// or filter typing. When the diff against baseline is empty — because a
// previous save failed and left baseline rolled forward to the unsaved
// value (see below) — only a real commitAttempted retries the save; plain
// navigation stays a cheap no-op that never touches disk.
func (b *BrowserPanel) flushChanges(inner tea.Cmd, commitAttempted bool) tea.Cmd {
	lines := configDiff(b.baseline, b.reg.Config())
	retry := len(lines) == 0 && commitAttempted && b.savePending
	if len(lines) == 0 && !retry {
		b.pendingKey = ""
		return inner
	}
	if b.saveBlocked != "" {
		b.savePending = true
		receipts := b.receipts(lines)
		// Return to the flat registry so nested frames do not hold closures
		// over collection entries that may be mutated while the save is
		// blocked. The in-memory edit itself is preserved.
		b.stack = nil
		b.list.CancelEdit()
		b.list.ErrMsg = b.saveBlocked
		b.pendingKey = ""
		changed := func() tea.Msg {
			return ChangedMsg{Receipts: receipts, Cfg: b.reg.Config(), BlockedReason: b.saveBlocked}
		}
		if inner == nil {
			return changed
		}
		return tea.Batch(inner, changed)
	}

	// Global-target branch: when the cursor row is a global-target field
	// (or flipTarget inverts it), write the single value to the user config
	// instead of the full project config.
	row := b.activeList().CursorRow()
	global := row != nil && FieldWriteGlobal(row) != b.flipTarget
	b.flipTarget = false // one-shot per commit gesture
	if global && row.TomlPath != "" && b.userConfigPath != "" {
		value, ok := config.LookupPath(b.reg.Config(), row.TomlPath)
		if !ok {
			b.savePending = true
			b.baseline = cloneConfig(b.reg.Config())
			b.pendingKey = ""
			changed := func() tea.Msg {
				return ChangedMsg{Receipts: nil, Cfg: b.baseline, SaveErr: fmt.Errorf("no config value at %s", row.TomlPath), GlobalTarget: true}
			}
			if inner == nil {
				return changed
			}
			return tea.Batch(inner, changed)
		}
		saveErr := config.SaveUserConfigValue(b.userConfigPath, row.TomlPath, value)
		var receipts []string
		if saveErr == nil {
			receipts = b.receipts(lines)
		}
		b.savePending = saveErr != nil
		b.baseline = cloneConfig(b.reg.Config())
		b.pendingKey = ""
		changed := func() tea.Msg {
			return ChangedMsg{Receipts: receipts, Cfg: b.baseline, SaveErr: saveErr, Saved: saveErr == nil, GlobalTarget: true}
		}
		if inner == nil {
			return changed
		}
		return tea.Batch(inner, changed)
	}

	saveErr := config.SaveProjectConfig(b.cfgPath, b.reg.Config())
	var receipts []string
	switch {
	case saveErr != nil:
		// No receipts on failure.
	case retry:
		// No new diff to describe — the value was already applied in
		// memory by the failed attempt this gesture is retrying.
		receipts = []string{b.pendingKey + " persisted"}
	default:
		receipts = b.receipts(lines)
	}
	b.savePending = saveErr != nil
	b.baseline = cloneConfig(b.reg.Config())
	b.pendingKey = ""
	changed := func() tea.Msg {
		return ChangedMsg{Receipts: receipts, Cfg: b.baseline, SaveErr: saveErr}
	}
	if inner == nil {
		return changed
	}
	return tea.Batch(inner, changed)
}

func (b *BrowserPanel) receipts(lines []diffLine) []string {
	if len(lines) == 1 && b.pendingKey != "" {
		return []string{b.pendingKey + lines[0].Detail}
	}
	receipts := make([]string, len(lines))
	for index, line := range lines {
		receipts[index] = line.String()
	}
	return receipts
}

// rowHints returns a contextual hint line for the given field list based on
// the cursor row's kind and whether the panel is at root or inside a section.
// The panel is passed so the hint can account for flipTarget (shift+enter)
// which inverts the write target for the next commit gesture.
func rowHints(b *BrowserPanel, atRoot bool) string {
	list := b.activeList()
	back := "Esc back"
	if atRoot {
		back = "Esc close"
	}
	if list.Editing() {
		return "↵ save · Esc cancel"
	}
	row := list.CursorRow()
	if row == nil {
		return back
	}
	var h string
	switch row.Kind {
	case kindToggle:
		h = "Space toggle"
	case kindScalar:
		if row.SetStr == nil {
			h = "read-only"
		} else {
			h = "↵ edit"
		}
	case kindEnum:
		h = "←→ cycle · ↵ pick"
	case kindDrill:
		h = "↵ open"
	case kindAction:
		h = "↵ run"
	case kindPicker:
		h = "↵ pick"
	case kindHeader:
		h = "↓ select"
	}
	if row.TomlPath != "" {
		effectiveGlobal := FieldWriteGlobal(row) != b.flipTarget
		target := "project config"
		if effectiveGlobal {
			target = "user config"
		}
		h += " · → " + target
	}
	return h + " · " + back
}

// Sizing declares the full-frame budget: the transcript hides while this
// panel is open.
func (b *BrowserPanel) Sizing() dock.Sizing { return dock.FullFrame }

// View renders the active flat browser, collection drill, or picker within
// the dock's dimensions. Past layout.WideBreakpoint the list splits into a
// list pane and a right-hand detail pane holding the cursor row's desc.
func (b *BrowserPanel) View(width, maxHeight int) string {
	// The panel needs a header row plus one content row.
	if maxHeight < 2 {
		return ""
	}
	if b.pickerModel != nil {
		return b.pickerModel.View(width, maxHeight)
	}

	panelWidth := layout.PanelWidth(width)
	innerWidth := panelWidth - 3

	twoCol := layout.TwoColumn(innerWidth)
	listWidth := innerWidth
	if twoCol {
		listWidth, _ = layout.SplitPanes(innerWidth)
	}

	// Suppress the inline desc line before rendering; in two-column mode the
	// desc renders in the detail pane instead.
	FieldListSetDescSuppressed(b.activeList(), twoCol)

	title := "Settings"
	var body string
	var footer string
	if b.stack != nil {
		rootTitle := b.stack.Stack[0].Title
		title += " › " + b.stack.Breadcrumb(rootTitle)
		b.stack.SetSize(listWidth, max(maxHeight-3, 1))
		listView := b.stack.Top().List.View()
		if twoCol {
			_, detailWidth := layout.SplitPanes(innerWidth)
			desc := ""
			if row := b.activeList().CursorRow(); row != nil {
				desc = row.Desc
				if b.provenance != nil && row.TomlPath != "" {
					if prov := b.provenance(row.TomlPath); prov != "" {
						if desc != "" {
							desc += "\n"
						}
						desc += prov
					}
				}
			}
			detail := lipgloss.NewStyle().Width(detailWidth).Foreground(settingsTheme().FGMuted).Render(desc)
			body = lipgloss.JoinHorizontal(lipgloss.Top, listView, "  ", detail)
		} else {
			body = listView
		}
		footer = fmt.Sprintf("%d settings", len(b.stack.Top().List.Rows()))
	} else {
		b.list.SetSize(listWidth, max(maxHeight-4, 1))
		listView := b.list.View()
		if twoCol {
			_, detailWidth := layout.SplitPanes(innerWidth)
			desc := ""
			if row := b.activeList().CursorRow(); row != nil {
				desc = row.Desc
				if b.provenance != nil && row.TomlPath != "" {
					if prov := b.provenance(row.TomlPath); prov != "" {
						if desc != "" {
							desc += "\n"
						}
						desc += prov
					}
				}
			}
			detail := lipgloss.NewStyle().Width(detailWidth).Foreground(settingsTheme().FGMuted).Render(desc)
			body = "/ " + b.filter.View() + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, listView, "  ", detail)
		} else {
			body = "/ " + b.filter.View() + "\n" + listView
		}
		footer = fmt.Sprintf("%d settings", len(b.list.Rows()))
	}
	hints := rowHints(b, b.stack == nil)
	content := body + "\n" + flDescStyle().Render(footer)
	panelHeight := min(lipgloss.Height(content)+1, maxHeight)
	return chrome.PanelWithHints(title, hints, content, panelWidth, panelHeight, true, settingsTheme())
}
