package settings

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so the browser's user-config
	// path resolves from the injected home dir, not the ambient environment.
	os.Exit(testenv.Run(m))
}
