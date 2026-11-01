package harness

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
	"github.com/mattn/go-runewidth"
)

// maxSeqCapture bounds the raw sequence bytes retained for error messages.
const maxSeqCapture = 128

// maxPayloadBytes mirrors the 64 KiB data buffer ansi.NewParser grants string
// sequences (OSC/DCS/SOS/PM/APC). Once a single sequence's payload outgrows
// it the parser silently drops bytes, so the model latches a sticky error
// instead of decoding a truncated guess.
const maxPayloadBytes = 64 * 1024

// termCell is a single viewport cell. A cell either holds the head of a
// grapheme cluster (g != "", w >= 1), the continuation of a wide grapheme
// started in the previous cell (cont), or is blank. A zero-value cell
// renders as a space.
type termCell struct {
	g    string
	w    int
	cont bool
}

// savedCursor is the cursor state stored by CSI s (SCP) and ESC 7 (DECSC).
type savedCursor struct {
	row, col int
	pending  bool
}

// headPos points at the head cell of the most recently printed grapheme so
// combining marks can extend it. ok is false until something is printed and
// once the head scrolls off the top of the viewport.
type headPos struct {
	row, col int
	ok       bool
}

// screenState is one screen buffer together with the cursor state that
// belongs to it. The primary and alternate screens each keep their own
// buffer and cursor: the application draws to whichever one lives in the
// terminalModel fields, and the other one waits here until the next switch.
type screenState struct {
	cells       [][]termCell
	row, col    int
	wrapPending bool
	head        headPos
}

// terminalModel incrementally decodes PTY output into an accurate bounded
// viewport for common writes, movement and erasure. Parser state is retained
// across Write calls, so a stream split at any byte boundary decodes the
// same as the whole stream in one write. Decoding and
// unsupported-sequence errors are sticky: Write always reports success and
// the error surfaces through Snapshot.Err, so a model error can never kill a
// PTY read loop.
type terminalModel struct {
	p *ansi.Parser

	width, height int
	cells         [][]termCell

	row         int
	col         int
	wrapPending bool
	saved       savedCursor
	head        headPos

	// autowrap is DECAWM (?7), on by default. With autowrap off, writing in
	// the last column overwrites that cell instead of moving to the next
	// line, so it changes visible cells and must be modelled.
	autowrap bool

	// topMargin and bottomMargin bound the scrolling region set by DECSTBM
	// (CSI r), 0-based and inclusive. They default to the whole viewport, so
	// a renderer that never sets a region sees plain full-screen scrolling;
	// one that pins header and footer rows (as the TUI does at 80x24) scrolls
	// only the rows it chose.
	topMargin    int
	bottomMargin int

	// primary and alt hold the inactive screen. Exactly one of them is
	// live: primary while altActive, alt otherwise. Snapshot renders only
	// the live buffer, so the inactive screen never reaches a snapshot.
	primary   screenState
	alt       screenState
	altActive bool

	err error

	inSeq        bool
	seq          []byte
	payloadBytes int
}

// newTerminalModel returns a model for a width x height viewport.
func newTerminalModel(width, height int) *terminalModel {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	m := &terminalModel{
		width:        width,
		height:       height,
		cells:        blankCells(width, height),
		alt:          screenState{cells: blankCells(width, height)},
		autowrap:     true,
		bottomMargin: height - 1,
	}
	m.p = ansi.NewParser()
	m.p.SetHandler(ansi.Handler{
		Print:     m.print,
		Execute:   m.execute,
		HandleCsi: m.handleCsi,
		HandleEsc: m.handleEsc,
		HandleDcs: m.handleDcs,
		HandleOsc: m.handleOsc,
		HandlePm:  m.handlePm,
		HandleApc: m.handleApc,
		HandleSos: m.handleSos,
	})
	return m
}

