package viewmodel

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	CheckWireMemo.Store(true)
	os.Exit(testenv.Run(m))
}
