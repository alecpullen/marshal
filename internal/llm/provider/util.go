package provider

import (
	"io"
	"net/http"
)

// HTTPError reads up to 4096 bytes of the response body and returns a
// *ProviderError with the provider name, status code, and body text. It is
// the shared implementation behind the provider error paths so the behavior
// is identical across backends.
func HTTPError(name string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &ProviderError{Provider: name, StatusCode: resp.StatusCode, Body: string(body)}
}
