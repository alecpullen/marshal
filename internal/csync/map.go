package csync

import "sync"

// Map is a guarded generic map (sync.Map but typed).
type Map[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

func (m *Map[K, V]) Store(k K, v V) {
	m.mu.Lock()
	if m.m == nil {
		m.m = make(map[K]V)
	}
	m.m[k] = v
	m.mu.Unlock()
}

func (m *Map[K, V]) Load(k K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.m[k]
	return v, ok
}

func (m *Map[K, V]) Delete(k K) {
	m.mu.Lock()
	delete(m.m, k)
	m.mu.Unlock()
}

func (m *Map[K, V]) Range(fn func(k K, v V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.m {
		if !fn(k, v) {
			break
		}
	}
}
