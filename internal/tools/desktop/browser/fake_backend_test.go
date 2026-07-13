package browser

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type FakePage struct {
	mu               sync.Mutex
	NavigatedTo      string
	ClickedSelectors []string
	FilledInputs     map[string]string
	SubmittedSel     string
	ScreenshotCalls  int
	TitleVal         string
	URLVal           string
	ReadableTextVal  string
	Closed           bool
	NavigateErr      error
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

func (p *FakePage) HTML(ctx context.Context, selector string) (string, error) {
	return "<html>" + selector + "</html>", nil
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

func (p *FakePage) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	return nil
}

func (p *FakePage) WaitForLoadState(ctx context.Context, state string) error {
	return nil
}

func (p *FakePage) Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error) {
	p.mu.Lock()
	p.ScreenshotCalls++
	p.mu.Unlock()
	return []byte("fake-screenshot-metadata"), nil
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
}

func (b *FakeBackend) NewPage(ctx context.Context) (PageHandle, error) {
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

func TestFakeBackendImplementsBrowserBackend(t *testing.T) {
	var _ BrowserBackend = (*FakeBackend)(nil)
	var _ PageHandle = (*FakePage)(nil)
}

func TestFakePageNavigate(t *testing.T) {
	p := &FakePage{}
	if err := p.Navigate(context.Background(), "https://test.com"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if p.NavigatedTo != "https://test.com" {
		t.Fatalf("navigated to %q, want https://test.com", p.NavigatedTo)
	}
}

func TestFakePageNavigateError(t *testing.T) {
	p := &FakePage{NavigateErr: errors.New("connection lost")}
	if err := p.Navigate(context.Background(), "https://test.com"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFakePageClickAndFill(t *testing.T) {
	p := &FakePage{}
	_ = p.Click(context.Background(), "#btn")
	_ = p.Fill(context.Background(), "#input", "hello")
	if len(p.ClickedSelectors) != 1 || p.ClickedSelectors[0] != "#btn" {
		t.Fatalf("clicked %v", p.ClickedSelectors)
	}
	if p.FilledInputs["#input"] != "hello" {
		t.Fatalf("filled %v", p.FilledInputs)
	}
}

func TestFakePageScreenshot(t *testing.T) {
	p := &FakePage{}
	data, err := p.Screenshot(context.Background(), ScreenshotOpts{Format: "png"})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if p.ScreenshotCalls != 1 {
		t.Fatalf("screenshot calls: %d", p.ScreenshotCalls)
	}
	if len(data) == 0 {
		t.Fatal("no screenshot data")
	}
}

func TestFakeBackendClose(t *testing.T) {
	b := &FakeBackend{CloseErr: errors.New("close failed")}
	if err := b.Close(); err == nil {
		t.Fatal("expected close error")
	}
}
