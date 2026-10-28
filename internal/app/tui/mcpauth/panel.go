// Package mcpauth renders the docked panel that drives the OAuth
// authorization-code flow for a remote MCP server. It supplies the
// oauth.Display the engine calls while the loopback receiver is up and a
// dock.Panel that shows the authorization URL with a spinner and Esc
// cancellation.
package mcpauth

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

// Display is the oauth.Display implementation the engine drives. The URL is
// delivered once via ShowURL; the panel renders it on each spinner tick so a
// slow round-trip never blocks the UI. Access is mutex-guarded because
// Authorize runs on its own goroutine while View runs on the UI goroutine.
type Display struct {
	mu  sync.Mutex
	url string
}

// NewDisplay returns an empty Display.
func NewDisplay() *Display { return &Display{} }

// ShowURL records the authorization URL for the panel to render.
func (d *Display) ShowURL(url string) {
	d.mu.Lock()
	d.url = url
	d.mu.Unlock()
}

// URL returns the recorded authorization URL, or "" before ShowURL.
func (d *Display) URL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.url
}

// Wait blocks until ctx is cancelled, then returns ctx.Err(). It is the
// rendezvous at which the caller observes user cancellation: Esc in the panel
// cancels the engine's context, which unblocks this Wait (and the engine's
// loopback select). The landed engine waits on the loopback callback directly
// and treats Wait as a hint, so this never sits on the critical path.
func (d *Display) Wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TickMsg advances the panel spinner and re-arms itself.
type TickMsg struct{}

// AuthDoneMsg carries the terminal result of an Authorize call back to the
// model, which closes the dock and reports success or failure.
type AuthDoneMsg struct{ Err error }

// Tick schedules the next spinner frame.
func Tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return TickMsg{} })
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// CopyLinkMsg asks the host to copy the MCP authorization URL through its
// existing clipboard path. The panel identity and the exact URL travel with
// the request so the host can verify it before copying; nothing here touches a
// clipboard or a browser, and the URL is never written to a log.
type CopyLinkMsg struct {
	Panel *Panel
	URL   string
}

// Panel is the docked authorization panel.
type Panel struct {
	name       string
	disp       *Display
	cancel     context.CancelFunc
	spinner    int
	cancelling bool
	// offset is the first wrapped URL row the viewport shows; pageRows and
	// maxOffset record the last rendered viewport so PgUp/PgDn and End move
	// by what the user can actually see. View re-clamps both after a resize.
	offset    int
	pageRows  int
	maxOffset int
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a panel for the named MCP server.
func NewPanel(name string, disp *Display) *Panel {
	return &Panel{name: name, disp: disp}
}

// SetCancel installs the context cancel func Esc uses to abort the flow.
func (p *Panel) SetCancel(cancel context.CancelFunc) { p.cancel = cancel }

// Sizing keeps the panel docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// OwnsMsg claims this panel's tick messages. Completion (AuthDoneMsg) is
// handled by the model, which must close the dock and print the outcome, so
// it is deliberately not claimed here.
func (p *Panel) OwnsMsg(msg tea.Msg) bool {
	_, ok := msg.(TickMsg)
	return ok
}

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case TickMsg:
		p.spinner++
		return Tick()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			if !p.cancelling {
				p.cancelling = true
				if p.cancel != nil {
					p.cancel()
				}
			}
		case "c":
			// Copy is meaningful only while a URL is on screen and the flow is
			// still live; before ShowURL and after cancellation there is
			// nothing to hand the clipboard.
			if url := p.disp.URL(); url != "" && !p.cancelling {
				return func() tea.Msg { return CopyLinkMsg{Panel: p, URL: url} }
			}
		case "pgdown":
			// View clamps to the last page, so paging past the end is inert.
			p.offset += max(p.pageRows, 1)
		case "pgup":
			p.offset = max(p.offset-max(p.pageRows, 1), 0)
		case "home":
			p.offset = 0
		case "end":
			p.offset = p.maxOffset
		}
	}
	return nil
}

// AcceptsCopyLink reports whether msg names this panel, carries the URL the
// panel is displaying right now, and arrives before cancellation began. It is
// a pure predicate: it never copies, clears state or cancels the flow, so the
// host can ask before committing to its clipboard path.
func (p *Panel) AcceptsCopyLink(msg CopyLinkMsg) bool {
	if msg.Panel != p || msg.URL == "" || msg.URL != p.disp.URL() {
		return false
	}
	return !p.cancelling
}

