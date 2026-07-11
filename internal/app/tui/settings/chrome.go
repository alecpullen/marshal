package settings

import "marshal/internal/app/tui/chrome"

// renderPanel draws a bordered panel via the shared chrome package with
// this package's theme.
func renderPanel(title, content string, w, h int, focused bool) string {
	return chrome.Panel(title, content, w, h, focused, settingsTheme)
}
