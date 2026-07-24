package config

import (
	"strings"
	"testing"
)

func TestDefaultRolloverDigestProvider(t *testing.T) {
	if got := Default().Session.Rollover.DigestProvider; got != "llm_summary" {
		t.Errorf("default Rollover.DigestProvider = %q, want %q", got, "llm_summary")
	}
}

func TestMergeRolloverDigestProvider(t *testing.T) {
	cfg := Default()
	dp := "files"
	if err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{DigestProvider: &dp}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.Session.Rollover.DigestProvider != "files" {
		t.Errorf("Rollover.DigestProvider = %q, want %q", cfg.Session.Rollover.DigestProvider, "files")
	}
}

func TestMergeRolloverDigestProviderAutoNormalizes(t *testing.T) {
	cfg := Default()
	dp := "auto"
	if err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{DigestProvider: &dp}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.Session.Rollover.DigestProvider != "llm_summary" {
		t.Errorf("auto normalized to %q, want %q", cfg.Session.Rollover.DigestProvider, "llm_summary")
	}
}

func TestMergeRolloverDigestProviderRejectsUnknown(t *testing.T) {
	cfg := Default()
	dp := "magic"
	err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{DigestProvider: &dp}},
	})
	if err == nil || !strings.Contains(err.Error(), "digest_provider") {
		t.Fatalf("merge err = %v, want error mentioning digest_provider", err)
	}
}

func TestDefaultRollover(t *testing.T) {
	cfg := Default()

	// Acceptance criteria 1: Default rollover is disabled.
	if cfg.Session.Rollover.Enabled {
		t.Errorf("default Rollover.Enabled = true, want false")
	}

	// Acceptance criteria 2: Default policy is "context_percent" at 70%.
	if cfg.Session.Rollover.Policy != "context_percent" {
		t.Errorf("default Rollover.Policy = %q, want %q", cfg.Session.Rollover.Policy, "context_percent")
	}
	if cfg.Session.Rollover.ContextPercentThreshold != 70 {
		t.Errorf("default Rollover.ContextPercentThreshold = %d, want %d", cfg.Session.Rollover.ContextPercentThreshold, 70)
	}

	// Acceptance criteria 3: Default turn_count_threshold is 40.
	if cfg.Session.Rollover.TurnCountThreshold != 40 {
		t.Errorf("default Rollover.TurnCountThreshold = %d, want %d", cfg.Session.Rollover.TurnCountThreshold, 40)
	}

	// Acceptance criteria 4: Default token_counter is "auto".
	if cfg.Session.Rollover.TokenCounter != "auto" {
		t.Errorf("default Rollover.TokenCounter = %q, want %q", cfg.Session.Rollover.TokenCounter, "auto")
	}

	// Acceptance criteria 5: Default recall_tool_enabled is "auto".
	if cfg.Session.Rollover.RecallToolEnabled != "auto" {
		t.Errorf("default Rollover.RecallToolEnabled = %q, want %q", cfg.Session.Rollover.RecallToolEnabled, "auto")
	}

	// Acceptance criteria 6: Default retention is "forever".
	if cfg.Session.Rollover.Retention != "forever" {
		t.Errorf("default Rollover.Retention = %q, want %q", cfg.Session.Rollover.Retention, "forever")
	}

	// Acceptance criteria 7: Default blob_threshold_bytes is 2048.
	if cfg.Session.Rollover.BlobThresholdBytes != 2048 {
		t.Errorf("default Rollover.BlobThresholdBytes = %d, want %d", cfg.Session.Rollover.BlobThresholdBytes, 2048)
	}
}

func TestMergeRollover(t *testing.T) {
	// Start from defaults.
	cfg := Default()

	// Verify defaults are set.
	if cfg.Session.Rollover.Enabled {
		t.Fatal("expected rollover disabled by default")
	}

	// Merge a config that sets only Enabled and Policy.
	enabled := true
	policy := "turn_count"
	digestModel := "gpt-4o"
	file := configFile{
		Session: &fileSession{
			Rollover: &fileRollover{
				Enabled:     &enabled,
				Policy:      &policy,
				DigestModel: &digestModel,
			},
		},
	}

	if err := merge(&cfg, file); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Acceptance criteria 8: Merge overrides only provided fields.
	if !cfg.Session.Rollover.Enabled {
		t.Errorf("Rollover.Enabled = false after merge, want true")
	}
	if cfg.Session.Rollover.Policy != "turn_count" {
		t.Errorf("Rollover.Policy = %q, want %q", cfg.Session.Rollover.Policy, "turn_count")
	}
	if cfg.Session.Rollover.DigestModel != "gpt-4o" {
		t.Errorf("Rollover.DigestModel = %q, want %q", cfg.Session.Rollover.DigestModel, "gpt-4o")
	}

	// Fields NOT in the merge file must retain their defaults.
	if cfg.Session.Rollover.ContextPercentThreshold != 70 {
		t.Errorf("Rollover.ContextPercentThreshold = %d after partial merge, want %d", cfg.Session.Rollover.ContextPercentThreshold, 70)
	}
	if cfg.Session.Rollover.TurnCountThreshold != 40 {
		t.Errorf("Rollover.TurnCountThreshold = %d after partial merge, want %d", cfg.Session.Rollover.TurnCountThreshold, 40)
	}
	if cfg.Session.Rollover.TokenCounter != "auto" {
		t.Errorf("Rollover.TokenCounter = %q after partial merge, want %q", cfg.Session.Rollover.TokenCounter, "auto")
	}
	if cfg.Session.Rollover.RecallToolEnabled != "auto" {
		t.Errorf("Rollover.RecallToolEnabled = %q after partial merge, want %q", cfg.Session.Rollover.RecallToolEnabled, "auto")
	}
	if cfg.Session.Rollover.Retention != "forever" {
		t.Errorf("Rollover.Retention = %q after partial merge, want %q", cfg.Session.Rollover.Retention, "forever")
	}
	if cfg.Session.Rollover.BlobThresholdBytes != 2048 {
		t.Errorf("Rollover.BlobThresholdBytes = %d after partial merge, want %d", cfg.Session.Rollover.BlobThresholdBytes, 2048)
	}
}

func TestDefaultRolloverCalibrationDisabled(t *testing.T) {
	if Default().Session.Rollover.Calibration.Enabled {
		t.Error("default calibration.Enabled = true, want false")
	}
}

func TestMergeRolloverCalibration(t *testing.T) {
	cfg := Default()
	enabled := true
	if err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{
			Calibration: &fileCalibration{Enabled: &enabled},
		}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !cfg.Session.Rollover.Calibration.Enabled {
		t.Error("calibration.Enabled not merged")
	}
}
