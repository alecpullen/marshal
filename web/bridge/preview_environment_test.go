package bridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestPreviewCapabilityOrderIsStable pins the preview surface's own frozen key
// list: three keys, always in this order, and an accessor that hands back a
// copy so a caller cannot reorder the contract for the whole process. It also
// pins that the discovery report's ten keys are untouched by this surface.
func TestPreviewCapabilityOrderIsStable(t *testing.T) {
	want := []string{"preview_environments", "preview_deployments", "preview_statuses"}
	got := previewCapabilityKeys()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("previewCapabilityKeys() = %v, want %v", got, want)
	}

	// The accessor returns a copy: mutating it must not change the contract.
	got[0] = "reordered"
	got[1] = "by"
	got[2] = "a caller"
	if again := previewCapabilityKeys(); !reflect.DeepEqual(again, want) {
		t.Fatalf("a caller reordered the frozen list: previewCapabilityKeys() = %v, want %v", again, want)
	}

	// The discovery report's key list belongs to that contract, not this one.
	discoveryWant := []string{
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
	if discoveryGot := discoveryCapabilityKeys(); !reflect.DeepEqual(discoveryGot, discoveryWant) {
		t.Errorf("discoveryCapabilityKeys() = %v, want the frozen ten keys untouched", discoveryGot)
	}
}

// TestPreviewContractJSONUsesStableShape pins the wire shape of the report: the
// field names of the HTTP and JSON contract, every array present and empty
// rather than null, and an error field only when the status is unavailable.
func TestPreviewContractJSONUsesStableShape(t *testing.T) {
	report := PreviewReport{
		RepoID:       "repo-1",
		Forge:        "github",
		Status:       discoveryStatusOK,
		Capabilities: []DiscoveryCapability{},
		Environments: []PreviewEnvironment{},
		Deployments:  []PreviewDeployment{},
		Gaps:         []DiscoveryGap{},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode report %s: %v", raw, err)
	}

	for _, key := range []string{
		"repoId", "forge", "status", "ref", "sha",
		"capabilities", "environments", "deployments", "gaps",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("report JSON is missing %q: %s", key, raw)
		}
	}
	if _, ok := decoded["error"]; ok {
		t.Errorf("report JSON carries error for status %q, want it omitted: %s", report.Status, raw)
	}
	for _, key := range []string{"capabilities", "environments", "deployments", "gaps"} {
		if got := string(decoded[key]); got != "[]" {
			t.Errorf("%s = %s, want [] (an empty array, never null)", key, got)
		}
	}
	if got := string(decoded["ref"]); got != `""` {
		t.Errorf("ref = %s, want an empty string when no scope resolved", got)
	}
	if got := string(decoded["sha"]); got != `""` {
		t.Errorf("sha = %s, want an empty string when no scope resolved", got)
	}

	// An outage is the only state that carries a top-level error.
	report.Status = discoveryStatusUnavailable
	report.Error = "the declared github forge could not be reached"
	raw, err = json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal unavailable report: %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode unavailable report %s: %v", raw, err)
	}
	if got := string(decoded["error"]); got == "" || got == "null" {
		t.Errorf("error = %s, want the outage reason present when status is unavailable", got)
	}

	// The environment and deployment rows use exactly the contract's names.
	environment := PreviewEnvironment{
		Name:                "production",
		ID:                  "1234567",
		HTMLURL:             "https://github.com/o/r/deployments/activity_log",
		BranchPolicy:        "protected_branches",
		ProtectionRuleCount: 2,
	}
	assertPreviewJSONKeys(t, "environment", environment, []string{
		"name", "id", "htmlUrl", "branchPolicy", "protectionRuleCount",
	})
	deployment := PreviewDeployment{
		ID:                  "99",
		Environment:         "production",
		OriginalEnvironment: "",
		Ref:                 "feature/x",
		SHA:                 "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
		Task:                "deploy",
		State:               "success",
		EnvironmentURL:      "https://preview.example.internal",
		TargetURL:           "https://ci.example/run/42",
		LogURL:              "https://ci.example/run/42/log",
		Description:         "deployed 8e4c1b2",
		CreatedAt:           "2026-11-12T09:00:00Z",
		UpdatedAt:           "2026-11-12T09:01:12Z",
	}
	assertPreviewJSONKeys(t, "deployment", deployment, []string{
		"id", "environment", "originalEnvironment", "ref", "sha", "task", "state",
		"environmentUrl", "targetUrl", "logUrl", "description", "transient",
		"production", "createdAt", "updatedAt",
	})
}

