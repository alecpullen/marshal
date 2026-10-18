package provider

import (
	"fmt"
	"sync"

	"marshal/internal/credentials"
	"marshal/internal/oauth"
)

type AuthResolver interface {
	OpenStore(service string) (credentials.Store, error)
	CodexOAuthEngine(name string) (*oauth.Engine, error)
}

type defaultAuthResolver struct{}

func DefaultAuthResolver() AuthResolver { return defaultAuthResolver{} }

func (defaultAuthResolver) OpenStore(service string) (credentials.Store, error) {
	return credentials.New(service)
}

func (defaultAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	return CodexOAuthEngine(name)
}

type scopedAuthResolver struct {
	mu      sync.Mutex
	open    func(string) (credentials.Store, error)
	stores  map[string]credentials.Store
	engines map[string]*oauth.Engine
}

func NewAuthResolver(open func(string) (credentials.Store, error)) (AuthResolver, error) {
	if open == nil {
		return nil, fmt.Errorf("auth resolver: store factory is required")
	}
	return &scopedAuthResolver{
		open: open, stores: make(map[string]credentials.Store),
		engines: make(map[string]*oauth.Engine),
	}, nil
}

func (r *scopedAuthResolver) storeLocked(service string) (credentials.Store, error) {
	if store, ok := r.stores[service]; ok {
		return store, nil
	}
	store, err := r.open(service)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("auth resolver: store factory returned nil: %w", credentials.ErrKeyringUnavailable)
	}
	r.stores[service] = store
	return store, nil
}

func (r *scopedAuthResolver) OpenStore(service string) (credentials.Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storeLocked(service)
}

func (r *scopedAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if engine, ok := r.engines[name]; ok {
		return engine, nil
	}
	store, err := r.storeLocked("marshal")
	if err != nil {
		return nil, err
	}
	engine, err := NewCodexOAuthEngineWithStore(name, store)
	if err != nil {
		return nil, err
	}
	r.engines[name] = engine
	return engine, nil
}
