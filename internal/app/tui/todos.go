package tui

import (
	"errors"
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/listpanel"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/commands"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
)

// todoLine renders one todo row: ✓ teal done, ▸ coral bold in-progress,
// · muted pending — matching native.TodoItem's statuses exactly.
func todoLine(t native.TodoItem, width int) string {
	var g string
	var c color.Color
	labelStyle := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
	switch t.Status {
	case native.TodoCompleted:
		g, c = glyph.OK, theme.Current().StatusSuccess
		labelStyle = lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
	case native.TodoInProgress:
		g, c = glyph.Running, theme.Current().AccentPrimary
		labelStyle = labelStyle.Bold(true)
	default:
		g, c = glyph.Ambient, theme.Current().FGMuted
	}
	return gutterPrefix(g, c) + labelStyle.Render(ansi.Truncate(viewmodel.ScrubText(t.Content), max(width-3, 1), "…"))
}

// todoProgress returns the completed count and the index of the
// in-progress item (-1 when there is none).
func todoProgress(todos []native.TodoItem) (done, inProgress int) {
	inProgress = -1
	for i, t := range todos {
		switch t.Status {
		case native.TodoCompleted:
			done++
		case native.TodoInProgress:
			if inProgress < 0 {
				inProgress = i
			}
		}
	}
	return done, inProgress
}

func todosAllDone(todos []native.TodoItem) bool {
	if len(todos) == 0 {
		return false
	}
	done, _ := todoProgress(todos)
	return done == len(todos)
}

// viewedTodos returns the todo list of the session the transcript is
// currently showing: the drilled-in subagent's list while drilling, the
// parent's otherwise. Mirrors the transcriptState pattern used for the
// active-tool row.
func (m Model) viewedTodos() []native.TodoItem {
	if len(m.viewStack) > 0 {
		if child := m.viewStack[len(m.viewStack)-1].Child; child != nil {
			return child.Todos()
		}
	}
	return m.state.Todos()
}

// tasksDoc builds the Ctrl+T Tasks panel: one row per todo, using the same
// glyphs as todoLine. docpanel supplies the `esc close` hint.
//
// Each row also carries its step count and finished working time, counted by
// viewmodel.TaskStats from the beats under that task's divider, so the panel,
// the transcript and the web Tasks tab agree.
func tasksDoc(todos []native.TodoItem, stats map[string]viewmodel.TaskStat) commands.Doc {
	done, _ := todoProgress(todos)
	rows := make([]commands.Row, 0, len(todos))
	for _, t := range todos {
		g := glyph.Ambient
		switch t.Status {
		case native.TodoCompleted:
			g = glyph.OK
		case native.TodoInProgress:
			g = glyph.Running
		}
		var detail []string
		if st := stats[t.ID]; t.ID != "" && st.Steps > 0 {
			detail = append(detail, pluralCount(st.Steps, "step", "steps"))
			if st.Work > 0 {
				detail = append(detail, compactDuration(st.Work))
			}
		}
		rows = append(rows, commands.Row{Text: g + " " + viewmodel.ScrubText(t.Content), Detail: strings.Join(detail, " · ")})
	}
	return commands.Doc{Title: fmt.Sprintf("Tasks %d/%d", done, len(todos)), Rows: rows}
}

// tasksOpen reports whether the Tasks panel is the open dock panel.
func (m Model) tasksOpen() bool {
	return m.tasksPanel != nil && m.dock.Panel() == dock.Panel(m.tasksPanel)
}

