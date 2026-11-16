package notify

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"marshal/internal/app/tui/fleet"
	"marshal/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

// call is one command the notifier ran, recorded by the injected runner.
type call struct {
	name string
	args []string
}

// recorder is an injected run that reports each command it is asked to run.
func recorder(calls chan<- call) func(ctx context.Context, name string, args ...string) error {
	return func(ctx context.Context, name string, args ...string) error {
		calls <- call{name: name, args: args}
		return nil
	}
}

// ran takes the one command the notifier ran.
func ran(t *testing.T, calls <-chan call) call {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("the notifier ran no command")
		return call{}
	}
}

// records is a slog.Handler that reports each record it is given.
type records chan slog.Record

func (r records) Enabled(context.Context, slog.Level) bool { return true }

func (r records) Handle(_ context.Context, rec slog.Record) error {
	r <- rec
	return nil
}

func (r records) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r records) WithGroup(string) slog.Handler { return r }

func TestMacOSRunsOsascript(t *testing.T) {
	calls := make(chan call, 1)
	d := New("darwin", recorder(calls), nil)

	// A body with a quote and a backslash, which AppleScript must see
	// escaped.
	d.Notify(fleet.Notification{Key: "a-1", Text: `say "hi" \ there`})

	got := ran(t, calls)
	if got.name != "osascript" {
		t.Fatalf("ran %q, want %q", got.name, "osascript")
	}
	want := []string{"-e", `display notification "say \"hi\" \\ there" with title "Marshal"`}
	if !slices.Equal(got.args, want) {
		t.Fatalf("ran osascript %q, want %q", got.args, want)
	}
}

func TestLinuxRunsNotifySend(t *testing.T) {
	calls := make(chan call, 1)
	d := New("linux", recorder(calls), nil)

	d.Notify(fleet.Notification{Key: "a-1", Text: "a needs you: approval"})

	got := ran(t, calls)
	if got.name != "notify-send" {
		t.Fatalf("ran %q, want %q", got.name, "notify-send")
	}
	want := []string{"Marshal", "a needs you: approval"}
	if !slices.Equal(got.args, want) {
		t.Fatalf("ran notify-send %q, want %q", got.args, want)
	}
}

func TestOtherSystemsDoNothing(t *testing.T) {
	for _, goos := range []string{"windows", "freebsd", ""} {
		t.Run(goos, func(t *testing.T) {
			calls := make(chan call, 1)
			logs := make(records, 1)
			d := New(goos, recorder(calls), slog.New(logs))

			d.Notify(fleet.Notification{Key: "a-1", Text: "a needs you"})

			// Give a command that wanted to run the time to be reported.
			select {
			case c := <-calls:
				t.Fatalf("%q ran %s", goos, c.name)
			case <-time.After(100 * time.Millisecond):
			}
			select {
			case rec := <-logs:
				t.Fatalf("%q logged %q", goos, rec.Message)
			default:
			}
		})
	}
}

func TestNotifyDoesNotBlock(t *testing.T) {
	ctxs := make(chan context.Context, 1)
	finished := make(chan struct{}, 1)
	run := func(ctx context.Context, name string, args ...string) error {
		ctxs <- ctx
		<-ctx.Done() // only the bound ends it
		finished <- struct{}{}
		return nil
	}
	d := New("linux", run, nil)

	done := make(chan struct{})
	go func() {
		d.Notify(fleet.Notification{Key: "a-1", Text: "a needs you"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify waited for the command")
	}

	var ctx context.Context
	select {
	case ctx = <-ctxs:
	case <-time.After(2 * time.Second):
		t.Fatal("the notifier ran no command")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the command's context has no deadline")
	}
	if left := time.Until(deadline); left > bound || left < time.Second {
		t.Fatalf("the command's bound is %v away, want about %v", left, bound)
	}

	select {
	case <-finished:
	case <-time.After(bound + 5*time.Second):
		t.Fatalf("the %v bound did not end the command's context", bound)
	}
}

func TestAFailureIsLoggedOnce(t *testing.T) {
	logs := make(records, 4)
	run := func(ctx context.Context, name string, args ...string) error {
		return errors.New("no display")
	}
	d := New("linux", run, slog.New(logs))

	d.Notify(fleet.Notification{Key: "a-1", Text: "a needs you: question"})

	select {
	case rec := <-logs:
		if rec.Level != slog.LevelWarn {
			t.Errorf("logged at %v, want %v", rec.Level, slog.LevelWarn)
		}
		if want := "desktop notification failed"; rec.Message != want {
			t.Errorf("logged %q, want %q", rec.Message, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a failed notification was not logged")
	}

	select {
	case rec := <-logs:
		t.Fatalf("one failure was logged twice: %q", rec.Message)
	case <-time.After(200 * time.Millisecond):
	}
}
