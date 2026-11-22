package snapshot

import (
	"testing"
	"time"
)

// TestCaptureBackoffSuppressesSecondAttempt covers the whole in-memory cycle:
// one failure suppresses the workspace with that failure's reason, and a
// success clears it.
func TestCaptureBackoffSuppressesSecondAttempt(t *testing.T) {
	m := newTestManager(t)
	root := t.TempDir()

	m.noteCaptureFailure(root, ReasonCommandTimeout)
	if reason, ok := m.suppressedCapture(root); !ok || reason != ReasonCommandTimeout {
		t.Fatalf("suppressedCapture after failure = (%q, %v), want (%q, true)",
			reason, ok, ReasonCommandTimeout)
	}

	m.clearCaptureSuppression(root)
	if reason, ok := m.suppressedCapture(root); ok {
		t.Fatalf("suppressedCapture after clear = (%q, true), want (\"\", false)", reason)
	}
}

// TestCaptureBackoffExpires pins the backoff to a timed one: once captureBackoff
// has elapsed the workspace is attempted again and the stale entry is dropped.
func TestCaptureBackoffExpires(t *testing.T) {
	m := newTestManager(t)
	root := t.TempDir()
	start := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return start }

	m.noteCaptureFailure(root, ReasonCommandTimeout)
	if _, ok := m.suppressedCapture(root); !ok {
		t.Fatal("suppressedCapture immediately after failure = false, want true")
	}

	m.now = func() time.Time { return start.Add(captureBackoff) }
	if reason, ok := m.suppressedCapture(root); ok {
		t.Fatalf("suppressedCapture after %s = (%q, true), want (\"\", false)", captureBackoff, reason)
	}

	key, ok := m.captureKey(root)
	if !ok {
		t.Fatalf("captureKey(%q) = not ok", root)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.captureSuppressions[key]; ok {
		t.Fatal("expired suppression entry was not deleted")
	}
}

// afterCaptureBackoff shifts the manager's clock past captureBackoff, keeping
// it live. A test that reuses a workspace after a failed capture retries at the
// moment the design allows one, instead of reaching into the suppression map.
func afterCaptureBackoff(m *Manager) {
	prev := m.now
	m.now = func() time.Time { return prev().Add(captureBackoff + time.Second) }
}

// TestCaptureKeyCanonicalisesRoot checks that two spellings of one directory
// share a single suppression entry.
func TestCaptureKeyCanonicalisesRoot(t *testing.T) {
	m := newTestManager(t)
	root := t.TempDir()

	direct, ok := m.captureKey(root)
	if !ok {
		t.Fatalf("captureKey(%q) = not ok", root)
	}
	dotted, ok := m.captureKey(root + "/./")
	if !ok {
		t.Fatalf("captureKey(%q) = not ok", root+"/./")
	}
	if direct != dotted {
		t.Fatalf("captureKey(%q) = %q, captureKey(%q) = %q; want equal",
			root, direct, root+"/./", dotted)
	}
}
