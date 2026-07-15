package db

import "sync"

// ProjectLocks provides per-project-ID mutexes so that operations like
// SaveFileIndex and SaveSymbols are serialised for the same project
// but can proceed concurrently for different projects.
type ProjectLocks struct {
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

// NewProjectLocks returns an initialised ProjectLocks.
func NewProjectLocks() *ProjectLocks {
	return &ProjectLocks{locks: map[int64]*sync.Mutex{}}
}

// Lock acquires the per-project mutex for id and returns a function that
// releases it. Multiple goroutines calling Lock with the same id will
// block until the current holder calls the returned unlock function.
func (p *ProjectLocks) Lock(id int64) func() {
	p.mu.Lock()
	m, ok := p.locks[id]
	if !ok {
		m = &sync.Mutex{}
		p.locks[id] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}
