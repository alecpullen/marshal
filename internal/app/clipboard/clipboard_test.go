package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Compile-time proof that the concrete writer satisfies the pinned interface.
var _ Writer = (*LocalWriter)(nil)

// call records one invocation of the injected Run function.
type call struct {
	path  string
	args  []string
	stdin string
}

// recorder is a fake Run implementation that records every invocation.
type recorder struct {
	calls []call
	err   error
	block bool
}

func (r *recorder) run(ctx context.Context, path string, args []string, stdin string) error {
	r.calls = append(r.calls, call{
		path:  path,
		args:  append([]string(nil), args...),
		stdin: stdin,
	})
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.err
}

// writer builds a LocalWriter wired to this recorder.
func (r *recorder) writer(goos string, env map[string]string, present ...string) *LocalWriter {
	return &LocalWriter{
		LookPath: lookPath(present...),
		Run:      r.run,
		GOOS:     goos,
		Env:      envFunc(env),
		Timeout:  time.Second,
	}
}

// lookPath returns a LookPath stub that resolves exactly the named executables.
func lookPath(present ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(present, name) {
			return "/usr/bin/" + name, nil
		}
		return "", fmt.Errorf("exec: %q: executable file not found in $PATH", name)
	}
}

func envFunc(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestWriteDeliversExactBytes(t *testing.T) {
	inputs := []string{
		"",
		"hello world",
		"emoji 🚀 and CJK 漢字テスト",
		"tab\tseparated\tvalues",
		"trailing newline\n",
		"two\nnewlines\n\n",
		"  leading and trailing spaces  ",
		"crlf\r\nline\r\n",
		"mixed \t 🎉 \n end",
	}

	for _, input := range inputs {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			rec := &recorder{}
			w := rec.writer("darwin", nil, "pbcopy")

			if err := w.Write(context.Background(), input); err != nil {
				t.Fatalf("Write() error = %v, want nil", err)
			}
			if len(rec.calls) != 1 {
				t.Fatalf("Run called %d times, want 1", len(rec.calls))
			}
			got := rec.calls[0].stdin
			if got != input {
				t.Fatalf("stdin = %q, want %q", got, input)
			}
			if len(got) != len(input) {
				t.Fatalf("stdin length = %d, want %d", len(got), len(input))
			}
			if !slices.Equal([]byte(got), []byte(input)) {
				t.Fatalf("stdin bytes = %v, want %v", []byte(got), []byte(input))
			}
		})
	}
}

func TestBackendSelection(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		present  []string
		wantPath string
		wantArgs []string
	}{
		{
			name:     "darwin uses pbcopy",
			goos:     "darwin",
			present:  []string{"pbcopy"},
			wantPath: "/usr/bin/pbcopy",
			wantArgs: []string{},
		},
		{
			name:     "linux wayland uses wl-copy",
			goos:     "linux",
			env:      map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			present:  []string{"wl-copy", "xclip", "xsel"},
			wantPath: "/usr/bin/wl-copy",
			wantArgs: []string{},
		},
		{
			name:     "linux x11 uses xclip",
			goos:     "linux",
			present:  []string{"xclip", "xsel"},
			wantPath: "/usr/bin/xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
		{
			name:     "linux prefers xclip over xsel",
			goos:     "linux",
			present:  []string{"xsel", "xclip"},
			wantPath: "/usr/bin/xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
		{
			name:     "linux falls back to xsel when xclip is missing",
			goos:     "linux",
			present:  []string{"xsel"},
			wantPath: "/usr/bin/xsel",
			wantArgs: []string{"--clipboard", "--input"},
		},
		{
			name:     "empty WAYLAND_DISPLAY is not wayland",
			goos:     "linux",
			env:      map[string]string{"WAYLAND_DISPLAY": ""},
			present:  []string{"xclip"},
			wantPath: "/usr/bin/xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			w := rec.writer(tc.goos, tc.env, tc.present...)

			if err := w.Write(context.Background(), "payload"); err != nil {
				t.Fatalf("Write() error = %v, want nil", err)
			}
			if len(rec.calls) != 1 {
				t.Fatalf("Run called %d times, want 1", len(rec.calls))
			}
			got := rec.calls[0]
			if got.path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.path, tc.wantPath)
			}
			if !slices.Equal(got.args, tc.wantArgs) {
				t.Errorf("args = %q, want %q", got.args, tc.wantArgs)
			}
			if got.stdin != "payload" {
				t.Errorf("stdin = %q, want %q", got.stdin, "payload")
			}
		})
	}
}

