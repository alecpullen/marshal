package theme

import "testing"

func TestThemeReloadPropagates(t *testing.T) {
	initial := LoadFor(false, "xterm-256color")
	Reload(initial)

	var got Theme
	Subscribe(func(th Theme) { got = th })

	light := LoadWithConfig("warm-sunset", ModeLight, nil)
	Reload(light)

	if got.BGBase != light.BGBase {
		t.Fatalf("subscriber not notified: got BGBase=%#v, want %#v", got.BGBase, light.BGBase)
	}
}

func TestCurrentReturnsLatest(t *testing.T) {
	initial := LoadFor(false, "xterm-256color")
	Reload(initial)
	if Current().AccentPrimary != initial.AccentPrimary {
		t.Fatal("Current() does not return the reloaded theme")
	}

	light := LoadWithConfig("warm-sunset", ModeLight, nil)
	Reload(light)
	if Current().BGBase != light.BGBase {
		t.Fatal("Current() does not return the latest reloaded theme")
	}
}