// Write decodes p into the viewport. It always returns len(p) with a nil
// error: a decode failure latches the model's sticky error and surfaces
// through Snapshot.Err.
func (m *terminalModel) Write(p []byte) (int, error) {
	for _, b := range p {
		if m.err != nil {
			break // Sticky: swallow the remainder.
		}
		starting := !m.inSeq && b == 0x1b
		if starting {
			m.seq = m.seq[:0]
			m.payloadBytes = 0
		}
		if starting || m.inSeq {
			// Keep the raw bytes of the current sequence for error messages,
			// bounded by maxSeqCapture.
			if len(m.seq) < maxSeqCapture {
				m.seq = append(m.seq, b)
			}
		}
		act := m.p.Advance(b)
		m.inSeq = m.p.State() != parser.GroundState
		if act == parser.PutAction {
			m.payloadBytes++
			if m.payloadBytes > maxPayloadBytes {
				m.fail("control sequence payload exceeds the %d byte parser buffer bound (sequence starts %s)", maxPayloadBytes, m.seqQuoted())
				break
			}
		}
	}
	return len(p), nil
}

// Snapshot returns the active viewport as plain UTF-8 plus the sticky error,
// if any. Lines always has exactly height entries, blank rows are "" and no
// line carries trailing cell padding; Content is strings.Join(Lines, "\n").
func (m *terminalModel) Snapshot() Snapshot {
	lines := make([]string, m.height)
	var b strings.Builder
	for r := 0; r < m.height; r++ {
		b.Reset()
		col := 0
		for col < m.width {
			c := m.cells[r][col]
			if c.g != "" {
				// Head of a grapheme: emit it once and skip the continuation
				// cells it covers.
				b.WriteString(c.g)
				col += c.w
				if c.w < 1 {
					col++
				}
			} else {
				// Blank, or a continuation whose head was erased: renders as
				// a space so the row below stays aligned.
				b.WriteByte(' ')
				col++
			}
		}
		lines[r] = strings.TrimRight(b.String(), " ")
	}
	return Snapshot{
		Width:   m.width,
		Height:  m.height,
		Content: []byte(strings.Join(lines, "\n")),
		Lines:   lines,
		Err:     m.err,
	}
}

// Resize changes the model dimensions and clips retained cells on both the
// primary and the alternate buffer. Buffers keep the upper-left intersection
// of the old viewport: rows are never reflowed, added cells are blank and
// both cursors clamp inside the new viewport. It rejects nonpositive sizes
// without touching the model, and otherwise reports the sticky error, if any.
func (m *terminalModel) Resize(width, height int) error {
	if width < 1 || height < 1 {
		return fmt.Errorf("terminal model: resize to %dx%d: width and height must be at least 1", width, height)
	}
	if width == m.width && height == m.height {
		return m.err
	}
	live := m.screen()
	live.resize(width, height)
	m.width, m.height = width, height
	m.installScreen(live)
	// The inactive screen keeps its own buffer and cursor for the next
	// switch, so it is clipped to the new viewport as well: switching back
	// must never install cells with stale dimensions.
	if m.altActive {
		m.alt = live
		m.primary.resize(width, height)
	} else {
		m.primary = live
		m.alt.resize(width, height)
	}
	m.saved.row = clampTo(m.saved.row, height)
	m.saved.col = clampTo(m.saved.col, width)
	// A resize resets the scrolling region to the whole viewport, so a
	// renderer that set margins for the old size starts from a clean one.
	m.topMargin, m.bottomMargin = 0, height-1
	return m.err
}

