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
	browser, err := pw.Chromium.Launch(standaloneLaunchOptions(b.headless))
	if err != nil {
		pw.Stop()
		return fmt.Errorf("launch chromium: %w", err)
	}
	b.pw = pw
	b.browser = browser
	b.startedOnce = true
	return nil
}

// standaloneLaunchOptions builds the BrowserTypeLaunchOptions for the
// standalone Chromium launch. It hardens the launch (TOOLS-MOD-F12):
//   - --no-sandbox: necessary when running inside a container or when the
//     user lacks permission to use Chromium's setuid sandbox. Combined
//     with ChromiumSandbox=false, this disables the sandbox entirely.
//
// Note: a dedicated --user-data-dir is intentionally NOT passed here.
// playwright's Launch API manages its own temporary profile by default and
// cleans it up on close, so the user's real Chromium profile is never
// touched. Passing --user-data-dir to Launch is rejected by playwright
// (it belongs on launchPersistentContext instead).
//
// A privilege drop to the "nobody" user is also intentionally NOT applied:
// playwright-go manages the browser process internally and does not expose
// SysProcAttr on BrowserTypeLaunchOptions, so we cannot set a Credential.
// The --no-sandbox + ChromiumSandbox=false flags are the primary hardening;
// a future custom browser-launch path could add a privilege drop.
func standaloneLaunchOptions(headless bool) playwright.BrowserTypeLaunchOptions {
	return playwright.BrowserTypeLaunchOptions{
		Headless:        playwright.Bool(headless),
		ChromiumSandbox: playwright.Bool(false),
		Args:            []string{"--no-sandbox"},
	}
}

// newContextOptions builds the context options for opts. The viewport is a
// context option: applying it here is what makes the device scale factor take
// effect at creation time. An absent or unusable size keeps playwright's
// default context, which is what this backend has always used.
func newContextOptions(opts PageOptions) playwright.BrowserNewContextOptions {
	if opts.Viewport == nil || opts.Viewport.Width <= 0 || opts.Viewport.Height <= 0 {
		return playwright.BrowserNewContextOptions{}
	}
	return playwright.BrowserNewContextOptions{
		Viewport: &playwright.Size{
			Width:  opts.Viewport.Width,
			Height: opts.Viewport.Height,
		},
		DeviceScaleFactor: playwright.Float(opts.Viewport.deviceScaleFactor()),
	}
}

func (b *StandaloneBackend) NewPage(ctx context.Context, opts PageOptions) (PageHandle, error) {
	if err := b.ensureStarted(); err != nil {
		return nil, err
	}
	pwCtx, err := b.browser.NewContext(newContextOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("new browser context: %w", err)
	}
	return newPage(pwCtx, true, b.timeout, opts.Viewport)
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

// newPage creates a page in pwCtx and wraps it in a standalonePage,
// applying the backend's default timeouts and recording the viewport the
// context was created with. owned records whether the caller created pwCtx
// (and therefore closes it on page-creation failure and passes ownership to
// the page).
func newPage(pwCtx playwright.BrowserContext, owned bool, timeout time.Duration, viewport *Viewport) (*standalonePage, error) {
	page, err := pwCtx.NewPage()
	if err != nil {
		if owned {
			_ = pwCtx.Close()
		}
		return nil, fmt.Errorf("new page: %w", err)
	}
	if timeout > 0 {
		timeoutMs := float64(timeout / time.Millisecond)
		page.SetDefaultTimeout(timeoutMs)
		page.SetDefaultNavigationTimeout(timeoutMs)
	}
	p := &standalonePage{page: page, ctx: pwCtx, owned: owned}
	if viewport != nil && viewport.Width > 0 && viewport.Height > 0 {
		p.viewport = viewportCopy(viewport)
	}
	return p, nil
}

type standalonePage struct {
	page  playwright.Page
	ctx   playwright.BrowserContext
	owned bool
	// viewport is the viewport the context was created with, nil when the
	// page runs on playwright's default one (whose scale factor is 1).
	viewport *Viewport
}

// deviceScaleFactor is the scale factor of the page's context. It is fixed
// when the context is created, so it cannot change while this page lives.
func (p *standalonePage) deviceScaleFactor() float64 {
	return p.viewport.deviceScaleFactor()
}

// SetViewport resizes the page in place. A different device scale factor is a
// context option and cannot be applied here: it is reported so the caller
// recreates the page instead.
func (p *standalonePage) SetViewport(ctx context.Context, v Viewport) error {
	dsf := p.deviceScaleFactor()
	if v.deviceScaleFactor() != dsf {
		return ErrViewportNeedsRecreate
	}
	if err := p.page.SetViewportSize(v.Width, v.Height); err != nil {
		return err
	}
	p.viewport = &Viewport{Width: v.Width, Height: v.Height, DeviceScaleFactor: dsf}
	return nil
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
	settle := opts.SettleMS
	if settle <= 0 {
		settle = DefaultSettleMS
	}
	if settle > MaxSettleMS {
		settle = MaxSettleMS
	}
	// document.fonts.ready resolves once the page's font loads have settled, so
	// waiting on it keeps a late webfont out of the capture. A page without
	// fonts — or without the API — is not a failure: the wait below still
	// gives a late layout its moment.
	_, _ = p.page.Evaluate("() => document.fonts.ready", nil)
	// The settle is cancellable: a capture the caller has given up on must not
	// hold the tool for the rest of the wait.
	timer := time.NewTimer(time.Duration(settle) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	pwOpts := playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(opts.FullPage),
		Type:     playwright.ScreenshotTypePng,
	}
	if opts.Format == "jpeg" {
		pwOpts.Type = playwright.ScreenshotTypeJpeg
		// quality is a JPEG-only option: playwright rejects it for a PNG
		// capture.
		if opts.Quality > 0 {
			pwOpts.Quality = playwright.Int(opts.Quality)
		}
	}
	if opts.Selector != "" {
		box, err := p.boundingBox(opts.Selector)
		if err != nil {
			return nil, err
		}
		pwOpts.Clip = &playwright.Rect{X: box.X, Y: box.Y, Width: box.Width, Height: box.Height}
		// One pixel per CSS pixel: the clip is the element's CSS box, so the
		// capture sizes to the element rather than to the viewport.
		pwOpts.Scale = playwright.ScreenshotScaleCss
		pwOpts.FullPage = playwright.Bool(false)
	}
	return p.page.Screenshot(pwOpts)
}

// boundingBox resolves selector and reports its box, failing with an error that
// names the selector: a clip to a missing element is a tool-input mistake the
// caller has to be able to read.
func (p *standalonePage) boundingBox(selector string) (*playwright.Rect, error) {
	el, err := p.page.QuerySelector(selector)
	if err != nil {
		return nil, fmt.Errorf("screenshot selector %q: %w", selector, err)
	}
	if el == nil {
		return nil, fmt.Errorf("screenshot selector %q: element not found", selector)
	}
	box, err := el.BoundingBox()
	if err != nil {
		return nil, fmt.Errorf("screenshot selector %q: bounding box: %w", selector, err)
	}
	if box == nil {
		return nil, fmt.Errorf("screenshot selector %q: element is not visible", selector)
	}
	return box, nil
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
