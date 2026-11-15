package fleetclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// actionBridge is a bridge that records the one request an action sends and
// answers it with a scripted status and body.
type actionBridge struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	method string
	path   string
	query  string
	body   []byte
	auth   string
	status int
	answer string
}

// newActionBridge starts the recording bridge.
func newActionBridge(t *testing.T) *actionBridge {
	t.Helper()
	b := &actionBridge{t: t, status: http.StatusOK}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.method, b.path, b.query, b.body = r.Method, r.URL.EscapedPath(), r.URL.RawQuery, body
		b.auth = r.Header.Get("Authorization")
		status, answer := b.status, b.answer
		b.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// reply scripts the next answer.
func (b *actionBridge) reply(status int, body string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status, b.answer = status, body
}

// client is an action client on the recording bridge. token, when not nil, is
// the transport's bearer-token function.
func (b *actionBridge) client(token func(ctx context.Context) (string, error)) *Client {
	return New(Transport{
		Server:  "recording",
		BaseURL: b.srv.URL,
		HTTP:    b.srv.Client(),
		Token:   token,
	})
}

// recorded is the request the bridge saw.
func (b *actionBridge) recorded() (method, path, query string, sent []byte, auth string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.method, b.path, b.query, append([]byte(nil), b.body...), b.auth
}

// sameJSON reports whether got and want are the same JSON value, whatever
// their spacing.
func sameJSON(t *testing.T, got []byte, want any) bool {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("the action's body is not JSON: %v: %q", err, got)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal the expected body: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(raw, &wantValue); err != nil {
		t.Fatalf("unmarshal the expected body: %v", err)
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

// TestEachActionSendsItsRoute: every action's method, path and body, for one
// call each. The ids carry a slash so the path escaping is part of the
// answer.
func TestEachActionSendsItsRoute(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		call   func(t *testing.T, c *Client) error
		method string
		path   string
		query  string
		body   any // nil: the action sends none
	}{
		{
			name:   "Spawn",
			answer: `{"agentId":"a1"}`,
			call: func(t *testing.T, c *Client) error {
				got, err := c.Spawn(context.Background(), contract.SpawnRequest{
					Project: "/repo/a", Name: "an agent", Prompt: "hello",
				})
				if err != nil {
					return err
				}
				if got.AgentID != "a1" {
					t.Errorf("Spawn answered %+v, want agentId a1", got)
				}
				return nil
			},
			method: http.MethodPost,
			path:   "/api/agents",
			body:   contract.SpawnRequest{Project: "/repo/a", Name: "an agent", Prompt: "hello"},
		},
		{
			name:   "AnswerPermission",
			answer: `{"status":"resolved"}`,
			call: func(t *testing.T, c *Client) error {
				return c.AnswerPermission(context.Background(), "p/1", contract.Decision{Approved: true, Edited: "yes"})
			},
			method: http.MethodPost,
			path:   "/api/permissions/p%2F1",
			body:   contract.Decision{Approved: true, Edited: "yes"},
		},
		{
			name:   "AnswerQuestion",
			answer: `{"status":"resolved"}`,
			call: func(t *testing.T, c *Client) error {
				return c.AnswerQuestion(context.Background(), "q/1", contract.Answers{
					Answers: []contract.Answer{{Question: "which?", Answer: "that one"}},
				})
			},
			method: http.MethodPost,
			path:   "/api/questions/q%2F1",
			body: contract.Answers{
				Answers: []contract.Answer{{Question: "which?", Answer: "that one"}},
			},
		},
		{
			name:   "Cancel",
			answer: `{"status":"cancelled"}`,
			call: func(t *testing.T, c *Client) error {
				return c.Cancel(context.Background(), "a/1")
			},
			method: http.MethodPost,
			path:   "/api/sessions/a%2F1/cancel",
		},
		{
			name:   "ApproveIntake",
			answer: `{"agentId":"a2","status":"running"}`,
			call: func(t *testing.T, c *Client) error {
				got, err := c.ApproveIntake(context.Background(), "s/1")
				if err != nil {
					return err
				}
				if got.AgentID != "a2" || got.Status != "running" {
					t.Errorf("ApproveIntake answered %+v, want a2 running", got)
				}
				return nil
			},
			method: http.MethodPost,
			path:   "/api/pending/s%2F1/approve",
		},
		{
			name:   "DenyIntake",
			answer: `{"status":"denied"}`,
			call: func(t *testing.T, c *Client) error {
				return c.DenyIntake(context.Background(), "s/2")
			},
			method: http.MethodPost,
			path:   "/api/pending/s%2F2/deny",
		},
		{
			name:   "RecentPrompts",
			answer: `{"prompts":["newest","older"]}`,
			call: func(t *testing.T, c *Client) error {
				got, err := c.RecentPrompts(context.Background(), "/repo/a", 5)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(got, []string{"newest", "older"}) {
					t.Errorf("RecentPrompts answered %v", got)
				}
				return nil
			},
			method: http.MethodGet,
			path:   "/api/prompts/recent",
			query:  "limit=5&project=%2Frepo%2Fa",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newActionBridge(t)
			b.reply(http.StatusOK, tc.answer)
			if err := tc.call(t, b.client(nil)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			method, path, query, body, _ := b.recorded()
			if method != tc.method {
				t.Errorf("%s used %s, want %s", tc.name, method, tc.method)
			}
			if path != tc.path {
				t.Errorf("%s sent %s, want %s", tc.name, path, tc.path)
			}
			if query != tc.query {
				t.Errorf("%s sent query %q, want %q", tc.name, query, tc.query)
			}
			if tc.body == nil {
				if len(body) != 0 {
					t.Errorf("%s sent body %q, want none", tc.name, body)
				}
				return
			}
			if !sameJSON(t, body, tc.body) {
				t.Errorf("%s sent body %s", tc.name, body)
			}
		})
	}
}

// TestARefusalCarriesTheBridgesSentence: a 409's {"error": …} is the seam's
// error, with its status.
func TestARefusalCarriesTheBridgesSentence(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusConflict, `{"error":"the agent is held by another operator"}`)

	err := b.client(nil).Cancel(context.Background(), "a1")

	var refusal *acpclient.FleetError
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %T (%v), want *acpclient.FleetError", err, err)
	}
	if refusal.Status != http.StatusConflict {
		t.Errorf("the refusal carries status %d, want %d", refusal.Status, http.StatusConflict)
	}
	if want := "the agent is held by another operator"; refusal.Error() != want {
		t.Errorf("the refusal says %q, want %q", refusal.Error(), want)
	}
}

