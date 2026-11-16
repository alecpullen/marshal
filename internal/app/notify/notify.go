// Package notify is the launch's desktop notifier (decision 7): the half of a
// notification the terminal bell cannot do. The TUI never imports it; launch
// hands one to the fleet watch as its fleet.Notifier.
package notify

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"marshal/internal/app/tui/fleet"
)

// bound is how long a notification's command may run before it is abandoned: a
// notifier must not outlive the thing it announces (decision 6).
const bound = 5 * time.Second

// title is every notification's title (decision 7).
const title = "Marshal"

// Desktop is the system's desktop notifier (decision 7).
type Desktop struct {
	// goos is the system whose notifier this is, injected so the shapes are
	// testable anywhere.
	goos string
	// run executes the notifier's command. It is the executor's, so no test
	// spawns a real osascript or notify-send.
	run func(ctx context.Context, name string, args ...string) error
	// log records a failed notification, once. A nil logger logs nothing.
	log *slog.Logger
}

// Desktop notifies through the desktop notifier's interface, so drift fails
// the build.
var _ fleet.Notifier = (*Desktop)(nil)

// New answers the notifier for goos, running commands with run.
func New(goos string, run func(ctx context.Context, name string, args ...string) error, log *slog.Logger) *Desktop {
	return &Desktop{goos: goos, run: run, log: log}
}

// Notify satisfies fleet.Notifier.
func (d *Desktop) Notify(n fleet.Notification) {
	name, args, ok := d.command(n.Text)
	if !ok {
		// A system without a desktop notifier does nothing at all: no
		// command, no log (decision 7).
		return
	}
	if d.run == nil {
		// No executor was wired, so there is nothing to run the command
		// with; the bell the program rings is the notification.
		return
	}
	// The command runs on its own goroutine so Notify returns before it
	// finishes: the fleet's watch calls this on its way past (decision 6).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), bound)
		defer cancel()
		if err := d.run(ctx, name, args...); err != nil {
			d.fail(name, err)
		}
	}()
}

// command is the program and arguments that show text on the desktop, and
// whether this system has one to show it on (decision 7).
func (d *Desktop) command(text string) (string, []string, bool) {
	switch d.goos {
	case "darwin":
		script := "display notification " + appleScriptString(text) +
			" with title " + appleScriptString(title)
		return "osascript", []string{"-e", script}, true
	case "linux":
		return "notify-send", []string{title, text}, true
	default:
		return "", nil, false
	}
}

// fail records one failed notification. There is no retry, so there is one
// record per failure (decision 6).
func (d *Desktop) fail(name string, err error) {
	if d.log == nil {
		return
	}
	d.log.Warn("desktop notification failed", "program", name, "err", err)
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