// toggleTasksPanel is Ctrl+T: close the Tasks panel if it is the open dock
// panel, otherwise open it on the viewed todo list (Plan) or the runtime's
// live work (Work). The first open defaults to Plan when there are todos,
// otherwise Work; later opens restore the tab the reader last left on. With
// neither a list nor a work projection it leaves a notice instead of opening
// a panel that would only swallow keys behind a notice.
func (m *Model) toggleTasksPanel() {
	if m.tasksOpen() {
		m.tasksPanelTab = m.tasksPanel.activeTab
		m.tasksPanelUsed = true
		m.dock.CloseNow()
		m.tasksPanel = nil
		m.refreshViewport()
		return
	}
	todos := m.viewedTodos()
	if len(todos) == 0 && !m.workLaneActive() {
		m.state.AddMessage(session.RoleSystem, "No task list in this session.", session.ContentTypePlain)
		m.refreshViewport()
		return
	}
	tab := tasksTabPlan
	switch {
	case m.tasksPanelUsed:
		tab = m.tasksPanelTab
	case len(todos) == 0:
		tab = tasksTabWork
	}
	m.sheetPanel = nil // opening replaces the session sheet if it was up
	m.tasksPanel = newTasksPanel(m, todos, m.taskStats, tab)
	m.dock.Open(m.tasksPanel)
	m.refreshViewport()
}

// workTab identifies the Tasks panel's active tab.
type workTab int

const (
	tasksTabPlan workTab = iota
	tasksTabWork
)

const (
	// workSearchMaxRunes bounds the Work history query, mirroring the
	// service's own 200-rune limit (viewmodel.workQueryMaxRunes) so the client
	// can never ask for a page the service would reject.
	workSearchMaxRunes = 200
	// workPageLimit is the page size every Work list request asks for.
	workPageLimit = 50
)

// Work list views selected by mode.
const (
	workModeLive      = "live"
	workModeAttention = "attention"
	workModeHistory   = "history"
)

// Work request kinds carried in workRequestMsg.kind (contract C5).
const (
	workReqList    = "list"
	workReqInspect = "inspect"
	workReqStop    = "stop"
	workReqAck     = "ack"
	workReqExpand  = "expand"
	workReqOrigin  = "origin"
	workReqPrompt  = "prompt"
)

// workPending is one outstanding request the panel has emitted but not yet
// seen a result for. It carries the query so a page result knows whether it
// replaces the list or appends to it.
type workPending struct {
	kind  string
	query viewmodel.WorkQuery
	raw   bool
}

// tasksPanelModel is the Ctrl+T Tasks panel: a Plan tab over the viewed todo
// list (the existing docpanel) and a Work tab over the runtime's live Work
// projection (a listpanel view). It implements dock.Panel.
//
// The panel never performs a runtime call. Every Work action emits a
// workRequestMsg for the model's pump to service, and results come back
// through applyPage/applyDetail, which reconcile by request ID and TaskKey so
// a page from a superseded request can never corrupt the list.
type tasksPanelModel struct {
	state   *session.State
	panelID string

	plan *docpanel.Panel

	activeTab   workTab
	previousTab workTab

	// Work availability and identity, refreshed when the panel opens and by
	// the pump (Task 18) when a snapshot arrives.
	available      bool
	conversationID string
	snapshot       viewmodel.WorkSnapshot

	// Work list
	list        *listpanel.FieldList
	mode        string // "live", "attention", "history"
	items       []viewmodel.WorkItem
	nextCursor  string
	stale       bool
	selectedKey string

	// search
	searchOpen bool
	search     textfield.Model
	query      string

	// detail
	detailOpen bool
	detail     *viewmodel.WorkDetail

	// request bookkeeping: a page or detail result is only applied while its
	// requestID still names an outstanding request.
	reqSeq  int
	pending map[string]workPending
	err     string
}

var _ dock.Panel = (*tasksPanelModel)(nil)

// newTasksPanel builds the Tasks panel over the viewed todo list (todos/stats
// seed the Plan doc; the model refreshes it each frame through SetSource) and
// the runtime's current Work projection. It allocates a fresh panel ID from
// the model's monotonic counter.
func newTasksPanel(m *Model, todos []native.TodoItem, stats map[string]viewmodel.TaskStat, tab workTab) *tasksPanelModel {
	p := &tasksPanelModel{
		state:          m.state,
		panelID:        m.nextPanelID(),
		activeTab:      tab,
		previousTab:    tab,
		available:      m.workLaneActive(),
		conversationID: m.state.SessionID(),
		snapshot:       m.workSnapshot,
		mode:           workModeLive,
		pending:        map[string]workPending{},
		search:         textfield.New(),
	}
	p.search.SetVirtualCursor(true)
	p.search.CharLimit = workSearchMaxRunes
	p.search.Placeholder = "search work history"
	if live, attention := p.liveAndAttention(); len(live) > 0 {
		p.selectedKey = live[0].TaskKey
	} else if len(attention) > 0 {
		p.selectedKey = attention[0].TaskKey
	}
	p.plan = docpanel.New(tasksDoc(todos, stats), m.state)
	p.list = listpanel.NewFieldList(p.workFields)
	return p
}

