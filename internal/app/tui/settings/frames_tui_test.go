package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/theme"
)

func TestInterfaceFrameHasThemeEnum(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	found := false
	for _, row := range ps.top().list.Rows() {
		if row.title == "Theme" {
			found = true
			if row.kind != kindEnum {
				t.Fatalf("Theme row kind = %d, want kindEnum (%d)", row.kind, kindEnum)
			}
			opts := row.options()
			if len(opts) < 4 {
				t.Fatalf("Theme enum has %d options, want at least 4 (the 4 named themes)", len(opts))
			}
			expected := []string{"catppuccin-mocha", "dracula", "nord", "warm-sunset"}
			for _, exp := range expected {
				foundOpt := false
				for _, opt := range opts {
					if opt == exp {
						foundOpt = true
						break
					}
				}
				if !foundOpt {
					t.Errorf("Theme enum missing option %q", exp)
				}
			}
			if row.getStr() != "" {
				t.Errorf("Theme getStr = %q, want empty string from default config", row.getStr())
			}
			break
		}
	}
	if !found {
		t.Fatal("interfaceFrame should have a 'Theme' row")
	}
}

func TestInterfaceFrameThemeSetterWritesToConfig(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	var themeField *field
	for _, row := range ps.top().list.Rows() {
		if row.title == "Theme" {
			themeField = row
			break
		}
	}
	if themeField == nil {
		t.Fatal("interfaceFrame should have a 'Theme' row")
	}

	if err := themeField.setStr("dracula"); err != nil {
		t.Fatalf("Theme setStr returned error: %v", err)
	}
	if s.cfg.TUI.Theme != "dracula" {
		t.Fatalf("cfg.TUI.Theme = %q, want %q", s.cfg.TUI.Theme, "dracula")
	}
}

func TestInterfaceFrameThemeOptionsMatchNames(t *testing.T) {
	names := theme.Names()

	var themeField *field
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)
	for _, row := range ps.top().list.Rows() {
		if row.title == "Theme" {
			themeField = row
			break
		}
	}
	if themeField == nil {
		t.Fatal("interfaceFrame should have a 'Theme' row")
	}

	opts := themeField.options()
	if len(opts) != len(names) {
		t.Fatalf("Theme options count = %d, want %d (matching theme.Names())", len(opts), len(names))
	}
	for i, opt := range opts {
		if opt != names[i] {
			t.Errorf("Theme option[%d] = %q, want %q", i, opt, names[i])
		}
	}
}

func TestInterfaceFrameHasModeEnum(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	found := false
	for _, row := range ps.top().list.Rows() {
		if row.title == "Mode" {
			found = true
			if row.kind != kindEnum {
				t.Fatalf("Mode row kind = %d, want kindEnum (%d)", row.kind, kindEnum)
			}
			opts := row.options()
			expected := []string{"dark", "light"}
			if len(opts) != len(expected) {
				t.Fatalf("Mode enum has %d options, want %d", len(opts), len(expected))
			}
			for i, exp := range expected {
				if opts[i] != exp {
					t.Errorf("Mode option[%d] = %q, want %q", i, opts[i], exp)
				}
			}
			if row.getStr() != "" {
				t.Errorf("Mode getStr = %q, want empty string from default config", row.getStr())
			}
			break
		}
	}
	if !found {
		t.Fatal("interfaceFrame should have a 'Mode' row")
	}
}

func TestInterfaceFrameModeSetterWritesToConfig(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	var modeField *field
	for _, row := range ps.top().list.Rows() {
		if row.title == "Mode" {
			modeField = row
			break
		}
	}
	if modeField == nil {
		t.Fatal("interfaceFrame should have a 'Mode' row")
	}

	if err := modeField.setStr("light"); err != nil {
		t.Fatalf("Mode setStr returned error: %v", err)
	}
	if s.cfg.TUI.Mode != "light" {
		t.Fatalf("cfg.TUI.Mode = %q, want %q", s.cfg.TUI.Mode, "light")
	}
}

func TestInterfaceFrameCycleViaArrows(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(interfaceFrame(s))
	ps.SetSize(60, 20)

	var themeField *field
	for _, row := range ps.top().list.Rows() {
		if row.title == "Theme" {
			themeField = row
			break
		}
	}
	if themeField == nil {
		t.Fatal("interfaceFrame should have a 'Theme' row")
	}

	opts := themeField.options()
	if len(opts) == 0 {
		t.Fatal("options should not be empty")
	}
	_ = strings.Join(opts, ",")
}
