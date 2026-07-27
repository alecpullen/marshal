package listpanel

import (
	"testing"
	"time"
)

func TestIntSetterClampsToMin(t *testing.T) {
	got := 0
	set := IntSetter(1, func(v int) { got = v })
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

func TestDurationSetter(t *testing.T) {
	var got time.Duration
	set := DurationSetter(func(d time.Duration) { got = d })
	if err := set("8h"); err != nil || got != 8*time.Hour {
		t.Fatalf("expected 8h, got %v err %v", got, err)
	}
	if err := set("nope"); err == nil {
		t.Fatal("expected error for bad duration")
	}
}
