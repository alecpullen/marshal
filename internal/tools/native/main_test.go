package native

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so config-tool writes and on-disk
	// assertions resolve from the paths the tests pass in, not the
	// ambient environment.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
