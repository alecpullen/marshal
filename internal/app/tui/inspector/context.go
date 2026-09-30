package inspector

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/redact"
	"marshal/internal/strutil"
)

// ContextScope names which context surface the Context tab is showing.
//
// The two scopes answer different questions and are deliberately not merged
// into one list: "what is the agent carrying" is a live, mutable reading, and
// "what did it actually send" is a fixed snapshot of one attempt. Presenting
// them as one list would let a reader believe the second is the first.
type ContextScope string

const (
	// ContextScopePack is the pack the session is carrying right now.
	//
	// It is the DEFAULT scope, because it is the one that always has an
	// answer: the pack exists before the first request is made, so a reader
	// who opens the tab in a fresh session sees content rather than an empty
	// state they have to interpret.
	ContextScopePack ContextScope = "current-pack"
	// ContextScopeRequest is the last conversation request Marshal submitted
	// to its provider adapter.
	ContextScopeRequest ContextScope = "last-request"
)

// contextScopes is the offered set, in the order the selector presents them.
var contextScopes = []ContextScope{ContextScopePack, ContextScopeRequest}

// ContextPack is the presentation copy of the session's current context pack.
//
// Every field is COPIED, never a live reference: the pack is rebudgeted on
// route resolution and re-seeded by background indexers, so a panel holding
// the session's own value would render whatever the pack happened to be at
// draw time, and its own scroll position would describe nothing.
type ContextPack struct {
	// Known distinguishes "a pack was read" from "no pack was recorded",
	// which are different facts about the agent's context.
	Known           bool
	GeneratedAt     time.Time
	EstimatedTokens int
	MaxTokens       int
	// Truncated reports that the pack's own budget cut something. It is the
	// PACK's budget, not the model's context window: the two are adjacent
	// numbers about the same request, and conflating them is how one gets
	// read as the other.
	Truncated bool
	Sections  []ContextSection
}

// ContextSection is one pack section as the reader sees it.
type ContextSection struct {
	Title  string
	Kind   string
	Source string
	// Priority is the pack's own allocation priority. At or above the pin
	// threshold it means the reader asked for this section by name.
	Priority        int
	EstimatedTokens int
	// Content is the bounded text the pack currently holds, and
	// ContentTruncated records that the budget cut it. The pair is what lets
	// the panel say "this is a prefix" rather than presenting it as whole.
	Content          string
	ContentTruncated bool
	OmittedTokens    int
}

// ContextRequest is the presentation copy of one conversation attempt's
// bounded inspection snapshot.
//
// It describes what was SUBMITTED TO THE ADAPTER, which is the most Marshal
// can truthfully claim: whether a remote server received it, and what it
// finally encoded onto the wire, are both outside what Marshal can observe.
type ContextRequest struct {
	Known      bool
	AttemptID  uint64
	At         time.Time
	Provider   string
	Model      string
	Generation string
	// Status is the attempt's outcome: dispatched, streaming, completed,
	// failed, or cancelled.
	Status string
	Err    string

	Messages []ContextMessage
	Tools    []ContextTool

	// The options that actually went out, read from the request rather than
	// from configuration: a provider with no reasoning support has its
	// thinking effort dropped, so showing the configured value would describe
	// a request that was never sent.
	Thinking       string
	Streaming      bool
	MaxTokens      int
	HasMaxTokens   bool
	Temperature    float64
	HasTemperature bool
	ToolChoice     string
	// ResponseFormat names the response format type that was set ("" when
	// none). Only the type is carried: the schema itself is derived rather than
	// chosen, and it can be large.
	ResponseFormat string

	// Truncated reports that a snapshot cap bit, and OmittedBytes is what was
	// dropped. A bounded snapshot presented as complete is the failure these
	// exist to prevent.
	Truncated       bool
	OmittedBytes    int
	MessagesOmitted int
	ToolsOmitted    int

	// The pack as it was at DISPATCH time. Kept separate from the live pack
	// for the same reason the rest of the snapshot is: the pack changes and
	// this does not.
	//
	// PackTokens is the pack's OWN estimate. It is not the model's context
	// window, and it is labelled that way wherever it is rendered.
	PackTokens    int
	PackWindow    int
	PackTruncated bool
	PackSections  int
	PackKnown     bool
}

// ContextMessage is one message as it went into the request.
type ContextMessage struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ContextToolCall
	// Truncated and OmittedBytes describe Content when the field cap bit.
	Truncated    bool
	OmittedBytes int
}

// ContextToolCall is one tool call attached to a message.
type ContextToolCall struct {
	ID           string
	Name         string
	Args         string
	Truncated    bool
	OmittedBytes int
}

// ContextTool is one tool definition offered on the request.
type ContextTool struct {
	Name         string
	Description  string
	Parameters   string
	Truncated    bool
	OmittedBytes int
}

// ContextData is the whole snapshot the Context tab renders from, handed in by
// the root.
//
// The inspector never reconstructs a provider request. Assembling one means
// walking the same routing, budget and capability decisions the runtime made,
// and a second implementation of that would describe a request that was never
// sent — the one failure this tab must never commit.
type ContextData struct {
	// ScopeLabel names the scope when it is not the root conversation (a
	// child agent). Empty at the root.
	ScopeLabel string
	// Child reports that this is a child agent's context rather than the
	// conversation's.
	Child   bool
	Pack    ContextPack
	Request ContextRequest
	// Now is the clock the ages are rendered against. Injected so a panel
	// never reads time.Now during a render: a view whose text changes every
	// frame makes its own scroll position meaningless.
	Now time.Time
}

// contextRow is one selectable line in the Context tab.
type contextRow struct {
	// key is the row's STABLE IDENTITY within the scope. Selection is stored
	// as a key rather than an index because a rebudget can drop or reorder
	// sections, and an index-based selection would silently move the reader
	// onto a different section — and, on Enter, open it.
	key       string
	label     string
	detail    string
	body      string
	truncated bool
}

