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
	m.mu.Lock()
	m.now = func() time.Time { return start }
	m.mu.Unlock()

	m.noteCaptureFailure(root, ReasonCommandTimeout)
	if _, ok := m.suppressedCapture(root); !ok {
		t.Fatal("suppressedCapture immediately after failure = false, want true")
	}

	m.mu.Lock()
	m.now = func() time.Time { return start.Add(captureBackoff) }
	m.mu.Unlock()
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
//
// The write is made under m.mu because production reads m.now under that lock;
// an unsynchronised write would race a live capture goroutine.
func afterCaptureBackoff(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.now
	m.now = func() time.Time { return prev().Add(captureBackoff + time.Second) }
}

// TestRecordsCaptureSuppression pins which failures suppress a workspace.
//
// Only a failure of the capture itself does. A refusal is a decision made
// before a byte is written and must leave the advice its warning gives
// actionable; a cancellation is an expected abort; an empty workspace is a
// property of the workspace, not a defect.
func TestRecordsCaptureSuppression(t *testing.T) {
	suppress := []StoreReason{
		ReasonCommandTimeout,
		ReasonUnreadableFile,
		ReasonLockTimeout,
		ReasonUnsupportedFileType,
		ReasonUnverifiedObject,
		ReasonInternal,
	}
	for _, reason := range suppress {
		if !recordsCaptureSuppression(reason) {
			t.Errorf("recordsCaptureSuppression(%q) = false, want true", reason)
		}
	}
	exempt := []StoreReason{
		"",
		ReasonBudgetExhausted,
		ReasonInsufficientFreeSpace,
		ReasonNoEligiblePaths,
		ReasonInterruptedCapture,
	}
	for _, reason := range exempt {
		if recordsCaptureSuppression(reason) {
			t.Errorf("recordsCaptureSuppression(%q) = true, want false", reason)
		}
	}
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
