package config

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Tests inject a home dir and write fixture configs at the default
	// paths; a temporary default home keeps UserDir/UserConfigPath on
	// that home instead of the ambient environment.
	os.Exit(testenv.Run(m))
}
