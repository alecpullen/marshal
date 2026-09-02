package skills

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Blank MARSHAL_*/XDG_* overrides so panel paths and user-config
	// writes resolve from the injected home dir, not the ambient env.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
