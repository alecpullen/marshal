package probe

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestIsLocalhost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://localhost:11434/v1", true},
		{"http://127.0.0.1:11434/v1", true},
		{"http://0.0.0.0:11434/v1", true},
		{"http://[::1]:11434/v1", true},
		{"http://[::1%25lo0]:11434/v1", true},
		{"https://api.openai.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsLocalhost(c.url); got != c.want {
			t.Errorf("IsLocalhost(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestProviderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b","owned_by":"ollama"},{"id":"llama3.1:8b","owned_by":"meta"}]}`))
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)

	if msg.Err != nil {
		t.Fatalf("Provider err = %v", msg.Err)
	}
	if msg.Provider != "testprov" || msg.FieldID != "test.field" {
		t.Fatalf("ResultMsg identity = %+v", msg)
	}
	if len(msg.Models) != 2 || msg.Models[0] != "qwen2.5-coder:7b" {
		t.Fatalf("ResultMsg models = %v", msg.Models)
	}
}

func TestProviderNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestProviderConnectionRefused(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	old := Timeout
	Timeout = 200 * time.Millisecond
	defer func() { Timeout = old }()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected timeout error")
	}
}
