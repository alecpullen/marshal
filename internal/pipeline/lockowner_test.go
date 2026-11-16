package pipeline

import (
	"os"
	"os/exec"
	"testing"
)

// TestLocalLivenessReportsOnlyWhatItCanProve pins the probe's conservative
// contract: it reports alive or dead only for a process it can actually see
// on this host, and unknown for everything else.
func TestLocalLivenessReportsOnlyWhatItCanProve(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Fatalf("hostname: %v", err)
	}
	if got := LocalLiveness(host, os.Getpid()); got != LivenessAlive {
		t.Errorf("LocalLiveness(this host, own pid) = %v, want %v", got, LivenessAlive)
	}

	// A child that has exited and been reaped is demonstrably gone.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run child: %v", err)
	}
	deadPID := cmd.Process.Pid
	if got := LocalLiveness(host, deadPID); got != LivenessDead {
		t.Errorf("LocalLiveness(this host, reaped pid %d) = %v, want %v", deadPID, got, LivenessDead)
	}

	// Another host cannot be probed at all.
	if got := LocalLiveness("other-host", 1234); got != LivenessUnknown {
		t.Errorf("LocalLiveness(other-host, 1234) = %v, want %v", got, LivenessUnknown)
	}
	// No usable owner identity is unknown, never dead.
	if got := LocalLiveness("", 0); got != LivenessUnknown {
		t.Errorf(`LocalLiveness("", 0) = %v, want %v`, got, LivenessUnknown)
	}
	if got := LocalLiveness(host, 0); got != LivenessUnknown {
		t.Errorf("LocalLiveness(this host, 0) = %v, want %v", got, LivenessUnknown)
	}
}
