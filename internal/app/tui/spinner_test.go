package tui

import "testing"

func TestSpinnerFramesWrap(t *testing.T) {
	s := NewSpinner()
	first := s.Next()
	for i := 0; i < 9; i++ {
		s.Next()
	}
	second := s.Next()
	if first != second {
		t.Fatalf("after 10 calls (len(frames)), Next() = %q, want %q (full wrap)", second, first)
	}
}

func TestSpinnerFramesAreNotEmpty(t *testing.T) {
	s := NewSpinner()
	for i := 0; i < 20; i++ {
		f := s.Next()
		if f == "" {
			t.Fatalf("frame %d is empty", i)
		}
	}
}
