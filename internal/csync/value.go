// Package csync provides small generic concurrent-state primitives (F19 R3)
// adopted for new concurrent state instead of ad-hoc mutex fields on
// session.State. They are thin wrappers over atomic/sync with copy-on-read
// semantics so callers never observe a mutated value mid-flight.
package csync

import "sync"

// Value is a guarded single value. Load returns (zero, false) until the
// first Store, (value, true) afterwards.
type Value[T any] struct {
	mu  sync.RWMutex
	v   T
	set bool
}

func (v *Value[T]) Store(x T) {
	v.mu.Lock()
	v.v = x
	v.set = true
	v.mu.Unlock()
}

func (v *Value[T]) Load() (T, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.v, v.set
}