// print writes a printable rune at the cursor using its terminal cell width.
func (m *terminalModel) print(r rune) {
	if m.err != nil {
		return
	}
	w := runewidth.RuneWidth(r)
	if w < 1 {
		// Combining mark: extend the preceding grapheme instead of occupying
		// a cell.
		if m.head.ok {
			if c := &m.cells[m.head.row][m.head.col]; c.g != "" && !c.cont {
				c.g += string(r)
			}
		}
		return
	}
	if m.wrapPending {
		m.wrap()
	} else if m.col+w > m.width && m.autowrap {
		// Wrap before drawing, including a two-cell grapheme that would
		// straddle the right edge.
		m.wrap()
	}
	if w > m.width {
		w = 1 // Degenerate viewport narrower than one wide cell.
	}
	if m.col+w > m.width {
		// Autowrap is off and the grapheme cannot fit in the last column: it
		// is not drawn and never split across rows.
		return
	}
	row, col := m.row, m.col
	// Overwriting either half of a wide cell clears the entire prior
	// grapheme.
	if m.cellAt(row, col).cont {
		m.setBlank(row, col-1)
	}
	if m.cellAt(row, col+w).cont {
		m.setBlank(row, col+w)
	}
	m.cells[row][col] = termCell{g: string(r), w: w}
	if w > 1 {
		m.cells[row][col+1] = termCell{cont: true}
	}
	m.head = headPos{row: row, col: col, ok: true}
	m.col += w
	if m.col >= m.width {
		m.col = m.width - 1
		m.wrapPending = m.autowrap
	}
}

// execute handles C0 control characters. Everything that does not affect
// cells (BEL, NUL, DEL, ...) is ignored.
func (m *terminalModel) execute(b byte) {
	if m.err != nil {
		return
	}
	switch b {
	case 0x0D: // CR
		m.col = 0
		m.wrapPending = false
	case 0x0A, 0x0B, 0x0C: // LF, VT, FF
		m.wrapPending = false
		if m.row == m.bottomMargin {
			m.scrollUp()
		} else if m.row < m.height-1 {
			m.row++
		}
	case 0x08: // BS
		m.wrapPending = false
		if m.col > 0 {
			m.col--
		}
	case 0x09: // TAB to the next 8-column stop
		m.wrapPending = false
		next := (m.col/8 + 1) * 8
		if next >= m.width {
			next = m.width - 1
		}
		m.col = next
	}
}

// handleCsi implements cursor addressing, erasure and save/restore, and
// ignores the harmless style/report families. Any other CSI is
// cell-changing but unsupported, so it fails the model.
func (m *terminalModel) handleCsi(cmd ansi.Cmd, params ansi.Params) {
	if m.err != nil {
		return
	}
	final := cmd.Final()
	switch cmd.Prefix() {
	case '?':
		switch final {
		case 'h', 'l': // private modes: alternate screen plus the screen-neutral ones
			set := final == 'h'
			params.ForEach(0, func(_ int, mode int, _ bool) {
				if m.err != nil {
					return
				}
				switch mode {
				case 47: // alternate screen, keeping its previous contents
					m.setAltScreen(set, false)
				case 1047, 1049: // alternate screen, cleared before use
					m.setAltScreen(set, true)
				case 7: // DECAWM autowrap: changes where the next cell lands
					m.autowrap = set
				default:
					if !ignorablePrivateMode(mode) {
						m.fail("unsupported DEC private mode %d in %s", mode, m.seqQuoted())
					}
				}
			})
		case 'n': // private DSR
		case 'p': // DECRQM mode query: a report, no cells
		case 'c', 'u', 'W': // DA1, kitty-protocol and DECRPM reports: no cells
		default:
			m.failUnsupported()
		}
	case 0:
		switch final {
		case 'H', 'f': // CUP/HVP absolute positioning
			m.moveTo(m.clampRow(cursorCount(params, 0)-1), m.clampCol(cursorCount(params, 1)-1))
		case 'A': // CUU
			m.moveTo(m.clampRow(m.row-cursorCount(params, 0)), m.col)
		case 'B': // CUD
			m.moveTo(m.clampRow(m.row+cursorCount(params, 0)), m.col)
		case 'C': // CUF
			m.moveTo(m.row, m.clampCol(m.col+cursorCount(params, 0)))
		case 'D': // CUB
			m.moveTo(m.row, m.clampCol(m.col-cursorCount(params, 0)))
		case 'G': // CHA column absolute
			m.moveTo(m.row, m.clampCol(cursorCount(params, 0)-1))
		case 'd': // VPA row absolute
			m.moveTo(m.clampRow(cursorCount(params, 0)-1), m.col)
		case 'J': // ED erase display
			m.eraseDisplay(eraseMode(params))
		case 'K': // EL erase line
			m.eraseLine(eraseMode(params))
		case 'X': // ECH erase characters from the cursor, without moving it
			m.eraseSpan(m.row, m.col, m.col+cursorCount(params, 0))
		case 's': // SCP save cursor
			m.saveCursor()
		case 'u': // RCP restore cursor
			m.restoreCursor()
		case 'r': // DECSTBM set top/bottom scrolling margins
			m.setScrollRegion(params)
		case 'S': // SU scroll the region up, leaving the cursor put
			m.scrollLines(-cursorCount(params, 0))
		case 'T': // SD scroll the region down, leaving the cursor put
			m.scrollLines(cursorCount(params, 0))
		case 'm', 'n', 'q', 'c': // SGR, DSR, cursor style, DA1 device-attributes query
		default:
			m.failUnsupported()
		}
	case '>':
		switch final {
		case 'c', 'n', 'q': // DA2, DSR and XTVERSION queries: reports, no cells
		case 'm': // modifyOtherKeys: keyboard reporting only, no cells
		default:
			m.failUnsupported()
		}
	case '=':
		switch final {
		case 'u': // kitty keyboard protocol: keyboard reporting only, no cells
		default:
			m.failUnsupported()
		}
	default:
		m.failUnsupported()
	}
}

