package browser

import (
	"context"
	"errors"
	"testing"
)

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
