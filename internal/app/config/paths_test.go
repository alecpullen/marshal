package config

import "testing"

func TestPaths(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := UserDir("/home/u"); got != "/home/u/.config/marshal" {
		t.Errorf("UserDir = %q", got)
	}
	if got := UserConfigPath("/home/u"); got != "/home/u/.config/marshal/config.toml" {
		t.Errorf("UserConfigPath = %q", got)
	}
	if got := ProjectConfigPath("/repo"); got != "/repo/.marshal/config.toml" {
		t.Errorf("ProjectConfigPath = %q", got)
	}
}
