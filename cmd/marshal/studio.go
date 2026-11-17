package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/mcpauth"
	"marshal/internal/daemon"
)

// runStudio implements `marshal studio`: it opens the web Studio on the local
// daemon, already authenticated.
//
// This is the browser's door onto the daemon the TUI and `marshal fleet` use.
// The page drives that daemon's own sessions and host-child agents, so a spawn
// needs no agent image and no workspace build: the container machinery belongs
// to a server-profile bridge, and this profile has none.
//
// The token is the daemon's own, from the 0600 file webbridge writes at
// startup; it rides in the URL as `?token=` (the pattern Jupyter uses) and the
// SPA strips it at boot. Nothing is typed, nothing is in a process list, and
// nothing reaches the daemon's log.
//
// Usage: marshal studio [--print] [--no-open]
func runStudio(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("studio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	printOnly := fs.Bool("print", false, "print the Studio URL instead of opening a browser")
	noOpen := fs.Bool("no-open", false, "do not open a browser (the URL is still printed)")
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
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		return fmt.Errorf("read the user config: %w", err)
	}
	addr, err := daemonStudioAddr(cfg.Daemon.StudioAddr)
	if err != nil {
		return err
	}
	if addr == "" {
		return fmt.Errorf("marshal studio: the Studio is off (daemon.studio_addr is %q); set it in %s and run `marshal daemon install`",
			cfg.Daemon.StudioAddr, config.UserConfigPath(home))
	}
	paths := daemon.PathsFor(home)
	token, err := readStudioToken(paths.Token)
	if err != nil {
		return err
	}

	base := "http://" + addr
	// Ask the daemon who it is before handing the browser a URL: a daemon
	// installed before this listener existed still answers on the socket, so
	// "start it" would be the wrong advice for a page that answers nothing.
	if err := probeStudio(ctx, base, token, paths.Token); err != nil {
		return err
	}

	page := base + "/?token=" + url.QueryEscape(token)
	fmt.Fprintf(stdout, "studio: %s\n", page)
	if *printOnly || *noOpen {
		return nil
	}
	if err := mcpauth.OpenBrowser(page); err != nil {
		// Not fatal: the URL above is the whole point, and a headless box has
		// no browser to open.
		fmt.Fprintf(stderr, "marshal studio: could not open a browser (%v); open the URL above\n", err)
	}
	return nil
}

// readStudioToken reads the bearer token webbridge wrote, and explains what to
// do when it is missing: the file appears when the daemon first starts with the
// Studio listener on, so an absent file is an install or start away from fixed.
func readStudioToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("marshal studio: no token at %s; run `marshal daemon install` and `marshal daemon start`", path)
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("marshal studio: %s is empty; restart the daemon so it writes one", path)
	}
	return token, nil
}

// probeStudio checks that the Studio listener answers on the token we hold, and
// distinguishes the two ways it can fail: nothing listening (an install from
// before the listener, or a stopped daemon) and a listener that refuses our
// token (a daemon restarted with a different one).
func probeStudio(ctx context.Context, base, token, tokenPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/daemon", nil)
	if err != nil {
		return fmt.Errorf("marshal studio: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("marshal studio: nothing is serving the Studio at %s (%w); run `marshal daemon install` and `marshal daemon start`", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("marshal studio: the daemon at %s refused the token in %s; restart it (`marshal daemon start`) so it writes the current one", base, tokenPath)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("marshal studio: %s answered %s", base, resp.Status)
	}
	return nil
}
