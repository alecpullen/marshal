package browser

import (
	"context"
	"sync"
)

type FakePage struct {
	mu               sync.Mutex
	NavigatedTo      string
	ClickedSelectors []string
	FilledInputs     map[string]string
	SubmittedSel     string
	ScreenshotCalls  int
	SettleCalls      int
	TitleVal         string
	URLVal           string
	ReadableTextVal  string
	Closed           bool
	NavigateErr      error
	// LastScreenshotOpts is the most recent ScreenshotOpts the page was asked
	// for; SetViewportCalls records every in-place resize.
	LastScreenshotOpts ScreenshotOpts
	SetViewportCalls   []Viewport
	SetViewportErr     error
}

func (p *FakePage) Navigate(ctx context.Context, url string) error {
	if p.NavigateErr != nil {
		return p.NavigateErr
	}
	p.mu.Lock()
	p.NavigatedTo = url
	p.mu.Unlock()
	return nil
}

func (p *FakePage) Title(ctx context.Context) (string, error) {
	return p.TitleVal, nil
}

func (p *FakePage) URL(ctx context.Context) (string, error) {
	return p.URLVal, nil
}

func (p *FakePage) Text(ctx context.Context, selector string) (string, error) {
	return p.ReadableTextVal, nil
}

func (p *FakePage) ReadableText(ctx context.Context) (string, error) {
	return p.ReadableTextVal, nil
}

func (p *FakePage) Click(ctx context.Context, selector string) error {
	p.mu.Lock()
	p.ClickedSelectors = append(p.ClickedSelectors, selector)
	p.mu.Unlock()
	return nil
}

func (p *FakePage) Fill(ctx context.Context, selector, value string) error {
	p.mu.Lock()
	if p.FilledInputs == nil {
		p.FilledInputs = map[string]string{}
	}
	p.FilledInputs[selector] = value
	p.mu.Unlock()
	return nil
}

func (p *FakePage) PressKey(ctx context.Context, key string) error {
	return nil
}

func (p *FakePage) Submit(ctx context.Context, selector string) error {
	p.mu.Lock()
	p.SubmittedSel = selector
	p.mu.Unlock()
	return nil
}

func (p *FakePage) Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error) {
	p.mu.Lock()
	p.ScreenshotCalls++
	p.SettleCalls++
	p.LastScreenshotOpts = opts
	p.mu.Unlock()
	return []byte("fake-screenshot-metadata"), nil
}

func (p *FakePage) SetViewport(ctx context.Context, v Viewport) error {
	p.mu.Lock()
	p.SetViewportCalls = append(p.SetViewportCalls, v)
	err := p.SetViewportErr
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func (p *FakePage) Close() error {
	p.mu.Lock()
	p.Closed = true
	p.mu.Unlock()
	return nil
}

type FakeBackend struct {
	Page     *FakePage
	ModeVal  string
	CloseErr error
	// NewPageCalls counts page creations — how a test sees a recreation — and
	// NewPageOpts records the options each one was asked for.
	NewPageCalls int
	NewPageOpts  []PageOptions
}

func (b *FakeBackend) NewPage(ctx context.Context, opts PageOptions) (PageHandle, error) {
	b.NewPageCalls++
	b.NewPageOpts = append(b.NewPageOpts, opts)
	if b.Page == nil {
		b.Page = &FakePage{TitleVal: "Fake Title", URLVal: "https://example.com", ReadableTextVal: "fake page text"}
	}
	return b.Page, nil
}

func (b *FakeBackend) Close() error {
	return b.CloseErr
}

func (b *FakeBackend) Mode() string {
	if b.ModeVal != "" {
		return b.ModeVal
	}
	return "standalone"
}