// TestABudgetRefusalSaysWhichBudget: a 429's scope names the budget, in the
// web UI's sentence.
func TestABudgetRefusalSaysWhichBudget(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusTooManyRequests, `{"error":"budget_exceeded","scope":"daily"}`)

	err := b.client(nil).AnswerPermission(context.Background(), "p1", contract.Decision{Approved: true})

	var refusal *acpclient.FleetError
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %T (%v), want *acpclient.FleetError", err, err)
	}
	if refusal.Status != http.StatusTooManyRequests {
		t.Errorf("the refusal carries status %d, want %d", refusal.Status, http.StatusTooManyRequests)
	}
	if want := "Budget reached (daily)"; refusal.Error() != want {
		t.Errorf("the refusal says %q, want %q", refusal.Error(), want)
	}
}

// TestALateAnswerIsSettled: a 410 is the seam's ErrSettled, not a refusal.
func TestALateAnswerIsSettled(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusGone, `{"error":"already answered"}`)

	if err := b.client(nil).DenyIntake(context.Background(), "s1"); !errors.Is(err, acpclient.ErrSettled) {
		t.Fatalf("the late answer is %v, want ErrSettled", err)
	}
}

// TestAnActionSendsTheToken: an action carries the transport's bearer token.
func TestAnActionSendsTheToken(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusOK, `{"status":"cancelled"}`)

	token := func(ctx context.Context) (string, error) { return "tok-1", nil }
	if err := b.client(token).Cancel(context.Background(), "a1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, _, _, _, auth := b.recorded(); auth != "Bearer tok-1" {
		t.Fatalf("the action carried %q, want %q", auth, "Bearer tok-1")
	}
}

