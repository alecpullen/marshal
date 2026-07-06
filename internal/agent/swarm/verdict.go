package swarm

import "strings"

// ParseVerdict extracts the tester's PASS/FAIL verdict from its final
// answer. The tester is instructed to end with a "VERDICT: PASS" or
// "VERDICT: FAIL" line (mirroring the reviewer's APPROVE convention).
// ok is false when no recognisable verdict line is present, so the
// orchestrator can treat an ambiguous tester as "stop, do not loop".
func ParseVerdict(summary string) (pass bool, ok bool) {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "verdict:") {
			continue
		}
		value := strings.TrimSpace(lower[len("verdict:"):])
		switch value {
		case "pass":
			return true, true
		case "fail":
			return false, true
		default:
			return false, false
		}
	}
	return false, false
}
