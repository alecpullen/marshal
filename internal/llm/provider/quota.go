package provider

import (
	"net/http"
	"strconv"

	"marshal/internal/llm/schema"
)

// Codex quota headers. Every 2xx response from the ChatGPT-subscription
// backend carries this family; the names and semantics were established by
// the live spike (docs/codex-spike-findings-2026-09-14.md §5). The article's
// "whisker-*" naming does not exist — the real family is x-codex-*.
const (
	headerCodexPlanType              = "X-Codex-Plan-Type"
	headerCodexPrimaryUsedPercent    = "X-Codex-Primary-Used-Percent"
	headerCodexPrimaryResetAfterSecs = "X-Codex-Primary-Reset-After-Seconds"
	headerCodexPrimaryWindowMinutes  = "X-Codex-Primary-Window-Minutes"
	headerCodexSecondaryUsedPercent  = "X-Codex-Secondary-Used-Percent"
	headerCodexSecondaryResetAfter   = "X-Codex-Secondary-Reset-After-Seconds"
	headerCodexSecondaryWindowMin    = "X-Codex-Secondary-Window-Minutes"
)

// quotaHeaders are the headers whose presence marks a response as carrying
// quota telemetry. Presence is tracked separately from value because a
// legitimate reading can be all zeros — a user who has used 0% of both
// windows must still see their quota, and comparing the parsed struct to its
// zero value would silently drop it.
var quotaHeaders = []string{
	headerCodexPlanType,
	headerCodexPrimaryUsedPercent,
	headerCodexPrimaryResetAfterSecs,
	headerCodexPrimaryWindowMinutes,
	headerCodexSecondaryUsedPercent,
	headerCodexSecondaryResetAfter,
	headerCodexSecondaryWindowMin,
}

// quotaFromHeaders parses the x-codex-* quota headers into a QuotaInfo.
//
// It returns nil when none of the quota headers are present, which is the
// normal case for non-OAuth providers and for error responses. Individual
// headers that are absent or unparsable are skipped rather than failing the
// whole parse: the endpoint is free to add or drop fields, and a missing
// counter must not cost the user the counters that did arrive.
func quotaFromHeaders(h http.Header) *schema.QuotaInfo {
	if h == nil {
		return nil
	}
	present := false
	for _, name := range quotaHeaders {
		if h.Get(name) != "" {
			present = true
			break
		}
	}
	if !present {
		return nil
	}
	return &schema.QuotaInfo{
		PlanType:                h.Get(headerCodexPlanType),
		PrimaryUsedPercent:      atoiHeader(h, headerCodexPrimaryUsedPercent),
		PrimaryResetAfterSecs:   atoiHeader(h, headerCodexPrimaryResetAfterSecs),
		PrimaryWindowMinutes:    atoiHeader(h, headerCodexPrimaryWindowMinutes),
		SecondaryUsedPercent:    atoiHeader(h, headerCodexSecondaryUsedPercent),
		SecondaryResetAfterSecs: atoiHeader(h, headerCodexSecondaryResetAfter),
		SecondaryWindowMinutes:  atoiHeader(h, headerCodexSecondaryWindowMin),
	}
}

// atoiHeader returns the integer value of a header, or 0 when the header is
// absent or not a number.
func atoiHeader(h http.Header, name string) int {
	v := h.Get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
