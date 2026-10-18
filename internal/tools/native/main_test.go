package native

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so config-tool writes and
	// on-disk assertions resolve from the paths the tests pass in, not the
	// ambient environment.
	os.Exit(testenv.Run(m))
}