// contextState is the Context tab's own navigation state.
type contextState struct {
	scope ContextScope
	data  ContextData

	// child is the explicitly-scoped child's context, or nil at the root.
	//
	// It is SEPARATE from data rather than swapped into it, so returning from
	// the child restores the reader to exactly what they were looking at.
	// Overwriting data would make "go back" mean "re-derive and hope".
	child     *ContextData
	childName string
	// rootScope is the scope selected before entering the child, and
	// rootCursor is the row the root was on. Both are restored on the way out.
	rootScope  ContextScope
	rootCursor int

	// cursor is per SCOPE PER SUBJECT. Each combination has its own list with
	// its own length, so one shared index would re-target Enter at an unrelated
	// row on every change — and the subject is part of the key, not just the
	// scope, because a child's "current pack" is a different list from the
	// conversation's and a shared slot would move the reader's place in one
	// whenever they browsed the other.
	//
	// The key is the scope, prefixed with the child's name when one is entered.
	cursor map[string]int

	// rows is the current scope's list, rebuilt whenever the data or the
	// scope changes.
	rows []contextRow

	// openKey and body describe the detail on screen. The BODY is kept, not
	// just the key, so a later snapshot can be compared against what the
	// reader is actually looking at.
	openKey       string
	hasOpen       bool
	body          string
	bodyTruncated bool
	// openLabel is the heading this row renders under. It lives HERE rather
	// than in the shared detailLabel field because that field is shared with
	// the Changes and Agents tabs: opening a diff overwrites it, so a context
	// body would come back under the diff's title — and the copy label, which
	// is built from it, would name the wrong thing.
	openLabel string
	// stale records that a newer snapshot would produce a different body for
	// the row on screen. The reader is told; the body is not replaced, because
	// yanking text out from under someone mid-sentence is the failure.
	stale bool

	// detail is THIS tab's own scrollable body. It is per-tab rather than a
	// single view shared with Changes and Agents because each tab re-asserts
	// its content on a different schedule: with one shared view, opening a
	// diff on Changes and then drawing the Context tab left the context body
	// under the diff's label — and the paging keys still pointed at the context
	// body. One body per tab removes the class of mislabelling.
	detail *DetailView
}

// ContextScope reports the scope on display.
func (m *Model) ContextScope() ContextScope { return m.context.scope }

// SetContextScope selects a scope. An unknown scope is REFUSED rather than
// stored: a scope the renderer cannot draw would show an empty panel with
// nothing on screen to explain it.
func (m *Model) SetContextScope(scope ContextScope) {
	if !validContextScope(scope) || scope == m.context.scope {
		return
	}
	m.context.scope = scope
	m.rebuildContextRows()
	// The open detail belonged to the other scope's list, so it does not
	// describe anything in this one.
	m.closeContextDetail()
}

// ParseContextScope resolves a user-written scope name.
//
// It accepts the short forms a user would actually type as well as the
// canonical values, and it is the ONE place that mapping lives: a second copy
// in the command dispatcher would drift, and the symptom would be a documented
// scope name that opens the other scope.
func ParseContextScope(name string) (ContextScope, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "pack", "current", "current-pack":
		return ContextScopePack, true
	case "request", "last", "last-request":
		return ContextScopeRequest, true
	}
	return "", false
}

