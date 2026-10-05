package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func postHook(s *Server, repo, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, hooksPrefix+repo, bytes.NewReader([]byte(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func githubHeaders(event, secret, body string) map[string]string {
	return map[string]string{"X-GitHub-Event": event, "X-Hub-Signature-256": "sha256=" + sign(secret, body)}
}

// hookEnv is an environment with a secret for r1 and the dispatch seams
// capturing events.
func hookEnv(t *testing.T) (*autoEnv, chan prEvent, chan checkEvent) {
	t.Helper()
	e := newAutoEnv(t, nil)
	if err := e.f.secrets.Put(t.Context(), DefaultOwnerID, "hooks/r1", []byte("s3cret")); err != nil {
		t.Fatal(err)
	}
	prs, checks := make(chan prEvent, 4), make(chan checkEvent, 4)
	e.f.auto.onPR = func(_ context.Context, ev prEvent) { prs <- ev }
	e.f.auto.onCheck = func(_ context.Context, ev checkEvent) { checks <- ev }
	return e, prs, checks
}

func TestHooksValidGitHubSignatureDispatchesAPREvent(t *testing.T) {
	e, prs, _ := hookEnv(t)
	body := `{"action":"synchronize","number":7,"pull_request":{"head":{"sha":"abc"}}}`
	rec := postHook(e.srv, "r1", body, githubHeaders("pull_request", "s3cret", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	select {
	case ev := <-prs:
		if ev.repoID != "r1" || ev.number != 7 || ev.headSHA != "abc" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no PR event dispatched")
	}
}

func TestHooksRejectBadMissingAndUnknownSignatures(t *testing.T) {
	e, prs, _ := hookEnv(t)
	body := `{"action":"opened","number":7}`
	cases := map[string]map[string]string{
		"wrong secret":   githubHeaders("pull_request", "other", body),
		"no signature":   {"X-GitHub-Event": "pull_request"},
		"no sha256= tag": {"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": sign("s3cret", body)},
		"not hex":        {"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": "sha256=zz"},
	}
	for name, h := range cases {
		if rec := postHook(e.srv, "r1", body, h); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: code = %d, want 401", name, rec.Code)
		}
	}
	// A repo with no secret answers the same, so the route does not say
	// which repos exist.
	if rec := postHook(e.srv, "nope", body, githubHeaders("pull_request", "s3cret", body)); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown repo: code = %d, want 401", rec.Code)
	}
	select {
	case ev := <-prs:
		t.Fatalf("a rejected hook dispatched %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHooksGiteaSignatureAndEvents(t *testing.T) {
	e, prs, checks := hookEnv(t)
	body := `{"action":"synchronized","number":3,"pull_request":{"head":{"sha":"g1"}}}`
	h := map[string]string{"X-Gitea-Event": "pull_request", "X-Gitea-Signature": sign("s3cret", body)}
	if rec := postHook(e.srv, "r1", body, h); rec.Code != http.StatusAccepted {
		t.Fatalf("pull_request code = %d", rec.Code)
	}
	if ev := <-prs; ev.number != 3 || ev.headSHA != "g1" {
		t.Fatalf("event = %+v", ev)
	}
	status := `{"state":"failure","sha":"g2","branches":[{"name":"main"}]}`
	h = map[string]string{"X-Gitea-Event": "status", "X-Gitea-Signature": sign("s3cret", status)}
	if rec := postHook(e.srv, "r1", status, h); rec.Code != http.StatusAccepted {
		t.Fatalf("status code = %d", rec.Code)
	}
	if ev := <-checks; ev.sha != "g2" || ev.ref != "main" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestHooksCheckEventsDispatchOnlyFailures(t *testing.T) {
	e, _, checks := hookEnv(t)
	for _, c := range []struct {
		event, body string
		want        bool
	}{
		{"check_run", `{"action":"completed","check_run":{"conclusion":"failure","head_sha":"c1","check_suite":{"head_branch":"main"}}}`, true},
		{"check_suite", `{"action":"completed","check_suite":{"conclusion":"failure","head_sha":"c2","head_branch":"dev"}}`, true},
		{"workflow_run", `{"action":"completed","workflow_run":{"conclusion":"failure","head_sha":"c3","head_branch":"main"}}`, true},
		{"check_run", `{"action":"completed","check_run":{"conclusion":"success","head_sha":"c4"}}`, false},
		{"workflow_run", `{"action":"requested","workflow_run":{"conclusion":"failure","head_sha":"c5"}}`, false},
	} {
		rec := postHook(e.srv, "r1", c.body, githubHeaders(c.event, "s3cret", c.body))
		want := http.StatusNoContent
		if c.want {
			want = http.StatusAccepted
		}
		if rec.Code != want {
			t.Errorf("%s %s: code = %d, want %d", c.event, c.body, rec.Code, want)
		}
	}
	got := map[string]string{}
	for range 3 {
		select {
		case ev := <-checks:
			got[ev.sha] = ev.ref
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d check events dispatched", len(got))
		}
	}
	if got["c1"] != "main" || got["c2"] != "dev" || got["c3"] != "main" {
		t.Fatalf("events = %v", got)
	}
}

func TestHooksIgnoreIrrelevantActionsAndEvents(t *testing.T) {
	e, _, _ := hookEnv(t)
	for _, c := range []struct{ event, body string }{
		{"pull_request", `{"action":"closed","number":7}`},
		{"pull_request", `{"action":"labeled","number":7}`},
		{"push", `{"ref":"refs/heads/main"}`},
		{"ping", `{}`},
	} {
		if rec := postHook(e.srv, "r1", c.body, githubHeaders(c.event, "s3cret", c.body)); rec.Code != http.StatusNoContent {
			t.Errorf("%s %s: code = %d, want 204", c.event, c.body, rec.Code)
		}
	}
}

func TestHooksOnlyAcceptPostAndBoundTheBody(t *testing.T) {
	e, _, _ := hookEnv(t)
	req := httptest.NewRequest(http.MethodGet, hooksPrefix+"r1", nil)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET code = %d, want 405", rec.Code)
	}
	big := strings.Repeat("x", maxHookBody+1)
	if rec := postHook(e.srv, "r1", big, githubHeaders("pull_request", "s3cret", big)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize code = %d, want 413", rec.Code)
	}
}

func TestHooksBypassTheBearerToken(t *testing.T) {
	e, prs, _ := hookEnv(t)
	secured := NewServer(e.f, "tok")
	body := `{"action":"opened","number":1,"pull_request":{"head":{"sha":"a"}}}`
	if rec := postHook(secured, "r1", body, githubHeaders("pull_request", "s3cret", body)); rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d: the signature is the credential, not the bearer token", rec.Code)
	}
	<-prs
}

func TestWebhookSecretRouteStoresAndReturnsOnce(t *testing.T) {
	e := newAutoEnv(t, nil)
	rec := doReq(t, e.srv, http.MethodPost, "/api/repos/r1/webhook-secret", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	var got struct{ Secret string }
	decodeBody(t, rec, &got)
	if len(got.Secret) != 64 {
		t.Fatalf("secret = %q, want 32 bytes of hex", got.Secret)
	}
	stored, err := e.f.secrets.Get(t.Context(), DefaultOwnerID, "hooks/r1")
	if err != nil || string(stored) != got.Secret {
		t.Fatalf("stored = %q, %v", stored, err)
	}
	// The secret verifies a delivery.
	body := `{"action":"ping"}`
	if rec := postHook(e.srv, "r1", body, githubHeaders("ping", got.Secret, body)); rec.Code != http.StatusNoContent {
		t.Errorf("signed delivery code = %d, want 204", rec.Code)
	}
	if rec := doReq(t, e.srv, http.MethodPost, "/api/repos/none/webhook-secret", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repo code = %d, want 404", rec.Code)
	}
}

func TestWebhookSecretRouteRefusesTheEnvBackend(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.f.SetSecrets(NewEnvProvider())
	rec := doReq(t, e.srv, http.MethodPost, "/api/repos/r1/webhook-secret", nil, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "secrets backend") {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
}
