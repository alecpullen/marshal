package sdd

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDAGTaskStatusConstants(t *testing.T) {
	cases := []struct {
		name string
		want TaskStatus
	}{
		{"pending", TaskPending},
		{"in_progress", TaskInProgress},
		{"merged", TaskMerged},
		{"blocked", TaskBlocked},
	}
	for _, c := range cases {
		if string(c.want) != c.name {
			t.Fatalf("%s = %q, want %q", c.name, c.want, c.name)
		}
	}
}

func TestDAGJSONRoundTrip(t *testing.T) {
	dag := DAG{
		SpecPath: ".marshal/sdd/spec.md",
		Tasks: []DAGTask{
			{
				ID:         "T1",
				Title:      "Foundation",
				Status:     TaskPending,
				Deps:       []string{},
				Files:      []string{"internal/sdd/types.go"},
				Acceptance: []string{"go build ./internal/sdd/"},
			},
			{
				ID:     "T2",
				Title:  "Git ops",
				Status: TaskPending,
				Deps:   []string{"T1"},
			},
		},
	}
	data, err := json.Marshal(dag)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DAG
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, dag) {
		t.Fatalf("round trip = %#v, want %#v", got, dag)
	}
}

func TestRepoStateJSONRoundTrip(t *testing.T) {
	rs := RepoState{
		Branch:       "sdd/feature",
		TargetBranch: "main",
		Head:         "abc123",
		Merged:       []string{"T1"},
		MergedAt:     map[string]string{"T1": "2026-07-25T19:00:00Z"},
	}
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RepoState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, rs) {
		t.Fatalf("round trip = %#v, want %#v", got, rs)
	}
}

func TestCheckpointJSONRoundTrip(t *testing.T) {
	cp := Checkpoint{
		Tag:       "after-T1",
		SHA:       "abc123",
		Timestamp: "2026-07-25T19:00:00Z",
		Branch:    "sdd/feature",
		Merged:    []string{"T1"},
		MergedAt:  map[string]string{"T1": "2026-07-25T19:00:00Z"},
		Message:   "merged T1",
	}
	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Checkpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, cp) {
		t.Fatalf("round trip = %#v, want %#v", got, cp)
	}
}

func TestSpecStatusConstants(t *testing.T) {
	if string(SpecStatusDraft) != "draft" {
		t.Fatalf("draft = %q", SpecStatusDraft)
	}
	if string(SpecStatusApproved) != "approved" {
		t.Fatalf("approved = %q", SpecStatusApproved)
	}
}

func TestParseSpecFrontmatter(t *testing.T) {
	spec := `---
status: draft
source_plan: docs/plans/feature.md
target_branch: main
---

# Feature Spec

Body.
`
	got, err := ParseSpecFrontmatter(spec)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Status != string(SpecStatusDraft) {
		t.Errorf("Status = %q, want draft", got.Status)
	}
	if got.SourcePlan != "docs/plans/feature.md" {
		t.Errorf("SourcePlan = %q", got.SourcePlan)
	}
	if got.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q", got.TargetBranch)
	}
}

func TestParseSpecFrontmatterMissingFrontmatter(t *testing.T) {
	_, err := ParseSpecFrontmatter("no frontmatter here")
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
}

func TestReportStatusConstants(t *testing.T) {
	cases := []struct {
		name string
		want ReportStatus
	}{
		{"DONE", ReportDone},
		{"BLOCKED", ReportBlocked},
		{"NEEDS_CONTEXT", ReportNeedsContext},
		{"PASS", ReportPass},
		{"FAIL", ReportFail},
		{"SKIP", ReportSkip},
		{"NEEDS_HUMAN", ReportNeedsHuman},
		{"HEALTH_ALERT", ReportHealthAlert},
	}
	for _, c := range cases {
		if string(c.want) != c.name {
			t.Fatalf("%s = %q, want %q", c.name, c.want, c.name)
		}
	}
}
