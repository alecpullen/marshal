package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultHTTPClientHasTimeout(t *testing.T) {
	// All three providers should set a non-zero response-header timeout on
	// their default HTTP client when no client is provided via Options.
	tests := []struct {
		name  string
		build func() (interface{ Name() string }, error)
	}{
		{
			name: "openai_compatible",
			build: func() (interface{ Name() string }, error) {
				return NewOpenAICompatible(Options{Name: "test", BaseURL: "http://localhost"})
			},
		},
		{
			name: "anthropic",
			build: func() (interface{ Name() string }, error) {
				return NewAnthropic(Options{Name: "test", BaseURL: "http://localhost"})
			},
		},
		{
			name: "ollama",
			build: func() (interface{ Name() string }, error) {
				return NewOllamaNative(Options{Name: "test", BaseURL: "http://localhost"})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.build()
			if err != nil {
				t.Fatalf("build provider: %v", err)
			}
			timeout := getHTTPResponseHeaderTimeout(p)
			if timeout <= 0 {
				t.Errorf("provider %s: default HTTP client has no response-header timeout", p.Name())
			}
		})
	}
}

func TestDefaultHTTPClientBindsOnlyFirstResponse(t *testing.T) {
	// The default client must cap time-to-first-byte, not the total request
	// duration: a whole-request Timeout would truncate long streaming
	// bodies mid-generation (e.g. a slow local Ollama model).
	client := defaultHTTPClient()
	if client.Timeout != 0 {
		t.Errorf("request-level Timeout = %v, want 0 so streaming bodies are not truncated", client.Timeout)
	}
	if client.Transport == nil {
		t.Fatal("default client has no transport")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport is %T, want *http.Transport", client.Transport)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want > 0", tr.ResponseHeaderTimeout)
	}
}

func getHTTPResponseHeaderTimeout(p interface{ Name() string }) time.Duration {
	var client *http.Client
	switch v := p.(type) {
	case *OpenAICompatible:
		client = v.httpClient
	case *Anthropic:
		client = v.httpClient
	case *OllamaNative:
		client = v.httpClient
	}
	if client == nil || client.Transport == nil {
		return 0
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		return 0
	}
	return tr.ResponseHeaderTimeout
}
