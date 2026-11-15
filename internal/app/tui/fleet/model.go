// Package fleet is the fleet screen's model (P5 F4): the agents and sessions
// list, the needs-you queue, the Events ring, the key bindings, and the
// transitions the screen reports. It reads the fleet through acpclient.Fleet
// and holds no policy of its own — the client decides what the fleet is, this
// package decides how it moves.
package fleet

import (
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/theme"
	"marshal/web/bridge/contract"
)

// The panels tab cycles, in this order.
const (
	focusAgents = iota
	focusNeedsYou
	focusEvents
	focusRight

	focusCount
)

// Model is the fleet screen (P5 F4). It is a value: Update returns a new copy,
// and the only shared state is the done gate that releases the wait command
// when the program quits.
type Model struct {
	fleet acpclient.Fleet
	// now is the clock for ages and event times. Tests replace it.
	now func() time.Time
	th  theme.Theme
	cfg config.TUIConfig

	// state is the last state read from the client.
	state acpclient.FleetState
	// events is the Events ring: oldest first, newest last, at most maxEvents
	// lines.
	events []string

	// focus is the panel that has focus, in tab order.
	focus int
	// selected is the selected row's ID — an agent's or a session's — so the
	// selection survives a reorder.
	selected string
	// startSelected is WithSelected's ID until a loaded state lists it: the
	// cursor starts there, once.
	startSelected string
	// notice is WithNotice's line, added to Events when the model starts.
	notice string
	// target is what the program ended to open. Only ⏎ sets it, so the quit
	// keys end with the zero target.
	target Target
	// agentCursor indexes the Agents panel's selectable rows, never a group
	// header. needsCursor indexes the Needs you panel's queue.
	agentCursor int
	needsCursor int
	// tab is the right panel's tab: 1 Activity, 3 Info.
	tab int

	zoom      bool
	help      bool
	filter    string
	filtering bool

	// dimmed is true while the state is not connected: the panels keep the
	// last state, and the rendering dims them.
	dimmed bool

	width, height int

	// done releases the wait command when the program quits, so the wait
	// cannot block a program that has stopped reading its messages. It is
	// held by pointer: Model is a value, and every copy shares one release.
	done *quitGate
}

// quitGate releases the wait command when the program quits. The close is
// guarded, so a second quit key cannot panic on an already-closed channel.
type quitGate struct {
	once sync.Once
	ch   chan struct{}
}

// newQuitGate is a gate that has not been released.
func newQuitGate() *quitGate { return &quitGate{ch: make(chan struct{})} }

// release closes the gate, once.
func (g *quitGate) release() { g.once.Do(func() { close(g.ch) }) }

// Target is what the fleet program ended to open (P5 F5). The zero value
// means quit.
type Target struct {
	AgentID   string // an agent's session, through the agent attach
	AgentName string
	Root      string // the project: an agent's, or a daemon session's child
	SessionID string // a daemon session, through the project attach; "" for an agent
}

// Option configures a Model.
type Option func(*Model)

// WithTUIConfig sets the configuration the model resolves its theme from.
func WithTUIConfig(cfg config.TUIConfig) Option {
	return func(m *Model) { m.cfg = cfg }
}

// WithNow sets the clock used for ages and event times.
func WithNow(now func() time.Time) Option {
	return func(m *Model) {
		if now != nil {
			m.now = now
		}
	}
}

// WithSelected starts the cursor on the row with this ID — an agent's, or a
// daemon session's — as soon as a loaded state lists it.
func WithSelected(id string) Option {
	return func(m *Model) { m.startSelected = id }
}

// WithNotice adds a line to the Events ring when the model starts: the loop
// says why the fleet screen came back. It is not a transition, so the baseline
// rule does not apply to it.
func WithNotice(line string) Option {
	return func(m *Model) { m.notice = line }
}

