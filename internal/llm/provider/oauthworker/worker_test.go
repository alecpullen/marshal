package oauthworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"marshal/internal/credentials"
	"marshal/internal/oauth"
)

// quietLogger discards log output so a deliberately-failing refresh does not
// spam the test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// sleepRecorder records the durations the worker asked to sleep for and
// returns immediately, so tests never actually wait.
type sleepRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
	// stopAfter makes Sleep return false (simulating ctx cancellation)
	// once this many sleeps have been recorded. 0 means never stop.
	stopAfter int
	clock     *fakeClock
}

func (s *sleepRecorder) Sleep(ctx context.Context, d time.Duration) bool {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	n := len(s.delays)
	stop := s.stopAfter > 0 && n >= s.stopAfter
	s.mu.Unlock()
	if s.clock != nil {
		s.clock.Advance(d)
	}
	return !stop
}

func (s *sleepRecorder) Delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.delays))
	copy(out, s.delays)
	return out
}

// tokenStore builds an engine backed by an in-memory store holding the given
// token entry.
func tokenStore(t *testing.T, entry *oauth.StoredTokens) *oauth.Engine {
	t.Helper()
	store := credentials.NewMemStore()
	if entry != nil {
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal tokens: %v", err)
		}
		if err := store.Set("marshal:provider:codex", raw); err != nil {
			t.Fatalf("store.Set: %v", err)
		}
	}
	return &oauth.Engine{
		ServerURL:  "https://auth.openai.com",
		StorageKey: "marshal:provider:codex",
		Store:      store,
		Flow: oauth.FlowConfig{
			Issuer:       "https://auth.openai.com",
			AuthorizeURL: "https://auth.openai.com/oauth/authorize",
			TokenURL:     "https://auth.openai.com/oauth/token",
			ClientID:     "app_test",
		},
	}
}

// --- nextRefreshDelay ---

func TestNextRefreshDelayUsesEarliestRefreshHint(t *testing.T) {
	clock := newFakeClock()
	hint := clock.Now().Add(10 * time.Minute)
	raw, _ := json.Marshal(hint.Format(time.RFC3339))

	w := &Worker{Now: clock.Now}
	got := w.nextRefreshDelay(&oauth.StoredTokens{
		IssuedAt:          clock.Now(),
		ExpiresAt:         clock.Now().Add(time.Hour),
		EarliestRefreshAt: raw,
	})
	if got != 10*time.Minute {
		t.Fatalf("delay = %v, want 10m (the server hint)", got)
	}
}

func TestNextRefreshDelayHintInPastRefreshesImmediately(t *testing.T) {
	clock := newFakeClock()
	hint := clock.Now().Add(-5 * time.Minute)
	raw, _ := json.Marshal(hint.Format(time.RFC3339))

	w := &Worker{Now: clock.Now}
	got := w.nextRefreshDelay(&oauth.StoredTokens{
		IssuedAt:          clock.Now().Add(-time.Hour),
		ExpiresAt:         clock.Now().Add(time.Hour),
		EarliestRefreshAt: raw,
	})
	if got != minRefreshDelay {
		t.Fatalf("delay = %v, want the %v floor (hint already passed)", got, minRefreshDelay)
	}
}

func TestNextRefreshDelayFallsBackToLifetimeFraction(t *testing.T) {
	clock := newFakeClock()
	w := &Worker{Now: clock.Now}
	// Issued now, expires in 100 minutes, no hint: refresh at 80 minutes.
	got := w.nextRefreshDelay(&oauth.StoredTokens{
		IssuedAt:  clock.Now(),
		ExpiresAt: clock.Now().Add(100 * time.Minute),
	})
	if got != 80*time.Minute {
		t.Fatalf("delay = %v, want 80m (0.8 of a 100m lifetime)", got)
	}
}

