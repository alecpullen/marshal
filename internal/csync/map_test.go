package csync

import (
	"sync"
	"testing"
)

func TestMapStoreLoadDelete(t *testing.T) {
	var m Map[string, int]
	m.Store("a", 1)
	got, ok := m.Load("a")
	if !ok || got != 1 {
		t.Fatalf("Load(a) = (%d,%v)", got, ok)
	}
	m.Delete("a")
	if _, ok := m.Load("a"); ok {
		t.Fatal("a still present after Delete")
	}
}

func TestMapRange(t *testing.T) {
	var m Map[string, int]
	for _, kv := range []struct {
		k string
		v int
	}{{"x", 1}, {"y", 2}, {"z", 3}} {
		m.Store(kv.k, kv.v)
	}
	seen := map[string]int{}
	m.Range(func(k string, v int) bool {
		seen[k] = v
		return true
	})
	if len(seen) != 3 || seen["y"] != 2 {
		t.Fatalf("Range seen = %v", seen)
	}
}

func TestMapConcurrent(t *testing.T) {
	var m Map[int, int]
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.Store(n, n*2)
		}(i)
	}
	wg.Wait()
	var count int
	m.Range(func(_, _ int) bool { count++; return true })
	if count != 100 {
		t.Fatalf("count = %d, want 100", count)
	}
}