// nextPanelID allocates the next dock panel ID from the model's monotonic
// counter. IDs are never derived from a label, so two panels that render the
// same title still get distinct identities.
func (m *Model) nextPanelID() string {
	m.panelSeq++
	return fmt.Sprintf("tasks-%d", m.panelSeq)
}

// SetSource makes the Plan tab re-read its doc from fn on every render, exactly
// as the bare docpanel did before this panel hosted it. The model calls it each
// frame so the source sees the current model value rather than the copy the
// panel was opened from. The active tab is prefixed so the title names it.
func (p *tasksPanelModel) SetSource(fn func() commands.Doc) {
	p.plan.SetSource(func() commands.Doc {
		doc := fn()
		doc.Title = "Plan · " + doc.Title
		return doc
	})
}

// setWork refreshes the Work availability, identity and projection. The model's
// Work pump (Task 18) calls it when a snapshot arrives; the panel never reads
// the service itself.
func (p *tasksPanelModel) setWork(available bool, conversationID string, snapshot viewmodel.WorkSnapshot) {
	p.available = available
	p.conversationID = conversationID
	p.snapshot = snapshot
}

// Sizing keeps the Tasks panel docked; the Plan doc may not take the full
// frame, so neither may the host.
func (p *tasksPanelModel) Sizing() dock.Sizing { return dock.Docked }

// Update routes to the active tab. Plan keeps the bare docpanel's behaviour
// (Enter drills, Esc pops or closes). Work handles its own keys: left/right (or
// Tab) switch tabs, Space expands the selected live item, "/" opens search, and
// Esc leaves detail or search before closing.
func (p *tasksPanelModel) Update(msg tea.Msg) tea.Cmd {
	if p.activeTab == tasksTabPlan {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "tab", "left", "right":
				p.switchTab()
				return nil
			}
		}
		return p.plan.Update(msg)
	}
	return p.updateWork(msg)
}

// switchTab moves between Plan and Work. Each tab keeps its own selection and
// text-editing state: the Plan docpanel keeps its own cursor, and the Work tab
// keeps its list cursor, selectedKey and search text.
func (p *tasksPanelModel) switchTab() {
	p.previousTab = p.activeTab
	if p.activeTab == tasksTabPlan {
		p.activeTab = tasksTabWork
	} else {
		p.activeTab = tasksTabPlan
	}
}

func (p *tasksPanelModel) updateWork(msg tea.Msg) tea.Cmd {
	if p.searchOpen {
		return p.updateSearch(msg)
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			if p.detailOpen {
				p.closeDetail()
				return nil
			}
			return func() tea.Msg { return docpanel.ClosedMsg{} }
		}
		// Tab switching, search and expansion are list gestures: inside the
		// detail view the keys belong to the detail's own rows.
		if !p.detailOpen {
			switch k.String() {
			case "tab", "left", "right":
				p.switchTab()
				return nil
			case "space":
				return p.expandCursor()
			case "/":
				p.openSearch()
				return nil
			}
		}
	}
	cmd := p.list.Update(msg)
	p.syncSelection()
	return cmd
}

func (p *tasksPanelModel) updateSearch(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			p.searchOpen = false
			p.search.Blur()
			return nil
		case "enter":
			return p.applySearch()
		}
	}
	var cmd tea.Cmd
	p.search, cmd = p.search.Update(msg)
	return cmd
}

