//go:build latencyprobe && !windows

package agent

import (
	"testing"
	"time"

	"marshal/internal/app/session/sessiontest"
	"marshal/internal/viewmodel"
)

// BenchmarkLatencyProbeUnchangedProjection isolates a projector invocation
// against a large, unchanged transcript. It does not claim to measure an idle
// process's CPU/RSS or include downstream diffing, notification and rendering.
func BenchmarkLatencyProbeUnchangedProjection(b *testing.B) {
	st := sessiontest.LargeSession(b)
	var memo viewmodel.WireMemo
	now := time.Now()
	_ = memo.ProjectState(st, true, now)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = memo.ProjectState(st, true, now)
	}
}
