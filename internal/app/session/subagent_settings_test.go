package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
)

// TestSubagentSettingsValidation pins the snapshot rule: a supplied snapshot
// is validated, never clamped. A cap outside 1..MaxSubagentConcurrency is a
// caller error, and an empty model is valid because it means normal child
// routing.
func TestSubagentSettingsValidation(t *testing.T) {
	valid := []SubagentSettings{
		{Enabled: true, Concurrency: 1},
		{Enabled: true, Concurrency: MaxSubagentConcurrency},
		{Enabled: false, Model: "mock/big", Concurrency: 2},
		{Enabled: true, Model: "mock/big", Concurrency: 3},
	}
	for _, s := range valid {
		if err := ValidateSubagentSettings(s); err != nil {
			t.Errorf("ValidateSubagentSettings(%+v) = %v, want nil", s, err)
		}
	}
	invalid := []SubagentSettings{
		{Enabled: true, Concurrency: 0},
		{Enabled: true, Concurrency: -1},
		{Enabled: true, Concurrency: MaxSubagentConcurrency + 1},
		{Enabled: true, Model: "mock/big", Concurrency: 99},
	}
	for _, s := range invalid {
		if err := ValidateSubagentSettings(s); err == nil {
			t.Errorf("ValidateSubagentSettings(%+v) = nil, want an out-of-range error", s)
		}
	}
}

// TestSubagentSettingsBindOnlyTheirSession pins that a creation-time snapshot
// reaches its own session only: it replaces that session's configured cap and
// leaves a sibling session on the ordinary defaults. Nothing is written to the
// session's config.
func TestSubagentSettingsBindOnlyTheirSession(t *testing.T) {
	bound := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{},
		WithSubagentMaxConcurrency(5),
		WithSubagentSettings(SubagentSettings{Enabled: false, Model: "mock/big", Concurrency: 2}),
	)
	plain := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{},
		WithSubagentMaxConcurrency(5),
	)

	got, ok := bound.SubagentSettings()
	if !ok {
		t.Fatal("bound session reports no child settings, want the snapshot it bound")
	}
	if got.Enabled || got.Model != "mock/big" || got.Concurrency != 2 {
		t.Fatalf("bound settings = %+v, want the resolved snapshot", got)
	}
	if cap := bound.SubagentMaxConcurrency(); cap != 2 {
		t.Fatalf("bound cap = %d, want the snapshot's 2 (it replaces the configured 5)", cap)
	}
	if _, ok := plain.SubagentSettings(); ok {
		t.Fatal("a session that bound no snapshot reports one; ordinary behaviour must stay unbound")
	}
	if cap := plain.SubagentMaxConcurrency(); cap != 5 {
		t.Fatalf("plain cap = %d, want the configured 5", cap)
	}
}

// TestSubagentDefaultsAreReportedAndDistinctFromTheSnapshot pins the two
// halves a client reads: the ordinary defaults a session reports (with the
// digest of their inputs), which stay unset until the runtime derives them,
// and the snapshot the session bound, which is never overwritten by them.
func TestSubagentDefaultsAreReportedAndDistinctFromTheSnapshot(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	if _, ok := state.SubagentDefaults(); ok {
		t.Fatal("a fresh state reports child defaults before the runtime derived them")
	}
	state.SetSubagentDefaults(SubagentDefaults{
		Settings: SubagentSettings{Enabled: true, Concurrency: 3},
		Digest:   "abc123",
	})
	defaults, ok := state.SubagentDefaults()
	if !ok {
		t.Fatal("SubagentDefaults() = false after SetSubagentDefaults")
	}
	if !defaults.Settings.Enabled || defaults.Settings.Model != "" || defaults.Settings.Concurrency != 3 {
		t.Fatalf("defaults = %+v, want enabled, empty model and the ordinary cap", defaults)
	}
	if defaults.Digest != "abc123" {
		t.Fatalf("digest = %q, want abc123", defaults.Digest)
	}
	if _, ok := state.SubagentSettings(); ok {
		t.Fatal("recording the ordinary defaults bound a snapshot; it must not")
	}

	// A session's own snapshot survives a later defaults refresh (a reload
	// recomputes the defaults and leaves the bound settings alone).
	state = New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{},
		WithSubagentSettings(SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 1}))
	state.SetSubagentDefaults(SubagentDefaults{
		Settings: SubagentSettings{Enabled: true, Concurrency: 3},
		Digest:   "def456",
	})
	if settings, ok := state.SubagentSettings(); !ok || settings.Concurrency != 1 {
		t.Fatalf("bound settings = %+v (ok=%v), want the snapshot's cap 1 after a defaults refresh", settings, ok)
	}
	if cap := state.SubagentMaxConcurrency(); cap != 1 {
		t.Fatalf("cap after defaults refresh = %d, want the bound snapshot's 1", cap)
	}
}

// TestEffectiveSubagentConcurrency pins the one normalization rule the
// configured cap and the reported defaults share.
func TestEffectiveSubagentConcurrency(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, defaultSubagentMaxConcurrency},
		{-3, defaultSubagentMaxConcurrency},
		{1, 1},
		{8, 8},
		{10, 10},
		{11, MaxSubagentConcurrency},
		{99, MaxSubagentConcurrency},
	}
	for _, tc := range cases {
		if got := EffectiveSubagentConcurrency(tc.in); got != tc.want {
			t.Errorf("EffectiveSubagentConcurrency(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
