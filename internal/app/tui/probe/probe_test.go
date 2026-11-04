package probe

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
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

// fakeHost is a client host that answers ProbeProvider without a network: it
// records what the probe asked for and returns a scripted result.
type fakeHost struct {
	acpclient.Host

	name  string
	pc    config.ProviderConfig
	calls int
	// models and err are the scripted answer.
	models []schema.ModelInfo
	err    error
	// block, when set, waits for the probe's context instead of answering.
	block bool
}

func (h *fakeHost) ProbeProvider(ctx context.Context, name string, pc config.ProviderConfig) ([]schema.ModelInfo, error) {
	h.calls++
	h.name = name
	h.pc = pc
	if h.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if h.err != nil {
		return nil, h.err
	}
	return h.models, nil
}

// TestProviderAsksTheHost: the probe asks the host for the named provider's
// models and carries the answer back with its field ID and name.
func TestProviderAsksTheHost(t *testing.T) {
	host := &fakeHost{models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}, {ID: "llama3.1:8b"}}}
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:11434/v1"}

	msg := Provider(host, "test.field", "testprov", pc)().(ResultMsg)

	if msg.Err != nil {
		t.Fatalf("Provider err = %v", msg.Err)
	}
	if host.calls != 1 {
		t.Fatalf("host calls = %d, want 1", host.calls)
	}
	if host.name != "testprov" || host.pc != pc {
		t.Fatalf("host asked for %q/%+v, want testprov/%+v", host.name, host.pc, pc)
	}
	if msg.FieldID != "test.field" || msg.Provider != "testprov" {
		t.Fatalf("ResultMsg identity = %+v", msg)
	}
	if len(msg.Models) != 2 || msg.Models[0].ID != "qwen2.5-coder:7b" || msg.Models[1].ID != "llama3.1:8b" {
		t.Fatalf("ResultMsg models = %v, want the host's list", msg.Models)
	}
}

// TestProviderReportsTheHostsError: a host failure is the result's error, not
// a swallowed or wrapped one.
func TestProviderReportsTheHostsError(t *testing.T) {
	hostErr := errors.New("host refused")
	host := &fakeHost{err: hostErr}
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}

	msg := Provider(host, "test.field", "testprov", pc)().(ResultMsg)

	if msg.Err == nil {
		t.Fatal("a failing host must produce a result error")
	}
	if !errors.Is(msg.Err, hostErr) {
		t.Fatalf("err = %v, want the host's error", msg.Err)
	}
	if msg.FieldID != "test.field" || msg.Provider != "testprov" {
		t.Fatalf("ResultMsg identity = %+v", msg)
	}
}

// TestProviderBoundsTheCall: the probe's context carries Timeout, so a host
// that never answers is cut off and the deadline surfaces as the error.
func TestProviderBoundsTheCall(t *testing.T) {
	old := Timeout
	Timeout = 50 * time.Millisecond
	defer func() { Timeout = old }()

	host := &fakeHost{block: true}
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}

	msg := Provider(host, "test.field", "testprov", pc)().(ResultMsg)

	if msg.Err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(msg.Err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", msg.Err)
	}
}

// TestProviderWithoutAHost: with no client host there is nothing to ask, and
// the result says so instead of panicking or reaching a default.
func TestProviderWithoutAHost(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}
	msg := Provider(nil, "test.field", "testprov", pc)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("a nil host must fail the probe")
	}
	if !errors.Is(msg.Err, errNoHost) {
		t.Fatalf("err = %v, want errNoHost", msg.Err)
	}
}
