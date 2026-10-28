package mcpauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// Panel geometry the paging tests drive: 80×24 is the reference frame, and
// dock.MaxRows(24) caps a docked panel at 9 rows.
const (
	authPanelWidth    = 80
	authPanelDockRows = 9
)

// authURLTail is the end of longAuthURL. A page that ends at the URL's end must
// show it, so a paging bug that hides the suffix fails loudly.
const authURLTail = "&prompt=consent&access_type=offline"

// longAuthURL is a loopback authorization URL with the query suffix a real MCP
// authorization request carries: long enough to need several viewport pages at
// 80 columns. It is only rendered and copied — nothing here dials it.
var longAuthURL = "http://127.0.0.1:53682/oauth/callback?response_type=code" +
	"&client_id=marshal-local-mcp-client" +
	"&redirect_uri=http%3A%2F%2F127.0.0.1%3A53682%2Fcallback" +
	"&scope=mcp%3Aread+mcp%3Awrite+offline_access" +
	"&code_challenge=" + strings.Repeat("Q", 43) +
	"&code_challenge_method=S256" +
	"&state=" + strings.Repeat("a1", 32) +
	"&resource=https%3A%2F%2Fmcp.example.com%2Fmcp" +
	"&resource_metadata=https%3A%2F%2Fmcp.example.com%2F.well-known%2Foauth-protected-resource" +
	"&audience=marshal-local-fixture" + authURLTail

// authURLRows returns the URL rows a rendered panel shows: the frame's content
// rows between the instruction line and the paging/status/instruction rows, with
// the border and the frame's right-padding removed.
func authURLRows(view string) []string {
	var rows []string
	inURL := false
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if !strings.HasPrefix(line, "│ ") {
			continue
		}
		row := strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(line, "│ "), "│"), " ")
		switch {
		case strings.Contains(row, "Open this URL to authorize"):
			inURL = true
		case !inURL:
		case row == "",
			strings.Contains(row, "URL lines"),
			strings.Contains(row, "waiting for authorization"),
			strings.Contains(row, "working…"),
			strings.Contains(row, "cancelling…"),
			strings.HasPrefix(row, "c copy URL"),
			strings.HasPrefix(row, "Esc to cancel"):
			inURL = false
		default:
			rows = append(rows, row)
		}
	}
	return rows
}

// authURLCoverage advances a cursor through want for every row the panel made
// visible, so the pages read in order must expose the whole URL. A row already
// consumed by an overlapping last page is ignored rather than counted twice.
func authURLCoverage(t *testing.T, want string, pos int, rows []string) int {
	t.Helper()
	for _, row := range rows {
		switch {
		case row != "" && strings.HasPrefix(want[pos:], row):
			pos += len(row)
		case strings.Contains(want, row):
			// repeated by the overlapping last page
		default:
			t.Fatalf("visible row %q is not part of the authorization URL", row)
		}
	}
	return pos
}

// Display must satisfy the oauth.Display contract shape used by
// oauth.Engine.Authorize: a one-shot ShowURL and a ctx-aware Wait.
func TestDisplayShowURLRecordsURL(t *testing.T) {
	d := NewDisplay()
	if got := d.URL(); got != "" {
		t.Fatalf("URL() before ShowURL = %q, want empty", got)
	}
	d.ShowURL("https://auth.example.com/authorize?x=1")
	if got := d.URL(); got != "https://auth.example.com/authorize?x=1" {
		t.Errorf("URL() = %q, want the shown url", got)
	}
}