// validContextScope reports whether scope is one of the offered scopes.
func validContextScope(scope ContextScope) bool {
	for _, s := range contextScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// SetContext records a new snapshot.
//
// An OPEN detail is deliberately left alone. The reader may be halfway through
// a section, and replacing the body under them is the failure; the panel marks
// the body stale instead, so they are told it changed without losing their
// place. Re-opening the row is the explicit refresh.
func (m *Model) SetContext(d ContextData) {
	m.context.data = d
	m.rebuildContextRows()
	m.markContextDetailStale()
}

// SetChildContext enters an explicitly-scoped child agent's context, or
// refreshes the one already entered.
//
// The root's scope selection and per-scope cursor are REMEMBERED, not
// recomputed: the reader chose "Last request" before they went into the child,
// and coming back must land them where they were rather than at the default.
//
// Refreshing an ALREADY-ENTERED child leaves the reader's open detail standing
// and marks it stale, exactly as SetContext does at the root. The caller
// re-pushes the snapshot on every turn boundary, so a version of this that
// dismissed the detail unconditionally would take the body away from the reader
// several times a turn with nothing having changed to justify it.
func (m *Model) SetChildContext(name string, d ContextData) {
	entering := m.context.child == nil || m.context.childName != name
	if entering {
		if m.context.child == nil {
			m.context.rootScope = m.context.scope
			m.context.rootCursor = m.context.cursorAt(m.subjectKey())
		}
		// The detail belonged to the previous subject's data, so it does not
		// describe anything here.
		m.closeContextDetail()
	}
	m.context.childName = name
	child := d
	if child.ScopeLabel == "" {
		child.ScopeLabel = name
	}
	child.Child = true
	m.context.child = &child
	m.rebuildContextRows()
	if entering {
		return
	}
	m.markContextDetailStale()
}

// ClearChildContext leaves the child scope, restoring the root's own selection.
// It reports false when there is nothing to leave, so a key handler can fall
// through to its other meanings rather than swallowing the key.
func (m *Model) ClearChildContext() bool {
	if m.context.child == nil {
		return false
	}
	m.context.child = nil
	m.context.childName = ""
	m.context.scope = m.context.rootScope
	m.closeContextDetail()
	m.rebuildContextRows()
	// The root's row position is restored, not left wherever the child's
	// browsing put it. The two lists are different lengths and describe
	// different things, so carrying an index across would land the reader on an
	// unrelated row — and Enter would open it.
	m.context.setCursorAt(m.subjectKey(), m.context.rootCursor)
	return true
}

// InChildContext reports whether the child scope is entered.
func (m *Model) InChildContext() bool { return m.context.child != nil }

// ChildContextName reports the child the Context tab is scoped to, or "" at the
// root.
//
// The caller compares against it before pushing a new child snapshot: a refresh
// runs on every turn boundary, and a SetChildContext that ran unconditionally
// would dismiss the detail the reader has open, over and over, with nothing
// having changed to justify it.
func (m *Model) ChildContextName() string { return m.context.childName }

// activeContextData is the data of the scope on display: the child's when one
// is entered, otherwise the root's.
//
// It is an explicit choice rather than a swap of the stored value, so a child
// scope can never render the root's pack under the child's name — on screen
// the two are indistinguishable.
func (m *Model) activeContextData() ContextData {
	if m.context.child != nil {
		return *m.context.child
	}
	return m.context.data
}

// ContextRowCount reports how many selectable rows the scope on display has.
func (m *Model) ContextRowCount() int { return len(m.context.rows) }

// subjectKey identifies the list the cursor belongs to: the scope on display,
// qualified by the child when one has been entered.
//
// Scope alone is not sufficient. A child's "current pack" and the conversation's
// are different lists of different lengths, so sharing one slot would move the
// reader's place in the conversation every time they browsed the child — and on
// the way back they would land on an unrelated row, where Enter would open it.
func (m *Model) subjectKey() string {
	if m.context.childName != "" {
		return m.context.childName + "|" + string(m.context.scope)
	}
	return string(m.context.scope)
}

// ContextCursor reports the row cursor for the list on display.
func (m *Model) ContextCursor() int { return m.context.cursorAt(m.subjectKey()) }

// cursorAt reads a list's cursor, defaulting to the top.
func (c *contextState) cursorAt(key string) int {
	if c.cursor == nil {
		return 0
	}
	return c.cursor[key]
}

// setCursorAt records a list's cursor.
func (c *contextState) setCursorAt(key string, v int) {
	if c.cursor == nil {
		c.cursor = map[string]int{}
	}
	c.cursor[key] = v
}

// MoveContextSelection moves the cursor by delta rows, clamped to the list.
func (m *Model) MoveContextSelection(delta int) {
	if delta == 0 || len(m.context.rows) == 0 {
		return
	}
	idx := min(max(m.ContextCursor()+delta, 0), len(m.context.rows)-1)
	m.context.setCursorAt(m.subjectKey(), idx)
}

// OpenContextRow opens the detail for row index, reporting false when the index
// is not a row of the scope on display.
func (m *Model) OpenContextRow(index int) bool {
	if index < 0 || index >= len(m.context.rows) {
		return false
	}
	m.openContextRow(m.context.rows[index])
	return true
}

// OpenContextRowByLabel opens the detail for the first row with this label,
// reporting false when no row matches.
//
// It exists for the targets a caller names by identity rather than by position
// — a section named in a link, or a test asserting on a specific row — where an
// index would be a second, driftable way to say the same thing.
func (m *Model) OpenContextRowByLabel(label string) bool {
	for _, row := range m.context.rows {
		if row.label == label {
			m.openContextRow(row)
			return true
		}
	}
	return false
}

// openContextRow fills the shared detail view with one row's body.
//
// The body is stored UNWRAPPED and displayed WRAPPED. That split is the same
// rule the transcript follows: a soft wrap is a property of the viewport, not
// of the text, and a hard newline inserted for display would land in the
// reader's file as a line break that was never in their repository. It also
// keeps the copy source independent of the width the panel happened to be at
// when the reader pressed Enter.
func (m *Model) openContextRow(row contextRow) {
	m.context.openKey = row.key
	m.context.hasOpen = true
	m.context.body = row.body
	m.context.bodyTruncated = row.truncated
	// Re-opening a row is the explicit refresh, so whatever staleness the
	// previous snapshot recorded is answered by adopting the new body.
	m.context.stale = false
	// The heading is recorded BEFORE the body is wrapped, because the label is
	// a ROW of the detail's budget and the content window is derived from it.
	// The heading lives on contextState, not on the shared model, so a diff
	// opened on another tab cannot relabel this body; View is handed it again
	// at render time.
	m.context.openLabel = row.label
	detail := m.contextDetail()
	detail.SetNoLongerChanged(false)
	m.contextWrapBody()
	// A section or a request body opens at its FIRST line. The detail view
	// follows by default, which is right for a stream that grows underneath
	// the reader and wrong here: these bodies are fetched whole, and opening
	// one at the bottom hides the head that says what it is.
	detail.Top()
}

// contextDetail reports the Context tab's own body, allocating it on first use.
func (m *Model) contextDetail() *DetailView {
	if m.context.detail == nil {
		m.context.detail = NewDetailView()
	}
	return m.context.detail
}

// contextWrapBody hands the detail view the stored body wrapped to the panel's
// current width.
//
// It is called whenever the body or the width changes, so the two never
// disagree: a body wrapped for one width and displayed at another would leave
// rows hanging past the frame, and the panel's own chrome would be pushed off
// the bottom.
func (m *Model) contextWrapBody() {
	if !m.context.hasOpen {
		return
	}
	detail := m.contextDetail()
	detail.SetLabel(m.context.openLabel)
	detail.SetContent(wrapContextBody(m.context.body, m.width), m.context.bodyTruncated)
}

// wrapContextBody soft-wraps every line of a body to width cells.
//
// It reuses the shared wrapper rather than a second implementation: two
// wrappers would disagree at the margin, and the symptom would be one tab whose
// rows fit while another's do not. The wrapper never drops text — a word longer
// than the width is cut into pieces rather than lost — because silently losing
// part of a section is worse than an ugly break.
func wrapContextBody(body string, width int) string {
	if body == "" {
		return body
	}
	// Below two cells there is no room to wrap into; the caller's own
	// truncation bounds the row instead.
	if width < 2 {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapToWidth(line, width)...)
	}
	return strings.Join(out, "\n")
}

