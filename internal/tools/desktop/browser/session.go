package browser

import (
	"context"
	"errors"
	"sync"
)

type Session struct {
	backend BrowserBackend
	page    PageHandle
	mu      sync.Mutex
	closed  bool
}

func NewSession(backend BrowserBackend) *Session {
	return &Session{backend: backend}
}

func (s *Session) Page(ctx context.Context) (PageHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errSessionClosed
	}
	if s.page != nil {
		return s.page, nil
	}
	page, err := s.backend.NewPage(ctx)
	if err != nil {
		return nil, err
	}
	s.page = page
	return page, nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var pageErr, backendErr error
	if s.page != nil {
		pageErr = s.page.Close()
	}
	backendErr = s.backend.Close()
	if pageErr != nil {
		return pageErr
	}
	return backendErr
}

var errSessionClosed = errors.New("desktop session is closed")
