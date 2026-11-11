package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/oauthworker"
	"marshal/internal/trust"
)

// newRefreshEmbedded builds an Embedded over home with cfg, an isolated auth
// resolver and a temporary project, as embedded_test.go's tests do.
func newRefreshEmbedded(t *testing.T, home, work string, cfg config.Config) *Embedded {
	t.Helper()
	auth, _ := memoryAuthResolver(t)
	embedded, err := NewEmbedded(context.Background(),
		WithWorkingDir(work),
		WithHomeDir(home),
		WithAuthResolver(auth),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	)
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = embedded.Close(context.Background()) })
	return embedded
}

// oauthLeasePath is the lock file the runtimes contend for.
func oauthLeasePath(home string) string {
	return filepath.Join(config.DataDir(home), oauthworker.LeaseFile)
}

// waitForLeaseHeld waits until another holder has the refresh lease. A probe
// that wins the race with the runtime's first attempt releases the lease again
// and retries: the runtime takes it on its next retry.
func waitForLeaseHeld(t *testing.T, home string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		release, held, err := oauthworker.TryLease(config.DataDir(home))
		if err != nil {
			t.Fatalf("TryLease: %v", err)
		}
		if !held {
			return
		}
		release()
		if time.Now().After(deadline) {
			t.Fatal("no runtime took the refresh lease within 2s")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForLeaseFree waits until the test can take the refresh lease, and
// releases it.
func waitForLeaseFree(t *testing.T, home string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		release, held, err := oauthworker.TryLease(config.DataDir(home))
		if err != nil {
			t.Fatalf("TryLease: %v", err)
		}
		if held {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh lease was still held 2s after the runtime finished")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAnEmbeddedRuntimeWithoutOAuthTakesNoLease is decision 3: a runtime with
// no OAuth provider never touches the lock file, so it cannot keep the
// daemon's helper from refreshing.
func TestAnEmbeddedRuntimeWithoutOAuthTakesNoLease(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	embedded := newRefreshEmbedded(t, home, work, config.Default())

	if _, err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Watch for the file rather than stat once: a goroutine that took the
	// lease would create it just after Start returns, and a single check can
	// miss it. The window is generous next to the work a lease-taking
	// goroutine does.
	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); {
		if _, err := os.Stat(oauthLeasePath(home)); err == nil {
			t.Fatal("a runtime with no OAuth provider created the refresh lease")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(oauthLeasePath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lock file exists after a runtime with no OAuth provider started: %v", err)
	}
	if err := embedded.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

// TestAnEmbeddedRuntimeHoldsTheLeaseWhileItRuns pins the lease's lifetime to
// the runtime's work: held from Start, released when Finish cancels it.
func TestAnEmbeddedRuntimeHoldsTheLeaseWhileItRuns(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	cfg := codexOAuthProviderConfig("codex", "https://example.invalid/backend-api")
	embedded := newRefreshEmbedded(t, home, work, cfg)
	embedded.leaseRetry = time.Millisecond

	if _, err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForLeaseHeld(t, home)

	if err := embedded.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	waitForLeaseFree(t, home)
}

// TestASecondEmbeddingWaitsAndTakesOver is W9 for embedded mode: a second
// process waits for the first, and takes the lease when it exits.
func TestASecondEmbeddingWaitsAndTakesOver(t *testing.T) {
	home := t.TempDir()
	cfg := codexOAuthProviderConfig("codex", "https://example.invalid/backend-api")
	first := newRefreshEmbedded(t, home, t.TempDir(), cfg)
	second := newRefreshEmbedded(t, home, t.TempDir(), cfg)
	second.leaseRetry = time.Millisecond

	if _, err := first.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	waitForLeaseHeld(t, home)

	if _, err := second.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	// While the first runs it keeps the lease, so the second refreshes
	// nothing: no probe over a short window gets it.
	for deadline := time.Now().Add(50 * time.Millisecond); time.Now().Before(deadline); {
		release, held, err := oauthworker.TryLease(config.DataDir(home))
		if err != nil {
			t.Fatalf("TryLease: %v", err)
		}
		if held {
			release()
			t.Fatal("the lease was free while the first embedding was refreshing")
		}
		time.Sleep(time.Millisecond)
	}

	if err := first.Finish(nil); err != nil {
		t.Fatalf("first Finish: %v", err)
	}
	// The second retries at leaseRetry, so it takes over promptly.
	waitForLeaseHeld(t, home)

	if err := second.Finish(nil); err != nil {
		t.Fatalf("second Finish: %v", err)
	}
	waitForLeaseFree(t, home)
}

// TestAResumeHandsTheLeaseOver is decision 4: a resume's next runtime holds
// the lease itself, without waiting out the 30s retry in the gap.
func TestAResumeHandsTheLeaseOver(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	cfg := codexOAuthProviderConfig("codex", "https://example.invalid/backend-api")
	embedded := newRefreshEmbedded(t, home, work, cfg)
	embedded.leaseRetry = time.Millisecond

	rt, err := embedded.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForLeaseHeld(t, home)

	embedded.Resume(rt.SessionID)
	if _, err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start after Resume: %v", err)
	}
	waitForLeaseHeld(t, home)

	// And the new runtime keeps it: the handoff does not leave a gap another
	// process could take.
	for i := 0; i < 50; i++ {
		release, held, err := oauthworker.TryLease(config.DataDir(home))
		if err != nil {
			t.Fatalf("TryLease: %v", err)
		}
		if held {
			release()
			t.Fatalf("the lease was free after the handoff (probe %d)", i)
		}
		time.Sleep(time.Millisecond)
	}

	if err := embedded.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	waitForLeaseFree(t, home)
}

// TestAStartWaitsForThePriorRefreshBoundedly pins decision 4's wait rather
// than its fallback: a runtime waits for the previous one's refresh work to
// release the lease, and gives up after oauthHandoffWait instead of blocking
// the session. A previous work channel that never closes stands in for a
// release that is slow to land: with the wait removed Start returns at once.
func TestAStartWaitsForThePriorRefreshBoundedly(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	cfg := codexOAuthProviderConfig("codex", "https://example.invalid/backend-api")
	embedded := newRefreshEmbedded(t, home, work, cfg)
	// The previous runtime's work has not returned, and never will.
	embedded.refreshDone = make(chan struct{})

	began := time.Now()
	if _, err := embedded.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	took := time.Since(began)
	if took < oauthHandoffWait {
		t.Fatalf("Start returned after %s, before the %s handoff wait", took, oauthHandoffWait)
	}
	if took > oauthHandoffWait+2*time.Second {
		t.Fatalf("Start waited %s, far past the %s handoff wait", took, oauthHandoffWait)
	}

	if err := embedded.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	waitForLeaseFree(t, home)
}