// handleEsc implements DECSC/DECRC and RI. The ST terminator dispatched as
// ESC \ after a string sequence is ignored; anything else is unsupported.
func (m *terminalModel) handleEsc(cmd ansi.Cmd) {
	if m.err != nil {
		return
	}
	switch cmd.Final() {
	case '7': // DECSC save cursor
		m.saveCursor()
	case '8': // DECRC restore cursor
		m.restoreCursor()
	case 'M': // RI, reverse index
		m.reverseIndex()
	case '\\': // ST terminating an ESC \ string sequence
	default:
		m.failUnsupported()
	}
}

// handleOsc ignores the sequences that cannot change visible cells: window
// titles (OSC 0/2) and the foreground/background/cursor colour queries and
// sets (OSC 10/11/12) the TUI issues at startup. Any other OSC is unsupported
// and fails the model.
func (m *terminalModel) handleOsc(cmd int, data []byte) {
	if m.err != nil {
		return
	}
	switch cmd {
	case 0, 2: // window title
	case 10, 11, 12: // foreground, background and cursor colour get/set
	default:
		m.failUnsupported()
	}
}

// handleDcs fails the model: no DCS sequence is supported.
func (m *terminalModel) handleDcs(cmd ansi.Cmd, params ansi.Params, data []byte) {
	if m.err == nil {
		m.failUnsupported()
	}
}

// handleSos fails the model: no SOS sequence is supported.
func (m *terminalModel) handleSos(data []byte) {
	if m.err == nil {
		m.failUnsupported()
	}
}

// handlePm fails the model: no PM sequence is supported.
func (m *terminalModel) handlePm(data []byte) {
	if m.err == nil {
		m.failUnsupported()
	}
}

// handleApc fails the model: no APC sequence is supported.
func (m *terminalModel) handleApc(data []byte) {
	if m.err == nil {
		m.failUnsupported()
	}
}

// saveCursor stores the cursor position and wrap-pending state.
func (m *terminalModel) saveCursor() {
	m.saved = savedCursor{row: m.row, col: m.col, pending: m.wrapPending}
}

// restoreCursor restores the saved cursor, clamped to the viewport. Without
// a prior save this homes the cursor.
func (m *terminalModel) restoreCursor() {
	m.row = m.clampRow(m.saved.row)
	m.col = m.clampCol(m.saved.col)
	m.wrapPending = m.saved.pending && m.col == m.width-1
}