// closeContextDetail dismisses the open detail.
func (m *Model) closeContextDetail() {
	m.context.openKey = ""
	m.context.hasOpen = false
	m.context.body = ""
	m.context.bodyTruncated = false
	m.context.openLabel = ""
	m.context.stale = false
}

// contextRowByKey finds a row of the current list by its stable key.
func (m *Model) contextRowByKey(key string) (contextRow, bool) {
	if key == "" {
		return contextRow{}, false
	}
	for _, row := range m.context.rows {
		if row.key == key {
			return row, true
		}
	}
	return contextRow{}, false
}

// markContextDetailStale reports whether a new snapshot would now produce a
// different body for the row on screen.
//
// The comparison is against the STORED body — what the reader is actually
// looking at — rather than against a revision counter, because the question is
// "would the reader be misled", and that is answered by what is on screen.
func (m *Model) markContextDetailStale() {
	if !m.context.hasOpen {
		return
	}
	row, ok := m.contextRowByKey(m.context.openKey)
	if !ok {
		// The row the reader was on is gone from the new snapshot. That is
		// also a change under their feet, and silently keeping the body would
		// show a section the pack no longer carries.
		m.context.stale = true
		return
	}
	if row.body != m.context.body || row.truncated != m.context.bodyTruncated {
		m.context.stale = true
	}
}

// ContextDetailStale reports that a newer snapshot would produce a different
// body for the row on screen.
func (m *Model) ContextDetailStale() bool { return m.context.stale }

// ContextDetailOpen reports whether a row's body is on screen.
//
// The key router reads it to decide whether the paging keys mean "move through
// the body" or "scroll the tab". Two things are scrollable in this tab, and a
// key that silently moved the wrong one is how a reader loses their place in a
// long section.
func (m *Model) ContextDetailOpen() bool { return m.context.hasOpen }

// NextContextScope cycles the scope selection forward, wrapping.
func (m *Model) NextContextScope() { m.cycleContextScopeBy(1) }

// PrevContextScope cycles the scope selection backward, wrapping.
func (m *Model) PrevContextScope() { m.cycleContextScopeBy(-1) }

// cycleContextScopeBy moves the scope selection within the offered set.
//
// Cycling rather than two named keys because there are exactly two scopes and a
// reader should not have to know which is which to get to the other one. The
// list is the offered set, so a third scope lands here and nothing else changes.
func (m *Model) cycleContextScopeBy(delta int) {
	if len(contextScopes) == 0 {
		return
	}
	idx := 0
	for i, s := range contextScopes {
		if s == m.context.scope {
			idx = i
			break
		}
	}
	next := contextScopes[(idx+delta+len(contextScopes))%len(contextScopes)]
	m.SetContextScope(next)
}

// ContextDetailTruncated reports whether the open row's SOURCE was capped, as
// opposed to merely being longer than the viewport. The two must not be
// confused: "scroll for more" and "there is no more" are different statements
// and only one of them is true.
func (m *Model) ContextDetailTruncated() bool { return m.context.bodyTruncated }

// ContextDetailScroll reports the detail body's scroll offset, so a caller can
// tell whether a key actually moved what the reader was looking at.
func (m *Model) ContextDetailScroll() int { return m.contextDetail().ScrollOffset() }

// PageContextDetail moves the detail body by whole viewports.
func (m *Model) PageContextDetail(delta int) {
	m.syncContextDetail()
	m.contextDetail().Page(delta)
}

// ContextDetailTop jumps the detail body to its first line.
func (m *Model) ContextDetailTop() {
	m.syncContextDetail()
	m.contextDetail().Top()
}

// ContextDetailBottom jumps the detail body to its last line and resumes
// following.
func (m *Model) ContextDetailBottom() {
	m.syncContextDetail()
	m.contextDetail().Bottom()
}

// syncContextDetail sizes the detail body from the model's own recorded area.
//
// The View arm does this too, for the running app. It is repeated here because
// a caller driving the body directly — a key handler in a test, or a caller
// that pages before the first frame — must not be paging a viewport with no
// height, which would silently move nothing and look like a broken key.
func (m *Model) syncContextDetail() { m.contextDetail().Resize(m.width, m.height) }

// CaptureContextCopy returns the open row's body for the clipboard, with a
// label naming what it is and whether it was capped.
//
// The label travels with the text because these bodies are BOUNDED: a prefix
// pasted as though it were whole surfaces as a broken file in another program,
// long after the feedback that said "Copied".
func (m *Model) CaptureContextCopy() (text string, label string, truncated bool, ok bool) {
	if !m.context.hasOpen || strings.TrimSpace(m.context.body) == "" {
		return "", "", false, false
	}
	label = "Copy context: " + m.context.openLabel
	if m.context.bodyTruncated {
		label += " (truncated)"
	}
	return m.context.body, label, m.context.bodyTruncated, true
}

// rebuildContextRows recomputes the scope's list from the active data, keeping
// the cursor inside the result.
func (m *Model) rebuildContextRows() {
	d := m.activeContextData()
	m.context.rows = buildContextRows(d, m.context.scope)
	if n := len(m.context.rows); n == 0 {
		m.context.setCursorAt(m.subjectKey(), 0)
	} else if c := m.ContextCursor(); c >= n {
		m.context.setCursorAt(m.subjectKey(), n-1)
	}
}

// buildContextRows lists the addressable items of one scope.
//
// Only CONTENT is a row. The attempt's own facts — when it ran, how it
// finished, which model served it — are preamble, because they describe the
// whole snapshot rather than any one line of it, and a row that opened a
// duplicate of the preamble would be a row with nothing behind it.
func buildContextRows(d ContextData, scope ContextScope) []contextRow {
	switch scope {
	case ContextScopePack:
		return buildPackRows(d)
	case ContextScopeRequest:
		return buildRequestRows(d)
	}
	return nil
}

