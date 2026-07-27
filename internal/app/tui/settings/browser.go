package settings

import (
	"fmt"
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
}

var _ dock.Panel = (*BrowserPanel)(nil)

func settingsTheme() theme.Theme { return theme.Current() }

// NewBrowser creates a docked browser pre-filtered by query.
func NewBrowser(cfg config.Config, cfgPath, query string) *BrowserPanel {
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
	browser.list = newFieldList(browser.matchedFields)
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
			ff := sectionFields[spec.title]
			if len(ff) == 0 {
				continue
			}
			fields = append(fields, &field{
				id:    "header." + spec.id,
				title: spec.title,
				kind:  kindHeader,
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
			return fields[i].id < fields[j].id
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
	if copy.title == "" {
		copy.title = field.id
	}
	if section != "" {
		copy.title = section + " · " + copy.title
	}
	if modified {
		copy.title = "● " + copy.title
	}
	if copy.desc == "" {
		copy.desc = field.id
	} else {
		copy.desc = field.id + " · " + copy.desc
	}
	if field.tomlPath != "" && field.tomlPath != field.id {
		copy.desc += " [toml: " + field.tomlPath + "]"
	}
	return &copy
}

// collectionFields exposes collection-root frames that otherwise have no
// addressable root row when empty (providers, presets, hooks, and similar).
func (b *BrowserPanel) collectionFields(query string) []*field {
	var fields []*field
	for _, spec := range sectionList() {
		root := spec.root(b.reg.st)
		if root.list.onAdd == nil && root.list.onAddMsg == nil {
			continue
		}
		haystack := spec.id + " " + spec.title + " collection"
		if !browserMatches(query, haystack) {
			continue
		}
		spec := spec
		fields = append(fields, &field{
			id:    "section." + spec.id,
			title: spec.title,
			desc:  "section." + spec.id + " · collection — ↵ to browse entries",
			kind:  kindDrill,
			summary: func() string {
				return fmt.Sprintf("%d entries", len(spec.root(b.reg.st).list.Rows()))
			},
			build: func() *frame {
				return withResetRow(b.reg.st, spec.id, spec.title, spec.root(b.reg.st))
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
		if !browserMatches(query, spec.id+" "+spec.title+" reset defaults") {
			continue
		}
		fields = append(fields, browserField(resetField(b.reg.st, spec.id, spec.title), "", false))
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
		strings.ToLower(field.id+" "+field.title+" "+field.desc),
		strings.ToLower(query),
	)
}

func (b *BrowserPanel) activeList() *fieldList {
	if b.stack != nil {
		return b.stack.top().list
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

	list := b.activeList()
	b.pendingKey = ""
	if row := list.CursorRow(); row != nil {
		b.pendingKey = row.id
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
			if b.stack.pop() {
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
		key.Code == tea.KeyEnter || key.Code == tea.KeySpace:
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
	return b.flushChanges(cmd, committed)
}

func (b *BrowserPanel) takePicker(list *fieldList) {
	request := list.TakePushPicker()
	if request == nil {
		return
	}
	b.pickerModel = picker.New(request.title, request.footer, request.items)
	b.pickerModel.SetAllowCustom(request.allowCustom)
	b.pickerOnPick = request.onPick
	b.pickerField = request.fieldID
}

func (b *BrowserPanel) handlePickerPicked(value string) tea.Cmd {
	b.pendingKey = b.pickerField
	if b.pickerOnPick != nil {
		if err := b.pickerOnPick(value); err != nil {
			b.activeList().errMsg = err.Error()
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
		b.list.errMsg = b.saveBlocked
		b.pendingKey = ""
		changed := func() tea.Msg {
			return ChangedMsg{Receipts: receipts, Cfg: b.reg.Config(), BlockedReason: b.saveBlocked}
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
func rowHints(list *fieldList, atRoot bool) string {
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
	switch row.kind {
	case kindToggle:
		h = "Space toggle"
	case kindScalar:
		if row.setStr == nil {
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
	b.activeList().setDescSuppressed(twoCol)

	title := "Settings"
	var body string
	var footer string
	if b.stack != nil {
		rootTitle := b.stack.stack[0].title
		title += " › " + b.stack.breadcrumb(rootTitle)
		b.stack.SetSize(listWidth, max(maxHeight-3, 1))
		listView := b.stack.top().list.View()
		if twoCol {
			_, detailWidth := layout.SplitPanes(innerWidth)
			desc := ""
			if row := b.activeList().CursorRow(); row != nil {
				desc = row.desc
			}
			detail := lipgloss.NewStyle().Width(detailWidth).Foreground(settingsTheme().FGMuted).Render(desc)
			body = lipgloss.JoinHorizontal(lipgloss.Top, listView, "  ", detail)
		} else {
			body = listView
		}
		footer = fmt.Sprintf("%d settings", len(b.stack.top().list.Rows()))
	} else {
		b.list.SetSize(listWidth, max(maxHeight-4, 1))
		listView := b.list.View()
		if twoCol {
			_, detailWidth := layout.SplitPanes(innerWidth)
			desc := ""
			if row := b.activeList().CursorRow(); row != nil {
				desc = row.desc
			}
			detail := lipgloss.NewStyle().Width(detailWidth).Foreground(settingsTheme().FGMuted).Render(desc)
			body = "/ " + b.filter.View() + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, listView, "  ", detail)
		} else {
			body = "/ " + b.filter.View() + "\n" + listView
		}
		footer = fmt.Sprintf("%d settings", len(b.list.Rows()))
	}
	hints := rowHints(b.activeList(), b.stack == nil)
	content := body + "\n" + flDescStyle().Render(footer)
	panelHeight := min(lipgloss.Height(content)+1, maxHeight)
	return chrome.PanelWithHints(title, hints, content, panelWidth, panelHeight, true, settingsTheme())
}