// moveTo positions the cursor; all movement clears wrap-pending.
func (m *terminalModel) moveTo(row, col int) {
	m.row, m.col = row, col
	m.wrapPending = false
}

// screen returns the state of the screen the application is drawing to.
func (m *terminalModel) screen() screenState {
	return screenState{
		cells:       m.cells,
		row:         m.row,
		col:         m.col,
		wrapPending: m.wrapPending,
		head:        m.head,
	}
}

// installScreen makes s the screen the application draws to.
func (m *terminalModel) installScreen(s screenState) {
	m.cells, m.row, m.col, m.wrapPending, m.head = s.cells, s.row, s.col, s.wrapPending, s.head
}

// setAltScreen applies a DEC private mode transition for the alternate
// screen. Entering with mode 47 keeps the alternate buffer's previous
// contents, while 1047 and 1049 clear it first. Leaving always restores the
// primary buffer and its cursor untouched, and a repeated enter or exit is
// a no-op so the visible buffer is never cleared underneath the application.
func (m *terminalModel) setAltScreen(set, clear bool) {
	if set {
		m.enterAltScreen(clear)
	} else {
		m.exitAltScreen()
	}
}

// enterAltScreen switches to the alternate buffer, clearing it first when
// clear is set.
func (m *terminalModel) enterAltScreen(clear bool) {
	if m.altActive {
		return
	}
	m.primary = m.screen()
	m.altActive = true
	if clear {
		m.alt = screenState{cells: blankCells(m.width, m.height)}
	}
	m.installScreen(m.alt)
	// Switching screens resets the scrolling region, as a real terminal
	// does; the renderer sets it again for the screen it draws next.
	m.topMargin, m.bottomMargin = 0, m.height-1
}

// exitAltScreen restores the primary buffer and its cursor.
func (m *terminalModel) exitAltScreen() {
	if !m.altActive {
		return
	}
	m.alt = m.screen()
	m.altActive = false
	m.installScreen(m.primary)
	m.topMargin, m.bottomMargin = 0, m.height-1
}

// resize clips the screen buffer to width x height and clamps its cursor.
func (s *screenState) resize(width, height int) {
	s.cells = resizeCells(s.cells, width, height)
	s.row = clampTo(s.row, height)
	s.col = clampTo(s.col, width)
	s.wrapPending = false
	s.head = liveHead(s.cells, s.head)
}

// blankCells allocates a blank width x height cell buffer.
func blankCells(width, height int) [][]termCell {
	cells := make([][]termCell, height)
	for r := range cells {
		cells[r] = make([]termCell, width)
	}
	return cells
}

// resizeCells clips cells to width x height, keeping the upper-left
// intersection: rows and columns outside the new viewport are dropped rather
// than reflowed, and added cells are blank.
func resizeCells(cells [][]termCell, width, height int) [][]termCell {
	out := make([][]termCell, height)
	for r := range out {
		row := make([]termCell, width)
		if r < len(cells) {
			copy(row, cells[r])
		}
		// A wide grapheme whose continuation cell fell outside the new width
		// is cleared: half a glyph would misalign the rest of the row. In a
		// well-formed buffer a multi-cell head only sits in the last retained
		// column when its continuation was clipped.
		if row[width-1].w > 1 {
			row[width-1] = termCell{}
		}
		out[r] = row
	}
	return out
}

// liveHead returns h when it still points at the head of a grapheme inside
// cells, and the zero position once clipping, erasure or scrolling moved
// that grapheme away.
func liveHead(cells [][]termCell, h headPos) headPos {
	if !h.ok || h.row < 0 || h.row >= len(cells) || h.col < 0 || h.col >= len(cells[h.row]) {
		return headPos{}
	}
	if c := cells[h.row][h.col]; c.g == "" || c.cont {
		return headPos{}
	}
	return h
}

// clampTo clamps v into [0, limit-1].
func clampTo(v, limit int) int {
	if v < 0 {
		return 0
	}
	if v >= limit {
		return limit - 1
	}
	return v
}

