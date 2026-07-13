package browser

import (
	"context"
	"time"
)

type BrowserBackend interface {
	NewPage(ctx context.Context) (PageHandle, error)
	Close() error
	Mode() string
}

type PageHandle interface {
	Navigate(ctx context.Context, url string) error
	Title(ctx context.Context) (string, error)
	URL(ctx context.Context) (string, error)
	Text(ctx context.Context, selector string) (string, error)
	HTML(ctx context.Context, selector string) (string, error)
	ReadableText(ctx context.Context) (string, error)
	Click(ctx context.Context, selector string) error
	Fill(ctx context.Context, selector, value string) error
	PressKey(ctx context.Context, key string) error
	Submit(ctx context.Context, selector string) error
	WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error
	WaitForLoadState(ctx context.Context, state string) error
	Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error)
	Close() error
}

type ScreenshotOpts struct {
	FullPage bool
	Format   string
}
