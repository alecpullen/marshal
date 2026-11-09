package daemon

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// The daemon's paths follow the ambient data-directory variables, so
	// every test gets a temporary home and a blank environment instead.
	os.Exit(testenv.Run(m))
}
