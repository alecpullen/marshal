package config

import (
	"path/filepath"
	"testing"
)

func TestUserDirHonoursMarshalConfigDir(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "/custom/cfg")
	if got := UserDir("/home/u"); got != "/custom/cfg" {
		t.Errorf("UserDir = %q", got)
	}
}

func TestUserDirHonoursXDGConfigHome(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg/cfg")
	if got := UserDir("/home/u"); got != filepath.Join("/xdg/cfg", "marshal") {
		t.Errorf("UserDir = %q", got)
	}
}

func TestUserDirFallsBackToHome(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := UserDir("/home/u"); got != "/home/u/.config/marshal" {
		t.Errorf("UserDir = %q", got)
	}
}

func TestDataDirHonoursMarshalDataDir(t *testing.T) {
	t.Setenv("MARSHAL_DATA_DIR", "/custom/data")
	if got := DataDir("/home/u"); got != "/custom/data" {
		t.Errorf("DataDir = %q", got)
	}
}

func TestDataDirHonoursXDGDataHome(t *testing.T) {
	t.Setenv("MARSHAL_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got := DataDir("/home/u"); got != filepath.Join("/xdg/data", "marshal") {
		t.Errorf("DataDir = %q", got)
	}
}

func TestDataDirFallsBackToHome(t *testing.T) {
	t.Setenv("MARSHAL_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	if got := DataDir("/home/u"); got != "/home/u/.local/share/marshal" {
		t.Errorf("DataDir = %q", got)
	}
}