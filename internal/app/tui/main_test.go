package tui

import (
	"os"
	"testing"

	"marshal/internal/testenv"
	"marshal/internal/viewmodel"
)

func TestMain(m *testing.M) {
	// Default the package to a temporary home so user-config writes
	// resolve from the injected home dir (Model.userHome) rather than the
	// developer's real environment.
	//
	// Every projection the model reads is also made afresh and compared: a
	// node that changed without its version, or a caller that changed a
	// node it was given, fails the suite instead of the transcript.
	viewmodel.CheckWireMemo.Store(true)
	os.Exit(testenv.Run(m))
}
