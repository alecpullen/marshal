package plugins

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so GlobalStoreDir resolves from the
	// home dir the test passes in, not the ambient environment.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