func TestUnavailableBackends(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		present []string
	}{
		{
			name: "darwin without pbcopy",
			goos: "darwin",
		},
		{
			name: "linux without xclip or xsel",
			goos: "linux",
		},
		{
			name:    "linux wayland without wl-copy",
			goos:    "linux",
			env:     map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			present: []string{"xclip", "xsel"},
		},
		{
			name:    "unsupported GOOS windows",
			goos:    "windows",
			present: []string{"pbcopy", "xclip", "xsel", "wl-copy"},
		},
		{
			name:    "unsupported GOOS plan9",
			goos:    "plan9",
			present: []string{"xclip"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			w := rec.writer(tc.goos, tc.env, tc.present...)

			err := w.Write(context.Background(), "payload")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Write() error = %v, want ErrUnavailable", err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("Run called %d times, want 0", len(rec.calls))
			}
			if w.Available() {
				t.Fatal("Available() = true, want false")
			}
		})
	}
}

func TestAvailableDoesNotRunBackend(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		present []string
		want    bool
	}{
		{name: "darwin with pbcopy", goos: "darwin", present: []string{"pbcopy"}, want: true},
		{name: "darwin without pbcopy", goos: "darwin", want: false},
		{
			name:    "linux wayland with wl-copy",
			goos:    "linux",
			env:     map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			present: []string{"wl-copy"},
			want:    true,
		},
		{name: "linux with xclip", goos: "linux", present: []string{"xclip"}, want: true},
		{name: "linux with xsel only", goos: "linux", present: []string{"xsel"}, want: true},
		{name: "linux with nothing", goos: "linux", want: false},
		{name: "unsupported GOOS", goos: "windows", present: []string{"pbcopy"}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			w := rec.writer(tc.goos, tc.env, tc.present...)

			if got := w.Available(); got != tc.want {
				t.Fatalf("Available() = %v, want %v", got, tc.want)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("Available() ran the backend %d times, want 0", len(rec.calls))
			}
		})
	}
}

func TestWriteTimesOut(t *testing.T) {
	rec := &recorder{block: true}
	w := rec.writer("darwin", nil, "pbcopy")
	w.Timeout = 50 * time.Millisecond

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- w.Write(context.Background(), "payload") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Write() error = nil, want a timeout error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Write() error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Write() took %v, want it bounded by the writer timeout", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Write() hung: the writer timeout was not applied")
	}
}

func TestWriteWithAlreadyCancelledContext(t *testing.T) {
	rec := &recorder{}
	w := rec.writer("darwin", nil, "pbcopy")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.Write(ctx, "payload")
	if err == nil {
		t.Fatal("Write() error = nil, want a cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write() error = %v, want context.Canceled", err)
	}
}

