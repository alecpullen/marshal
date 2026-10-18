package app

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home and config/data dirs so
	// config/trust paths resolve from the home dir tests inject
	// (HOME / WithHomeDir) rather than the developer's real environment.
	os.Exit(testenv.Run(m))
}
