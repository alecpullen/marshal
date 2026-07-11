package settings

import (
	"testing"
	"time"
)

func TestIntSetterClampsToMin(t *testing.T) {
	got := 0
	set := intSetter(1, func(v int) { got = v })
	if err := set("0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected clamp to 1, got %d", got)
	}
	if err := set("x"); err == nil {
		t.Fatal("expected error for non-number")
	}
}

func TestFloatSetter(t *testing.T) {
	var got float64
	set := floatSetter(func(v float64) { got = v })
	if err := set("3.14"); err != nil || got != 3.14 {
		t.Fatalf("expected 3.14, got %v err %v", got, err)
	}
	if err := set("x"); err == nil {
		t.Fatal("expected error for non-number")
	}
}

func TestDurationSetter(t *testing.T) {
	var got time.Duration
	set := durationSetter(func(d time.Duration) { got = d })
	if err := set("8h"); err != nil || got != 8*time.Hour {
		t.Fatalf("expected 8h, got %v err %v", got, err)
	}
	if err := set("nope"); err == nil {
		t.Fatal("expected error for bad duration")
	}
}