// buildPackRows lists one row per pack section.
func buildPackRows(d ContextData) []contextRow {
	if !d.Pack.Known {
		return nil
	}
	rows := make([]contextRow, 0, len(d.Pack.Sections))
	for i, s := range d.Pack.Sections {
		label := contextSectionLabel(s)
		detail := contextSectionDetail(s)
		rows = append(rows, contextRow{
			// The key carries the ordinal as well as the label, because a pack
			// can legitimately hold two sections with the same title (two
			// snippets of one file) and they are different sections.
			key:       fmt.Sprintf("section:%s#%d", label, i),
			label:     label,
			detail:    detail,
			body:      contextSectionBody(s),
			truncated: s.ContentTruncated,
		})
	}
	return rows
}

// contextSectionLabel names a section, falling back through title, source and
// kind so a section is never an unlabelled row.
func contextSectionLabel(s ContextSection) string {
	switch {
	case s.Title != "":
		return s.Title
	case s.Source != "":
		return s.Source
	case s.Kind != "":
		return string(s.Kind)
	default:
		return "untitled section"
	}
}

// contextSectionDetail is the row's one-line summary: what kind of section it
// is, what it cost, and whether that cost was cut.
func contextSectionDetail(s ContextSection) string {
	parts := make([]string, 0, 4)
	if s.Kind != "" {
		parts = append(parts, s.Kind)
	}
	// The source's extra detail is shown: the part of it the title does not
	// already state. A pinned @file section carries "path:start-end" as its
	// source while its title is the bare path, so the line range is exactly the
	// part a reader cannot infer — and the only part worth the cells. Printing
	// the whole source would repeat the path the row already shows.
	if extra := contextSourceDetail(s); extra != "" {
		parts = append(parts, extra)
	}
	parts = append(parts, strutil.CompactTokens(s.EstimatedTokens)+" tokens (estimated)")
	if s.ContentTruncated {
		// The omitted count is reported HERE rather than in the body, so the
		// reader can see how much is missing without the number travelling
		// into a copy of the content.
		note := "truncated"
		if s.OmittedTokens > 0 {
			note += " (" + strutil.CompactTokens(s.OmittedTokens) + " tokens omitted)"
		}
		parts = append(parts, note)
	}
	return strings.Join(parts, " · ")
}

// contextSourceDetail returns the part of a section's source that its title
// does not already state, or "" when it adds nothing.
//
// The comparison is a string trim rather than a semantic parse, deliberately:
// the pack's sources are not a grammar Marshal owns (a @file snippet writes
// "path:start-end", a repo map writes "repository", a memory note writes
// nothing), and inventing a parser for them would produce a label that is a
// guess about a format that may change.
func contextSourceDetail(s ContextSection) string {
	source := strings.TrimSpace(s.Source)
	if source == "" {
		return ""
	}
	if source == strings.TrimSpace(s.Title) {
		return ""
	}
	// A source that begins with the title is that title plus a suffix, which is
	// the @file case: keep the suffix rather than repeating the path.
	if title := strings.TrimSpace(s.Title); title != "" && strings.HasPrefix(source, title) {
		if extra := strings.TrimSpace(strings.TrimPrefix(source, title)); extra != "" {
			return "from " + extra
		}
		return ""
	}
	return "from " + source
}

// contextSectionBody is the section's text as the pack holds it.
//
// The truncation disclosure is deliberately NOT spliced in here. This text is a
// copy source as well as a reading surface, and a marker appended to it would
// land in the reader's file as content that was never in their repository. The
// cut is disclosed where it belongs instead: in the row's summary, in the copy
// LABEL, and in the detail's own truncation footer. Task 7's captured patch
// draws that line in exactly the same place.
func contextSectionBody(s ContextSection) string { return s.Content }

// buildRequestRows lists one row per message and one per tool definition.
func buildRequestRows(d ContextData) []contextRow {
	if !d.Request.Known {
		return nil
	}
	req := d.Request
	rows := make([]contextRow, 0, len(req.Messages)+len(req.Tools))
	for i, msg := range req.Messages {
		label := fmt.Sprintf("#%d %s", i+1, contextMessageRole(msg))
		rows = append(rows, contextRow{
			key:       fmt.Sprintf("message:%d", i),
			label:     label,
			detail:    contextMessageDetail(msg),
			body:      contextMessageBody(msg),
			truncated: msg.Truncated,
		})
	}
	for i, tool := range req.Tools {
		rows = append(rows, contextRow{
			key:       fmt.Sprintf("tool:%d", i),
			label:     contextToolLabel(tool),
			detail:    contextToolDetail(tool),
			body:      contextToolBody(tool),
			truncated: tool.Truncated,
		})
	}
	return rows
}

// contextMessageRole names a message's role, saying so plainly when the runtime
// did not record one.
func contextMessageRole(msg ContextMessage) string {
	if msg.Role != "" {
		return msg.Role
	}
	return "(role not recorded)"
}

// contextMessageDetail is the row's one-line summary of a message body.
func contextMessageDetail(msg ContextMessage) string {
	parts := make([]string, 0, 3)
	if n := len(msg.ToolCalls); n > 0 {
		names := make([]string, 0, n)
		for _, tc := range msg.ToolCalls {
			names = append(names, tc.Name)
		}
		parts = append(parts, "calls "+strings.Join(names, ", "))
	}
	if msg.ToolCallID != "" {
		parts = append(parts, "answers "+msg.ToolCallID)
	}
	if preview := contextPreview(msg.Content); preview != "" {
		parts = append(parts, preview)
	}
	if msg.Truncated {
		parts = append(parts, "truncated")
	}
	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, " · ")
}

