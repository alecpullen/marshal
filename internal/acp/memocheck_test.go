package acp

import (
	"testing"

	"marshal/internal/viewmodel"
)

// withoutMemoCheck turns viewmodel.CheckWireMemo off until tb ends, so a
// budget test or benchmark measures the memo and not the check.
func withoutMemoCheck(tb testing.TB) {
	tb.Helper()
	prev := viewmodel.CheckWireMemo.Load()
	viewmodel.CheckWireMemo.Store(false)
	tb.Cleanup(func() { viewmodel.CheckWireMemo.Store(prev) })
}
