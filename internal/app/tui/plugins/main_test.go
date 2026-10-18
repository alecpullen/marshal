package plugins

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so panel paths resolve from
	// the injected home dir, not the ambient environment.
	os.Exit(testenv.Run(m))
}
