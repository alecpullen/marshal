package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/mcpauth"
	"marshal/internal/daemon"
)

// studioProbeTimeout bounds every socket and browser call `studio` makes: the
// command either opens something in a moment or says why it cannot.
const studioProbeTimeout = 5 * time.Second

// studioAppBundleID is the native app's bundle identifier (app/), which is
// what `open` is asked for by name: the app is signed and installed by the
// bundle, so its path is not this command's to guess.
const studioAppBundleID = "dev.marshal.app"

// errNoStudioApp is the answer when no native app is installed here: the
// command goes on to the web Studio rather than failing, and when that is not
// set up either it is the error the user sees.
var errNoStudioApp = errors.New("no Marshal app is installed (no app has the bundle identifier " + studioAppBundleID + ")")

// studioOpenApp opens the native Studio. It is a var so a test can answer for
// a platform without opening anything.
var studioOpenApp = func(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		// D14: the app is the desktop's door onto the daemon; everywhere
		// else the web Studio is the only one.
		return errNoStudioApp
	}
	out, err := exec.CommandContext(ctx, "open", "-b", studioAppBundleID).CombinedOutput()
	if err == nil {
		return nil
	}
	// `open` answers "Unable to find application named ..." when no app
	// carries the identifier, which is the absent app rather than a failure
	// to launch one.
	if strings.Contains(string(out), "Unable to find application") {
		return errNoStudioApp
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("open the Marshal app: %s", msg)
	}
	return fmt.Errorf("open the Marshal app: %w", err)
}

// studioOpenURL opens a URL in the browser; tests replace it.
var studioOpenURL = mcpauth.OpenBrowser

// runStudio implements `marshal studio`: it opens the native Studio, or else
// the web Studio daemon.web_url names (D14).
//
// Nothing is minted and no port is probed: the web Studio is whatever the
// daemon's web listener serves, and the address it is reached on is the one
// the user put in daemon.web_url. The daemon's own answer is read only to say
// so when the listener is not up.
//
// Usage: marshal studio [--print]
func runStudio(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("studio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	printOnly := fs.Bool("print", false, "print the web Studio's address instead of opening it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal studio: unexpected argument %q", rest[0])
	}

	// The app is tried only when the command will not be printing an
	// address: --print asks for the address, not for the app.
	if !*printOnly {
		switch err := studioOpenApp(ctx); {
		case err == nil:
			fmt.Fprintln(stdout, "studio: opened the Marshal app")
			return nil
		case errors.Is(err, errNoStudioApp):
			// No app: the web Studio is the other door.
		default:
			// Not fatal, for the reason a missing browser is not: the
			// web Studio may still answer.
			fmt.Fprintf(stderr, "marshal studio: could not open the Marshal app (%v)\n", err)
		}
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		return fmt.Errorf("marshal studio: read the user config: %w", err)
	}
	webURL := strings.TrimSpace(cfg.Daemon.WebURL)
	if webURL == "" {
		return errors.New("marshal studio: neither the Marshal app nor a web Studio is set up: install the app, or set daemon.web_listen and daemon.web_url and run marshal daemon install (docs/one-daemon/design.md D10, D14)")
	}

	if line := studioListenerWarning(ctx, home); line != "" {
		fmt.Fprintln(stderr, line)
	}

	if !*printOnly {
		if err := studioOpenURL(webURL); err != nil {
			// Not fatal: a headless box has no browser to open.
			fmt.Fprintf(stderr, "marshal studio: could not open a browser (%v); run `marshal studio --print` for the address\n", err)
		}
	}
	fmt.Fprintf(stdout, "studio: %s\n", webURL)
	return nil
}

// studioListenerWarning is the one line the daemon's own answer earns here:
// that its web listener is not listening, which is why a set-up web Studio
// would not answer. A daemon that does not answer adds nothing — this command
// is not the place to diagnose a stopped daemon.
func studioListenerWarning(ctx context.Context, home string) string {
	infoCtx, cancel := context.WithTimeout(ctx, studioProbeTimeout)
	defer cancel()
	info, err := daemonClientFor(daemon.PathsFor(home).Socket).Info(infoCtx)
	if err != nil {
		return ""
	}
	reason := info.WebError
	if reason == "" {
		if info.WebListen != "" {
			return ""
		}
		reason = "daemon.web_listen is not set"
	}
	return fmt.Sprintf("marshal studio: the daemon's web listener is not listening (%s)", reason)
}
