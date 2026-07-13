package desktop

import (
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/desktop/browser"
)

type Options struct {
	Config         config.DesktopConfig
	BackendFactory func() (browser.BrowserBackend, error)
	SessionState   *session.State
}
