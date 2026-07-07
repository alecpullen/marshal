package sandbox

import (
	"time"
)

// nowFn returns the current monotonic-ish time. Wrapped as a function so tests
// can stub clock reads if needed (kept minimal — backends also derive
// DurationMS from process state where available).
func nowFn() time.Time {
	return time.Now()
}

// elapsedMS returns the milliseconds between start and now.
func elapsedMS(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
