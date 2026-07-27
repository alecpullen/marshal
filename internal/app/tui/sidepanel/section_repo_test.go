package sidepanel

import (
	"strings"
	"testing"

	"marshal/internal/app/tui/gitinfo"
)

func repoData() Data {
	return Data{
		Git: gitinfo.Info{InRepo: true, Branch: "main"},
		Repo: RepoStats{
			Files:   412,
			Symbols: 3100,
			Languages: []LanguageShare{
				{Name: "go", Share: 89},
				{Name: "md", Share: 7},
				{Name: "toml", Share: 4},
			},
		},
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
	for _, want := range []string{"main", "412", "3k", "go"} {
		if !strings.Contains(StripANSI(got), want) {
			t.Errorf("Render missing %q:\n%s", want, StripANSI(got))
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
	d.Git = gitinfo.Info{}
	got := StripANSI(strings.Join((RepoSection{}).Render(d, 30, 10), "\n"))
	if strings.Contains(got, "⎇") {
		t.Errorf("branch glyph rendered outside a repo:\n%s", got)
	}
	if !strings.Contains(got, "412") {
		t.Errorf("file count should still render outside a repo:\n%s", got)
	}
}