// openSearch focuses the Work history text field, seeded with the applied
// query, so "/" edits the current search rather than starting over.
func (p *tasksPanelModel) openSearch() {
	p.searchOpen = true
	p.search.SetValue(p.query)
	p.search.CursorEnd()
	p.search.Focus()
}

// applySearch commits the search box and asks for the first page of history
// matching it. It replaces the list rather than concatenating onto whatever was
// on screen.
func (p *tasksPanelModel) applySearch() tea.Cmd {
	q := viewmodel.ScrubText(p.search.Value())
	if r := []rune(q); len(r) > workSearchMaxRunes {
		q = string(r[:workSearchMaxRunes])
	}
	p.query = q
	p.searchOpen = false
	p.search.Blur()
	p.mode = workModeHistory
	p.items = nil
	p.nextCursor = ""
	p.stale = false
	p.selectedKey = ""
	return p.listCmd()
}

// openView switches to the attention or history view and asks for its first
// page. The history query resets for the attention set; a carried-over query
// would silently filter the wrong list.
func (p *tasksPanelModel) openView(name string) tea.Cmd {
	p.mode = name
	p.items = nil
	p.nextCursor = ""
	p.stale = false
	p.selectedKey = ""
	if name != workModeHistory {
		p.query = ""
	}
	return p.listCmd()
}

// listCmd builds a Work-list request for the current view and query. The
// conversation identity is filled from the panel's current conversation so a
// page can never be requested for the wrong root.
func (p *tasksPanelModel) listCmd() tea.Cmd {
	return p.request(workReqList, "", "", viewmodel.WorkQuery{
		ConversationID: p.conversationID,
		View:           p.mode,
		Query:          p.query,
		Limit:          workPageLimit,
	}, false)
}

// request emits one workRequestMsg and records it as outstanding. The panel
// returns the command; it never calls the Work service.
func (p *tasksPanelModel) request(kind, taskKey, resultRevision string, query viewmodel.WorkQuery, raw bool) tea.Cmd {
	p.reqSeq++
	id := fmt.Sprintf("%s.r%d", p.panelID, p.reqSeq)
	p.pending[id] = workPending{kind: kind, query: query, raw: raw}
	msg := workRequestMsg{
		conversationID: p.conversationID,
		panelID:        p.panelID,
		requestID:      id,
		kind:           kind,
		taskKey:        taskKey,
		resultRevision: resultRevision,
		query:          query,
		raw:            raw,
	}
	return func() tea.Msg { return msg }
}

// applyPage merges one decoded page. It is a no-op unless requestID names an
// outstanding list request, so a late page from a superseded query, a different
// panel or a stale epoch is dropped rather than concatenated. A stale-cursor
// error marks the list stale instead of appending; the refresh row then asks
// for page 1 again. Selection is reconciled by TaskKey, never by list index.
func (p *tasksPanelModel) applyPage(requestID string, page viewmodel.WorkPage, err error) bool {
	req, ok := p.pending[requestID]
	if !ok || req.kind != workReqList {
		return false
	}
	delete(p.pending, requestID)
	if err != nil {
		if errors.Is(err, viewmodel.ErrWorkStaleCursor) {
			p.stale = true
			p.err = ""
			return true
		}
		p.err = err.Error()
		return true
	}
	p.err = ""
	p.stale = false
	if req.query.Cursor == "" {
		p.items = append([]viewmodel.WorkItem(nil), page.Items...)
	} else {
		for _, it := range page.Items {
			if p.keyPresent(it.TaskKey) {
				continue // an overlapping page boundary must not duplicate a row
			}
			p.items = append(p.items, it)
		}
	}
	p.nextCursor = page.NextCursor
	if !p.keyPresent(p.selectedKey) && len(p.items) > 0 {
		p.selectedKey = p.items[0].TaskKey
	}
	p.reconcileCursor()
	return true
}

// applyDetail opens the read-only detail for one item. Opening or inspecting
// work never acknowledges it; only the ack action does.
func (p *tasksPanelModel) applyDetail(requestID string, detail viewmodel.WorkDetail, err error) bool {
	req, ok := p.pending[requestID]
	if !ok || req.kind != workReqInspect {
		return false
	}
	delete(p.pending, requestID)
	if err != nil {
		p.err = err.Error()
		return true
	}
	p.err = ""
	d := detail
	p.detail = &d
	p.detailOpen = true
	p.list.SetCursor(0)
	return true
}

