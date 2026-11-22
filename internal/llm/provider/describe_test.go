package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The report that prompted the summary: Ollama Cloud sends a past-due
// subscription as a 403 whose body is OpenAI-shaped JSON. The banner must say
// billing, carry the provider's sentence without the JSON or the support ref,
// and offer no raw detail, because nothing in it is Marshal's to fix.
func TestDescribeFailureOllamaPastDueIsBilling(t *testing.T) {
	err := fmt.Errorf("chat: %w", &ProviderError{
		Provider:   "ollama-cloud",
		StatusCode: 403,
		Body:       `{"error":{"message":"your subscription payment is past due. update your payment method: https://ollama.com/settings/billing (ref: 31606384-57d0-440a-8096-bb8220014b05)","type":"api_error","param":null,"code":null}}`,
	})
	f, ok := DescribeFailure(err)
	if !ok {
		t.Fatal("DescribeFailure ok = false for a wrapped ProviderError")
	}
	if f.Kind != FailureBilling {
		t.Fatalf("kind = %q, want billing", f.Kind)
	}
	if got, want := f.Title(), "ollama-cloud · billing"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	want := "Your subscription payment is past due. update your payment method: https://ollama.com/settings/billing"
	if f.Message != want {
		t.Errorf("message = %q, want %q", f.Message, want)
	}
	if f.Detail != "" {
		t.Errorf("detail = %q, want none for a billing failure", f.Detail)
	}
}

func TestDescribeFailureKinds(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		kind       FailureKind
		message    string
		wantDetail bool
	}{
		{"openai quota as 429", 429, `{"error":{"message":"You exceeded your current quota.","type":"insufficient_quota"}}`, FailureBilling, "You exceeded your current quota.", false},
		{"anthropic credit", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the API."}}`, FailureBilling, "Your credit balance is too low to access the API.", false},
		{"payment required", 402, ``, FailureBilling, "HTTP 402 Payment Required", false},
		{"unauthorized", 401, `{"error":"invalid api key"}`, FailureAuth, "Invalid api key", false},
		{"forbidden", 403, `{"message":"model not enabled for this key"}`, FailureAccess, "Model not enabled for this key", true},
		{"rate limited", 429, `{"error":{"message":"Rate limit reached"}}`, FailureRateLimit, "Rate limit reached", false},
		{"overloaded", 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, FailureUnavailable, "Overloaded", true},
		{"html 502", 502, `<html><body>Bad Gateway</body></html>`, FailureUnavailable, "HTTP 502 Bad Gateway", true},
		{"not found", 404, `{"error":"model 'qwen9' not found"}`, FailureNotFound, "Model 'qwen9' not found", true},
		{"bad request text", 400, "context length exceeded\nmore", FailureRejected, "Context length exceeded", true},
		{"detail field", 422, `{"detail":"messages: field required"}`, FailureRejected, "Messages: field required", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := DescribeFailure(&ProviderError{Provider: "p", StatusCode: tc.status, Body: tc.body})
			if !ok {
				t.Fatal("ok = false")
			}
			if f.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", f.Kind, tc.kind)
			}
			if f.Message != tc.message {
				t.Errorf("message = %q, want %q", f.Message, tc.message)
			}
			if (f.Detail != "") != tc.wantDetail {
				t.Errorf("detail = %q, want present = %v", f.Detail, tc.wantDetail)
			}
			if f.Hint == "" {
				t.Error("hint is empty")
			}
		})
	}
}

func TestDescribeFailureClipsLongMessages(t *testing.T) {
	f, _ := DescribeFailure(&ProviderError{Provider: "p", StatusCode: 500, Body: strings.Repeat("x", 1000)})
	if n := len([]rune(f.Message)); n > maxMessageRunes+1 {
		t.Fatalf("message is %d runes, want at most %d", n, maxMessageRunes+1)
	}
	if !strings.HasSuffix(f.Message, "…") {
		t.Errorf("clipped message should end with an ellipsis: %q", f.Message)
	}
}

func TestDescribeFailureUnreachable(t *testing.T) {
	err := &RequestError{Provider: "lmstudio", Op: "chat request failed", Err: errors.New("dial tcp 127.0.0.1:1234: connect: connection refused")}
	f, ok := DescribeFailure(err)
	if !ok || f.Kind != FailureUnreachable {
		t.Fatalf("got %+v, %v; want unreachable", f, ok)
	}
	if f.Message != "Dial tcp 127.0.0.1:1234: connect: connection refused" {
		t.Errorf("message = %q", f.Message)
	}
	if f.Detail == "" {
		t.Error("an unreachable provider keeps its raw error as detail")
	}
}

func TestDescribeFailureIgnoresOtherErrors(t *testing.T) {
	if _, ok := DescribeFailure(errors.New("boom")); ok {
		t.Fatal("a plain error is not a provider failure")
	}
}
