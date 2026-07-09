package csync

import "sync"

// Slice is a guarded append-only slice. Load returns a fresh copy.
type Slice[T any] struct {
	mu sync.Mutex
	s  []T
}

func (s *Slice[T]) Append(x T) {
	s.mu.Lock()
	s.s = append(s.s, x)
	s.mu.Unlock()
}

func (s *Slice[T]) Load() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]T(nil), s.s...)
}

func (s *Slice[T]) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.s)
}