func (p *tasksPanelModel) closeDetail() {
	p.detailOpen = false
	p.detail = nil
	p.reconcileCursor()
}

// expandCursor emits an expansion for the live item under the cursor. Headers
// and action rows, and items that are no longer live, are ignored.
func (p *tasksPanelModel) expandCursor() tea.Cmd {
	row := p.list.CursorRow()
	if row == nil || !strings.HasPrefix(row.ID, "item:") {
		return nil
	}
	key := strings.TrimPrefix(row.ID, "item:")
	if !p.liveKey(key) {
		return nil
	}
	return p.request(workReqExpand, key, "", viewmodel.WorkQuery{ConversationID: p.conversationID}, false)
}

// syncSelection records the item under the cursor so the selection survives a
// page merge by TaskKey.
func (p *tasksPanelModel) syncSelection() {
	if row := p.list.CursorRow(); row != nil && strings.HasPrefix(row.ID, "item:") {
		p.selectedKey = strings.TrimPrefix(row.ID, "item:")
	}
}

// reconcileCursor moves the list cursor back onto selectedKey's row after the
// rows change, so a merge neither jumps to the top nor lands on a stranger.
func (p *tasksPanelModel) reconcileCursor() {
	if p.selectedKey == "" {
		return
	}
	want := "item:" + p.selectedKey
	for i, r := range p.list.Rows() {
		if r.ID == want {
			p.list.SetCursor(i)
			return
		}
	}
}

func (p *tasksPanelModel) keyPresent(key string) bool {
	if key == "" {
		return false
	}
	for _, it := range p.items {
		if it.TaskKey == key {
			return true
		}
	}
	return false
}

func (p *tasksPanelModel) liveKey(key string) bool {
	for _, it := range p.snapshot.Items {
		if it.TaskKey == key && viewmodel.WorkActive(it) {
			return true
		}
	}
	return false
}

// liveAndAttention splits the snapshot into the cards the Work view shows:
// active membership and settled work that still needs attention. Receipts are
// counted in the summary, never carded here.
func (p *tasksPanelModel) liveAndAttention() (live, attention []viewmodel.WorkItem) {
	for _, it := range p.snapshot.Items {
		switch {
		case viewmodel.WorkActive(it):
			live = append(live, it)
		case viewmodel.WorkNeedsAttention(it):
			attention = append(attention, it)
		}
	}
	return live, attention
}

func (p *tasksPanelModel) summaryLine() string {
	live, attention := p.liveAndAttention()
	return fmt.Sprintf("%d live · %d need attention · %d total", len(live), len(attention), len(p.snapshot.Items))
}

func (p *tasksPanelModel) conversationLine() string {
	if p.conversationID == "" {
		return "Current conversation"
	}
	return "Conversation " + p.conversationID
}

func (p *tasksPanelModel) viewTitle() string {
	name := "History"
	if p.mode == workModeAttention {
		name = "Attention"
	}
	if p.query != "" {
		return name + " · " + p.query
	}
	return name
}

// itemTitle names one Work row: kind, label and the owner badge, so a row from
// a subagent or pipeline worker is attributable at a glance.
func (p *tasksPanelModel) itemTitle(it viewmodel.WorkItem) string {
	kind := workClean(it.Kind, 24)
	if kind == "" {
		kind = "work"
	}
	title := workClean(it.Label, 120)
	if title == "" {
		title = workClean(it.DisplayID, 60)
	}
	if title == "" {
		title = workClean(it.SourceID, 60)
	}
	out := kind
	if title != "" {
		out += " " + title
	}
	if owner := workClean(it.Owner, 40); owner != "" {
		out += " · @" + owner
	}
	return out
}