func TestNextRefreshDelayUnknownLifetimeUsesRemaining(t *testing.T) {
	clock := newFakeClock()
	w := &Worker{Now: clock.Now}
	// No IssuedAt: lifetime is unknown, so fall back to remaining time.
	got := w.nextRefreshDelay(&oauth.StoredTokens{
		ExpiresAt: clock.Now().Add(30 * time.Minute),
	})
	if got != 30*time.Minute {
		t.Fatalf("delay = %v, want 30m (remaining lifetime)", got)
	}
}

func TestNextRefreshDelayFloorsAtMinimum(t *testing.T) {
	clock := newFakeClock()
	w := &Worker{Now: clock.Now}
	// Already expired and no hint: must not return a negative or zero delay.
	got := w.nextRefreshDelay(&oauth.StoredTokens{
		IssuedAt:  clock.Now().Add(-2 * time.Hour),
		ExpiresAt: clock.Now().Add(-time.Hour),
	})
	if got != minRefreshDelay {
		t.Fatalf("delay = %v, want the %v floor", got, minRefreshDelay)
	}
}

// --- Run ---

func TestRunRefreshesAtHintAndStopsOnCancel(t *testing.T) {
	clock := newFakeClock()
	hint := clock.Now().Add(5 * time.Minute)
	raw, _ := json.Marshal(hint.Format(time.RFC3339))

	// The refresh endpoint returns a new token.
	var refreshes int
	var mu sync.Mutex
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer refresh.Close()

	engine := tokenStore(t, &oauth.StoredTokens{
		AccessToken:       "old-access",
		RefreshToken:      "old-refresh",
		IssuedAt:          clock.Now(),
		ExpiresAt:         clock.Now().Add(time.Hour),
		EarliestRefreshAt: raw,
	})
	engine.Flow.TokenURL = refresh.URL

	sleeper := &sleepRecorder{stopAfter: 2, clock: clock}
	w := &Worker{
		Engine:   engine,
		Provider: "codex",
		Logger:   quietLogger(),
		Now:      clock.Now,
		Sleep:    sleeper.Sleep,
	}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run returned %v, want nil on clean stop", err)
	}

	delays := sleeper.Delays()
	if len(delays) < 1 {
		t.Fatal("worker never slept")
	}
	if delays[0] != 5*time.Minute {
		t.Fatalf("first sleep = %v, want 5m (the hint)", delays[0])
	}
	mu.Lock()
	got := refreshes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1", got)
	}
}

func TestRunSleepsAndRetriesWhenNoToken(t *testing.T) {
	engine := tokenStore(t, nil) // no token stored
	sleeper := &sleepRecorder{stopAfter: 3}
	w := &Worker{
		Engine:   engine,
		Provider: "codex",
		Logger:   quietLogger(),
		Sleep:    sleeper.Sleep,
	}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	delays := sleeper.Delays()
	if len(delays) != 3 {
		t.Fatalf("slept %d times, want 3", len(delays))
	}
	for i, d := range delays {
		if d != noTokenRetryDelay {
			t.Errorf("sleep[%d] = %v, want %v", i, d, noTokenRetryDelay)
		}
	}
}

func TestRunReturnsNilOnImmediateCancel(t *testing.T) {
	engine := tokenStore(t, &oauth.StoredTokens{
		AccessToken: "a",
		IssuedAt:    time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &Worker{Engine: engine, Provider: "codex", Logger: quietLogger()}
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run on a cancelled ctx returned %v, want nil", err)
	}
}

func TestRunReturnsNilWhenEngineNil(t *testing.T) {
	w := &Worker{Provider: "codex", Logger: quietLogger()}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run with a nil engine returned %v, want nil", err)
	}
}

