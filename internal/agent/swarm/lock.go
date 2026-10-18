package swarm

import (
	"context"
	"sync"
)

type WriteLock struct {
	init  sync.Once
	token chan struct{}
}

func (l *WriteLock) initialize() {
	l.init.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
}
func (l *WriteLock) Acquire() func() {
	release, _ := l.AcquireContext(context.Background())
	return release
}
func (l *WriteLock) AcquireContext(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.initialize()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.token:
	}
	var once sync.Once
	release := func() { once.Do(func() { l.token <- struct{}{} }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