// wrap moves the cursor to the start of the next line, scrolling the
// scrolling region up when already at its bottom row.
func (m *terminalModel) wrap() {
	m.wrapPending = false
	if m.row == m.bottomMargin {
		m.scrollUp()
	} else if m.row < m.height-1 {
		m.row++
	}
	m.col = 0
}

// scrollUp shifts the scrolling region up one row and blanks its last row.
func (m *terminalModel) scrollUp() {
	top, bottom := m.topMargin, m.bottomMargin
	copy(m.cells[top:bottom], m.cells[top+1:bottom+1])
	// A fresh row: reusing the old backing array would alias the row that
	// just moved up.
	m.cells[bottom] = make([]termCell, m.width)
	if m.head.ok {
		switch {
		case m.head.row < top, m.head.row > bottom:
			// Outside the region, so the scroll does not touch it.
		case m.head.row == top:
			// Its row scrolled off the top of the region.
			m.head = headPos{}
		default:
			m.head.row--
		}
	}
}

// reverseIndex implements RI (ESC M): move the cursor up one line, scrolling
// the viewport down when it is already on the top row. A renderer that grows a
// frame upward instead of repainting from row 0 emits it, so a model that
// rejected it would report an error for a correct redraw.
func (m *terminalModel) reverseIndex() {
	m.wrapPending = false
	if m.row == m.topMargin {
		m.scrollDown()
		return
	}
	if m.row > 0 {
		m.row--
	}
}

// scrollDown shifts the scrolling region down one row and blanks its first
// row.
func (m *terminalModel) scrollDown() {
	top, bottom := m.topMargin, m.bottomMargin
	copy(m.cells[top+1:bottom+1], m.cells[top:bottom])
	// A fresh row, for the same reason scrollUp allocates one.
	m.cells[top] = make([]termCell, m.width)
	if m.head.ok {
		switch {
		case m.head.row < top, m.head.row > bottom:
			// Outside the region, so the scroll does not touch it.
		case m.head.row == bottom:
			// Its row scrolled off the bottom of the region.
			m.head = headPos{}
		default:
			m.head.row++
		}
	}
}

// setScrollRegion implements DECSTBM (CSI r). An omitted or zero lower
// parameter means the last row. An invalid region is ignored, leaving the
// previous one in force, and a valid one homes the cursor to the region's
// top-left, as terminals do.
func (m *terminalModel) setScrollRegion(params ansi.Params) {
	top := cursorCount(params, 0) - 1
	bottom := m.height - 1
	if v, _, ok := params.Param(1, 0); ok && v > 0 {
		bottom = v - 1
	}
	if top < 0 || bottom >= m.height || top >= bottom {
		return
	}
	m.topMargin, m.bottomMargin = top, bottom
	m.moveTo(top, 0)
}

// scrollLines implements SU and SD (CSI Ps S / CSI Ps T): scroll the region by
// count lines without moving the cursor. A negative count scrolls up, a
// positive one down. The scroll is clamped to the region's height, which is
// what stops a large count from being a long loop over an unchanged screen.
func (m *terminalModel) scrollLines(count int) {
	height := m.bottomMargin - m.topMargin + 1
	if height < 1 {
		return
	}
	if count > height {
		count = height
	}
	if count < -height {
		count = -height
	}
	for ; count > 0; count-- {
		m.scrollDown()
	}
	for ; count < 0; count++ {
		m.scrollUp()
	}
}

// eraseDisplay implements ED modes 0 (cursor to end, inclusive), 1 (start to
// cursor, inclusive) and 2 (all).
func (m *terminalModel) eraseDisplay(mode int) {
	switch mode {
	case 0:
		m.eraseSpan(m.row, m.col, m.width)
		for r := m.row + 1; r < m.height; r++ {
			m.eraseSpan(r, 0, m.width)
		}
	case 1:
		for r := 0; r < m.row; r++ {
			m.eraseSpan(r, 0, m.width)
		}
		m.eraseSpan(m.row, 0, m.col+1)
	case 2:
		for r := 0; r < m.height; r++ {
			m.eraseSpan(r, 0, m.width)
		}
	default:
		m.failUnsupported()
	}
}

