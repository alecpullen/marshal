package plugins

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so GlobalStoreDir resolves
	// from the home dir the test passes in, not the ambient environment.
	os.Exit(testenv.Run(m))
}
