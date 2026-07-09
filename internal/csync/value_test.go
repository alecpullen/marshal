package csync

import (
	"sync"
	"testing"
)

func TestValueLoadStore(t *testing.T) {
	var v Value[int]
	if got, ok := v.Load(); ok || got != 0 {
		t.Fatalf("empty Load = (%d,%v), want (0,false)", got, ok)
	}
	v.Store(42)
	got, ok := v.Load()
	if !ok || got != 42 {
		t.Fatalf("Load = (%d,%v), want (42,true)", got, ok)
	}
}

func TestValueConcurrentStore(t *testing.T) {
	var v Value[int]
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			v.Store(n)
		}(i)
	}
	wg.Wait()
	_, ok := v.Load()
	if !ok {
		t.Fatal("Load after concurrent stores returned ok=false")
	}
}
