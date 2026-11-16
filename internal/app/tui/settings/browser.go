package settings

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/presetflow"
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

// WithUserConfigPath sets the user config path used for global-target writes.
// It is the only source of that path: a browser without it writes no user
// config at all.
func WithUserConfigPath(path string) BrowserOption {
	return func(b *BrowserPanel) { b.userConfigPath = path }
}

// WithContext sets the parent context of every bounded call this panel makes.
// The model owns it and cancels it at shutdown, so a write in flight on a slow
// daemon returns instead of outliving the program. A nil context falls back to
// context.Background().
func WithContext(ctx context.Context) BrowserOption {
	return func(b *BrowserPanel) { b.ctx = ctx }
}

// WithSession sets the session the browser's project-scope saves go through,
// and the one the Studio section reads and writes its access through. Without
// one, a project save saves nothing and counts as succeeding, and the Studio
// section says it has no session to ask.
//
// The Studio read is not started here: a browser opens with the user's own
// section, and reading a connection nobody looked at would make every settings
// screen wait on a client call. The section's own rows offer the read, and a
// write refuses until it has answered, because a write must name the version it
// was edited from.
func WithSession(sess acpclient.Session) BrowserOption {
	return func(b *BrowserPanel) {
		b.sess = sess
		b.reg.st.sess = sess
	}
}

// WithHost sets the client host every probe this browser launches asks. Without
// it those probes fail: the browser holds no provider client of its own.
func WithHost(host acpclient.Host) BrowserOption {
	return func(b *BrowserPanel) { b.reg.st.host = host }
}

// probeCmd launches a model-list probe through the client host this state
// holds. It is the single probe entry point for settings frames and for the
// browser's own discovery paths: routing every call through the state means a
// rebuilt or cloned frame can never drift back to a provider client the
// browser built itself.
func (s *state) probeCmd(fieldID, name string, pc config.ProviderConfig) tea.Cmd {
	return probe.Provider(s.host, fieldID, name, pc)
}

// BrowserPanel is the docked settings browser. It presents a filterable flat
// registry while reusing the existing field list and collection drill frames.
// Every config mutation is persisted immediately.
type BrowserPanel struct {
	reg      *Registry
	filter   textfield.Model
	list     *fieldList
	stack    *paneStack
	baseline config.Config

	pickerModel  *picker.Model
	pickerOnPick func(string) error
	pickerField  string

	confirm        *presetflow.ConfirmState
	confirmTarget  *pendingMaterialization
	detectingCaps  bool
	capProbeID     uint64
	cancelCapProbe context.CancelFunc

	// ctx is the parent of every bounded call this panel makes. The model owns
	// it and cancels it at shutdown; nil reads as context.Background().
	ctx context.Context
	// saving reports that a write is in flight (AUD-10), and saveSeq numbers
	// commits so the result of a superseded commit is dropped rather than
	// applied over the newer one's.
	saving  bool
	saveSeq uint64

	pendingKey  string
	saveBlocked string

	// savePending mirrors Model's configSavePending: set when the session's
	// project save fails, cleared on the next successful save.
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

	// sess is the session the project-scope save goes through. Nil saves
	// nothing: the project save counts as succeeding, as an empty path did.
	sess acpclient.Session

	// flipTarget inverts the write target for the next commit gesture. Set by
	// shift+enter and cleared after each flushChanges call.
	flipTarget bool
}

var _ dock.Panel = (*BrowserPanel)(nil)

func settingsTheme() theme.Theme { return theme.Current() }

// NewBrowser creates a docked browser pre-filtered by query.
func NewBrowser(cfg config.Config, query string, opts ...BrowserOption) *BrowserPanel {
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
		filter:   filter,
		baseline: cloneConfig(cfg),
	}
	browser.list = newFieldList(browser.matchedFields)
	for _, opt := range opts {
		opt(browser)
	}
	return browser
}

// HostForTest exposes the client host this browser launches its probes with,
// so the parent package's wiring test can verify that a docked browser kept
// the model's host. It is test-only API: production code reads the host
// through the state.
func (b *BrowserPanel) HostForTest() acpclient.Host { return b.reg.st.host }

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

