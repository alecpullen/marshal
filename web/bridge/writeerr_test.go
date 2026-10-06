package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// writeErr maps bridge errors onto status codes. A caller's mistake must
// not read as a gateway fault: the SPA branches on the code, and a 502
// tells it the bridge is broken when in fact the request named something
// that does not exist.
func TestWriteErrMapsCallerMistakes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unregistered repo", ErrUnregisteredRepo, http.StatusNotFound},
		{"unknown repo", ErrUnknownRepo, http.StatusNotFound},
		{"unknown pending submission", ErrUnknownPending, http.StatusNotFound},
		{"unknown agent", ErrUnknownAgent, http.StatusNotFound},
		{"unknown session", ErrUnknownSession, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErr(rec, tc.err)
			if rec.Code != tc.want {
				t.Fatalf("writeErr(%v) = %d, want %d (body %s)",
					tc.err, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// A wrapped sentinel must map the same way as the bare one: the fleet
// wraps these with the offending id for the log.
func TestWriteErrMapsWrappedCallerMistakes(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, errors.Join(ErrUnregisteredRepo, errors.New("nope")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a wrapped ErrUnregisteredRepo = %d, want 404", rec.Code)
	}
}

// Patch export only applies to a git-sourced agent: a local agent's work
// is the project itself, so there is no checkout to diff against. That is
// the caller asking for something the agent cannot provide, not a gateway
// fault, so it must not be a 502.
func TestPatchExportOnALocalAgentIsNotAGatewayError(t *testing.T) {
	f, _, _ := agentFleet(t)
	id, err := f.Spawn(ctlContext(t), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_, err = f.Patch(context.Background(), id)
	if err == nil {
		t.Fatal("Patch succeeded for a local agent")
	}
	rec := httptest.NewRecorder()
	writeErr(rec, err)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("patch export on a local agent maps to 502: %v", err)
	}
	if rec.Code != http.StatusConflict && rec.Code != http.StatusBadRequest {
		t.Fatalf("patch export on a local agent = %d, want 400 or 409", rec.Code)
	}
}

// The review bot must report an unknown repo as unknown, not as "the bot
// is off". The SPA shows a different message for each, and 409 tells the
// user to switch the bot on for a repo that does not exist.
func TestRunReviewBotOnAnUnknownRepoIsNotFound(t *testing.T) {
	f, _, _ := agentFleet(t)
	err := f.RunReviewBot(context.Background(), "no-such-repo", 1)
	if err == nil {
		t.Fatal("RunReviewBot succeeded for an unknown repo")
	}
	rec := httptest.NewRecorder()
	writeAutomationErr(rec, err)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("RunReviewBot on an unknown repo = %d, want 404 (body %s)",
			rec.Code, rec.Body.String())
	}
}
