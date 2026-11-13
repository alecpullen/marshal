package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestDiscoveryCapabilityStatesAreExactlyFive pins the five-state capability
// vocabulary. The studio client and the report contract switch on these exact
// strings, so an addition or removal is a contract break, not a refinement.
func TestDiscoveryCapabilityStatesAreExactlyFive(t *testing.T) {
	want := []DiscoveryCapabilityState{
		"supported",
		"unconfigured",
		"insufficient_permission",
		"temporarily_unavailable",
		"unsupported",
	}
	got := []DiscoveryCapabilityState{
		DiscoveryCapabilitySupported,
		DiscoveryCapabilityUnconfigured,
		DiscoveryCapabilityInsufficientPermission,
		DiscoveryCapabilityTemporarilyUnavailable,
		DiscoveryCapabilityUnsupported,
	}

	if len(got) != len(want) {
		t.Fatalf("capability state count = %d, want exactly %d", len(got), len(want))
	}
	seen := make(map[DiscoveryCapabilityState]bool, len(got))
	for i, state := range got {
		if state != want[i] {
			t.Errorf("state %d = %q, want %q", i, state, want[i])
		}
		if seen[state] {
			t.Errorf("state %q is declared twice", state)
		}
		seen[state] = true
	}
	for _, state := range want {
		if !seen[state] {
			t.Errorf("state %q is missing from the frozen set of five", state)
		}
	}
}

// TestDiscoveryCapabilityOrderIsStable pins the ten capability keys and the
// order the report always emits them in. Callers index into this list, so the
// order is part of the contract.
func TestDiscoveryCapabilityOrderIsStable(t *testing.T) {
	want := []string{
		"repository_metadata",
		"branches",
		"pull_requests",
		"ci_checks",
		"ci_logs",
		"checkout_instructions",
		"checkout_docs",
		"checkout_tooling",
		"checkout_environment",
		"checkout_ci_definition",
	}
	got := discoveryCapabilityKeys()
	if len(got) != len(want) {
		t.Fatalf("capability count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("capability %d = %q, want %q (order is frozen)", i, got[i], want[i])
		}
	}

	// The accessor hands back a copy: a caller must not be able to reorder or
	// truncate the contract for everyone else in the process.
	got[0] = "mutated"
	if fresh := discoveryCapabilityKeys(); fresh[0] != want[0] {
		t.Fatalf("capability order is mutable through the accessor: %q", fresh[0])
	}
}

