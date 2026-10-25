package acpwire

import "time"

// Millis is t as Unix milliseconds, and 0 for the zero time.
func Millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Time is the time ms names, and the zero time for 0.
func Time(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
