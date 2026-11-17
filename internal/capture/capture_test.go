package capture

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// fakeRunner stands in for the compositor helpers: it records each argv it saw
// and returns canned output keyed by command name.
type fakeRunner struct {
	outputs map[string][]byte
	errs    map[string]error
	calls   []string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	out, ok := f.outputs[name]
	if !ok {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	return out, nil
}

func (f *fakeRunner) assertCalls(t *testing.T, want ...string) {
	t.Helper()
	if len(f.calls) != len(want) {
		t.Fatalf("commands = %q, want %q", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("command %d = %q, want %q (all: %q)", i, f.calls[i], want[i], f.calls)
		}
	}
}

func envWithout() Env {
	return Env{GOOS: "linux", Getenv: func(string) string { return "" }}
}

// sway focused-window tree: a workspace, a container at x=1440, a nested
// container, and the focused view below them.
const swayTreeFocused = `{
  "nodes": [
    {
      "name": "DP-1",
      "rect": {"x": 0, "y": 0, "width": 2880, "height": 1800},
      "nodes": [
        {
          "name": "1",
          "rect": {"x": 1440, "y": 0, "width": 1440, "height": 900},
          "nodes": [
            {
              "name": "H[kitty]",
              "rect": {"x": 0, "y": 0, "width": 1440, "height": 900},
              "nodes": [
                {
                  "name": "~/src/marshal",
                  "app_id": "kitty",
                  "window": 4194315,
                  "focused": true,
                  "rect": {"x": 0, "y": 0, "width": 1440, "height": 900}
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}`

func TestSwayActiveWindowGeometry(t *testing.T) {
	png := []byte("\x89PNG sway")
	r := &fakeRunner{outputs: map[string][]byte{"swaymsg": []byte(swayTreeFocused), "grim": png}}
	b := &wlrootsBackend{kind: "sway", run: r.run, env: envWithout()}

	img, err := b.Capture(context.Background(), TargetActiveWindow)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got, want := string(img.PNG), string(png); got != want {
		t.Errorf("PNG = %q, want %q", got, want)
	}
	if got, want := img.Backend, "sway"; got != want {
		t.Errorf("Backend = %q, want %q", got, want)
	}
	if got, want := img.Label, "window: kitty — ~/src/marshal"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}
	r.assertCalls(t, "swaymsg -t get_tree -r", "grim -g 1440,0 1440x900 -")
}

func TestHyprlandActiveWindowGeometry(t *testing.T) {
	png := []byte("\x89PNG hypr")
	active := []byte(`{"class":"kitty","title":"~/src/marshal","at":[0,32],"size":[1280,720]}`)
	r := &fakeRunner{outputs: map[string][]byte{"hyprctl": active, "grim": png}}
	b := &wlrootsBackend{kind: "hyprland", run: r.run, env: envWithout()}

	img, err := b.Capture(context.Background(), TargetActiveWindow)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got, want := string(img.PNG), string(png); got != want {
		t.Errorf("PNG = %q, want %q", got, want)
	}
	if got, want := img.Backend, "hyprland"; got != want {
		t.Errorf("Backend = %q, want %q", got, want)
	}
	if got, want := img.Label, "window: kitty — ~/src/marshal"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}
	r.assertCalls(t, "hyprctl -j activewindow", "grim -g 0,32 1280x720 -")
}

func TestScreenCaptureUsesNoGeometry(t *testing.T) {
	for _, kind := range []string{"sway", "hyprland"} {
		t.Run(kind, func(t *testing.T) {
			png := []byte("\x89PNG screen")
			r := &fakeRunner{outputs: map[string][]byte{"grim": png}}
			b := &wlrootsBackend{kind: kind, run: r.run, env: envWithout()}

			img, err := b.Capture(context.Background(), TargetScreen)
			if err != nil {
				t.Fatalf("Capture: %v", err)
			}
			if got, want := string(img.PNG), string(png); got != want {
				t.Errorf("PNG = %q, want %q", got, want)
			}
			if got, want := img.Label, "entire screen"; got != want {
				t.Errorf("Label = %q, want %q", got, want)
			}
			if got, want := img.Backend, kind; got != want {
				t.Errorf("Backend = %q, want %q", got, want)
			}
			r.assertCalls(t, "grim -")
		})
	}
}

