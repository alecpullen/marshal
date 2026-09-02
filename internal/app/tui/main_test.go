package tui

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so user-config writes resolve from
	// the injected home dir (Model.userHome), not the process environment.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
