package csync

import (
	"sync"
	"testing"
)

func TestSliceAppendLoadLen(t *testing.T) {
	var s Slice[string]
	s.Append("a")
	s.Append("b")
	s.Append("c")
	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3", s.Len())
	}
	got := s.Load()
	if len(got) != 3 || got[1] != "b" {
		t.Fatalf("Load = %v", got)
	}
	// Load returns a copy; mutating it must not affect the slice.
	got[0] = "X"
	if s.Load()[0] != "a" {
		t.Fatal("Load did not return a copy")
	}
}

func TestSliceConcurrentAppend(t *testing.T) {
	var s Slice[int]
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Append(n)
		}(i)
	}
	wg.Wait()
	if s.Len() != 200 {
		t.Fatalf("Len = %d, want 200", s.Len())
	}
}
