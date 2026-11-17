package capture

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
)

// onePixelPNG is a real, decodable PNG, standing in for what screencapture
// would write to disk.
func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encoding the fixture PNG: %v", err)
	}
	return buf.Bytes()
}

func TestDarwinScreenWritesThroughTempFile(t *testing.T) {
	want := onePixelPNG(t)
	r := &fakeRunner{shots: map[string][]byte{"screencapture": want}}
	b := &darwinBackend{run: r.run}

	img, err := b.Capture(context.Background(), TargetScreen)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(img.PNG)); err != nil {
		t.Fatalf("returned bytes are not a PNG: %v", err)
	}
	if got, want := img.Label, "entire screen"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}
	if got, want := img.Backend, "darwin"; got != want {
		t.Errorf("Backend = %q, want %q", got, want)
	}
	r.assertCalls(t, "screencapture -x -t png "+r.shotPath)
	if _, err := os.Stat(r.shotPath); !os.IsNotExist(err) {
		t.Errorf("temp file %s outlived the capture (stat err = %v)", r.shotPath, err)
	}
}

func TestDarwinActiveWindowUsesWindowID(t *testing.T) {
	want := onePixelPNG(t)
	r := &fakeRunner{
		outputs: map[string][]byte{"osascript": []byte("12345\n")},
		shots:   map[string][]byte{"screencapture": want},
	}
	b := &darwinBackend{run: r.run}

	img, err := b.Capture(context.Background(), TargetActiveWindow)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got, want := string(img.PNG), string(want); got != want {
		t.Errorf("PNG = %q, want %q", got, want)
	}
	if got, want := img.Label, "window id 12345"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}
	if got, want := img.Backend, "darwin"; got != want {
		t.Errorf("Backend = %q, want %q", got, want)
	}
	if len(r.calls) != 2 {
		t.Fatalf("commands = %q, want osascript then screencapture", r.calls)
	}
	if !strings.HasPrefix(r.calls[0], "osascript -l JavaScript -e ") ||
		!strings.Contains(r.calls[0], "CGWindowListCopyWindowInfo") {
		t.Errorf("command 0 = %q, want a single-line JXA osascript invocation", r.calls[0])
	}
	if got, want := r.calls[1], "screencapture -x -l12345 -t png "+r.shotPath; got != want {
		t.Errorf("command 1 = %q, want %q", got, want)
	}
	if _, err := os.Stat(r.shotPath); !os.IsNotExist(err) {
		t.Errorf("temp file %s outlived the capture (stat err = %v)", r.shotPath, err)
	}
}

func TestDarwinEmptyWindowIDIsNoFocused(t *testing.T) {
	r := &fakeRunner{outputs: map[string][]byte{"osascript": []byte("\n")}}
	b := &darwinBackend{run: r.run}

	_, err := b.Capture(context.Background(), TargetActiveWindow)
	if !errors.Is(err, ErrNoFocused) {
		t.Fatalf("err = %v, want ErrNoFocused", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("commands = %q, want only the osascript probe", r.calls)
	}
}

func TestDarwinTCCDeniedMapsToErrPermission(t *testing.T) {
	t.Run("helper refuses on stderr", func(t *testing.T) {
		r := &fakeRunner{
			outputs: map[string][]byte{"osascript": []byte("12345")},
			errs: map[string]error{"screencapture": errors.New(
				"screencapture -x -l12345 -t png /tmp/x.png: exit status 1: " +
					"screencapture: not authorized to capture the screen")},
		}
		b := &darwinBackend{run: r.run}

		_, err := b.Capture(context.Background(), TargetActiveWindow)
		if !errors.Is(err, ErrPermission) {
			t.Fatalf("err = %v, want ErrPermission", err)
		}
		if !strings.Contains(err.Error(), "Screen Recording") {
			t.Errorf("err = %v, want the System Settings hint", err)
		}
	})

	t.Run("helper exits without a file", func(t *testing.T) {
		r := &fakeRunner{outputs: map[string][]byte{"screencapture": nil}}
		b := &darwinBackend{run: r.run}

		_, err := b.Capture(context.Background(), TargetScreen)
		if !errors.Is(err, ErrPermission) {
			t.Fatalf("err = %v, want ErrPermission", err)
		}
	})

	// A file with no bytes in it is the same decline as no file: screencapture
	// creates the target and then writes nothing when TCC withholds the frame.
	t.Run("helper writes an empty file", func(t *testing.T) {
		r := &fakeRunner{shots: map[string][]byte{"screencapture": {}}}
		b := &darwinBackend{run: r.run}

		_, err := b.Capture(context.Background(), TargetScreen)
		if !errors.Is(err, ErrPermission) {
			t.Fatalf("err = %v, want ErrPermission", err)
		}
		if _, err := os.Stat(r.shotPath); !os.IsNotExist(err) {
			t.Errorf("temp file %s outlived the capture (stat err = %v)", r.shotPath, err)
		}
	})
}
