package browser

import (
	"context"
	"errors"
)

type BrowserBackend interface {
	NewPage(ctx context.Context, opts PageOptions) (PageHandle, error)
	Close() error
	Mode() string
}

type PageHandle interface {
	Navigate(ctx context.Context, url string) error
	Title(ctx context.Context) (string, error)
	URL(ctx context.Context) (string, error)
	Text(ctx context.Context, selector string) (string, error)
	ReadableText(ctx context.Context) (string, error)
	Click(ctx context.Context, selector string) error
	Fill(ctx context.Context, selector, value string) error
	PressKey(ctx context.Context, key string) error
	Submit(ctx context.Context, selector string) error
	Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error)
	SetViewport(ctx context.Context, v Viewport) error
	Close() error
}

type Viewport struct {
	Width, Height     int
	DeviceScaleFactor float64 // 0 = 1
}

// deviceScaleFactor is the scale factor the viewport asks for, with the
// documented zero-means-one default applied. A context's scale factor cannot be
// changed after the fact, so every comparison goes through this.
func (v *Viewport) deviceScaleFactor() float64 {
	if v == nil || v.DeviceScaleFactor <= 0 {
		return 1
	}
	return v.DeviceScaleFactor
}

// viewportCopy records a viewport the session asked a backend for, so a later
// request can be compared against what the live page was created with.
func viewportCopy(v *Viewport) *Viewport {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

type PageOptions struct {
	Viewport *Viewport // nil = backend default
}

type ScreenshotOpts struct {
	FullPage bool
	Format   string // "png" | "jpeg"
	Quality  int    // jpeg only, 0 = default
	Selector string // clip to this element's bounding box
	SettleMS int    // settle after fonts.ready; <=0 uses DefaultSettleMS
}

const DefaultSettleMS = 150

// ErrViewportNeedsRecreate reports that the requested viewport cannot be applied
// to the current page in place, because the device scale factor is a context
// option. The caller recreates the page and re-navigates.
var ErrViewportNeedsRecreate = errors.New("browser: viewport needs a new page")
