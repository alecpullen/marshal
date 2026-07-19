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
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/theme"
)

// BrowserPanel is the docked settings browser. It presents a filterable flat
// registry while reusing the existing field list and collection drill frames.
// Every config mutation is persisted immediately.
type BrowserPanel struct {
	reg      *Registry
	cfgPath  string
	filter   textinput.Model
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
	filter := textinput.New()
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
	for _, field := range b.reg.matchFields(query) {
		fields = append(fields, browserField(field))
	}
	fields = append(fields, b.collectionFields(query)...)
	if query != "" {
		fields = append(fields, b.resetFields(query)...)
	}
	sort.SliceStable(fields, func(i, j int) bool {
		leftDirect := browserDirectMatch(fields[i], query)
		rightDirect := browserDirectMatch(fields[j], query)
		if leftDirect != rightDirect {
			return leftDirect
		}
		return fields[i].id < fields[j].id
	})
	return fields
}

// browserField keeps the existing field behavior while rendering the
// registry's canonical dotted key in the flat browser.
func browserField(field *field) *field {
	copy := *field
	title := field.title
	copy.title = field.id
	if title != "" && title != field.id {
		if copy.desc == "" {
			copy.desc = title
		} else {
			copy.desc = title + " · " + copy.desc
		}
	}
	return &copy
}

// collectionFields exposes collection-root frames that otherwise have no
// addressable root row when empty (providers, presets, hooks, and similar).
func (b *BrowserPanel) collectionFields(query string) []*field {
	var fields []*field
	for _, spec := range sectionList() {
		root := spec.root(b.reg.st)
		if root.list.onAdd == nil && root.list.addWizard == nil {
			continue
		}
		haystack := spec.id + " " + spec.title + " collection"
		if !browserMatches(query, haystack) {
			continue
		}
		spec := spec
		fields = append(fields, &field{
			id:    "section." + spec.id,
			title: spec.id,
			desc:  spec.title + " collection",
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
		fields = append(fields, browserField(resetField(b.reg.st, spec.id, spec.title)))
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

func truncateErr(message string) string {
	runes := []rune(message)
	if len(runes) > 40 {
		return string(runes[:37]) + "…"
	}
	return message
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
			label = "✗ " + truncateErr(msg.Err.Error())
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
	fieldID := b.pickerField
	b.closePicker()
	if fieldID == wizardFieldID {
		b.drillIntoNewestProvider()
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

func (b *BrowserPanel) drillIntoNewestProvider() {
	name := b.reg.st.wizardCreatedProvider
	b.reg.st.wizardCreatedProvider = ""
	if name == "" || b.stack == nil {
		return
	}
	for b.stack.pop() {
	}
	for index, row := range b.stack.top().list.Rows() {
		if row.id != "providers."+name || row.kind != kindDrill {
			continue
		}
		b.stack.top().list.SetCursor(index)
		if frame := row.build(); frame != nil {
			b.stack.push(frame)
		}
		return
	}
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

// View renders the active flat browser, collection drill, or picker within
// the dock's dimensions.
func (b *BrowserPanel) View(width, maxHeight int) string {
	// chrome.Panel needs three rows for its borders and body. The dock owns
	// the normal six-row usability floor; direct callers may supply less.
	if maxHeight < 3 {
		return ""
	}
	if b.pickerModel != nil {
		return b.pickerModel.View(width, maxHeight)
	}

	panelWidth := min(72, max(width-2, 30))
	innerWidth := panelWidth - 2

	title := "Settings"
	var body string
	var footer string
	if b.stack != nil {
		rootTitle := b.stack.stack[0].title
		title += " › " + b.stack.breadcrumb(rootTitle)
		b.stack.SetSize(innerWidth, max(maxHeight-4, 1))
		body = b.stack.top().list.View()
		footer = fmt.Sprintf("%d settings · [Esc] back", len(b.stack.top().list.Rows()))
	} else {
		b.list.SetSize(innerWidth, max(maxHeight-6, 1))
		body = "/ " + b.filter.View() + "\n" +
			flDescStyle().Render(strings.Repeat("─", innerWidth)) + "\n" +
			b.list.View()
		footer = fmt.Sprintf("%d settings · [↵] edit · [Esc] close", len(b.list.Rows()))
	}
	content := body + "\n" + flDescStyle().Render(footer)
	panelHeight := min(lipgloss.Height(content)+2, maxHeight)
	return chrome.Panel(title, content, panelWidth, panelHeight, true, settingsTheme())
}
