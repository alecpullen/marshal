package provider

import (
	"testing"
	"time"
)

func TestDefaultHTTPClientHasTimeout(t *testing.T) {
	// All three providers should set a non-zero timeout on their default
	// HTTP client when no client is provided via Options.
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
			timeout := getHTTPClientTimeout(p)
			if timeout <= 0 {
				t.Errorf("provider %s: default HTTP client has no timeout", p.Name())
			}
		})
	}
}

func getHTTPClientTimeout(p interface{ Name() string }) time.Duration {
	switch v := p.(type) {
	case *OpenAICompatible:
		return v.httpClient.Timeout
	case *Anthropic:
		return v.httpClient.Timeout
	case *OllamaNative:
		return v.httpClient.Timeout
	}
	return 0
}