// View renders the panel: the authorization URL as a pageable body wrapped to
// the panel's cell width, with the copy and cancel instructions kept on screen.
// The frame never exceeds maxHeight rows.
func (p *Panel) View(width, maxHeight int) string {
	if width <= 0 || maxHeight <= 0 {
		return ""
	}
	pw := layout.PanelWidth(width)
	inner := max(pw-2, 1)
	frame := spinnerFrames[p.spinner%len(spinnerFrames)]
	url := p.disp.URL()

	status := frame + " working…"
	switch {
	case p.cancelling:
		status = frame + " cancelling…"
	case url != "":
		status = frame + " waiting for authorization in the browser…"
	}
	hint := "Esc to cancel"
	if url != "" {
		hint = "c copy URL · Esc cancel"
	}

	head := []string{fmt.Sprintf("Authorizing MCP server %q", p.name)}
	if url != "" {
		head = append(head, "Open this URL to authorize (a browser should have opened):")
	} else {
		head = append(head, "Contacting the authorization server…")
	}

	// The frame's top border is one of maxHeight rows, so the panel draws at
	// most maxHeight-1 content rows.
	budget := max(maxHeight-1, 0)
	tail := []string{status, hint}
	if budget < len(head)+len(tail)+1 {
		// Too short for both instruction rows and a URL row: fold the hint
		// into the status row so neither instruction is lost.
		tail = []string{status + " · " + hint}
	}

	var urlLines []string
	if url != "" {
		urlLines = strings.Split(ansi.Wrap(displayURL(url), inner, "/-:?&=#"), "\n")
	}

	rows := head
	p.pageRows, p.maxOffset = 0, 0
	viewport := max(budget-len(head)-len(tail), 0)
	if len(urlLines) > 0 && viewport > 0 {
		textRows := viewport
		scrollable := len(urlLines) > viewport
		if scrollable && viewport <= 1 {
			// A one-row viewport cannot afford the indicator row, so the
			// paging keys ride on the hint instead of vanishing entirely.
			if len(tail) == 2 {
				tail[1] = hint + " · PgUp/PgDn scroll"
			} else {
				tail[0] += " · PgUp/PgDn scroll"
			}
		}
		if scrollable && viewport > 1 {
			// One viewport row pays for the position indicator.
			textRows = viewport - 1
		}
		p.pageRows = textRows
		p.maxOffset = max(len(urlLines)-textRows, 0)
		p.offset = min(max(p.offset, 0), p.maxOffset)
		end := min(p.offset+textRows, len(urlLines))
		rows = append(rows, urlLines[p.offset:end]...)
		if scrollable && viewport > 1 {
			rows = append(rows, p.indicator(len(urlLines), end))
		}
	}

	rows = append(rows, tail...)
	h := min(len(rows)+1, maxHeight)
	return chrome.Panel("mcp auth", strings.Join(rows, "\n"), pw, h, true, theme.Current())
}

// indicator reports where the URL viewport sits in the whole URL and which keys
// move it, so paging is discoverable without a browser or a clipboard.
func (p *Panel) indicator(total, end int) string {
	where := "↓ PgDn"
	switch {
	case p.offset > 0 && end < total:
		where = "↑ PgUp · ↓ PgDn"
	case p.offset > 0:
		where = "↑ PgUp"
	}
	label := fmt.Sprintf("%s · URL lines %d–%d of %d", where, p.offset+1, end, total)
	return theme.MutedStyle().Render(label)
}

// displayURL makes a URL safe to render: control characters (a stray newline, a
// smuggled ESC sequence) become single-cell control pictures so they never
// reach the terminal as control. Only the rendering is escaped — CopyLinkMsg
// still carries the exact URL the authorization server returned.
func displayURL(url string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0x7f:
			return 0x2421 // ␡
		case r < 0x20:
			return 0x2400 + r // ␀ … ␟
		case unicode.IsControl(r):
			return 0x2421 // C1 controls: visible, and inert
		}
		return r
	}, url)
}

// OpenBrowser opens url in the platform's default browser. It is best-effort: a
// missing browser is not an error the flow depends on, because the panel always
// renders the whole authorization URL as a pageable body (PgUp/PgDn/Home/End)
// and `c` copies it verbatim. A browser that never opened, an unavailable
// clipboard and a terminal that ignores OSC 52 all leave the user a way to
// reach the URL.
func OpenBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
		args = []string{url}
	case "windows":
		name = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		name = "xdg-open"
		args = []string{url}
	}
	return exec.Command(name, args...).Start()
}
