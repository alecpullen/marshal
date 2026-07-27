package sidepanel

import (
	"os"
	"testing"

	"marshal/internal/app/tui/theme"
)

func TestMain(m *testing.M) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	os.Exit(m.Run())
}
