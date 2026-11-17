package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type AttachBackend struct {
	cdpURL      string
	timeout     time.Duration
	pw          *playwright.Playwright
	browser     playwright.Browser
	mu          sync.Mutex
	startedOnce bool
}

func NewAttachBackend(cdpURL string, timeout time.Duration) (*AttachBackend, error) {
	if cdpURL == "" {
		return nil, fmt.Errorf("attach mode requires a cdp_url")
	}
	return &AttachBackend{
		cdpURL:  cdpURL,
		timeout: timeout,
	}, nil
}

func (b *AttachBackend) ensureConnected(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startedOnce {
		return nil
	}
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright: %w", err)
	}

	// ConnectOverCDP does not accept a context, so wrap it in a goroutine
	// and bound it with the configured timeout. Without this, a
	// non-responsive CDP endpoint hangs indefinitely at playwright's
	// default (TOOLS-MIN-F22).
	connectCtx := ctx
	if b.timeout > 0 {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithTimeout(ctx, b.timeout)
		defer cancel()
	}

	type connectResult struct {
		browser playwright.Browser
		err     error
	}
	ch := make(chan connectResult, 1)
	go func() {
		browser, err := pw.Chromium.ConnectOverCDP(b.cdpURL)
		ch <- connectResult{browser, err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			pw.Stop()
			return fmt.Errorf("connect to browser at %s: %w", b.cdpURL, res.err)
		}
		b.pw = pw
		b.browser = res.browser
		b.startedOnce = true
		return nil
	case <-connectCtx.Done():
		// Best-effort: stop playwright to clean up the goroutine. The
		// goroutine may still complete and write to ch, but we've already
		// returned.
		pw.Stop()
		// Distinguish parent-context cancellation from our own timeout so
		// the error message is accurate (e.g. not "timeout after 0s" when
		// the caller cancelled the context).
		if ctx.Err() != nil {
			return fmt.Errorf("connect to browser at %s: %w", b.cdpURL, ctx.Err())
		}
		return fmt.Errorf("connect to browser at %s: timeout after %s", b.cdpURL, b.timeout)
	}
}

func (b *AttachBackend) NewPage(ctx context.Context, opts PageOptions) (PageHandle, error) {
	if err := b.ensureConnected(ctx); err != nil {
		return nil, err
	}
	contexts := b.browser.Contexts()
	var pwCtx playwright.BrowserContext
	isNewCtx := false
	if len(contexts) > 0 {
		pwCtx = contexts[0]
	} else {
		var err error
		pwCtx, err = b.browser.NewContext(playwright.BrowserNewContextOptions{})
		if err != nil {
			return nil, fmt.Errorf("new browser context: %w", err)
		}
		isNewCtx = true
	}
	page, err := newPage(pwCtx, isNewCtx, b.timeout, nil)
	if err != nil {
		return nil, err
	}
	// An attached browser's context is not this process's to configure — the
	// user's session may already be running in it — so a requested viewport is
	// resized in place, best-effort: a context that refuses the size leaves the
	// page on the size the browser already had rather than failing creation.
	// The exception is the device scale factor, which is a context option this
	// page was created without: swallowing that refusal would leave the caller
	// believing the factor took effect, so it is reported instead.
	if err := applyAttachedViewport(ctx, page, opts.Viewport); err != nil {
		return nil, err
	}
	return page, nil
}

// applyAttachedViewport applies a requested viewport to a page in the user's
// attached browser. A resize is best-effort; a device scale factor the page's
// context cannot take is an error the caller has to read, because attach mode
// can never honour it.
func applyAttachedViewport(ctx context.Context, page PageHandle, v *Viewport) error {
	if v == nil || v.Width <= 0 || v.Height <= 0 {
		return nil
	}
	if err := page.SetViewport(ctx, *v); err != nil {
		if errors.Is(err, ErrViewportNeedsRecreate) {
			_ = page.Close()
			return fmt.Errorf("browser: an attached page cannot apply device_scale_factor %g; omit it", v.DeviceScaleFactor)
		}
	}
	return nil
}

func (b *AttachBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.startedOnce {
		return nil
	}
	b.startedOnce = false
	if b.browser != nil {
		_ = b.browser.Close()
	}
	if b.pw != nil {
		_ = b.pw.Stop()
	}
	return nil
}

func (b *AttachBackend) Mode() string {
	return "attach"
}
