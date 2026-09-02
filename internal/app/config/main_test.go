package config

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Tests inject a home dir and write fixture configs at the default
	// paths; blank the MARSHAL_*/XDG_* overrides so UserDir/UserConfigPath
	// resolve from that home, not the ambient environment.
	testenv.SanitizeXDG()
	os.Exit(m.Run())
}