// assertPreviewJSONKeys pins one row's exact field names.
func assertPreviewJSONKeys(t *testing.T, what string, value any, want []string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", what, err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s %s: %v", what, raw, err)
	}
	got := make([]string, 0, len(decoded))
	for key := range decoded {
		got = append(got, key)
	}
	if len(got) != len(want) {
		t.Fatalf("%s fields = %v, want %v", what, got, want)
	}
	for _, key := range want {
		if _, ok := decoded[key]; !ok {
			t.Errorf("%s JSON is missing %q: %s", what, key, raw)
		}
	}
}

// TestPreviewFixRulesMatchTheContract pins each state's fix text: the four the
// specification fixes, an empty fix for a supported capability, and an
// unsupported fix that names the instance rather than promising an adapter.
func TestPreviewFixRulesMatchTheContract(t *testing.T) {
	cases := []struct {
		state DiscoveryCapabilityState
		want  string
	}{
		{
			DiscoveryCapabilityUnconfigured,
			"this repository needs a personal access token credential to read preview environments; register or repair one and retry",
		},
		{
			DiscoveryCapabilityInsufficientPermission,
			"grant the credential read access to the repository's deployments and environments, and retry",
		},
		{
			DiscoveryCapabilityTemporarilyUnavailable,
			"the forge could not answer right now; retry later",
		},
		{
			DiscoveryCapabilityUnsupported,
			"this forge instance does not expose this resource; no adapter can read what the instance does not serve",
		},
		{
			DiscoveryCapabilitySupported,
			"",
		},
	}
	for _, tc := range cases {
		if got := previewFix(tc.state, true); got != tc.want {
			t.Errorf("previewFix(%q, forge declared) = %q, want %q", tc.state, got, tc.want)
		}
	}

	// The unsupported fix is an instance fact. It must never repeat the
	// discovery surface's promise that adapter support is what is missing:
	// here the adapter exists and the instance answered.
	unsupported := previewFix(DiscoveryCapabilityUnsupported, true)
	if !strings.Contains(unsupported, "instance") {
		t.Errorf("unsupported fix = %q, want it to name the instance", unsupported)
	}
	if strings.Contains(unsupported, "adapter support would be needed") {
		t.Errorf("unsupported fix = %q, want no adapter-support wording", unsupported)
	}

	// With no forge declared there is no instance to blame: the fix is the
	// missing declaration, exactly as the discovery surface words it.
	if got := previewFix(DiscoveryCapabilityUnsupported, false); got != "declare github or gitea for this repository" {
		t.Errorf("previewFix(unsupported, no forge declared) = %q, want the missing declaration", got)
	}
}

// TestPreviewGapDetailJoinsCauseAndFix pins the gap detail shape: the cause
// followed by the fix, and no duplicate when the cause already carries it.
func TestPreviewGapDetailJoinsCauseAndFix(t *testing.T) {
	cause := "the credential lacks permission to read this resource (HTTP 403)"
	fix := previewFix(DiscoveryCapabilityInsufficientPermission, true)
	want := cause + "; " + fix
	if got := previewGapDetail(cause, DiscoveryCapabilityInsufficientPermission, true); got != want {
		t.Fatalf("previewGapDetail(cause) = %q, want %q", got, want)
	}

	// A detail that already carries the fix is not given it twice.
	if got := previewGapDetail(want, DiscoveryCapabilityInsufficientPermission, true); got != want {
		t.Errorf("previewGapDetail(joined) = %q, want %q (the fix is not repeated)", got, want)
	}

	// An empty cause is just the fix.
	if got := previewGapDetail("", DiscoveryCapabilityUnconfigured, true); got != previewFix(DiscoveryCapabilityUnconfigured, true) {
		t.Errorf("previewGapDetail(no cause) = %q, want the fix alone", got)
	}

	// A supported capability has no fix, so the cause stands alone.
	if got := previewGapDetail(cause, DiscoveryCapabilitySupported, true); got != cause {
		t.Errorf("previewGapDetail(supported) = %q, want %q", got, cause)
	}
}