func (p *tasksPanelModel) itemDesc(it viewmodel.WorkItem) string {
	var parts []string
	if it.State != "" {
		parts = append(parts, it.State)
	}
	if it.Activity != "" {
		parts = append(parts, workClean(it.Activity, 60))
	}
	return strings.Join(parts, " · ")
}

func (p *tasksPanelModel) headerField(id, title string) *listpanel.Field {
	return &listpanel.Field{ID: id, Title: title, Kind: listpanel.KindHeader}
}

// scalarField is a read-only notice row: the title carries the whole message,
// so the value cell stays empty. KindScalar requires a GetStr closure —
// valueCell calls it unconditionally — and a nil one panics on render.
func (p *tasksPanelModel) scalarField(id, title string) *listpanel.Field {
	return &listpanel.Field{
		ID: id, Title: title, Kind: listpanel.KindScalar,
		GetStr: func() string { return "" },
	}
}

func (p *tasksPanelModel) actionField(id, title string, act func() tea.Cmd) *listpanel.Field {
	return &listpanel.Field{ID: id, Title: title, Kind: listpanel.KindAction, Act: act}
}

func (p *tasksPanelModel) itemField(it viewmodel.WorkItem) *listpanel.Field {
	item := it
	return &listpanel.Field{
		ID:       "item:" + item.TaskKey,
		Title:    p.itemTitle(item),
		Desc:     p.itemDesc(item),
		Kind:     listpanel.KindAction,
		ActLabel: func() string { return "↵ inspect" },
		Act: func() tea.Cmd {
			return p.request(workReqInspect, item.TaskKey, "",
				viewmodel.WorkQuery{ConversationID: p.conversationID}, false)
		},
	}
}

// workFields builds the Work tab's rows for the current mode. It is the
// FieldList's Fields provider, so it is re-read on every render and every key.
func (p *tasksPanelModel) workFields() []*listpanel.Field {
	if p.detailOpen && p.detail != nil {
		return p.detailFields()
	}
	if !p.available {
		return []*listpanel.Field{p.scalarField("work-unavailable", "Work is unavailable in this runtime.")}
	}
	if p.mode == workModeLive {
		return p.liveFields()
	}
	return p.pageFields()
}

func (p *tasksPanelModel) liveFields() []*listpanel.Field {
	rows := []*listpanel.Field{
		p.headerField("h-conv", p.conversationLine()),
		p.headerField("h-summary", p.summaryLine()),
	}
	live, attention := p.liveAndAttention()
	if len(live) == 0 && len(attention) == 0 {
		rows = append(rows, p.scalarField("work-empty", "No work yet"))
	}
	for _, it := range live {
		rows = append(rows, p.itemField(it))
	}
	for _, it := range attention {
		rows = append(rows, p.itemField(it))
	}
	rows = append(rows,
		p.actionField("work-attention", "Attention", func() tea.Cmd { return p.openView(workModeAttention) }),
		p.actionField("work-history", "History", func() tea.Cmd { return p.openView(workModeHistory) }),
		p.actionField("work-search", "Search history (/)", func() tea.Cmd { p.openSearch(); return nil }),
	)
	return rows
}

func (p *tasksPanelModel) pageFields() []*listpanel.Field {
	rows := []*listpanel.Field{p.headerField("h-view", p.viewTitle())}
	if len(p.items) == 0 {
		rows = append(rows, p.scalarField("work-noresults", "No results yet"))
	}
	for _, it := range p.items {
		rows = append(rows, p.itemField(it))
	}
	switch {
	case p.stale:
		rows = append(rows, p.actionField("work-refresh", "History changed — refresh", func() tea.Cmd {
			p.stale = false
			p.nextCursor = ""
			return p.listCmd()
		}))
	case p.nextCursor != "":
		cursor := p.nextCursor
		rows = append(rows, p.actionField("work-next", "Next page ›", func() tea.Cmd {
			return p.request(workReqList, "", "", viewmodel.WorkQuery{
				ConversationID: p.conversationID,
				View:           p.mode,
				Query:          p.query,
				Cursor:         cursor,
				Limit:          workPageLimit,
			}, false)
		}))
	}
	rows = append(rows, p.actionField("work-live", "‹ Live", func() tea.Cmd {
		p.mode = workModeLive
		p.query = ""
		return nil
	}))
	return rows
}

