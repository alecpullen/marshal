package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FailureKind is what a provider failure is about, as the user acts on it.
type FailureKind string

const (
	FailureBilling     FailureKind = "billing"
	FailureAuth        FailureKind = "sign-in"
	FailureAccess      FailureKind = "access denied"
	FailureRateLimit   FailureKind = "rate limited"
	FailureUnavailable FailureKind = "unavailable"
	FailureNotFound    FailureKind = "not found"
	FailureRejected    FailureKind = "request rejected"
	FailureUnreachable FailureKind = "unreachable"
	FailureOther       FailureKind = "error"
)

// Failure is a provider error summarised for a person: what kind of failure it
// is, the provider's own sentence pulled out of its response body, and what to
// do next. Detail is the raw error text, kept for failures the summary may not
// explain; it is empty when the kind says everything (billing, sign-in, rate
// limits), so a banner offers no wall of JSON for a problem on the user's
// provider account.
type Failure struct {
	Kind     FailureKind
	Provider string
	Message  string
	Hint     string
	Detail   string
}

// Title is the failure's one-line label: the provider and the kind.
func (f Failure) Title() string {
	if f.Provider == "" {
		return string(f.Kind)
	}
	return f.Provider + " · " + string(f.Kind)
}

// DescribeFailure summarises err when it is a provider's HTTP error response
// or a transport failure reaching one, and reports false for anything else.
func DescribeFailure(err error) (Failure, bool) {
	var httpErr *ProviderError
	if errors.As(err, &httpErr) {
		return describeHTTP(httpErr), true
	}
	var reqErr *RequestError
	if errors.As(err, &reqErr) {
		msg := "Could not reach the provider."
		if reqErr.Err != nil {
			msg = capitalise(firstLineOf(reqErr.Err.Error()))
		}
		return Failure{
			Kind:     FailureUnreachable,
			Provider: reqErr.Provider,
			Message:  msg,
			Hint:     "Check the provider's endpoint with /connect, or /models to pick another model.",
			Detail:   err.Error(),
		}, true
	}
	return Failure{}, false
}

func describeHTTP(e *ProviderError) Failure {
	msg := bodyMessage(e.Body)
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", e.StatusCode)
		if text := http.StatusText(e.StatusCode); text != "" {
			msg += " " + text
		}
	}
	msg = capitalise(stripRefs(msg))
	f := Failure{Provider: e.Provider, Message: msg, Detail: e.Error()}
	switch {
	case e.StatusCode == http.StatusPaymentRequired || isBilling(msg+" "+e.Body):
		f.Kind = FailureBilling
		f.Hint = "Settle it in your " + nameOr(e.Provider) + " account, or /models to pick another model."
		f.Detail = ""
	case e.StatusCode == http.StatusUnauthorized:
		f.Kind = FailureAuth
		f.Hint = "Run /connect to sign in to " + nameOr(e.Provider) + " again."
		f.Detail = ""
	case e.StatusCode == http.StatusForbidden:
		f.Kind = FailureAccess
		f.Hint = "Check the key's access with /connect, or /models to pick another model."
	case e.StatusCode == http.StatusTooManyRequests:
		f.Kind = FailureRateLimit
		f.Hint = "Wait a moment and retry, or /models to pick another model."
		f.Detail = ""
	case e.StatusCode == http.StatusNotFound:
		f.Kind = FailureNotFound
		f.Hint = "Check the model and endpoint with /models and /connect."
	case e.StatusCode >= 500:
		f.Kind = FailureUnavailable
		f.Hint = "The provider is having trouble — retry shortly, or /models to pick another model."
	case e.StatusCode >= 400:
		f.Kind = FailureRejected
		f.Hint = "Run /connect to review the provider, or /models to pick another model."
	default:
		f.Kind = FailureOther
		f.Hint = "Run /connect to review the provider, or /models to pick another model."
	}
	return f
}

// billingMarkers are phrases providers use for an account that cannot pay
// for the request, whatever status code they send it with (Ollama sends a
// past-due subscription as 403, OpenAI an exhausted quota as 429).
var billingMarkers = []string{
	"payment",
	"billing",
	"past due",
	"insufficient_quota",
	"insufficient quota",
	"insufficient credit",
	"insufficient balance",
	"credit balance",
	"out of credits",
}

func isBilling(text string) bool {
	text = strings.ToLower(text)
	for _, m := range billingMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// bodyMessage is the human sentence in a provider's error body: the
// OpenAI-compatible {"error":{"message":…}}, Anthropic's {"type":"error",
// "error":{"message":…}}, Ollama's {"error":"…"}, or a top-level "message" or
// "detail". A body that is not JSON is used as text unless it is HTML, which
// says nothing a person can act on.
func bodyMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) == nil {
		switch e := v["error"].(type) {
		case string:
			if s := strings.TrimSpace(e); s != "" {
				return clip(firstLineOf(s))
			}
		case map[string]any:
			if s, ok := e["message"].(string); ok && strings.TrimSpace(s) != "" {
				return clip(firstLineOf(s))
			}
		}
		for _, k := range []string{"message", "detail"} {
			if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
				return clip(firstLineOf(s))
			}
		}
		return ""
	}
	if strings.HasPrefix(body, "<") {
		return ""
	}
	return clip(firstLineOf(body))
}

// refPattern matches a trailing support reference such as "(ref: 3160…)" or
// "(request id: req_…)": useful to the provider's support desk, noise in a
// one-line summary. The raw error keeps it.
var refPattern = regexp.MustCompile(`(?i)\s*\((?:ref|request[ _-]?id|trace[ _-]?id)\s*[:=][^)]*\)`)

func stripRefs(s string) string {
	return strings.TrimSpace(refPattern.ReplaceAllString(s, ""))
}

const maxMessageRunes = 300

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxMessageRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:maxMessageRunes])) + "…"
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t\r")
}

func capitalise(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || !unicode.IsLower(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

func nameOr(provider string) string {
	if provider == "" {
		return "the provider"
	}
	return provider
}
