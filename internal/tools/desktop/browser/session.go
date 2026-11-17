package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Session struct {
	backend BrowserBackend
	page    PageHandle
	// pageViewport records the viewport the current page was created with, nil
	// when the backend gave it its own default (whose scale factor is 1).
	pageViewport *Viewport
	// lastURL is the last URL this session navigated to, so a page recreated
	// for a new viewport comes back on the same document.
	lastURL string
	mu      sync.Mutex
	closed  bool
}

func NewSession(backend BrowserBackend) *Session {
	return &Session{backend: backend}
}

// Page returns the session's page, creating it with opts when there is none
// yet. A live page is resized in place for a new viewport, but a different
// device scale factor is a context option: the page is recreated with opts and
// re-navigated to LastURL instead, so the caller keeps its document.
func (s *Session) Page(ctx context.Context, opts PageOptions) (PageHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pageLocked(ctx, opts)
}

// Navigate navigates the session's page and records the URL on success, so the
// session knows what a recreation has to come back to. It creates the page
// when there is none yet.
func (s *Session) Navigate(ctx context.Context, url string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	page, err := s.pageLocked(ctx, PageOptions{})
	if err != nil {
		return err
	}
	if err := page.Navigate(ctx, url); err != nil {
		return err
	}
	s.lastURL = url
	return nil
}

// LastURL reports the last URL this session navigated to, "" before the first
// navigation.
func (s *Session) LastURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

// pageLocked is Page's body; the caller holds s.mu.
func (s *Session) pageLocked(ctx context.Context, opts PageOptions) (PageHandle, error) {
	if s.closed {
		return nil, errSessionClosed
	}
	if s.page == nil {
		return s.createLocked(ctx, opts)
	}
	if opts.Viewport == nil {
		return s.page, nil
	}
	if opts.Viewport.deviceScaleFactor() != s.pageViewport.deviceScaleFactor() {
		return s.recreateLocked(ctx, opts)
	}
	if err := s.page.SetViewport(ctx, *opts.Viewport); err != nil {
		if errors.Is(err, ErrViewportNeedsRecreate) {
			return s.recreateLocked(ctx, opts)
		}
		return nil, err
	}
	s.pageViewport = viewportCopy(opts.Viewport)
	return s.page, nil
}

// createLocked builds a page for opts; the caller holds s.mu.
func (s *Session) createLocked(ctx context.Context, opts PageOptions) (PageHandle, error) {
	page, err := s.backend.NewPage(ctx, opts)
	if err != nil {
		return nil, err
	}
	s.page = page
	s.pageViewport = viewportCopy(opts.Viewport)
	return page, nil
}

// recreateLocked replaces the current page with one created for opts and
// re-navigates it to lastURL: a recreation exists to change a context option,
// and must not cost the caller the document it was reading. The caller holds
// s.mu.
func (s *Session) recreateLocked(ctx context.Context, opts PageOptions) (PageHandle, error) {
	if s.page != nil {
		// Best-effort: the page is being replaced, so a failed close must not
		// stop the replacement.
		_ = s.page.Close()
		s.page = nil
		s.pageViewport = nil
	}
	page, err := s.createLocked(ctx, opts)
	if err != nil {
		return nil, err
	}
	if s.lastURL != "" {
		if err := page.Navigate(ctx, s.lastURL); err != nil {
			return nil, fmt.Errorf("re-navigate to %s: %w", s.lastURL, err)
		}
	}
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