// contextMessageBody is the message's full text, REDACTED, with the tool calls
// that travelled with it.
//
// Redaction happens here rather than at the display site because this is also
// the copy source: one function serves both, so a masked panel cannot sit
// beside an unmasked clipboard.
func contextMessageBody(msg ContextMessage) string {
	var b strings.Builder
	b.WriteString(redactContextText(msg.Content))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	for _, tc := range msg.ToolCalls {
		b.WriteString("\n" + contextToolCallLine(tc) + "\n")
	}
	if msg.ToolCallID != "" {
		b.WriteString("\n[answers tool call " + msg.ToolCallID + "]\n")
	}
	if msg.Truncated {
		b.WriteString("\n" + contextOmittedNote(msg.OmittedBytes) + "\n")
	}
	return b.String()
}

// contextToolCallLine renders one tool call attached to a message.
func contextToolCallLine(tc ContextToolCall) string {
	name := tc.Name
	if name == "" {
		name = "(tool not recorded)"
	}
	line := "call " + name
	if tc.ID != "" {
		line += " [" + tc.ID + "]"
	}
	line += "\n" + redactContextText(tc.Args)
	if tc.Truncated {
		line += "\n" + contextOmittedNote(tc.OmittedBytes)
	}
	return line
}

// contextToolLabel names a tool definition.
func contextToolLabel(tool ContextTool) string {
	if tool.Name != "" {
		return tool.Name
	}
	return "(tool not recorded)"
}

// contextToolDetail is the row's one-line summary of a tool definition.
func contextToolDetail(tool ContextTool) string {
	parts := make([]string, 0, 2)
	if preview := contextPreview(tool.Description); preview != "" {
		parts = append(parts, preview)
	}
	if tool.Truncated {
		parts = append(parts, "truncated")
	}
	if len(parts) == 0 {
		return "(no description recorded)"
	}
	return strings.Join(parts, " · ")
}

// contextToolBody is the tool's full definition, redacted.
func contextToolBody(tool ContextTool) string {
	var b strings.Builder
	b.WriteString(contextToolLabel(tool) + "\n")
	if strings.TrimSpace(tool.Description) != "" {
		b.WriteString("\n" + redactContextText(tool.Description) + "\n")
	}
	if strings.TrimSpace(tool.Parameters) != "" {
		b.WriteString("\nparameters:\n" + redactContextText(tool.Parameters) + "\n")
	}
	if tool.Truncated {
		b.WriteString("\n" + contextOmittedNote(tool.OmittedBytes) + "\n")
	}
	return b.String()
}

// contextOmittedNote describes a bounded field.
func contextOmittedNote(bytes int) string {
	if bytes <= 0 {
		return "[truncated — the rest is not held in this snapshot]"
	}
	return fmt.Sprintf("[truncated — %d bytes omitted from this snapshot]", bytes)
}

// contextPreview renders a one-line preview of a body, for a list row.
//
// The preview is REDACTED like the body it summarises. A masked detail behind
// an unmasked row still leaks the credential that row is a preview of.
func contextPreview(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	line := redactContextText(s)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return ansi.Truncate(strings.TrimSpace(line), 60, "…")
}

// redactContextText masks secret-bearing values for display and for copying.
//
// Only the REQUEST scope is redacted. The pack carries the reader's own
// repository content and ordinary code, which is the same class of text Task 4's
// code-copy actions deliberately leave alone; masking it would corrupt
// legitimate transcripts and teach the reader to ignore the mask.
func redactContextText(s string) string { return redact.Secrets(s) }

// contextTextRedacted reports whether redaction changed any of a request's
// text, so the panel can say the mask is the APP's doing rather than something
// the request itself contained.
func contextTextRedacted(req ContextRequest) bool {
	changed := func(s string) bool { return redact.Secrets(s) != s }
	if changed(req.Err) {
		return true
	}
	for _, m := range req.Messages {
		if changed(m.Content) {
			return true
		}
		for _, tc := range m.ToolCalls {
			if changed(tc.Args) {
				return true
			}
		}
	}
	for _, t := range req.Tools {
		if changed(t.Description) || changed(t.Parameters) {
			return true
		}
	}
	return false
}

// contextAge renders when something happened, relative to the snapshot's clock.
//
// An unrecorded time is said to be unrecorded. Rendering it as "just now" would
// put a fabricated instant beside a real one with nothing to tell them apart.
//
// With no clock supplied the ABSOLUTE time is rendered rather than falling back
// to time.Now. A render that read the wall clock would produce different text
// on every frame, which makes the panel's own scroll position meaningless and
// makes a snapshot impossible to assert on. An absolute time is still true, and
// it does not move.
func contextAge(at, now time.Time) string {
	if at.IsZero() {
		return "time not recorded"
	}
	if now.IsZero() {
		return "at " + at.Format("15:04:05")
	}
	age := now.Sub(at)
	if age < time.Minute {
		return "just now"
	}
	return strutil.HumanAge(age) + " ago"
}

