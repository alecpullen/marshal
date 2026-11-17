package browser

import (
	"context"
	"testing"
)

// TestSetViewportInPlaceDoesNotRecreate: a width/height change is a page
// operation, not a context one, so the session must keep its page.
func TestSetViewportInPlaceDoesNotRecreate(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)

	first, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600}})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	second, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 1024, Height: 768}})
	if err != nil {
		t.Fatalf("Page with new size: %v", err)
	}
	if first != second {
		t.Fatal("a resize must not replace the page")
	}
	if backend.NewPageCalls != 1 {
		t.Fatalf("NewPage calls = %d, want 1", backend.NewPageCalls)
	}
	calls := backend.Page.SetViewportCalls
	if len(calls) != 1 {
		t.Fatalf("SetViewport calls = %v, want one", calls)
	}
	if calls[0].Width != 1024 || calls[0].Height != 768 {
		t.Fatalf("SetViewport got %+v, want 1024x768", calls[0])
	}
}

// TestDeviceScaleFactorRecreatesAndRenavigates: the scale factor belongs to the
// context, so the only way to apply it is a new page — and the caller must not
// lose the document it was on.
func TestDeviceScaleFactorRecreatesAndRenavigates(t *testing.T) {
	backend := &FakeBackend{}
	page := &FakePage{URLVal: "https://example.com/report"}
	backend.Page = page
	s := NewSession(backend)

	if _, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600}}); err != nil {
		t.Fatalf("Page: %v", err)
	}
	if err := s.Navigate(context.Background(), "https://example.com/report"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	// Forget the first navigation so the re-navigation is what the assertion
	// below sees.
	page.NavigatedTo = ""

	if _, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600, DeviceScaleFactor: 2}}); err != nil {
		t.Fatalf("Page with new scale factor: %v", err)
	}
	if backend.NewPageCalls != 2 {
		t.Fatalf("NewPage calls = %d, want 2 (a recreation)", backend.NewPageCalls)
	}
	if got := backend.NewPageOpts[1].Viewport; got == nil || got.DeviceScaleFactor != 2 {
		t.Fatalf("recreated with viewport %+v, want device scale factor 2", got)
	}
	if page.NavigatedTo != "https://example.com/report" {
		t.Fatalf("recreated page navigated to %q, want the last URL", page.NavigatedTo)
	}
	if len(page.SetViewportCalls) != 0 {
		t.Fatalf("SetViewport calls = %v, want none: the scale factor needs a new page", page.SetViewportCalls)
	}
	if got := s.LastURL(); got != "https://example.com/report" {
		t.Fatalf("LastURL = %q, want https://example.com/report", got)
	}
}

// TestRecreateUsesLiveURLWhenThePageMoved: a click, a submit or a redirect can
// move the page without the session hearing about it, so a recreation must
// re-navigate to where the page actually is, not to the last URL the session
// was told about.
func TestRecreateUsesLiveURLWhenThePageMoved(t *testing.T) {
	backend := &FakeBackend{}
	page := &FakePage{URLVal: "https://example.com/a"}
	backend.Page = page
	s := NewSession(backend)

	if _, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600}}); err != nil {
		t.Fatalf("Page: %v", err)
	}
	if err := s.Navigate(context.Background(), "https://example.com/a"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	// A click moved the page on; the session never learned the new URL.
	page.URLVal = "https://example.com/b"
	page.NavigatedTo = ""

	if _, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600, DeviceScaleFactor: 2}}); err != nil {
		t.Fatalf("Page with new scale factor: %v", err)
	}
	if got, want := page.NavigatedTo, "https://example.com/b"; got != want {
		t.Fatalf("recreated page navigated to %q, want the live URL %q", got, want)
	}
	if got, want := s.LastURL(), "https://example.com/b"; got != want {
		t.Fatalf("LastURL = %q, want the live URL %q", got, want)
	}

	// The session is truthful now, so a second recreation stays on the same
	// document even when the page's own URL cannot be read.
	page.URLVal = ""
	page.NavigatedTo = ""
	if _, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600}}); err != nil {
		t.Fatalf("Page with reverted scale factor: %v", err)
	}
	if got, want := page.NavigatedTo, "https://example.com/b"; got != want {
		t.Fatalf("second recreation navigated to %q, want %q from lastURL", got, want)
	}
}

// TestSetViewportErrorFallsBackToRecreate: when the page reports that the
// viewport cannot be applied in place, the session recreates and re-navigates
// rather than surfacing the sentinel to the caller.
func TestSetViewportErrorFallsBackToRecreate(t *testing.T) {
	backend := &FakeBackend{}
	page := &FakePage{URLVal: "https://example.com/form", SetViewportErr: ErrViewportNeedsRecreate}
	backend.Page = page
	s := NewSession(backend)

	if _, err := s.Page(context.Background(), PageOptions{}); err != nil {
		t.Fatalf("Page: %v", err)
	}
	if err := s.Navigate(context.Background(), "https://example.com/form"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	page.NavigatedTo = ""

	// The scale factor matches the default (0 means 1), so this goes through
	// SetViewport rather than the session's own scale-factor check.
	if _, err := s.Page(context.Background(), PageOptions{Viewport: &Viewport{Width: 1024, Height: 768}}); err != nil {
		t.Fatalf("Page after SetViewport error: %v", err)
	}
	if len(page.SetViewportCalls) != 1 {
		t.Fatalf("SetViewport calls = %v, want one attempt", page.SetViewportCalls)
	}
	if backend.NewPageCalls != 2 {
		t.Fatalf("NewPage calls = %d, want 2 (a recreation)", backend.NewPageCalls)
	}
	if page.NavigatedTo != "https://example.com/form" {
		t.Fatalf("recreated page navigated to %q, want the last URL", page.NavigatedTo)
	}
}
