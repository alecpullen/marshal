package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type StandaloneBackend struct {
	headless    bool
	timeout     time.Duration
	pw          *playwright.Playwright
	browser     playwright.Browser
	mu          sync.Mutex
	startedOnce bool
}

func NewStandaloneBackend(headless bool, timeout time.Duration) (*StandaloneBackend, error) {
	return &StandaloneBackend{
		headless: headless,
		timeout:  timeout,
	}, nil
}

func (b *StandaloneBackend) ensureStarted() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startedOnce {
		return nil
	}
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright: %w", err)
	}
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(b.headless),
	})
	if err != nil {
		pw.Stop()
		return fmt.Errorf("launch chromium: %w", err)
	}
	b.pw = pw
	b.browser = browser
	b.startedOnce = true
	return nil
}

func (b *StandaloneBackend) NewPage(ctx context.Context) (PageHandle, error) {
	if err := b.ensureStarted(); err != nil {
		return nil, err
	}
	pwCtx, err := b.browser.NewContext(playwright.BrowserNewContextOptions{})
	if err != nil {
		return nil, fmt.Errorf("new browser context: %w", err)
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
	return &standalonePage{page: page, ctx: pwCtx, owned: true}, nil
}

func (b *StandaloneBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.startedOnce {
		return nil
	}
	var errs []error
	if b.browser != nil {
		if err := b.browser.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if b.pw != nil {
		if err := b.pw.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	b.startedOnce = false
	if len(errs) > 0 {
		return fmt.Errorf("standalone backend close: %v", errs)
	}
	return nil
}

func (b *StandaloneBackend) Mode() string {
	return "standalone"
}

type standalonePage struct {
	page  playwright.Page
	ctx   playwright.BrowserContext
	owned bool
}

func (p *standalonePage) Navigate(ctx context.Context, url string) error {
	_, err := p.page.Goto(url)
	return err
}

func (p *standalonePage) Title(ctx context.Context) (string, error) {
	return p.page.Title()
}

func (p *standalonePage) URL(ctx context.Context) (string, error) {
	return p.page.URL(), nil
}

func (p *standalonePage) Text(ctx context.Context, selector string) (string, error) {
	el, err := p.page.QuerySelector(selector)
	if err != nil {
		return "", err
	}
	if el == nil {
		return "", fmt.Errorf("element %q not found", selector)
	}
	return el.InnerText()
}

func (p *standalonePage) ReadableText(ctx context.Context) (string, error) {
	return p.page.InnerText("body")
}

func (p *standalonePage) Click(ctx context.Context, selector string) error {
	return p.page.Click(selector)
}

func (p *standalonePage) Fill(ctx context.Context, selector, value string) error {
	return p.page.Fill(selector, value)
}

func (p *standalonePage) PressKey(ctx context.Context, key string) error {
	return p.page.Keyboard().Press(key)
}

func (p *standalonePage) Submit(ctx context.Context, selector string) error {
	return p.page.Click(selector)
}

func (p *standalonePage) Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error) {
	pwOpts := playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(opts.FullPage),
		Type:     playwright.ScreenshotTypePng,
	}
	if opts.Format == "jpeg" {
		pwOpts.Type = playwright.ScreenshotTypeJpeg
	}
	return p.page.Screenshot(pwOpts)
}

func (p *standalonePage) Close() error {
	var errs []error
	if err := p.page.Close(); err != nil {
		errs = append(errs, err)
	}
	if p.owned {
		if err := p.ctx.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("standalone page close: %v", errs)
	}
	return nil
}