func TestRunSurvivesRefreshFailure(t *testing.T) {
	clock := newFakeClock()
	// The refresh endpoint always fails.
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer refresh.Close()

	engine := tokenStore(t, &oauth.StoredTokens{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		IssuedAt:     clock.Now(),
		ExpiresAt:    clock.Now().Add(time.Hour),
	})
	engine.Flow.TokenURL = refresh.URL

	// Two cycles: the first refresh fails, the worker must loop rather than
	// terminate.
	sleeper := &sleepRecorder{stopAfter: 3, clock: clock}
	w := &Worker{
		Engine:   engine,
		Provider: "codex",
		Logger:   quietLogger(),
		Now:      clock.Now,
		Sleep:    sleeper.Sleep,
	}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run returned %v, want nil (a failed refresh must not terminate the worker)", err)
	}
	if len(sleeper.Delays()) < 3 {
		t.Fatalf("worker stopped early after a refresh failure: %v", sleeper.Delays())
	}
}

// TestRunBacksOffAfterRefreshFailure: a transient refresh failure must not
// retry at the 1s floor. The stored token is still expired, so recomputing
// the schedule would return minRefreshDelay and hammer the token endpoint
// once a second for the whole outage.
func TestRunBacksOffAfterRefreshFailure(t *testing.T) {
	clock := newFakeClock()
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer refresh.Close()

	engine := tokenStore(t, &oauth.StoredTokens{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		IssuedAt:     clock.Now().Add(-2 * time.Hour),
		ExpiresAt:    clock.Now().Add(-time.Hour), // already expired
	})
	engine.Flow.TokenURL = refresh.URL

	sleeper := &sleepRecorder{stopAfter: 3, clock: clock}
	w := &Worker{
		Engine:   engine,
		Provider: "codex",
		Logger:   quietLogger(),
		Now:      clock.Now,
		Sleep:    sleeper.Sleep,
	}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	delays := sleeper.Delays()
	// Each retry cycle is two sleeps: the schedule (the 1s floor, because
	// the stored token is still expired) followed by the failure backoff.
	// The contract that matters is that the cycle is dominated by the
	// backoff — two consecutive 1s sleeps would mean a 1 Hz retry storm.
	if len(delays) < 2 {
		t.Fatalf("expected at least 2 sleeps, got %v", delays)
	}
	if delays[1] != failureRetryDelay {
		t.Errorf("sleep[1] = %v, want the %v backoff after a failed refresh", delays[1], failureRetryDelay)
	}
	for i := 1; i < len(delays); i++ {
		if delays[i] == minRefreshDelay && delays[i-1] == minRefreshDelay {
			t.Fatalf("consecutive 1s sleeps at %d-%d (%v): the worker is retrying at 1 Hz", i-1, i, delays)
		}
	}
	// At least one backoff must have been paid.
	var backoffs int
	for _, d := range delays {
		if d == failureRetryDelay {
			backoffs++
		}
	}
	if backoffs == 0 {
		t.Fatalf("no failure backoff was paid: %v", delays)
	}
}

// TestRunDoesNotBackOffAfterSuccess: a successful refresh must return to the
// normal schedule rather than paying the failure backoff.
func TestRunDoesNotBackOffAfterSuccess(t *testing.T) {
	clock := newFakeClock()
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer refresh.Close()

	engine := tokenStore(t, &oauth.StoredTokens{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		IssuedAt:     clock.Now(),
		ExpiresAt:    clock.Now().Add(time.Hour),
	})
	engine.Flow.TokenURL = refresh.URL

	sleeper := &sleepRecorder{stopAfter: 2, clock: clock}
	w := &Worker{
		Engine:   engine,
		Provider: "codex",
		Logger:   quietLogger(),
		Now:      clock.Now,
		Sleep:    sleeper.Sleep,
	}

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	for i, d := range sleeper.Delays() {
		if d == failureRetryDelay {
			t.Errorf("sleep[%d] = %v; a successful refresh must not pay the failure backoff", i, d)
		}
	}
}

func TestName(t *testing.T) {
	w := &Worker{Provider: "codex"}
	if got := w.Name(); got != "oauth-refresh-codex" {
		t.Fatalf("Name() = %q, want oauth-refresh-codex", got)
	}
	w.WorkerName = "custom"
	if got := w.Name(); got != "custom" {
		t.Fatalf("Name() = %q, want custom", got)
	}
}