// contextHeading renders the scope's identity and its whole-snapshot facts.
//
// The subject and the scope get a line each. On one line they read as a single
// sentence of about forty characters, which is wider than the narrowest panel
// the rail is allowed to open at (tui.side_panel.min_cols, default 30) — so the
// single-line form was truncated exactly where it mattered, leaving a reader at
// the supported minimum unable to tell which agent they were looking at.
//
// Naming both in every combination is the point. A child heading that replaced
// the scope name would leave the reader unable to tell whether they were
// looking at the child's pack or its last request, which is the one question
// this panel exists to answer.
func contextHeading(d ContextData, scope ContextScope, childName string, width int) []string {
	subject := "Context — the conversation"
	if childName != "" {
		subject = "Context — " + childName
	}
	scopeName := "current pack"
	if scope == ContextScopeRequest {
		scopeName = "last conversation request"
	}

	lines := []string{subject, scopeName}
	if d.Child {
		lines = append(lines, "this is a CHILD agent's own context, not the conversation's")
	}
	switch scope {
	case ContextScopePack:
		lines = append(lines, contextPackSummary(d.Pack, d.Now)...)
	case ContextScopeRequest:
		lines = append(lines, contextRequestSummary(d.Request, d.Now)...)
	}
	// The width budget is applied HERE rather than left to the caller, because
	// the caller counts these lines as ROWS to budget the list against. A heading
	// that returned unclamped lines would be counted as one row each and then
	// wrap into several in the terminal, so the panel would spend its budget on
	// a document other than the one it draws and the surplus would escape into
	// the frame. Clamping keeps the count and the render describing the same rows.
	//
	// The width is not theoretical: the subject embeds a child agent's LABEL,
	// which is author text and can be arbitrarily long.
	return clampLines(lines, width)
}

// clampLines truncates each line to width display cells.
//
// The slice's LENGTH is deliberately preserved: callers use it as a row count, so
// dropping or joining lines here would silently change the panel's budget.
func clampLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = clampToWidth(line, width)
	}
	return out
}

// contextPackSummary describes the pack as a whole.
//
// A pack with nothing in it contributes NO heading lines. The empty state is
// already stated below by contextEmptyNote, and saying it twice is how a reader
// learns to skim past the sentence that matters — as well as making an unbuilt
// pack and an empty one read as the same paragraph repeated.
func contextPackSummary(p ContextPack, now time.Time) []string {
	if !p.Known || len(p.Sections) == 0 {
		return nil
	}
	line := fmt.Sprintf("generated %s · %s / %s tokens (estimated) · %d sections",
		contextAge(p.GeneratedAt, now),
		strutil.CompactTokens(p.EstimatedTokens),
		strutil.CompactTokens(p.MaxTokens),
		len(p.Sections))
	if p.Truncated {
		line += " · the pack budget cut content"
	}
	return []string{
		line,
		// The label is the whole point of the sentence: these numbers are the
		// PACK's own estimate against the PACK's own budget, and a reader must
		// not take them for the model's context window, which is a different
		// number arrived at a different way.
		"tokens are the pack's own estimate against its own budget, not the model's context limit",
		"",
	}
}

// contextRequestSummary describes one attempt as a whole.
func contextRequestSummary(req ContextRequest, now time.Time) []string {
	if !req.Known {
		return []string{
			"No request has been submitted yet, so there is nothing to inspect.",
			"A request snapshot is held in memory only, so a session loaded from disk has none.",
			"",
		}
	}

	out := []string{"submitted " + contextAge(req.At, now) + " · " + contextAttemptStatus(req)}
	out = append(out, contextRouteLine(req))
	if p := contextOptionsLine(req); p != "" {
		out = append(out, p)
	}
	if req.PackKnown {
		out = append(out, fmt.Sprintf(
			"context pack at dispatch: %s / %s estimated (the pack's own budget, not the model's context limit) · %d sections%s",
			strutil.CompactTokens(req.PackTokens),
			strutil.CompactTokens(req.PackWindow),
			req.PackSections,
			contextCutSuffix(req.PackTruncated)))
	}
	out = append(out, contextCapsLine(req))
	// Redaction is disclosed only when it actually happened. A redaction
	// warning on every request teaches the reader to ignore the one that
	// matters.
	if contextTextRedacted(req) {
		out = append(out, "content below has been redacted for display and copying ("+redact.MaskToken+")")
	}
	out = append(out, "")
	return out
}

// contextAttemptStatus names the attempt's outcome, including why it failed.
func contextAttemptStatus(req ContextRequest) string {
	status := req.Status
	if status == "" {
		return "outcome not recorded"
	}
	line := status
	if req.Err != "" {
		// A provider's error string is arbitrary text about the same request,
		// and it can embed a credential (a URL with a query token, a header
		// echoed back). It goes through the same redaction as the body rather
		// than being trusted because it is "just an error".
		line += " — " + redactContextText(req.Err)
	}
	return line
}

// contextRouteLine names the model and provider that served the attempt.
//
// Missing provenance is REPORTED, not filled in. A fabricated provider beside
// a real timestamp is indistinguishable on screen from a recorded one.
func contextRouteLine(req ContextRequest) string {
	if req.Provider == "" && req.Model == "" {
		return "route not recorded for this attempt"
	}
	var b strings.Builder
	switch {
	case req.Model != "" && req.Provider != "":
		b.WriteString(req.Model + " @ " + req.Provider)
	case req.Model != "":
		b.WriteString(req.Model)
	default:
		b.WriteString(req.Provider)
	}
	if req.Generation != "" {
		b.WriteString(" · generation " + req.Generation)
	} else {
		b.WriteString(" · generation not recorded")
	}
	return b.String()
}

// contextOptionsLine lists the request options that actually went out.
func contextOptionsLine(req ContextRequest) string {
	parts := make([]string, 0, 5)
	if req.Thinking != "" {
		parts = append(parts, "thinking "+req.Thinking)
	}
	if req.HasMaxTokens {
		parts = append(parts, fmt.Sprintf("max output %d", req.MaxTokens))
	}
	if req.HasTemperature {
		parts = append(parts, fmt.Sprintf("temperature %g", req.Temperature))
	}
	if req.ToolChoice != "" {
		parts = append(parts, "tool choice "+req.ToolChoice)
	}
	if req.ResponseFormat != "" {
		parts = append(parts, "response format "+req.ResponseFormat)
	}
	if req.Streaming {
		parts = append(parts, "streaming")
	}
	if len(parts) == 0 {
		return "no request options were recorded"
	}
	return strings.Join(parts, " · ")
}

