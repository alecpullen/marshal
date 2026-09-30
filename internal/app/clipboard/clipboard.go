// Package clipboard puts text on the local system clipboard.
//
// It is deliberately tiny and dependency-free: it shells out to the platform's
// clipboard helper (pbcopy, wl-copy, xclip, xsel) and never invokes a shell.
// Over SSH a local helper writes to the *remote* machine's clipboard, so
// callers should consult Remote before offering a copy affordance.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Writer puts text on the system clipboard.
type Writer interface {
	Write(ctx context.Context, text string) error
}

// ErrUnavailable reports that no local clipboard backend is usable.
var ErrUnavailable = errors.New("no local clipboard backend available")

// defaultTimeout bounds a single clipboard write so a hung helper cannot block
// the TUI.
const defaultTimeout = 2 * time.Second

// LocalWriter writes through a local clipboard executable.
//
// The zero value is usable: every nil field falls back to a real default via
// resolve. Fields are exported so tests (and callers with unusual setups) can
// substitute fakes.
type LocalWriter struct {
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, path string, args []string, stdin string) error
	GOOS     string
	Env      func(string) string
	Timeout  time.Duration
}

// backend is a resolved clipboard helper: an executable name plus its exact
// argv. It is never a shell.
type backend struct {
	name string
	args []string
}

// resolve returns a copy of w with every unset field replaced by its real
// default. It never mutates the receiver, so the zero value stays usable.
func (w *LocalWriter) resolve() *LocalWriter {
	r := &LocalWriter{}
	if w != nil {
		*r = *w
	}
	if r.LookPath == nil {
		r.LookPath = exec.LookPath
	}
	if r.Run == nil {
		r.Run = execRun
	}
	if r.GOOS == "" {
		r.GOOS = runtime.GOOS
	}
	if r.Env == nil {
		r.Env = os.Getenv
	}
	if r.Timeout <= 0 {
		r.Timeout = defaultTimeout
	}
	return r
}

// candidates lists the backends to try, in preference order, for this GOOS.
// An empty result means the platform has no supported backend.
func (w *LocalWriter) candidates() []backend {
	switch w.GOOS {
	case "darwin":
		return []backend{{name: "pbcopy"}}
	case "linux":
		if w.Env("WAYLAND_DISPLAY") != "" {
			return []backend{{name: "wl-copy"}}
		}
		return []backend{
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	default:
		return nil
	}
}

// resolveBackend returns the first candidate that LookPath can find, without
// running it.
func (w *LocalWriter) resolveBackend() (backend, string, error) {
	for _, c := range w.candidates() {
		path, err := w.LookPath(c.name)
		if err == nil && path != "" {
			return c, path, nil
		}
	}
	return backend{}, "", ErrUnavailable
}

// Available reports whether a backend resolves via LookPath. It does not run
// the backend, so it is safe to call from a render path.
func (w *LocalWriter) Available() bool {
	r := w.resolve()
	_, _, err := r.resolveBackend()
	return err == nil
}

// Write resolves the platform backend and runs it with text on stdin.
//
// The write is bounded by w.Timeout (2s by default), imposed on top of the
// caller's ctx, so a hung helper cannot block the caller. The helper is
// executed directly with exec.CommandContext — never through a shell.
func (w *LocalWriter) Write(ctx context.Context, text string) error {
	r := w.resolve()

	b, path, err := r.resolveBackend()
	if err != nil {
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	if err := r.Run(ctx, path, b.args, text); err != nil {
		return fmt.Errorf("clipboard: %s: %w", b.name, err)
	}
	return nil
}

// maxStderrBytes caps how much of a helper's diagnostics are kept. All the
// caller does with them is append them to an error message, so a few KiB is more
// than enough; the point is that a misbehaving binary cannot grow the buffer for
// the whole timeout window.
const maxStderrBytes = 4 * 1024

// boundedBuffer is an io.Writer that keeps at most limit bytes and silently
// discards the rest.
//
// It bounds MEMORY DURING the run, which is the property that matters: buffering
// a bytes.Buffer and truncating afterwards still lets the binary allocate its
// whole output first. Overflow is not reported to the caller — the truncation is
// already visible to the reader as the message simply ending, and an error here
// would replace the helper's real failure with a bookkeeping one.
type boundedBuffer struct {
	buf   []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) > room {
			b.buf = append(b.buf, p[:room]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	// Report every byte as written: a short count would surface as a write
	// error at the far end of the process's pipe and turn a diagnostics problem
	// into a failed clipboard write.
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.buf) }

// execRun is the real Run implementation: it starts path with exactly args and
// feeds stdin, with no shell in between.
//
// Both output streams are set explicitly rather than left nil. A nil Stdout and
// Stderr both mean os.DevNull (os/exec writerDescriptor) and not inherited
// descriptors, so the previous code was not leaking terminal output — but it was
// relying on that default for STDOUT while attaching an UNBOUNDED bytes.Buffer to
// stderr, and a helper that failed in a loop could grow that buffer for the whole
// timeout window. Stdout is io.Discard (a clipboard helper has nothing useful to
// say there; the intent is now on the page rather than inferred from a default)
// and stderr is bounded so the memory a misbehaving binary can commit is capped
// during the run.
func execRun(ctx context.Context, path string, args []string, stdin string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = io.Discard

	stderr := &boundedBuffer{limit: maxStderrBytes}
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// Remote reports whether the session looks like it is running over SSH. A local
// clipboard executable would then write to the remote machine's clipboard
// rather than the user's, so callers should not claim the text was copied.
func Remote(env func(string) string) bool {
	if env == nil {
		return false
	}
	return env("SSH_CONNECTION") != "" || env("SSH_TTY") != ""
}
