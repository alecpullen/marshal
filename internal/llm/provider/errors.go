package provider

import "fmt"

// ProviderError wraps a non-2xx HTTP response from a provider so callers can
// use errors.As to inspect the status code instead of parsing error strings.
type ProviderError struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %q returned HTTP %d: %s", e.Provider, e.StatusCode, e.Body)
}

// RequestError marks a transport-level failure (DNS, refused connection,
// TLS, timeout) as provider-originated, distinct from a provider's HTTP
// error response (ProviderError). The UI classifies with errors.As; Op
// keeps the pre-existing message wording ("chat request failed").
type RequestError struct {
	Provider string
	Op       string
	Err      error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("provider %q: %s: %v", e.Provider, e.Op, e.Err)
}

func (e *RequestError) Unwrap() error { return e.Err }
