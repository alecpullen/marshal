package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrNoImage reports that the clipboard holds no image.
var ErrNoImage = errors.New("clipboard has no image")

// Reader reads an image out of the local clipboard.
type Reader interface {
	ReadImage(ctx context.Context) ([]byte, error)
}

// LocalImageReader reads through a local clipboard executable.
//
// The zero value is usable. Candidates, in order: darwin pngpaste, then an
// osascript fallback writing a temp PNG; linux wl-paste under Wayland, else
// xclip. A missing executable is ErrUnavailable, so the caller can say which
// package to install.
type LocalImageReader struct {
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, path string, args []string) ([]byte, error)
	GOOS     string
	Env      func(string) string
	Timeout  time.Duration
}

// resolve returns a copy of r with every unset field replaced by its real
// default. It never mutates the receiver, so the zero value stays usable.
func (r *LocalImageReader) resolve() *LocalImageReader {
	c := &LocalImageReader{}
	if r != nil {
		*c = *r
	}
	if c.LookPath == nil {
		c.LookPath = exec.LookPath
	}
	if c.Run == nil {
		c.Run = execCapture
	}
	if c.GOOS == "" {
		c.GOOS = runtime.GOOS
	}
	if c.Env == nil {
		c.Env = os.Getenv
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	return c
}

// candidates lists the backends to try, in preference order, for this GOOS.
// An empty result means the platform has no supported backend.
func (r *LocalImageReader) candidates() []backend {
	switch r.GOOS {
	case "darwin":
		return []backend{{name: "pngpaste", args: []string{"-"}}}
	case "linux":
		if r.Env("WAYLAND_DISPLAY") != "" {
			return []backend{{name: "wl-paste", args: []string{"--type", "image/png", "--no-newline"}}}
		}
		return []backend{{name: "xclip", args: []string{"-selection", "clipboard", "-t", "image/png", "-o"}}}
	default:
		return nil
	}
}

// resolveBackend returns the first candidate that LookPath can find.
func (r *LocalImageReader) resolveBackend() (backend, string, error) {
	for _, c := range r.candidates() {
		path, err := r.LookPath(c.name)
		if err == nil && path != "" {
			return c, path, nil
		}
	}
	return backend{}, "", ErrUnavailable
}

// ReadImage resolves the platform backend and runs it, returning the PNG it
// writes to stdout.
//
// The read is bounded by r.Timeout (2s by default), imposed on top of the
// caller's ctx, so a hung helper cannot block the caller. A helper that exits
// without writing anything is ErrNoImage: that is what an empty clipboard looks
// like from the outside. A missing helper is ErrUnavailable, and on darwin
// alone the osascript fallback is tried instead.
func (r *LocalImageReader) ReadImage(ctx context.Context) ([]byte, error) {
	c := r.resolve()

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b, path, err := c.resolveBackend()
	if err != nil {
		if c.GOOS == "darwin" {
			return c.readViaOsascript(ctx)
		}
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	out, err := c.Run(ctx, path, b.args)
	if err != nil {
		return nil, fmt.Errorf("clipboard: %s: %w", b.name, err)
	}
	if len(out) == 0 {
		return nil, ErrNoImage
	}
	return out, nil
}

// readViaOsascript is the darwin fallback for a machine without pngpaste: the
// clipboard's PNG flavour is written to a temp file, which is read back and
// removed.
func (r *LocalImageReader) readViaOsascript(ctx context.Context) ([]byte, error) {
	path, err := r.LookPath("osascript")
	if err != nil || path == "" {
		return nil, ErrUnavailable
	}

	f, err := os.CreateTemp("", "marshal-clipboard-*.png")
	if err != nil {
		return nil, fmt.Errorf("clipboard: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	// osascript creates the file itself: an existing one would make "open for
	// access" append to it rather than start it empty.
	_ = os.Remove(name)
	defer os.Remove(name)

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	script := fmt.Sprintf("set f to (open for access POSIX file %q with write permission)\n"+
		"set eof f to 0\n"+
		"write (the clipboard as «class PNGf») to f\n"+
		"close access f", name)
	if _, err := r.Run(ctx, path, []string{"-e", script}); err != nil {
		return nil, fmt.Errorf("clipboard: osascript: %w", err)
	}

	data, err := os.ReadFile(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("clipboard: %w", err)
	}
	if len(data) == 0 {
		return nil, ErrNoImage
	}
	return data, nil
}

// execCapture is the real Run implementation: it runs path with exactly args
// and returns stdout, with no shell in between. Stderr is bounded and folded
// into the error.
func execCapture(ctx context.Context, path string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	stderr := &boundedBuffer{limit: maxStderrBytes}
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}
