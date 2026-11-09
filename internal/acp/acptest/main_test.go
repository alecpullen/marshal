package acptest

import (
	"os"
	"testing"

	"marshal/internal/testenv"
	"marshal/internal/viewmodel"
)

func TestMain(m *testing.M) {
	// Every projection the reads serve is also made afresh and compared, as
	// the TUI's own TestMain does: a node that changed without its version
	// fails here rather than silently.
	viewmodel.CheckWireMemo.Store(true)
	os.Exit(testenv.Run(m))
}