func TestWriteSurfacesRunError(t *testing.T) {
	sentinel := errors.New("boom")
	rec := &recorder{err: sentinel}
	w := rec.writer("darwin", nil, "pbcopy")

	err := w.Write(context.Background(), "payload")
	if err == nil {
		t.Fatal("Write() error = nil, want the run error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Write() error = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "pbcopy") {
		t.Fatalf("Write() error = %v, want it to name the backend %q", err, "pbcopy")
	}
}

func TestNoShellInvocation(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		present  []string
		wantPath string
		wantArgs []string
	}{
		{
			name:     "darwin",
			goos:     "darwin",
			present:  []string{"pbcopy"},
			wantPath: "/usr/bin/pbcopy",
			wantArgs: []string{},
		},
		{
			name:     "linux wayland",
			goos:     "linux",
			env:      map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			present:  []string{"wl-copy"},
			wantPath: "/usr/bin/wl-copy",
			wantArgs: []string{},
		},
		{
			name:     "linux xclip",
			goos:     "linux",
			present:  []string{"xclip"},
			wantPath: "/usr/bin/xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
		{
			name:     "linux xsel",
			goos:     "linux",
			present:  []string{"xsel"},
			wantPath: "/usr/bin/xsel",
			wantArgs: []string{"--clipboard", "--input"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			var looked []string
			w := rec.writer(tc.goos, tc.env, tc.present...)
			inner := w.LookPath
			w.LookPath = func(name string) (string, error) {
				looked = append(looked, name)
				return inner(name)
			}

			if err := w.Write(context.Background(), "payload"); err != nil {
				t.Fatalf("Write() error = %v, want nil", err)
			}
			if len(rec.calls) != 1 {
				t.Fatalf("Run called %d times, want 1", len(rec.calls))
			}
			got := rec.calls[0]

			// The executable must be the one LookPath resolved, invoked directly.
			if got.path != tc.wantPath {
				t.Errorf("path = %q, want the resolved executable %q", got.path, tc.wantPath)
			}
			if !slices.Contains(looked, filepath.Base(tc.wantPath)) {
				t.Errorf("LookPath calls = %q, want one for %q", looked, filepath.Base(tc.wantPath))
			}
			if !slices.Equal(got.args, tc.wantArgs) {
				t.Errorf("args = %q, want exactly %q", got.args, tc.wantArgs)
			}
			// No shell wrapper, ever.
			base := filepath.Base(got.path)
			if base == "sh" || base == "bash" || base == "zsh" || base == "cmd" {
				t.Errorf("path = %q, want a clipboard executable, not a shell", got.path)
			}
			if slices.Contains(got.args, "-c") {
				t.Errorf("args = %q, want no shell -c invocation", got.args)
			}
		})
	}
}

func TestRemote(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "SSH_CONNECTION", env: map[string]string{"SSH_CONNECTION": "10.0.0.1 5000 10.0.0.2 22"}, want: true},
		{name: "SSH_TTY", env: map[string]string{"SSH_TTY": "/dev/pts/3"}, want: true},
		{name: "both", env: map[string]string{"SSH_CONNECTION": "a", "SSH_TTY": "b"}, want: true},
		{name: "neither", env: map[string]string{"TERM": "xterm-256color"}, want: false},
		{name: "empty values", env: map[string]string{"SSH_CONNECTION": "", "SSH_TTY": ""}, want: false},
		{name: "nil env map", env: nil, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Remote(envFunc(tc.env)); got != tc.want {
				t.Fatalf("Remote() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExecRunFeedsStdinByteForByte exercises the real runner end to end using a
// genuine executable (tee) as the backend, with no shell involved.
func TestExecRunFeedsStdinByteForByte(t *testing.T) {
	tee, err := exec.LookPath("tee")
	if err != nil {
		t.Skipf("tee not available: %v", err)
	}

	inputs := []string{
		"",
		"hello world",
		"emoji 🚀 and CJK 漢字テスト",
		"tab\tseparated\tvalues",
		"trailing newline\n",
		"two\nnewlines\n\n",
	}

	for _, input := range inputs {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "captured")

			if err := execRun(context.Background(), tee, []string{out}, input); err != nil {
				t.Fatalf("execRun() error = %v, want nil", err)
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("reading captured output: %v", err)
			}
			if !bytes.Equal(got, []byte(input)) {
				t.Fatalf("captured %q, want %q", got, input)
			}
		})
	}
}

// TestExecRunReportsFailure proves a non-zero exit reaches the caller.
func TestExecRunReportsFailure(t *testing.T) {
	err := execRun(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), nil, "payload")
	if err == nil {
		t.Fatal("execRun() error = nil, want an error for a missing executable")
	}
}

// TestExecRunHonoursContext proves the real runner is cancellable.
func TestExecRunHonoursContext(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- execRun(ctx, sleep, []string{"30"}, "") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("execRun() error = nil, want a cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execRun() hung: the context was not honoured")
	}
}

func TestZeroValueResolvesRealDefaults(t *testing.T) {
	var w LocalWriter
	r := w.resolve()

	if r.LookPath == nil {
		t.Error("resolve() left LookPath nil")
	}
	if r.Run == nil {
		t.Error("resolve() left Run nil")
	}
	if r.Env == nil {
		t.Error("resolve() left Env nil")
	}
	if r.GOOS == "" {
		t.Error("resolve() left GOOS empty")
	}
	if r.Timeout != 2*time.Second {
		t.Errorf("resolve() Timeout = %v, want 2s", r.Timeout)
	}
}

func TestZeroValueAvailableDoesNotPanic(t *testing.T) {
	var w LocalWriter
	_ = w.Available()
}