// OwnsMsg claims this browser's own async results so the dock host delivers
// them: the Studio access read and its writes carry an unexported type, which
// the model cannot name and would otherwise drop on the floor.
func (b *BrowserPanel) OwnsMsg(msg tea.Msg) bool {
	_, ok := msg.(marshalAccessMsg)
	return ok
}

// handleMarshalAccess applies one answer from the Studio surface: the cached
// view becomes the answer's, the row that acted shows its receipt, and the list
// is refreshed so every row renders the values now stored. A refused stale
// write is not an error — its answer carries the value that won, and the row
// says so rather than re-reading blind and discarding the user's edit.
func (b *BrowserPanel) handleMarshalAccess(msg marshalAccessMsg) tea.Cmd {
	st := b.reg.st
	if msg.err != nil {
		if msg.fieldID == "" {
			// A refused read is the connection's own state: the section
			// reports it and refuses writes until the next read answers.
			st.accessErr = msg.err.Error()
			return nil
		}
		// A refused write says nothing about the connection. The cached view
		// and its versions stay as they were, so the section keeps editing
		// instead of being locked until someone re-reads by hand.
		st.applyActionResult(msg.fieldID, "✗ "+strutil.Truncate(msg.err.Error(), 37, true))
		return nil
	}
	st.accessErr = ""
	st.accessLoaded = true
	st.access = msg.view
	if list := b.activeList(); list != nil {
		list.Refresh()
	}
	switch {
	case msg.fieldID == "":
		// A plain read: the rows already show the values it returned.
	case msg.view.Stale:
		st.applyActionResult(msg.fieldID, "✗ changed elsewhere; the stored value is shown")
	default:
		st.applyActionResult(msg.fieldID, "✓ saved")
	}
	return nil
}

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
		if root.List.OnAdd == nil && root.List.OnAddMsg == nil && len(root.List.Rows()) == 0 {
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

// browserDirectMatch reports whether a field's ID/title/desc literally
// contains the query (case-insensitive). Caller (the filtered sort in
// FilterFields) only invokes this with a non-empty query, so the empty
// query short-circuit is omitted here.
func browserDirectMatch(field *field, query string) bool {
	return strings.Contains(
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
	case canProbeMsg:
		return b.handleCanProbeAnswered(msg)
	case SaveDoneMsg:
		return b.handleSaveDone(msg)
	case presetflow.CapabilityProbedMsg:
		return b.handleCapabilityProbed(msg)
	case actionResultMsg:
		b.reg.st.applyActionResult(msg.FieldID, msg.Label)
		return nil
	case marshalAccessMsg:
		return b.handleMarshalAccess(msg)
	}

	// Forward paste to the active picker, the drilled pane stack, the field
	// being edited, or the flat filter input.
	if paste, ok := msg.(tea.PasteMsg); ok {
		if b.confirm != nil {
			if b.detectingCaps || !b.confirm.Editing() {
				return nil
			}
			return b.confirm.UpdateInput(paste)
		}
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
	if b.confirm != nil {
		return b.handleConfirmKey(key)
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
	if pending := b.reg.st.takePendingMaterialization(); pending != nil {
		return b.startConfirm(*pending)
	}
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

// canProbeMsg is the answer to startConfirm's CanProbeModel check. It carries
// the provider config the check was made against, so the probe that follows
// uses the entry the picker saw rather than whatever the config holds when the
// answer lands.
type canProbeMsg struct {
	RequestID   int
	Provider    string
	Model       string
	ProviderCfg config.ProviderConfig
	CanProbe    bool
}

// startConfirm opens the shared confirm-limits screen for a just-picked
// discovered model. It asks whether the client host can tell anything about
// the model through a command rather than inline, so the Update loop never
// waits on the host while the screen opens. Only a yes answer starts the
// capability probe; that keeps the "detecting…" state from ever appearing for
// providers that can't report anything beyond their bulk model list.
func (b *BrowserPanel) startConfirm(pending pendingMaterialization) tea.Cmd {
	b.cancelCapabilityProbe()
	pc := b.reg.st.cfg.Providers[pending.ProviderName]
	lim := presetflow.Resolve(b.reg.st.discovered[pending.ProviderName], pending.ModelID, probe.IsLocalhost(pc.BaseURL))
	if preset, ok := findPresetFor(b.reg.st, pending.ProviderName, pending.ModelID); ok {
		lim = lim.WithPreset(preset.ContextWindow, preset.MaxOutputTokens)
	}
	b.confirm = presetflow.NewConfirmState(lim)
	b.confirmTarget = &pending

	// Capability probing only needs the client host. Limit-table discovery
	// already happened during model-list discovery and must not block the TUI
	// while this confirmation screen is opening.
	host := b.reg.st.host
	if host == nil {
		return nil
	}
	requestID := b.capProbeID
	parent := b.callContext()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, settingsSaveTimeout)
		defer cancel()
		return canProbeMsg{
			RequestID:   int(requestID),
			Provider:    pending.ProviderName,
			Model:       pending.ModelID,
			ProviderCfg: pc,
			CanProbe:    host.CanProbeModel(ctx, pending.ProviderName, pc),
		}
	}
}

// handleCanProbeAnswered acts on startConfirm's check: a stale answer — one for
// a confirm screen that has since been abandoned or replaced — is dropped, and
// only a "yes" starts the capability probe.
func (b *BrowserPanel) handleCanProbeAnswered(msg canProbeMsg) tea.Cmd {
	if uint64(msg.RequestID) != b.capProbeID || b.confirm == nil || b.confirmTarget == nil ||
		msg.Provider != b.confirmTarget.ProviderName || msg.Model != b.confirmTarget.ModelID ||
		b.detectingCaps {
		return nil // stale answer from an abandoned confirm
	}
	if !msg.CanProbe {
		return nil
	}
	b.detectingCaps = true
	cmd, cancel := presetflow.ProbeCapabilitiesCmdWithCancel(b.reg.st.host, msg.Provider,
		msg.ProviderCfg, msg.Model, b.capProbeID)
	b.cancelCapProbe = cancel
	return cmd
}

func (b *BrowserPanel) handleCapabilityProbed(msg presetflow.CapabilityProbedMsg) tea.Cmd {
	if !b.detectingCaps || msg.RequestID != b.capProbeID || b.confirm == nil || b.confirmTarget == nil ||
		msg.Provider != b.confirmTarget.ProviderName || msg.Model != b.confirmTarget.ModelID {
		return nil // stale result from an abandoned confirm
	}
	b.finishCapabilityProbe()
	b.confirm.Limits = b.confirm.Limits.WithProbed(msg.Caps.ToolCalling, msg.Caps.ContextWindow)
	return nil
}

func (b *BrowserPanel) handleConfirmKey(k tea.KeyPressMsg) tea.Cmd {
	if b.detectingCaps {
		if k.String() == "esc" {
			b.cancelCapabilityProbe()
			b.confirm, b.confirmTarget = nil, nil
			b.pendingKey = ""
		}
		return nil
	}
	result, cmd := b.confirm.HandleKey(k)
	switch result {
	case presetflow.ConfirmDone:
		target := *b.confirmTarget
		pc := b.reg.st.cfg.Providers[target.ProviderName]
		name, err := presetflow.Materialize(&b.reg.st.cfg, target.ProviderName, target.ModelID, pc.BaseURL, b.confirm.Limits)
		if err != nil {
			b.confirm.Err = err.Error()
			return nil
		}
		setRoleBinding(b.reg.st, target.Profile, target.Role, name)
		b.confirm, b.confirmTarget = nil, nil
		return b.flushChanges(nil, true)
	case presetflow.ConfirmCancelled:
		b.cancelCapabilityProbe()
		b.confirm, b.confirmTarget = nil, nil
		b.pendingKey = ""
		return nil
	}
	return cmd
}

func (b *BrowserPanel) cancelCapabilityProbe() {
	if b.cancelCapProbe != nil {
		b.cancelCapProbe()
		b.cancelCapProbe = nil
	}
	b.capProbeID++
	b.detectingCaps = false
}

func (b *BrowserPanel) finishCapabilityProbe() {
	if b.cancelCapProbe != nil {
		b.cancelCapProbe()
		b.cancelCapProbe = nil
	}
	b.detectingCaps = false
}

// userPath is the user config path the global writes go to, or "" when there
// is nowhere to write: a host is what owns that file, so a browser without one
// falls through to the project save.
func (b *BrowserPanel) userPath() string {
	if b.reg.st.host == nil {
		return ""
	}
	return b.userConfigPath
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
	// A write already in flight owns the diff until it lands: plain navigation
	// and filter typing must not queue a second one behind it (AUD-10). An
	// explicit commit still goes ahead, numbered so its result supersedes the
	// older one's.
	if b.saving && !commitAttempted {
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
	if global && row.TomlPath != "" && b.userPath() != "" {
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
		// The write moves off the Update goroutine (AUD-10): the decision —
		// which path, which value, which receipts — is made here on it, and the
		// call returns its outcome as a SaveDoneMsg this panel handles.
		host, path := b.reg.st.host, row.TomlPath
		receipts := b.receipts(lines)
		cfg := cloneConfig(b.reg.Config())
		parent := b.callContext()
		seq := b.beginSave()
		b.pendingKey = ""
		write := func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, settingsSaveTimeout)
			defer cancel()
			err := host.SaveUserValue(ctx, path, value)
			return SaveDoneMsg{Seq: seq, Cfg: cfg, Receipts: receipts, Err: err, Saved: err == nil, GlobalTarget: true}
		}
		if inner == nil {
			return write
		}
		return tea.Batch(inner, write)
	}

	newCfg := cloneConfig(b.reg.Config())
	base := cloneConfig(b.baseline)
	// Providers and presets are user-global; the project save never emits
	// them, so persist changed sections to the user config first. DeepEqual
	// keeps a benign project-only save from rewriting the 0600 user file.
	// A provider rename rekeys both maps, so either change writes both. The
	// retry branch repeats the writes because the baseline was rolled
	// forward past the failed attempt, hiding the diff.
	providersChanged := !reflect.DeepEqual(base.Providers, newCfg.Providers)
	presetsChanged := !reflect.DeepEqual(base.Models.Presets, newCfg.Models.Presets)
	saveUsers := providersChanged || presetsChanged || (retry && b.userPath() != "")
	var immediateErr error
	if (providersChanged || presetsChanged) && b.userPath() == "" {
		immediateErr = fmt.Errorf("cannot save providers or presets: user config path is unavailable")
		saveUsers = false
	}
	var receipts []string
	switch {
	case retry:
		// No new diff to describe — the value was already applied in
		// memory by the failed attempt this gesture is retrying.
		receipts = []string{b.pendingKey + " persisted"}
	default:
		receipts = b.receipts(lines)
	}
	// Baseline for the user-config merge: the panel-open snapshot (base), the
	// same reference the diff above used. Renames rekey the maps — the old key
	// is in the baseline but not the write, so the merge drops it; entries
	// another marshal process persisted meanwhile are in neither and survive.
	renamed := make(map[string]string, len(b.reg.st.renamed))
	for name, to := range b.reg.st.renamed {
		renamed[name] = to
	}
	// The writes run off the Update goroutine (AUD-10), so a slow daemon
	// answering them cannot freeze the screen. The panel marks itself saving
	// here and applies the outcome in handleSaveDone.
	host, sess := b.reg.st.host, b.sess
	parent := b.callContext()
	seq := b.beginSave()
	b.pendingKey = ""
	write := func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, settingsSaveTimeout)
		defer cancel()
		err := immediateErr
		if err == nil && saveUsers {
			if err = host.SaveUserProviders(ctx, newCfg.Providers, base.Providers, renamed); err == nil {
				err = host.SaveUserPresets(ctx, newCfg.Models.Presets, base.Models.Presets)
			}
		}
		if err == nil && sess != nil {
			err = sess.SaveProjectConfig(ctx, newCfg)
		}
		return SaveDoneMsg{Seq: seq, Cfg: newCfg, Receipts: receipts, Err: err, UserCfg: saveUsers}
	}
	if inner == nil {
		return write
	}
	return tea.Batch(inner, write)
}