func TestDisplayWaitReturnsOnCancel(t *testing.T) {
	d := NewDisplay()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Wait(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Wait returned %v before cancellation", err)
	case <-time.After(20 * time.Millisecond):
		// still waiting, as expected
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}

// Panel must satisfy the dock.Panel contract and claim its own tick messages
// so the dock routes them without the model needing to name them.
func TestPanelOwnsTickMsg(t *testing.T) {
	p := NewPanel("github", NewDisplay())
	if !p.OwnsMsg(TickMsg{}) {
		t.Error("OwnsMsg(TickMsg) = false, want true")
	}
	if p.OwnsMsg(AuthDoneMsg{}) {
		t.Error("OwnsMsg(AuthDoneMsg) = true; completion must reach the model, not the panel")
	}
}

func TestPanelEscCancels(t *testing.T) {
	p := NewPanel("github", NewDisplay())
	cancelled := false
	p.SetCancel(func() { cancelled = true })
	p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled {
		t.Error("Esc did not invoke the cancel func")
	}
}

// TestMCPAuthLongURLCanBePagedAndCopied covers the manual-transcription path: a
// user with no browser and no working clipboard pages the wrap-to-cell-width URL
// body at 80×24 and reads the exact same URL back out of the copy request.
func TestMCPAuthLongURLCanBePagedAndCopied(t *testing.T) {
	disp := NewDisplay()
	disp.ShowURL(longAuthURL)
	p := NewPanel("github", disp)

	covered := 0
	for pages := 1; ; pages++ {
		view := p.View(authPanelWidth, authPanelDockRows)
		plain := ansi.Strip(view)
		if got := lipgloss.Height(view); got > authPanelDockRows {
			t.Fatalf("frame is %d rows tall, want at most %d:\n%s", got, authPanelDockRows, plain)
		}
		if !strings.Contains(plain, "c copy URL") || !strings.Contains(plain, "Esc cancel") {
			t.Fatalf("copy and cancel instructions are not both visible:\n%s", plain)
		}
		rows := authURLRows(view)
		if len(rows) == 0 {
			t.Fatalf("page %d shows no URL rows:\n%s", pages, plain)
		}
		if pages == 1 && !strings.HasPrefix(strings.Join(rows, ""), "http://127.0.0.1:53682/oauth/callback") {
			t.Fatalf("first page does not start at the authorization URL: %v", rows)
		}
		covered = authURLCoverage(t, longAuthURL, covered, rows)
		if p.offset == p.maxOffset {
			break // the last page is on screen
		}
		if pages == 40 {
			t.Fatal("paging never reached the end of the URL")
		}
		if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyPgDown}); cmd != nil {
			t.Fatalf("PgDn returned a command: %T", cmd)
		}
	}
	if covered != len(longAuthURL) {
		t.Fatalf("paging exposed %d of %d URL bytes as text", covered, len(longAuthURL))
	}

	// Home returns to the first page; End reaches the query suffix, which is
	// the part a manual transcription is most likely to lose.
	p.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if p.offset != 0 {
		t.Fatalf("Home left offset = %d, want 0", p.offset)
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if tail := strings.Join(authURLRows(p.View(authPanelWidth, authPanelDockRows)), ""); !strings.HasSuffix(tail, authURLTail) {
		t.Fatalf("End does not page to the URL's query suffix: %q", tail)
	}

	cmd := p.Update(tea.KeyPressMsg{Code: 'c'})
	if cmd == nil {
		t.Fatal("c emitted no copy request while the URL was displayed")
	}
	msg, ok := cmd().(CopyLinkMsg)
	if !ok {
		t.Fatalf("c emitted %T, want CopyLinkMsg", msg)
	}
	if msg.URL != longAuthURL {
		t.Fatalf("copy request carries %q, want the complete original URL", msg.URL)
	}
	if msg.Panel != p {
		t.Fatal("copy request does not name the panel that emitted it")
	}
	if !p.AcceptsCopyLink(msg) {
		t.Fatal("AcceptsCopyLink rejected the panel's own current URL")
	}
	if disp.URL() != longAuthURL {
		t.Fatal("rendering the URL rewrote the raw value the copy request carries")
	}
}

