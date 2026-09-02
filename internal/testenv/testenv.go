// Package testenv provides small helpers for tests that must be immune to
// the ambient process environment. Marshal resolves user-level paths from
// MARSHAL_CONFIG_DIR / XDG_CONFIG_HOME and MARSHAL_DATA_DIR /
// XDG_DATA_HOME before falling back to the injected home directory, so a
// machine (or CI runner) that exports XDG_* variables silently redirects
// any test that assumes a bare home dir.
package testenv

import "os"

// SanitizeXDG blanks the MARSHAL_*/XDG_* path overrides in the process
// environment so that config/data paths resolve from the home directory a
// test passes in (or sets via HOME). Call it from a package TestMain; it is
// safe to call multiple times.
//
// Empty-string Setenv is enough: the resolvers treat empty as unset.
func SanitizeXDG() {
	for _, key := range []string{
		"MARSHAL_CONFIG_DIR",
		"MARSHAL_DATA_DIR",
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
	} {
		_ = os.Setenv(key, "")
	}
}
