package acp

import (
	"os"
	"testing"

	"marshal/internal/testenv"
	"marshal/internal/viewmodel"
)

func TestMain(m *testing.M) {
	// Every projection this package makes is also made afresh and compared:
	// a node that changed without its version, or a caller that changed a
	// node it was given, fails the suite instead of the transcript.
	viewmodel.CheckWireMemo.Store(true)
	os.Exit(testenv.Run(m))
}
