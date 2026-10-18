package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A legacy store's size is reused across usage passes within legacyUsageTTL,
// and re-measured once it expires, when the store directory itself changes,
// or after the cache is cleared by a recovery command.
func TestLegacyStoreUsageIsReusedWithinItsTTL(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	m, _, ctx := testCatalog(t, WithClock(func() time.Time { return now }))
	legacy := filepath.Join(m.Root(), "abcdefabcdef")
	deep := filepath.Join(legacy, "objects", "pack")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deep, "a.pack"), make([]byte, 64<<10), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	legacyBytes := func() int64 {
		t.Helper()
		u, err := m.UsageContext(ctx)
		if err != nil {
			t.Fatalf("UsageContext: %v", err)
		}
		return u.LegacyAllocated
	}
	grow := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(deep, name), make([]byte, 256<<10), 0o644); err != nil {
			t.Fatalf("grow legacy: %v", err)
		}
	}

	first := legacyBytes()
	grow("b.pack") // deep growth: the store directory's own mtime is unchanged
	if got := legacyBytes(); got != first {
		t.Fatalf("legacy usage re-measured within the TTL: %d then %d", first, got)
	}
	now = now.Add(legacyUsageTTL)
	expired := legacyBytes()
	if expired <= first {
		t.Fatalf("legacy usage after the TTL = %d, want growth past %d", expired, first)
	}

	grow("c.pack")
	m.forgetLegacyUsage()
	if got := legacyBytes(); got <= expired {
		t.Fatalf("legacy usage after forgetLegacyUsage = %d, want growth past %d", got, expired)
	}

	// A change to the store directory itself invalidates immediately.
	before := legacyBytes()
	if err := os.WriteFile(filepath.Join(legacy, "HEAD"), make([]byte, 256<<10), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if got := legacyBytes(); got <= before {
		t.Fatalf("legacy usage after a top-level change = %d, want growth past %d", got, before)
	}
}
