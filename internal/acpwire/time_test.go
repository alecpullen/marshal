package acpwire

import (
	"testing"
	"time"
)

// TestMillisOfTheZeroTimeIsZero pins the zero-time mapping: a status field
// that was never set carries 0, not a large negative number.
func TestMillisOfTheZeroTimeIsZero(t *testing.T) {
	if got := Millis(time.Time{}); got != 0 {
		t.Errorf("Millis(zero) = %d, want 0", got)
	}
	if got := Time(0); !got.IsZero() {
		t.Errorf("Time(0) = %v, want the zero time", got)
	}
}

// TestMillisRoundTrips pins the conversion both ways.
func TestMillisRoundTrips(t *testing.T) {
	at := time.Unix(100, 5_000_000)
	if got := Time(Millis(at)); !got.Equal(at) {
		t.Errorf("Time(Millis(%v)) = %v, want the same instant", at, got)
	}
	if got := Millis(at); got != 100005 {
		t.Errorf("Millis(%v) = %d, want 100005", at, got)
	}
}
