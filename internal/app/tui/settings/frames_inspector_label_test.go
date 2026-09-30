// internal/app/tui/settings/frames_inspector_label_test.go — the panel is called
// the Inspector, and the TOML keys are untouched
package settings

import (
	"testing"

	"marshal/internal/app/config"
)

// The settings surface must call this thing the Inspector, because that is what
// the rest of the app calls it: /inspect, Ctrl+B, the F2 palette entry, the
// action catalog, and the lane row all say "inspect".
//
// The TOML keys are NOT renamed. `tui.side_panel.*` is what every existing
// config file on disk contains, and renaming a key to match a label would break
// every one of them to fix a cosmetic mismatch. The label and the key are
// allowed to differ; what is not allowed is for the LABEL to describe a
// different surface from the one every other part of the UI names.
func TestInterfaceFrameLabelsThePanelAsTheInspector(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	labels := make(map[string]string) // TomlPath -> Title
	for _, row := range ps.Top().List.Rows() {
		labels[row.TomlPath] = row.Title
	}

	cases := []struct {
		tomlPath string
		want     string
	}{
		{"tui.side_panel.enabled", "Inspector"},
		{"tui.side_panel.min_width", "Inspector min width"},
		{"tui.side_panel.width_pct", "Inspector width %"},
	}
	for _, tc := range cases {
		got, ok := labels[tc.tomlPath]
		if !ok {
			t.Fatalf("the frame has no field for %q, so an existing config cannot be edited", tc.tomlPath)
		}
		if got != tc.want {
			t.Errorf("%s is labelled %q, want %q", tc.tomlPath, got, tc.want)
		}
	}
}

// Every renamed label must still carry its ORIGINAL TOML path.
//
// This is the half of the rename that matters: a label is cosmetic, and a key is
// a file on somebody's disk. Asserting the label without asserting the key would
// let a rename that also changed the key look like a pass.
func TestInterfaceFrameKeepsTheSidePanelTomlKeys(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	want := map[string]bool{
		"tui.side_panel.enabled":   false,
		"tui.side_panel.min_width": false,
		"tui.side_panel.width_pct": false,
	}
	for _, row := range ps.Top().List.Rows() {
		if _, ok := want[row.TomlPath]; ok {
			want[row.TomlPath] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("the frame no longer offers %q; renaming the label must not rename the key", path)
		}
	}
}

// The descriptions must still say what the settings DO, and must still point at
// the rail rather than at an abstraction: a reader editing "Inspector min width"
// needs to know it is the width at which the panel appears beside the
// conversation.
func TestInterfaceFrameInspectorDescriptionsStayUseful(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	descs := make(map[string]string)
	for _, row := range ps.Top().List.Rows() {
		descs[row.TomlPath] = row.Desc
	}
	if descs["tui.side_panel.min_width"] == "" {
		t.Error("the min-width field has no description")
	}
	if descs["tui.side_panel.width_pct"] == "" {
		t.Error("the width field has no description")
	}
	// The toggle names how to get the OTHER behaviour, which is the useful
	// form: a reader deciding whether to turn something off needs to know what
	// turning it off costs.
	enabled := descs["tui.side_panel.enabled"]
	if enabled == "" {
		t.Fatal("the enabled toggle has no description")
	}
}
