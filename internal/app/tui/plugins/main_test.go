package plugins

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so panel paths resolve from the
	// injected home dir, not the ambient environment.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