func (p *tasksPanelModel) detailFields() []*listpanel.Field {
	d := p.detail
	it := d.Item
	rows := []*listpanel.Field{p.headerField("h-detail", "Detail · "+p.itemTitle(it))}
	if it.State != "" {
		rows = append(rows, p.scalarField("d-state", "State: "+it.State))
	}
	if owner := workClean(it.Owner, 60); owner != "" {
		rows = append(rows, p.scalarField("d-owner", "Owner: @"+owner))
	}
	if d.Unavailable != "" {
		rows = append(rows, p.scalarField("d-unavailable", d.Unavailable))
	}
	if d.Output != "" {
		for i, line := range strings.Split(d.Output, "\n") {
			if i >= 20 {
				rows = append(rows, p.scalarField("d-more", "…"))
				break
			}
			rows = append(rows, p.scalarField(fmt.Sprintf("d-output-%d", i), workClean(line, 200)))
		}
	}
	rows = append(rows, p.actionField("d-origin", "Jump to origin", func() tea.Cmd {
		return p.request(workReqOrigin, it.TaskKey, "", viewmodel.WorkQuery{ConversationID: p.conversationID}, false)
	}))
	rawLabel := "Show raw"
	if d.Raw {
		rawLabel = "Show formatted"
	}
	rows = append(rows, p.actionField("d-raw", rawLabel, func() tea.Cmd {
		return p.request(workReqInspect, it.TaskKey, "",
			viewmodel.WorkQuery{ConversationID: p.conversationID}, !d.Raw)
	}))
	if it.Capabilities.Stop {
		rows = append(rows, p.actionField("d-stop", "Stop", func() tea.Cmd {
			return p.request(workReqStop, it.TaskKey, "", viewmodel.WorkQuery{ConversationID: p.conversationID}, false)
		}))
	}
	if it.Capabilities.Acknowledge {
		revision := viewmodel.WorkResultRevision(it)
		ack := func() tea.Cmd {
			return p.request(workReqAck, it.TaskKey, revision,
				viewmodel.WorkQuery{ConversationID: p.conversationID}, false)
		}
		rows = append(rows, p.actionField("d-ack", "Mark reviewed", ack))
		if viewmodel.WorkNeedsAttention(it) {
			rows = append(rows, p.actionField("d-dismiss", "Dismiss", ack))
		}
	}
	if it.Capabilities.ResolvePrompt && it.Prompt != nil {
		prompt := *it.Prompt
		rows = append(rows, p.actionField("d-prompt", "Resolve prompt", func() tea.Cmd {
			return func() tea.Msg {
				return workRequestMsg{
					conversationID: p.conversationID,
					panelID:        p.panelID,
					requestID:      prompt.RequestID,
					kind:           workReqPrompt,
					taskKey:        it.TaskKey,
				}
			}
		}))
	}
	rows = append(rows, p.actionField("d-close", "Close", func() tea.Cmd { p.closeDetail(); return nil }))
	return rows
}

// View renders the active tab. Plan delegates to the docpanel it hosts; Work
// renders its list inside the same chrome. The search box sits under the list
// while it is open.
func (p *tasksPanelModel) View(width, maxHeight int) string {
	if width < 4 || maxHeight < 2 {
		return ""
	}
	if p.activeTab == tasksTabPlan {
		return p.plan.View(width, maxHeight)
	}
	pw := layout.PanelWidth(width)
	inner := pw - 3
	p.list.SetSize(inner, max(maxHeight-1, 1))
	body := p.list.View()
	if p.searchOpen {
		body += "\n" + p.search.View()
	}
	title := "Work"
	if p.mode == workModeHistory && p.query != "" {
		title = "Work · history"
	}
	hints := "←→ tabs · ↑↓ move · ↵ inspect · space expand/collapse · / search · esc close"
	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints(title, hints, body, pw, ph, true, theme.Current())
}