// settingsSaveTimeout bounds one settings write, so a daemon that never answers
// releases the panel's saving state instead of holding it forever.
const settingsSaveTimeout = 10 * time.Second

// callContext is the parent of the panel's bounded calls: the context the model
// passed, or a background one when it passed none.
func (b *BrowserPanel) callContext() context.Context {
	if b.ctx == nil {
		return context.Background()
	}
	return b.ctx
}

// beginSave numbers a commit and marks the panel as saving. The number travels
// with the result, so a superseded commit's outcome is dropped (AUD-10).
func (b *BrowserPanel) beginSave() uint64 {
	b.saveSeq++
	b.saving = true
	return b.saveSeq
}

// handleSaveDone applies a settings write that ran off the Update goroutine
// (AUD-10). A result for a superseded commit is dropped: the newer commit owns
// the saving state, the baseline and the receipts.
func (b *BrowserPanel) handleSaveDone(msg SaveDoneMsg) tea.Cmd {
	if msg.Seq != b.saveSeq {
		return nil
	}
	b.saving = false
	b.savePending = msg.Err != nil
	// The commit is spent: a replay of its result is not applied again, and the
	// next commit numbers past it.
	b.saveSeq++
	if msg.Err == nil && msg.UserCfg {
		// The names are now the stored ones, so a later rename in this browser
		// records against them afresh. A failed write keeps the map for the
		// retry.
		b.reg.st.renamed = map[string]string{}
	}
	b.baseline = cloneConfig(msg.Cfg)
	receipts := msg.Receipts
	if msg.Err != nil {
		// No receipts on failure.
		receipts = nil
	}
	changed := ChangedMsg{Receipts: receipts, Cfg: msg.Cfg, SaveErr: msg.Err, Saved: msg.Saved, GlobalTarget: msg.GlobalTarget}
	return func() tea.Msg { return changed }
}

