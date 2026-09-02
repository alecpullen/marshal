package config

import (
	"os"
	"path/filepath"
)

// UserDir returns the user-level Marshal config directory for home.
// MARSHAL_CONFIG_DIR overrides the location outright; otherwise the XDG
// base directory specification is honoured ($XDG_CONFIG_HOME/marshal,
// defaulting to ~/.config/marshal).
func UserDir(home string) string {
	if dir := os.Getenv("MARSHAL_CONFIG_DIR"); dir != "" {
		return dir
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "marshal")
	}
	return filepath.Join(home, ".config", "marshal")
}

// UserConfigPath returns the user-level config file path for home.
func UserConfigPath(home string) string {
	return filepath.Join(UserDir(home), "config.toml")
}

// ProjectConfigPath returns the project-local config file path for work.
func ProjectConfigPath(work string) string {
	return filepath.Join(work, ".marshal", "config.toml")
}

// DataDir returns the Marshal data directory for home (trust store,
// model cache, logs). MARSHAL_DATA_DIR overrides the location outright;
// otherwise $XDG_DATA_HOME/marshal is honoured, defaulting to
// ~/.local/share/marshal.
func DataDir(home string) string {
	if dir := os.Getenv("MARSHAL_DATA_DIR"); dir != "" {
		return dir
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "marshal")
	}
	return filepath.Join(home, ".local", "share", "marshal")
}