// eraseLine implements EL modes 0 (cursor to end, inclusive), 1 (start to
// cursor, inclusive) and 2 (all).
func (m *terminalModel) eraseLine(mode int) {
	switch mode {
	case 0:
		m.eraseSpan(m.row, m.col, m.width)
	case 1:
		m.eraseSpan(m.row, 0, m.col+1)
	case 2:
		m.eraseSpan(m.row, 0, m.width)
	default:
		m.failUnsupported()
	}
}

// eraseSpan blanks cells [from, to) of a row, clipped to the viewport.
func (m *terminalModel) eraseSpan(row, from, to int) {
	if row < 0 || row >= m.height {
		return
	}
	if from < 0 {
		from = 0
	}
	if to > m.width {
		to = m.width
	}
	for c := from; c < to; c++ {
		m.cells[row][c] = termCell{}
	}
}

// cellAt returns the cell at a position, or the zero cell when out of the
// viewport.
func (m *terminalModel) cellAt(row, col int) termCell {
	if row < 0 || row >= m.height || col < 0 || col >= m.width {
		return termCell{}
	}
	return m.cells[row][col]
}

// setBlank blanks the cell at a position, ignoring out-of-viewport writes.
func (m *terminalModel) setBlank(row, col int) {
	if row < 0 || row >= m.height || col < 0 || col >= m.width {
		return
	}
	m.cells[row][col] = termCell{}
}

func (m *terminalModel) clampRow(row int) int {
	if row < 0 {
		return 0
	}
	if row >= m.height {
		return m.height - 1
	}
	return row
}

func (m *terminalModel) clampCol(col int) int {
	if col < 0 {
		return 0
	}
	if col >= m.width {
		return m.width - 1
	}
	return col
}

// cursorCount returns params[i] with omitted and zero both counting as one,
// as terminal control sequences specify.
func cursorCount(params ansi.Params, i int) int {
	v, _, _ := params.Param(i, 1)
	if v < 1 {
		v = 1
	}
	return v
}

// eraseMode returns the erase mode parameter, defaulting to zero.
func eraseMode(params ansi.Params) int {
	v, _, _ := params.Param(0, 0)
	return v
}

// ignorablePrivateMode reports whether setting or resetting a DEC private
// mode cannot change visible cells or the cursor position. Every other mode
// (column mode, autowrap, reverse video, ...) must be rejected rather than
// swallowed: a silently ignored mode would yield a viewport the terminal
// never showed. The alternate-screen modes 47, 1047 and 1049 are handled by
// setAltScreen because they do change the visible cells.
func ignorablePrivateMode(mode int) bool {
	switch mode {
	case 1, 12, 25: // application cursor keys, cursor blink, cursor visibility
		return true
	case 1000, 1002, 1003, 1004, 1005, 1006, 1015, 1016: // mouse tracking, focus reporting
		return true
	case 2004, 2026: // bracketed paste, synchronized output
		return true
	}
	return false
}

// fail latches the first sticky model error.
func (m *terminalModel) fail(format string, args ...any) {
	if m.err == nil {
		m.err = fmt.Errorf("terminal model: "+format, args...)
	}
}

// failUnsupported latches a sticky error naming the offending sequence
// bytes.
func (m *terminalModel) failUnsupported() {
	m.fail("unsupported screen control sequence %s", m.seqQuoted())
}

// seqQuoted renders the captured raw bytes of the current sequence for an
// error message, marking truncation.
func (m *terminalModel) seqQuoted() string {
	q := fmt.Sprintf("%q", m.seq)
	if len(m.seq) >= maxSeqCapture {
		q += "…"
	}
	return q
}
