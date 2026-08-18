package provider

import (
	"net/http"
	"time"
)

// defaultHTTPClient is the client used by every provider backend when no
// client is supplied via Options.HTTPClient.
//
// It deliberately bounds only the time to first response byte
// (ResponseHeaderTimeout) rather than the whole request. A request-level
// Timeout would also cap how long we keep reading a streaming body, so a
// legitimate long generation (e.g. a slow local Ollama model) would be
// truncated mid-stream. Overall cancellation is still governed by the
// caller's request context.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 120 * time.Second},
	}
}
