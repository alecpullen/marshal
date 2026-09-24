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

	"charm.land/bubbletea/v2"

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

// Panel is the docked authorization panel.
type Panel struct {
	name       string
	disp       *Display
	cancel     context.CancelFunc
	spinner    int
	cancelling bool
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
		if msg.String() == "esc" && !p.cancelling {
			p.cancelling = true
			if p.cancel != nil {
				p.cancel()
			}
		}
	}
	return nil
}

func (p *Panel) View(width, maxHeight int) string {
	pw := layout.PanelWidth(width)
	frame := spinnerFrames[p.spinner%len(spinnerFrames)]

	var b strings.Builder
	fmt.Fprintf(&b, "Authorizing MCP server %q\n", p.name)
	if u := p.disp.URL(); u != "" {
		b.WriteString("Open this URL to authorize (a browser should have opened):\n")
		b.WriteString(u + "\n")
	} else {
		b.WriteString("Contacting the authorization server…\n")
	}

	switch {
	case p.cancelling:
		b.WriteString(frame + " cancelling…")
	case p.disp.URL() != "":
		b.WriteString(frame + " waiting for authorization in the browser…")
	default:
		b.WriteString(frame + " working…")
	}
	b.WriteString("\nEsc to cancel")

	h := min(strings.Count(b.String(), "\n")+3, maxHeight)
	return chrome.Panel("mcp auth", b.String(), pw, h, true, theme.Current())
}

// OpenBrowser opens url in the platform's default browser. It is best-effort:
// a missing browser is not an error the flow depends on, because the
// authorization URL is always rendered in the panel for manual copy.
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
