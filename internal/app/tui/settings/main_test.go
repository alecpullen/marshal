package settings

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so the browser's user-config path
	// resolves from the injected home dir, not the ambient environment.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
