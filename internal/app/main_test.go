package app

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so config/trust paths resolve from
	// the home dir tests inject (HOME / WithHomeDir), not the process env.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
