package policy

import (
	"testing"

	"marshal/internal/tools/registry"
)

// TestClassifyFindAndDD pins S-02 and S-03: find -delete and dd of=… are
// first-class destructive primitives containing no "rm", so neither the
// substring guardrails nor the previous argv0 switch saw them. In auto and
// copilot mode the guardrail floor is the only barrier, and the default
// restricted sandbox provides no filesystem isolation.
func TestClassifyFindAndDD(t *testing.T) {
	destructive := []string{
		"find / -delete",
		"find . -name '*.tmp' -delete",
		"find / -exec rm -rf {} +",
		"find / -execdir shred {} ;",
		"find . -ok rm {} ;",
		"dd if=/dev/zero of=/dev/sda",
		"dd if=/dev/urandom of=/dev/disk2 bs=1m",
		"dd if=backup.img of=important.db",
	}
	for _, cmd := range destructive {
		t.Run(cmd, func(t *testing.T) {
			got, err := ClassifyCommand(cmd)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if got.Risk != registry.RiskDestructive {
				t.Errorf("risk = %v, want destructive (reason %q)", got.Risk, got.Reason)
			}
		})
	}

	// Argv-aware, not substring: these are searches and reads. Flagging them
	// would train users to approve destructive prompts reflexively, which
	// costs more safety than it buys.
	safe := []string{
		"find . -name '*-delete*'",
		"find . -name '*.go' -print",
		"find . -type f",
		"dd --help",
	}
	for _, cmd := range safe {
		t.Run("safe/"+cmd, func(t *testing.T) {
			got, err := ClassifyCommand(cmd)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if got.Risk == registry.RiskDestructive {
				t.Errorf("false positive: %q classified destructive (%q)", cmd, got.Reason)
			}
		})
	}
}

// TestGuardrailBlocksFindAndDD pins the end-to-end path: the classification
// must reach analyzeCommand's blocked verdict, not merely return a label.
func TestGuardrailBlocksFindAndDD(t *testing.T) {
	for _, cmd := range []string{"find / -delete", "dd if=/dev/zero of=/dev/sda"} {
		v, err := analyzeCommand(cmd)
		if err != nil {
			t.Fatalf("analyze %q: %v", cmd, err)
		}
		if !v.blocked {
			t.Errorf("%q not blocked by the guardrail floor", cmd)
		}
	}
}
