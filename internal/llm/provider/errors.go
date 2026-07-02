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