// contextCapsLine discloses the snapshot's own bounds.
func contextCapsLine(req ContextRequest) string {
	line := fmt.Sprintf("%d messages · %d tools", len(req.Messages), len(req.Tools))
	if req.MessagesOmitted > 0 {
		line += fmt.Sprintf(" · %d messages omitted", req.MessagesOmitted)
	}
	if req.ToolsOmitted > 0 {
		line += fmt.Sprintf(" · %d tools omitted", req.ToolsOmitted)
	}
	if req.Truncated {
		line += " · truncated"
		if req.OmittedBytes > 0 {
			line += fmt.Sprintf(", %d bytes omitted", req.OmittedBytes)
		}
	}
	if req.AttemptID != 0 {
		line += fmt.Sprintf(" · attempt #%d", req.AttemptID)
	}
	return line
}

// contextCutSuffix renders the pack-truncation marker.
func contextCutSuffix(cut bool) string {
	if cut {
		return " (cut)"
	}
	return ""
}

// viewContext renders the Context tab.
func (m *Model) viewContext() string {
	d := m.activeContextData()
	scope := m.context.scope
	width := max(m.width, 20)
	heading := contextHeading(d, scope, m.context.childName, width)

	// An UNMEASURED panel renders every row, following the same rule the detail
	// body uses for an unmeasured width.
	if m.height <= 0 {
		var b strings.Builder
		for _, line := range heading {
			b.WriteString(m.contextLine(line, width))
			b.WriteString("\n")
		}
		if len(m.context.rows) == 0 {
			b.WriteString(m.contextLine(contextEmptyNote(d, scope), width))
			b.WriteString("\n")
			return b.String()
		}
		for i, row := range m.context.rows {
			b.WriteString(m.contextLine(m.contextRowLine(i, row), width))
			b.WriteString("\n")
		}
		return b.String()
	}

	// The tab is BUDGETED end to end, for the same reason the other two are:
	// the panel is joined into the frame as a second column, and a join pads the
	// shorter column to the taller one, so rows emitted beyond m.height escape
	// into the frame and push the status line off the bottom. Every row goes
	// through the budget, including the blank line before the body and the stale
	// note after it — an uncounted row is how this panel used to overshoot.
	rb := newRowBudget(m.height)
	for _, line := range heading {
		rb.line(m.contextLine(line, width))
	}

	if len(m.context.rows) == 0 {
		rb.line(m.contextLine(contextEmptyNote(d, scope), width))
		return rb.String()
	}

	// The list and the detail body SHARE what is left. The stale note (one row)
	// is reserved FIRST, because it is conditional and its row must not be
	// handed to the list or the body.
	rows := rb.left()
	if m.context.hasOpen && m.context.stale {
		rows--
	}
	rows = max(rows, 1)

	bodyRows := 0
	if m.context.hasOpen && rows >= 3 {
		// The body wants a blank separator row plus at least one content row.
		bodyShare := max((rows-1)/2, 1)
		bodyRows = min(bodyShare, rows-2)
		rows -= bodyRows + 1
	}
	listRows := max(rows, 1)

	w := windowList(len(m.context.rows), listRows, m.ContextCursor(), m.State(TabContext).Scroll, 2)
	if w.ShowAbove() {
		rb.line(m.contextLine(aboveNote(w.Above(), m.width), width))
	}
	for i := w.Start; i < w.End; i++ {
		rb.line(m.contextLine(m.contextRowLine(i, m.context.rows[i]), width))
	}
	if w.ShowBelow() {
		rb.line(m.contextLine(belowNote(w.Below(), m.width), width))
	}

	if m.context.hasOpen && bodyRows > 0 {
		rb.blank()
		// The body gets ITS share, so it windows its own content to what it was
		// given rather than to the whole panel.
		detail := m.contextDetail()
		detail.Resize(m.width, bodyRows)
		// Re-wrapped per frame so a resize reflows the body rather than
		// leaving rows cut for the old width.
		m.contextWrapBody()
		rb.body(detail.View(m.context.openLabel))
		if m.context.stale {
			// The reader is told rather than moved: the body on screen is
			// theirs to finish reading, and the note says the snapshot behind
			// it has moved on.
			rb.line(m.contextLine(
				"[this snapshot changed since you opened it — press Enter again to refresh]", width))
		}
	}
	return rb.String()
}

// contextRowLine renders one list row: the cursor marker, its label, and the
// row's one-line detail.
func (m *Model) contextRowLine(index int, row contextRow) string {
	cursor := "  "
	if index == m.ContextCursor() {
		cursor = "▸ "
	}
	line := cursor + row.label
	if row.detail != "" {
		line += "  " + row.detail
	}
	return line
}

// contextLine truncates a rendered line to the panel width.
//
// A row that overflows wraps, which shifts every row below it and pushes the
// panel's own chrome off the bottom — the reader then loses what they were
// reading because a section had a long name.
//
// It uses ansi.Truncate rather than strutil.Truncate, and that is not a
// preference: strutil.Truncate cuts to N runes and THEN appends the ellipsis,
// so it returns N+1 cells and every truncated row would be one cell wider than
// the frame it is joined into. ansi.Truncate budgets the ellipsis inside the
// width, and it counts cells rather than runes, so a wide or combining
// character does not overshoot either.
func (m *Model) contextLine(s string, width int) string {
	return ansi.Truncate(s, max(width, 1), "…")
}

// contextEmptyNote explains an empty scope.
//
// The reasons differ and so does the reader's next step, so each says which one
// it is: nothing has been assembled, or nothing was recorded for the scope.
func contextEmptyNote(d ContextData, scope ContextScope) string {
	switch scope {
	case ContextScopeRequest:
		if d.Request.Known {
			return "This request holds no messages and no tool definitions."
		}
		return "No request has been submitted in this conversation yet."
	default:
		if !d.Pack.Known {
			return "The context pack has not been built yet."
		}
		return "The context pack is empty — no sections were admitted."
	}
}