// TestDiscoveryCapabilityStateForForgeErrors pins the contextual mapping from a
// probe error to a capability state, including the 404 ambiguity that depends
// on whether repository metadata was already readable with the same credential.
func TestDiscoveryCapabilityStateForForgeErrors(t *testing.T) {
	const secret = "ghp_doNotEchoThisToken"

	transport := fmt.Errorf("forge API call: %w",
		errors.New("dial tcp 127.0.0.1:443: connect: connection refused"))

	cases := []struct {
		name             string
		err              error
		metadataReadable bool
		want             DiscoveryCapabilityState
		wantDetail       string // substring the detail must carry
	}{
		{
			name: "success is supported",
			err:  nil,
			want: DiscoveryCapabilitySupported,
		},
		{
			name:       "http 401 is unconfigured",
			err:        &forgeAPIError{statusCode: http.StatusUnauthorized, message: "Bad credentials for " + secret},
			want:       DiscoveryCapabilityUnconfigured,
			wantDetail: "401",
		},
		{
			name:             "http 403 without a retry signal is insufficient permission",
			err:              &forgeAPIError{statusCode: http.StatusForbidden},
			metadataReadable: true,
			want:             DiscoveryCapabilityInsufficientPermission,
			wantDetail:       "403",
		},
		{
			name:             "http 403 with retry-after is temporarily unavailable",
			err:              &forgeAPIError{statusCode: http.StatusForbidden, retryAfter: "60"},
			metadataReadable: true,
			want:             DiscoveryCapabilityTemporarilyUnavailable,
			wantDetail:       "retry",
		},
		{
			name:       "http 404 while metadata is unreadable is insufficient permission",
			err:        &forgeAPIError{statusCode: http.StatusNotFound},
			want:       DiscoveryCapabilityInsufficientPermission,
			wantDetail: "404",
		},
		{
			name:             "http 404 after metadata was readable is unsupported",
			err:              &forgeAPIError{statusCode: http.StatusNotFound},
			metadataReadable: true,
			want:             DiscoveryCapabilityUnsupported,
			wantDetail:       "404",
		},
		{
			name:             "http 429 is temporarily unavailable",
			err:              &forgeAPIError{statusCode: http.StatusTooManyRequests},
			metadataReadable: true,
			want:             DiscoveryCapabilityTemporarilyUnavailable,
			wantDetail:       "429",
		},
		{
			name:             "http 500 is temporarily unavailable",
			err:              &forgeAPIError{statusCode: http.StatusInternalServerError},
			metadataReadable: true,
			want:             DiscoveryCapabilityTemporarilyUnavailable,
			wantDetail:       "500",
		},
		{
			name:             "http 503 is temporarily unavailable",
			err:              &forgeAPIError{statusCode: http.StatusServiceUnavailable, retryAfter: "120"},
			metadataReadable: true,
			want:             DiscoveryCapabilityTemporarilyUnavailable,
			wantDetail:       "retry",
		},
		{
			name:             "transport failure is temporarily unavailable",
			err:              transport,
			metadataReadable: true,
			want:             DiscoveryCapabilityTemporarilyUnavailable,
			wantDetail:       "forge",
		},
		{
			name:             "unconfigured sentinel wins over http classification",
			err:              errDiscoveryUnconfigured,
			metadataReadable: true,
			want:             DiscoveryCapabilityUnconfigured,
			wantDetail:       "credential",
		},
		{
			name:             "wrapped unconfigured sentinel is unconfigured",
			err:              fmt.Errorf("gitea: %w", errDiscoveryUnconfigured),
			metadataReadable: true,
			want:             DiscoveryCapabilityUnconfigured,
			wantDetail:       "credential",
		},
		{
			name:             "unsupported sentinel wins over http classification",
			err:              errDiscoveryUnsupported,
			metadataReadable: true,
			want:             DiscoveryCapabilityUnsupported,
			wantDetail:       "does not expose",
		},
		{
			name:             "wrapped unsupported sentinel is unsupported",
			err:              fmt.Errorf("probe: %w", errDiscoveryUnsupported),
			metadataReadable: false,
			want:             DiscoveryCapabilityUnsupported,
			wantDetail:       "does not expose",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := discoveryCapabilityState(tc.err, tc.metadataReadable)
			if got != tc.want {
				t.Fatalf("state = %q, want %q (detail %q)", got, tc.want, detail)
			}
			if tc.want == DiscoveryCapabilitySupported {
				if detail != "" {
					t.Errorf("supported capability detail = %q, want empty", detail)
				}
				return
			}
			if detail == "" {
				t.Fatalf("%s must carry a usable detail", got)
			}
			if tc.wantDetail != "" && !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", detail, tc.wantDetail)
			}
			if strings.Contains(detail, secret) {
				t.Errorf("detail %q echoes the credential", detail)
			}
		})
	}
}

