package browser

import (
	"context"
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
	browser, err := pw.Chromium.ConnectOverCDP(b.cdpURL)
	if err != nil {
		pw.Stop()
		return fmt.Errorf("connect to browser at %s: %w", b.cdpURL, err)
	}
	b.pw = pw
	b.browser = browser
	b.startedOnce = true
	return nil
}

func (b *AttachBackend) NewPage(ctx context.Context) (PageHandle, error) {
	if err := b.ensureConnected(ctx); err != nil {
		return nil, err
	}
	contexts := b.browser.Contexts()
	var pwCtx playwright.BrowserContext
	if len(contexts) > 0 {
		pwCtx = contexts[0]
	} else {
		var err error
		pwCtx, err = b.browser.NewContext(playwright.BrowserNewContextOptions{})
		if err != nil {
			return nil, fmt.Errorf("new browser context: %w", err)
		}
	}
	page, err := pwCtx.NewPage()
	if err != nil {
		pwCtx.Close()
		return nil, fmt.Errorf("new page: %w", err)
	}
	if b.timeout > 0 {
		timeoutMs := float64(b.timeout / time.Millisecond)
		page.SetDefaultTimeout(timeoutMs)
		page.SetDefaultNavigationTimeout(timeoutMs)
	}
	return &standalonePage{page: page, ctx: pwCtx}, nil
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