func TestSwayNoFocusedWindow(t *testing.T) {
	tree := []byte(`{
      "nodes": [
        {"name": "1", "rect": {"x": 0, "y": 0, "width": 1920, "height": 1080},
         "nodes": [
           {"name": "~/src/marshal", "app_id": "kitty", "window": 4194315, "focused": false,
            "rect": {"x": 0, "y": 0, "width": 1920, "height": 1080}}
         ]}
      ]
    }`)
	r := &fakeRunner{outputs: map[string][]byte{"swaymsg": tree}}
	b := &wlrootsBackend{kind: "sway", run: r.run, env: envWithout()}

	_, err := b.Capture(context.Background(), TargetActiveWindow)
	if !errors.Is(err, ErrNoFocused) {
		t.Fatalf("err = %v, want ErrNoFocused", err)
	}
	r.assertCalls(t, "swaymsg -t get_tree -r")
}

func TestHyprlandNoFocusedWindow(t *testing.T) {
	r := &fakeRunner{outputs: map[string][]byte{"hyprctl": []byte(`{}`)}}
	b := &wlrootsBackend{kind: "hyprland", run: r.run, env: envWithout()}

	_, err := b.Capture(context.Background(), TargetActiveWindow)
	if !errors.Is(err, ErrNoFocused) {
		t.Fatalf("err = %v, want ErrNoFocused", err)
	}
	r.assertCalls(t, "hyprctl -j activewindow")
}

func TestGrimMissingMapsToErrHelperMissing(t *testing.T) {
	r := &fakeRunner{
		outputs: map[string][]byte{"swaymsg": []byte(swayTreeFocused)},
		errs:    map[string]error{"grim": &exec.Error{Name: "grim", Err: exec.ErrNotFound}},
	}
	b := &wlrootsBackend{kind: "sway", run: r.run, env: envWithout()}

	_, err := b.Capture(context.Background(), TargetActiveWindow)
	if !errors.Is(err, ErrHelperMissing) {
		t.Fatalf("err = %v, want ErrHelperMissing", err)
	}
	if !strings.Contains(err.Error(), "install grim") {
		t.Errorf("err = %v, want the install hint", err)
	}
}

func TestDetect(t *testing.T) {
	env := func(kv map[string]string) Env {
		return Env{GOOS: "linux", Getenv: func(k string) string { return kv[k] }}
	}
	kindOf := func(t *testing.T, b Backend) string {
		t.Helper()
		w, ok := b.(*wlrootsBackend)
		if !ok {
			t.Fatalf("backend = %T, want *wlrootsBackend", b)
		}
		return w.kind
	}

	tests := []struct {
		name    string
		env     Env
		forced  string
		want    string
		wantErr error
	}{
		{
			name: "swaysock only",
			env:  env(map[string]string{"SWAYSOCK": "/run/user/1000/sway-ipc.sock"}),
			want: "sway",
		},
		{
			name: "hyprland only",
			env:  env(map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "abc123"}),
			want: "hyprland",
		},
		{
			name:    "neither",
			env:     env(nil),
			wantErr: ErrNoBackend,
		},
		{
			name:   "forced overrides sway environment",
			env:    env(map[string]string{"SWAYSOCK": "/run/user/1000/sway-ipc.sock"}),
			forced: "hyprland",
			want:   "hyprland",
		},
		{
			name:    "unknown forced value",
			env:     env(map[string]string{"SWAYSOCK": "/run/user/1000/sway-ipc.sock"}),
			forced:  "x11",
			wantErr: ErrNoBackend,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Detect(tc.env, tc.forced)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got := kindOf(t, b); got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}