// New builds the fleet screen over f. It resolves the theme from the TUI config
// (decision 13) and publishes it, so the shared chrome helpers paint with it,
// exactly as the session screen's loadTheme does.
func New(f acpclient.Fleet, opts ...Option) Model {
	m := &Model{
		fleet:  f,
		now:    time.Now,
		cfg:    config.Default().TUI,
		tab:    1,
		dimmed: true,
		done:   newQuitGate(),
	}
	for _, opt := range opts {
		opt(m)
	}
	// Seed the state from the client, so the screen can name the bridge it is
	// waiting for before the first read lands. The client's state carries the
	// server's name from the moment it is built.
	if f != nil {
		m.state = f.State()
	}
	m.dimmed = m.state.Conn != acpclient.FleetConnected
	m.th = theme.LoadWithConfig(m.cfg.Theme, m.cfg.Mode, theme.ParseDepth(m.cfg.Depth), theme.PaletteOverrides(m.cfg.Palette))
	theme.Reload(m.th)
	// The notice is the loop's line, not a transition: it goes in before the
	// first read can add one of its own.
	if m.notice != "" {
		m.addEvent(m.notice)
	}
	return *m
}

// Open is the target the program ended to open: the row ⏎ chose. The zero
// target means the program ended without opening anything.
func (m Model) Open() Target { return m.target }

// Selected is the selected row's ID: an agent's or a daemon session's.
func (m Model) Selected() string { return m.selected }

// Theme is the theme the model resolved from its TUI config.
func (m Model) Theme() theme.Theme { return m.th }

// changedMsg reports that the client's state may have changed.
type changedMsg struct{}

// tickMsg refreshes the ages once a second.
type tickMsg struct{}

// Init waits on the client's change signal and starts the one-second tick the
// ages need.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.waitForChange(), tickCmd())
}

// waitForChange waits for the client's change signal. It gives up as soon as
// the program quits, so it never leaks a goroutine past the program's life.
// A closed client ends the wait too: there is nothing more to read.
func (m Model) waitForChange() tea.Cmd {
	f, done := m.fleet, m.done
	return func() tea.Msg {
		select {
		case <-done.ch:
			return nil
		case _, open := <-f.Changed():
			if !open {
				// The client is closed: its signal will never fire again.
				return nil
			}
			return changedMsg{}
		}
	}
}

// quit ends the program: it releases the wait command, so no goroutine is
// left blocked on the client's signal, and then quits.
func (m Model) quit() tea.Cmd {
	return func() tea.Msg {
		m.done.release()
		return tea.QuitMsg{}
	}
}

// tickCmd is the one-second tick that refreshes the ages.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles the client's changes, the tick, the terminal size and the
// keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case changedMsg:
		m.applyState()
		return m, m.waitForChange()
	case tickMsg:
		return m, tickCmd()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applyState reads the client's state, reports what changed since the last
// read, and re-arms the selection over the new rows.
func (m *Model) applyState() {
	prev, next := m.state, m.fleet.State()
	m.addConnectionEvents(prev, next)
	for _, t := range transitions(prev, next) {
		m.addEvent(transitionText(t))
	}
	m.state = next
	m.dimmed = next.Conn != acpclient.FleetConnected
	m.reconcile()
}

// reconcile holds the selection by ID across a rebuilt row list. A selection
// that is still there follows its ID; one that has gone keeps the cursor's
// index, clamped to the list.
func (m *Model) reconcile() {
	_, rows := m.agentLines()
	m.restoreStart(rows)
	switch {
	case len(rows) == 0:
		m.agentCursor = 0
		m.selected = ""
	default:
		if idx, ok := rowIndex(rows, m.selected); ok {
			m.agentCursor = idx
		} else {
			m.agentCursor = clamp(m.agentCursor, 0, len(rows)-1)
			m.selected = rows[m.agentCursor].id
		}
	}
	if n := len(m.state.Inbox.Queue); n == 0 {
		m.needsCursor = 0
	} else {
		m.needsCursor = clamp(m.needsCursor, 0, n-1)
	}
}

// restoreStart applies WithSelected: the cursor starts on the row with its ID
// the first time a loaded state lists it, and never again, so a later reorder
// leaves the operator's own move alone.
func (m *Model) restoreStart(rows []agentRow) {
	if m.startSelected == "" {
		return
	}
	idx, ok := rowIndex(rows, m.startSelected)
	if !ok {
		return
	}
	m.agentCursor = idx
	m.selected = rows[idx].id
	m.startSelected = ""
}