// TestAnUnreachableBridgeIsAStatusZeroRefusal: a dial failure is a refusal with
// no status and the dial's sentence, not a bare error (decision 2).
func TestAnUnreachableBridgeIsAStatusZeroRefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}

	c := New(Transport{Server: "gone", BaseURL: "http://" + addr})
	err = c.Cancel(context.Background(), "a1")

	var refusal *acpclient.FleetError
	if !errors.As(err, &refusal) {
		t.Fatalf("the dial failure is %T (%v), want *acpclient.FleetError", err, err)
	}
	if refusal.Status != 0 {
		t.Errorf("the refusal carries status %d, want 0", refusal.Status)
	}
	if refusal.Message == "" {
		t.Error("the refusal carries no sentence, want the dial's")
	}
}

// TestARefusalWithoutASentenceSaysItsStatus: a refusal body with no "error"
// field still reads as a sentence, and still carries its status.
func TestARefusalWithoutASentenceSaysItsStatus(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusConflict, `{}`)

	err := b.client(nil).Cancel(context.Background(), "a1")

	var refusal *acpclient.FleetError
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %T (%v), want *acpclient.FleetError", err, err)
	}
	if refusal.Status != http.StatusConflict {
		t.Errorf("the refusal carries status %d, want %d", refusal.Status, http.StatusConflict)
	}
	if want := "status 409"; refusal.Error() != want {
		t.Errorf("the refusal says %q, want %q", refusal.Error(), want)
	}
}

// TestAnUnreadableAnswerIsAnError: a 2xx body that is not the action's result
// fails loudly rather than passing a zero value off as an answer.
func TestAnUnreadableAnswerIsAnError(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusOK, `not json`)

	_, err := b.client(nil).Spawn(context.Background(), contract.SpawnRequest{Project: "/repo/a"})
	if err == nil {
		t.Fatal("an unreadable answer was accepted")
	}
	if !strings.Contains(err.Error(), "fleet action") {
		t.Errorf("the failure is %q, want it to name the action", err)
	}
	var refusal *acpclient.FleetError
	if errors.As(err, &refusal) {
		t.Errorf("an unreadable answer is a refusal (%v), want a decode failure", refusal)
	}
}

// TestARedirectIsRefusedNotFollowed: a redirect answers as a refusal, so an
// action's body is never re-sent to wherever it points.
func TestARedirectIsRefusedNotFollowed(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer target.Close()

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer bridge.Close()

	c := New(Transport{Server: "redirecting", BaseURL: bridge.URL, HTTP: bridge.Client()})
	err := c.Cancel(context.Background(), "a1")

	var refusal *acpclient.FleetError
	if !errors.As(err, &refusal) {
		t.Fatalf("the redirect is %T (%v), want *acpclient.FleetError", err, err)
	}
	if refusal.Status != http.StatusFound {
		t.Errorf("the refusal carries status %d, want %d", refusal.Status, http.StatusFound)
	}
	if got := reached.Load(); got != 0 {
		t.Errorf("the redirect was followed: the target answered %d times", got)
	}
}

// TestAReadRefusesLikeAnActionDoes: RecentPrompts is an action too, so its
// refusal is the seam's error with its sentence — not a plain read failure.
func TestAReadRefusesLikeAnActionDoes(t *testing.T) {
	b := newActionBridge(t)
	b.reply(http.StatusTooManyRequests, `{"error":"budget_exceeded","scope":"daily"}`)

	_, err := b.client(nil).RecentPrompts(context.Background(), "/repo/a", 5)

	var refusal *acpclient.FleetError
	if !errors.As(err, &refusal) {
		t.Fatalf("the read's refusal is %T (%v), want *acpclient.FleetError", err, err)
	}
	if refusal.Status != http.StatusTooManyRequests {
		t.Errorf("the refusal carries status %d, want %d", refusal.Status, http.StatusTooManyRequests)
	}
	if want := "Budget reached (daily)"; refusal.Error() != want {
		t.Errorf("the refusal says %q, want %q", refusal.Error(), want)
	}
}
