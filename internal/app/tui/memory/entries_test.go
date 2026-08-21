package memory

import (
	"testing"
	"time"
)

func TestFormatAgeNegativeDuration(t *testing.T) {
	// A future timestamp (clock skew) should return "just now", not a negative
	// duration.
	future := time.Now().Add(5 * time.Minute)
	got := formatAge(future)
	if got != "just now" {
		t.Errorf("formatAge with future timestamp = %q, want %q", got, "just now")
	}
}
