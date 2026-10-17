package skills

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so panel paths and
	// user-config writes resolve from the injected home dir, not the
	// ambient env.
	os.Exit(testenv.Run(m))
}