// openTarget is what ⏎ opens from the current selection. An agent row gives
// its ID, its name and its project root; a Sessions row gives its child's root
// and its session ID; a Needs you agent item gives that agent's target. An
// intake item, and an empty selection, open nothing: plan 5 gives intake items
// approve and deny.
func (m Model) openTarget() Target {
	id := m.selected
	if m.focus == focusNeedsYou {
		queue := m.state.Inbox.Queue
		if m.needsCursor < 0 || m.needsCursor >= len(queue) {
			return Target{}
		}
		item := queue[m.needsCursor]
		if item.Kind != contract.InboxItemAgent || item.AgentID == "" {
			return Target{}
		}
		id = item.AgentID
	}
	_, rows := m.agentLines()
	idx, ok := rowIndex(rows, id)
	if !ok {
		return Target{}
	}
	row := rows[idx]
	if row.session {
		return Target{Root: row.project, SessionID: row.id}
	}
	return Target{AgentID: row.id, AgentName: row.name, Root: row.project}
}

// agentRow is one selectable row in the Agents panel: an agent from a group of
// the inbox, or one of the local daemon's sessions.
type agentRow struct {
	id      string
	name    string
	project string
	session bool
}

// line is one display line of the Agents panel: a group header, or a row.
type line struct {
	header bool
	text   string
	// row is the index into the selectable rows, or -1 on a header. The
	// cursor indexes the selectable rows, so it never lands on a header.
	row int
}

// agentLines builds the Agents panel's display list and its selectable rows.
// The display list is groups in order — needs you, running, ready, earlier —
// each preceded by a header when non-empty, then a Sessions group for every
// session of every child of the local daemon. The selectable rows are the same
// without the headers.
func (m Model) agentLines() (lines []line, rows []agentRow) {
	group := func(header string, ids []string) {
		groupRows := make([]agentRow, 0, len(ids))
		for _, id := range ids {
			a, ok := m.agentByID(id)
			if !ok || !m.agentVisible(a) {
				continue
			}
			groupRows = append(groupRows, agentRow{id: a.ID, name: agentName(a), project: a.Project})
		}
		if len(groupRows) == 0 {
			return
		}
		lines = append(lines, line{header: true, text: header, row: -1})
		for _, r := range groupRows {
			lines = append(lines, line{text: r.name, row: len(rows)})
			rows = append(rows, r)
		}
	}
	group("Needs you", m.state.Inbox.Groups.NeedsYou)
	group("Running", m.state.Inbox.Groups.Running)
	group("Ready", m.state.Inbox.Groups.Ready)
	group("Earlier", m.state.Inbox.Groups.Earlier)

	if m.state.Daemon != nil {
		sessions := make([]agentRow, 0, len(m.state.Daemon.Children))
		for _, child := range m.state.Daemon.Children {
			for _, s := range child.Sessions {
				sessions = append(sessions, agentRow{id: s.ID, name: s.ID, project: child.Root, session: true})
			}
		}
		if len(sessions) > 0 {
			lines = append(lines, line{header: true, text: "Sessions", row: -1})
			for _, r := range sessions {
				lines = append(lines, line{text: r.name, row: len(rows)})
				rows = append(rows, r)
			}
		}
	}
	return lines, rows
}

// agentByID finds an agent in the last state by ID.
func (m Model) agentByID(id string) (contract.AgentStatus, bool) {
	for _, a := range m.state.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return contract.AgentStatus{}, false
}

// agentVisible reports whether an agent survives the Agents filter: a
// case-insensitive substring of its name or its project. Sessions are never
// filtered.
func (m Model) agentVisible(a contract.AgentStatus) bool {
	if m.filter == "" {
		return true
	}
	f := strings.ToLower(m.filter)
	return strings.Contains(strings.ToLower(a.Name), f) || strings.Contains(strings.ToLower(a.Project), f)
}

// agentName is an agent's Name, else the first 8 characters of its ID.
func agentName(a contract.AgentStatus) string {
	if a.Name != "" {
		return a.Name
	}
	return shortID(a.ID)
}

// shortID is the first 8 characters of id. It cuts on a rune, so a
// multi-byte character is never split.
func shortID(id string) string {
	runes := []rune(id)
	if len(runes) > 8 {
		return string(runes[:8])
	}
	return id
}

// rowIndex finds id in rows.
func rowIndex(rows []agentRow, id string) (int, bool) {
	if id == "" {
		return 0, false
	}
	for i, r := range rows {
		if r.id == id {
			return i, true
		}
	}
	return 0, false
}

// clamp confines v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
