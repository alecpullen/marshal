package config

import "path/filepath"

// UserDir returns the user-level Marshal config directory for home.
func UserDir(home string) string {
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