// TestMCPAuthCopyWaitsForURLAndStopsAfterCancel pins when `c` is live: never
// before ShowURL, and never once Esc has begun cancelling.
func TestMCPAuthCopyWaitsForURLAndStopsAfterCancel(t *testing.T) {
	disp := NewDisplay()
	p := NewPanel("github", disp)

	if cmd := p.Update(tea.KeyPressMsg{Code: 'c'}); cmd != nil {
		t.Fatal("c emitted a copy request before the URL was available")
	}
	if p.AcceptsCopyLink(CopyLinkMsg{Panel: p, URL: "http://127.0.0.1:53682/oauth/callback"}) {
		t.Fatal("AcceptsCopyLink accepted a URL the panel is not displaying")
	}

	const url = "http://127.0.0.1:53682/oauth/callback?state=ready"
	disp.ShowURL(url)
	cmd := p.Update(tea.KeyPressMsg{Code: 'c'})
	if cmd == nil {
		t.Fatal("c emitted no copy request once the URL was available")
	}
	msg, ok := cmd().(CopyLinkMsg)
	if !ok || msg.Panel != p || msg.URL != url {
		t.Fatalf("copy request = %+v, want this panel and the exact URL", msg)
	}
	if !p.AcceptsCopyLink(msg) {
		t.Fatal("AcceptsCopyLink rejected a current URL from its own panel")
	}
	for _, stale := range []CopyLinkMsg{
		{Panel: p, URL: url + "&stale=1"},
		{Panel: NewPanel("github", disp), URL: url},
		{Panel: p},
	} {
		if p.AcceptsCopyLink(stale) {
			t.Fatalf("AcceptsCopyLink accepted %+v", stale)
		}
	}

	// Esc still cancels, and cancellation closes the copy path without
	// touching the display the request would have carried.
	cancelled := false
	p.SetCancel(func() { cancelled = true })
	p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled {
		t.Fatal("Esc did not invoke the cancel func")
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'c'}); cmd != nil {
		t.Fatal("c emitted a copy request after cancellation began")
	}
	if p.AcceptsCopyLink(CopyLinkMsg{Panel: p, URL: url}) {
		t.Fatal("AcceptsCopyLink accepted a request after cancellation began")
	}
	if disp.URL() != url {
		t.Fatal("cancelling the flow rewrote the displayed URL")
	}
	if view := ansi.Strip(p.View(authPanelWidth, authPanelDockRows)); !strings.Contains(view, "cancelling…") {
		t.Fatalf("cancelling is not reported:\n%s", view)
	}
}

// TestMCPAuthURLViewportClampsOnResize keeps the viewport honest across resizes
// and in monochrome: the offset starts at zero, a taller dock pulls it back to
// the last page, and the frame stays inside every height budget it is given.
func TestMCPAuthURLViewportClampsOnResize(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.Reload(previous) })

	for _, mono := range []bool{false, true} {
		theme.Reload(theme.LoadFor(mono, "xterm-256color"))
		if theme.IsMonochrome() != mono {
			t.Fatalf("theme.IsMonochrome() = %v, want %v", theme.IsMonochrome(), mono)
		}
		disp := NewDisplay()
		disp.ShowURL(longAuthURL)
		p := NewPanel("github", disp)

		p.View(authPanelWidth, authPanelDockRows)
		if p.offset != 0 {
			t.Fatalf("initial offset = %d, want 0", p.offset)
		}
		for range 8 {
			p.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		}
		p.View(authPanelWidth, authPanelDockRows)
		if p.offset != p.maxOffset {
			t.Fatalf("paging past the end left offset %d, want %d", p.offset, p.maxOffset)
		}
		paged := p.offset
		if paged == 0 {
			t.Fatal("the fixture fit one page; the resize case is untested")
		}

		const tall = 20
		view := p.View(authPanelWidth, tall)
		if p.offset > paged {
			t.Fatalf("a taller dock raised the offset: %d → %d", paged, p.offset)
		}
		if p.offset > p.maxOffset {
			t.Fatalf("resize left offset %d past the last page %d", p.offset, p.maxOffset)
		}
		if got := lipgloss.Height(view); got > tall {
			t.Fatalf("frame is %d rows tall at maxHeight %d", got, tall)
		}
		if rows := authURLRows(view); !strings.HasSuffix(strings.Join(rows, ""), authURLTail) {
			t.Fatalf("the resized page does not end at the URL tail: %v", rows)
		}
		for _, h := range []int{1, 2, 3, 4, 6, authPanelDockRows} {
			if got := lipgloss.Height(p.View(authPanelWidth, h)); got > h {
				t.Fatalf("frame is %d rows tall at maxHeight %d", got, h)
			}
		}
	}
}