// TestDiscoveryReportJSONUsesStableShape pins the wire shape later tasks emit:
// non-nil slices, the worktree dirty tri-state, omitted optional fields, the
// field names of every nested record, and the absence of any credential value.
func TestDiscoveryReportJSONUsesStableShape(t *testing.T) {
	const token = "ghp_reportMustNotCarryToken"

	// The report has no credential field at all; holding one nearby must not
	// leak it onto the wire.
	cred := Credential{ID: "gh", Kind: "pat", OwnerID: "local", literal: token}

	report := DiscoveryReport{
		RepoID:       "repo-1",
		Forge:        "github",
		Status:       "degraded",
		Capabilities: []DiscoveryCapability{},
		Facts:        []DiscoveryFact{},
		Recommended:  []DiscoveryRecommendation{},
		Gaps:         []DiscoveryGap{},
		Worktree:     &DiscoveryWorktree{Path: "/tmp/checkout"},
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if cred.literal != token {
		t.Fatalf("credential fixture changed shape: literal = %q", cred.literal)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("report JSON carries the credential literal: %s", raw)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	for _, key := range []string{
		"repoId", "forge", "status", "worktree",
		"capabilities", "facts", "recommended", "gaps",
	} {
		if _, ok := top[key]; !ok {
			t.Errorf("report JSON is missing %q: %s", key, raw)
		}
	}
	for _, key := range []string{"error", "revision", "branch"} {
		if _, ok := top[key]; ok {
			t.Errorf("empty %q must be omitted from the report JSON: %s", key, raw)
		}
	}
	for _, key := range []string{"capabilities", "facts", "recommended", "gaps"} {
		if got := string(top[key]); got != "[]" {
			t.Errorf("%s = %s, want [] (report slices are non-nil)", key, got)
		}
	}

	// The worktree dirty flag is a tri-state: null means undetermined, which
	// differs from a known clean checkout.
	dirtyFalse, dirtyTrue := false, true
	for _, tc := range []struct {
		name  string
		dirty *bool
		want  string
	}{
		{name: "undetermined", dirty: nil, want: "null"},
		{name: "clean", dirty: &dirtyFalse, want: "false"},
		{name: "dirty", dirty: &dirtyTrue, want: "true"},
	} {
		t.Run("dirty "+tc.name, func(t *testing.T) {
			wt, err := json.Marshal(DiscoveryWorktree{
				Path:  "/tmp/checkout",
				Head:  "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
				Dirty: tc.dirty,
			})
			if err != nil {
				t.Fatalf("marshal worktree: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wt, &fields); err != nil {
				t.Fatalf("decode worktree: %v", err)
			}
			got, ok := fields["dirty"]
			if !ok {
				t.Fatalf("worktree JSON always carries dirty: %s", wt)
			}
			if string(got) != tc.want {
				t.Errorf("dirty = %s, want %s", got, tc.want)
			}
			if _, ok := fields["head"]; !ok {
				t.Errorf("worktree JSON is missing head: %s", wt)
			}
			if _, ok := fields["branch"]; ok {
				t.Errorf("detached HEAD with no branch must omit branch: %s", wt)
			}
		})
	}

	// Every nested record keeps the frozen field names; detail is always
	// present on a gap (it explains the state) and omitted elsewhere when empty.
	populated := DiscoveryReport{
		RepoID:   "repo-1",
		Forge:    "github",
		Status:   "unavailable",
		Error:    "forge unreachable",
		Revision: "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
		Branch:   "main",
		Worktree: &DiscoveryWorktree{
			Path:   "/tmp/checkout",
			Branch: "main",
			Head:   "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
		},
		Capabilities: []DiscoveryCapability{
			{Key: "repository_metadata", State: DiscoveryCapabilitySupported},
		},
		Facts: []DiscoveryFact{{
			Origin:  "detected",
			Section: "instructions",
			Kind:    "instructions",
			Value:   "AGENTS.md",
			Source:  "checkout:AGENTS.md",
		}},
		Recommended: []DiscoveryRecommendation{{
			Origin:  "recommended",
			Section: "environment",
			Kind:    "environment",
			Value:   "Define a development environment",
			Reason:  "no recognised environment definition was found",
		}},
		Gaps: []DiscoveryGap{{
			Key:     "ci_checks",
			Section: "ci",
			State:   DiscoveryCapabilityUnsupported,
		}},
	}

	raw, err = json.Marshal(populated)
	if err != nil {
		t.Fatalf("marshal populated report: %v", err)
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode populated report: %v", err)
	}
	wantTop := []string{
		"repoId", "forge", "status", "error", "revision", "branch", "worktree",
		"capabilities", "facts", "recommended", "gaps",
	}
	if len(top) != len(wantTop) {
		t.Errorf("top-level keys = %d (%s), want %d", len(top), raw, len(wantTop))
	}
	for _, key := range wantTop {
		if _, ok := top[key]; !ok {
			t.Errorf("populated report JSON is missing %q: %s", key, raw)
		}
	}

	assertKeys := func(name string, fields map[string]json.RawMessage, want ...string) {
		t.Helper()
		if len(fields) != len(want) {
			t.Errorf("%s keys = %d, want %d: %v", name, len(fields), len(want), fields)
		}
		for _, key := range want {
			if _, ok := fields[key]; !ok {
				t.Errorf("%s is missing %q", name, key)
			}
		}
	}
	var capabilities []map[string]json.RawMessage
	if err := json.Unmarshal(top["capabilities"], &capabilities); err != nil || len(capabilities) != 1 {
		t.Fatalf("decode capabilities: %v (%s)", err, top["capabilities"])
	}
	assertKeys("capability", capabilities[0], "key", "state")

	var facts []map[string]json.RawMessage
	if err := json.Unmarshal(top["facts"], &facts); err != nil || len(facts) != 1 {
		t.Fatalf("decode facts: %v (%s)", err, top["facts"])
	}
	assertKeys("fact", facts[0], "origin", "section", "kind", "value", "source")

	var recommended []map[string]json.RawMessage
	if err := json.Unmarshal(top["recommended"], &recommended); err != nil || len(recommended) != 1 {
		t.Fatalf("decode recommended: %v (%s)", err, top["recommended"])
	}
	assertKeys("recommendation", recommended[0], "origin", "section", "kind", "value", "reason")

	var gaps []map[string]json.RawMessage
	if err := json.Unmarshal(top["gaps"], &gaps); err != nil || len(gaps) != 1 {
		t.Fatalf("decode gaps: %v (%s)", err, top["gaps"])
	}
	assertKeys("gap", gaps[0], "key", "section", "state", "detail")
}
