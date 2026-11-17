package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/mcpauth"
	"marshal/internal/daemon"
)

// studioProbeTimeout bounds every socket and loopback call `studio` makes: the
// command either opens a page in a moment or says why it cannot.
const studioProbeTimeout = 5 * time.Second

// runStudio implements `marshal studio`: it opens the web Studio on the local
// daemon, already authenticated.
//
// This is the browser's door onto the daemon the TUI and `marshal fleet` use.
// The page drives that daemon's own sessions and host-child agents, so a spawn
// needs no agent image and no workspace build: the container machinery belongs
// to a server-profile bridge, and this profile has none.
//
// The daemon's bearer token is long-lived and never appears in a URL, a
// process argument, a shell history or a server log. Instead the daemon mints
// a short-lived, single-use code on its peer-checked unix socket; the browser
// opens `/#code=<code>`, a fragment the browser never sends to the server, and
// the page exchanges it once for the token over the loopback listener
// (web/bridge/studio_access.go). Before any of that, the port is asked who it
// is, so a stale listener that happens to hold the same port is refused rather
// than handed a code.
//
// Usage: marshal studio [--print] [--no-open]
func runStudio(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("studio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	printOnly := fs.Bool("print", false, "print the one-time sign-in URL instead of opening a browser")
	noOpen := fs.Bool("no-open", false, "do not open a browser (the one-time sign-in URL is printed)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal studio: unexpected argument %q", rest[0])
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	paths := daemon.PathsFor(home)

	ctx, cancel := context.WithTimeout(ctx, studioProbeTimeout)
	defer cancel()

	// The socket is the daemon's own interface and is peer-checked by user
	// ID, so reading what it did needs no credential. That answer is also how
	// a bind failure reaches the user by name instead of as "nothing is
	// serving".
	client := daemon.NewClient(paths.Socket)
	info, err := client.Info(ctx)
	if err != nil {
		return fmt.Errorf("marshal studio: the daemon's socket at %s did not answer (%w); run `marshal daemon install` and `marshal daemon start`", paths.Socket, err)
	}
	if info.StudioError != "" {
		return fmt.Errorf("marshal studio: the daemon could not serve the Studio at %s: %s; free the port or change daemon.studio_addr in %s, then restart the daemon",
			info.StudioAddr, info.StudioError, config.UserConfigPath(home))
	}
	if info.StudioAddr == "" {
		return fmt.Errorf("marshal studio: the daemon serves no Studio (daemon.studio_addr is off); set it in %s and run `marshal daemon install`",
			config.UserConfigPath(home))
	}

	base := "http://" + info.StudioAddr
	// Ask the port who it is before handing it anything: a listener that is
	// not this daemon must not be given a code, and a daemon that answers
	// nothing has nothing to open.
	if err := probeStudio(ctx, base, info); err != nil {
		return err
	}

	code, err := client.MintStudioCode(ctx)
	if err != nil {
		return fmt.Errorf("marshal studio: %w", err)
	}
	page := studioSignInURL(base, code.Code)

	// The URL carrying the code is printed only when the command will not
	// open it. A code is a credential for its two minutes, and a terminal's
	// scrollback outlives that by a lot; the default path prints the address
	// and puts the code straight into the browser.
	if *printOnly || *noOpen {
		fmt.Fprintf(stdout, "studio: %s\n", page)
		return nil
	}
	fmt.Fprintf(stdout, "studio: %s/\n", base)
	if err := mcpauth.OpenBrowser(page); err != nil {
		// Not fatal: a headless box has no browser to open.
		fmt.Fprintf(stderr, "marshal studio: could not open a browser (%v); run `marshal studio --print` for a one-time sign-in URL\n", err)
	}
	return nil
}

// studioSignInURL is the page address carrying a single-use code. The code goes
// after `#`, so the browser keeps it out of the request line and the daemon's
// access log, and the page can read it without the server ever seeing it.
func studioSignInURL(base, code string) string {
	return base + "/#code=" + url.QueryEscape(code)
}

// studioHello is what GET /api/studio/hello answers: the daemon's identity,
// and nothing secret.
type studioHello struct {
	Version   string `json:"version"`
	Profile   string `json:"profile"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"` // Unix milliseconds
	Socket    string `json:"socket"`
}

// probeStudio confirms that the port answers as the daemon whose socket was
// just read, before a code is sent to it. It sends no credential: the identity
// it compares is the one the socket already gave us.
func probeStudio(ctx context.Context, base string, want daemon.Info) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/studio/hello", nil)
	if err != nil {
		return fmt.Errorf("marshal studio: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("marshal studio: nothing is serving the Studio at %s (%w); run `marshal daemon install` and `marshal daemon start`", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("marshal studio: the bridge at %s has no Studio sign-in route; restart the daemon (`marshal daemon start`)", base)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("marshal studio: %s answered %s", base, resp.Status)
	}
	var got studioHello
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&got); err != nil {
		return fmt.Errorf("marshal studio: %s answered something that is not a daemon identity (%w)", base, err)
	}
	if got.PID != want.PID || got.StartedAt != want.StartedAt || got.Socket != want.Socket {
		return fmt.Errorf("marshal studio: %s is not the daemon at %s (it reports pid %d, started %d); refusing to sign a page in to it",
			base, want.Socket, got.PID, got.StartedAt)
	}
	return nil
}
