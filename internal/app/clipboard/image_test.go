package clipboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Compile-time proof that the concrete reader satisfies the pinned interface.
var _ Reader = (*LocalImageReader)(nil)

// imageRecorder is a fake Run implementation for the image reader.
type imageRecorder struct {
	calls [][]string
	out   []byte
	err   error
}

func (r *imageRecorder) run(ctx context.Context, path string, args []string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{path}, args...))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.out, r.err
}

// reader builds a LocalImageReader wired to this recorder.
func (r *imageRecorder) reader(goos string, env map[string]string, present ...string) *LocalImageReader {
	return &LocalImageReader{
		LookPath: lookPath(present...),
		Run:      r.run,
		GOOS:     goos,
		Env:      envFunc(env),
		Timeout:  time.Second,
	}
}

// TestReadImageCandidates pins the linux backend order: wl-paste under
// Wayland, xclip elsewhere.
func TestReadImageCandidates(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		present string
		want    string
		wantArg []string
	}{
		{
			name:    "wayland",
			env:     map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			present: "wl-paste",
			want:    "wl-paste",
			wantArg: []string{"--type", "image/png", "--no-newline"},
		},
		{
			name:    "x11",
			present: "xclip",
			want:    "xclip",
			wantArg: []string{"-selection", "clipboard", "-t", "image/png", "-o"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &imageRecorder{out: []byte("\x89PNG fixture")}
			r := rec.reader("linux", tc.env, tc.present)

			got, err := r.ReadImage(context.Background())
			if err != nil {
				t.Fatalf("ReadImage() error = %v, want nil", err)
			}
			if string(got) != "\x89PNG fixture" {
				t.Fatalf("ReadImage() = %q, want the helper's stdout", got)
			}
			if len(rec.calls) != 1 {
				t.Fatalf("Run called %d times, want 1", len(rec.calls))
			}
			call := rec.calls[0]
			if call[0] != "/usr/bin/"+tc.want {
				t.Fatalf("Run path = %q, want /usr/bin/%s", call[0], tc.want)
			}
			if len(call)-1 != len(tc.wantArg) {
				t.Fatalf("Run args = %v, want %v", call[1:], tc.wantArg)
			}
			for i, arg := range tc.wantArg {
				if call[i+1] != arg {
					t.Fatalf("Run args = %v, want %v", call[1:], tc.wantArg)
				}
			}
		})
	}
}

// TestReadImageMissingToolIsUnavailable reports a helper the machine does not
// have as ErrUnavailable, so the caller can name the package to install.
func TestReadImageMissingToolIsUnavailable(t *testing.T) {
	rec := &imageRecorder{out: []byte("unused")}
	r := rec.reader("linux", nil)

	if _, err := r.ReadImage(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ReadImage() error = %v, want ErrUnavailable", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("Run called %d times, want 0", len(rec.calls))
	}
}

// TestReadImageEmptyIsNoImage pins an empty stdout as an empty clipboard.
func TestReadImageEmptyIsNoImage(t *testing.T) {
	rec := &imageRecorder{}
	r := rec.reader("linux", nil, "xclip")

	if _, err := r.ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("ReadImage() error = %v, want ErrNoImage", err)
	}
}
