package sessionsheet

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
)

func repoData() Data {
	return Data{
		Git:  acpwire.Git{InRepo: true, Branch: "main"},
		Repo: acpwire.SheetRepo{Files: 412, Symbols: 3100},
	}
}

func TestRepoSectionAlwaysRelevant(t *testing.T) {
	if !(RepoSection{}).Relevant(Data{}) {
		t.Error("Relevant(zero Data) = false, want true")
	}
}

func TestRepoSectionIdentity(t *testing.T) {
	s := RepoSection{}
	if s.ID() != "repo" {
		t.Errorf("ID = %q, want repo", s.ID())
	}
	if s.Priority() != 5 {
		t.Errorf("Priority = %d, want 5", s.Priority())
	}
	if s.Clippable() {
		t.Error("Clippable = true, want false (fixed row set)")
	}
}

func TestRepoSectionRender(t *testing.T) {
	got := strings.Join((RepoSection{}).Render(repoData(), 30, 10), "\n")
	for _, want := range []string{"main", "412", "3k"} {
		if !strings.Contains(StripANSI(got), want) {
			t.Errorf("Render missing %q:\n%s", want, StripANSI(got))
		}
	}
}

// The sandbox and loaded-tool rows describe a live session, so they render
// only when the sheet carries one.
func TestRepoSectionShowsTheSandboxForASession(t *testing.T) {
	d := repoData()
	d.StartedAt = time.Unix(100, 0)
	d.Sandbox = acpwire.SheetSandbox{Backend: "restricted", AllowNetwork: true}
	d.LoadedTools = 3

	got := StripANSI(strings.Join((RepoSection{}).Render(d, 40, 10), "\n"))
	for _, want := range []string{"sandbox restricted · net ✓", "3 tools loaded"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}

	d.StartedAt = time.Time{}
	got = StripANSI(strings.Join((RepoSection{}).Render(d, 40, 10), "\n"))
	for _, unwanted := range []string{"sandbox", "tools loaded"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Render carries %q with no session:\n%s", unwanted, got)
		}
	}
}

func TestRepoSectionRespectsMaxRows(t *testing.T) {
	if got := (RepoSection{}).Render(repoData(), 30, 2); len(got) > 2 {
		t.Errorf("got %d rows, want at most 2", len(got))
	}
}

func TestRepoSectionOneLine(t *testing.T) {
	got := StripANSI((RepoSection{}).OneLine(repoData(), 40))
	if !strings.Contains(got, "main") || !strings.Contains(got, "412") {
		t.Errorf("OneLine = %q, want branch and file count", got)
	}
}

func TestRepoSectionOutsideRepo(t *testing.T) {
	d := repoData()
	d.Git = acpwire.Git{}
	got := StripANSI(strings.Join((RepoSection{}).Render(d, 30, 10), "\n"))
	if strings.Contains(got, "⎇") {
		t.Errorf("branch glyph rendered outside a repo:\n%s", got)
	}
	if !strings.Contains(got, "412") {
		t.Errorf("file count should still render outside a repo:\n%s", got)
	}
}
