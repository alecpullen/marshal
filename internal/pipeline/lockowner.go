package pipeline

import (
	"os"
)

// Liveness describes what is known about a lock owner's process.
type Liveness int

const (
	// LivenessUnknown means liveness cannot be determined — the lock names
	// another host, carries no usable pid, or the platform cannot probe
	// processes. An unknown owner is never taken over.
	LivenessUnknown Liveness = iota
	// LivenessAlive means the owner is demonstrably running.
	LivenessAlive
	// LivenessDead means the owner is demonstrably gone.
	LivenessDead
)

// LivenessProbe reports the liveness of a lock owner. It is injected so
// tests can decide liveness without a real process.
type LivenessProbe func(host string, pid int) Liveness

// LocalLiveness is the default probe. It judges only owners on this host:
// another host's lock cannot be probed, so it reports LivenessUnknown.
func LocalLiveness(host string, pid int) Liveness {
	local, err := os.Hostname()
	if err != nil || local == "" || host == "" || host != local {
		return LivenessUnknown
	}
	if pid <= 0 {
		return LivenessUnknown
	}
	alive, known := pidAlive(pid)
	if !known {
		return LivenessUnknown
	}
	if alive {
		return LivenessAlive
	}
	return LivenessDead
}
