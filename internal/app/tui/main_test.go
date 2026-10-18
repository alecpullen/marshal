package tui

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so user-config writes
	// resolve from the injected home dir (Model.userHome) rather than the
	// developer's real environment.
	os.Exit(testenv.Run(m))
}