// savingHint is what the panel shows while a write is in flight (AUD-10): the
// same waiting idiom as the capability probe's, so a slow daemon reads as a
// save in progress rather than a frozen screen.
func (b *BrowserPanel) savingHint() string {
	if !b.saving {
		return ""
	}
	return "saving…"
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
	// Deletion is a two-press gesture on rows that have one, so the hint names
	// the key, and names the confirmation once the first press armed it
	// (AUD-13). Rows without a delete keep their existing text.
	if row.Del != nil {
		if list.DeleteArmed() {
			h += " · d again to delete"
		} else {
			h += " · d delete"
		}
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
	if b.confirm != nil {
		panelWidth := layout.PanelWidth(width)
		var content, hints string
		if b.detectingCaps {
			content = flDescStyle().Render("detecting model capabilities…")
			hints = "please wait…"
		} else {
			content = b.confirm.Render(panelWidth - 3)
			hints = "[↵] confirm  [c] edit context  [o] edit max output  [Esc] cancel"
		}
		panelHeight := min(lipgloss.Height(content)+1, maxHeight)
		return chrome.PanelWithHints("Confirm model limits", hints, content, panelWidth, panelHeight, true, settingsTheme())
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
	// Group dividers only in the single-column layout: in two columns the rule
	// would erase the detail pane's text on its row.
	FieldListSetGroupDividers(b.activeList(), !twoCol)

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
	if hint := b.savingHint(); hint != "" {
		// A write is in flight: say so rather than leave a screen that looks
		// frozen while a slow daemon answers (AUD-10).
		footer = hint + " · " + footer
	}
	content := body
	panelHeight := min(lipgloss.Height(content)+2, maxHeight)
	return chrome.Frame{
		Title:   title,
		Hints:   hints,
		Footer:  footer,
		Closed:  true,
		Focused: true,
	}.Render(content, panelWidth+2, panelHeight, settingsTheme())
}
