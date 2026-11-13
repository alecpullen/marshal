package bridge

import (
	"strings"
	"testing"
)

func prBodyCheck(name, conclusion, url, log string) FactoryCheck {
	return FactoryCheck{Key: FactoryCheckKey{Kind: "check_run", Name: name}, SHA: "head", Status: "completed", Conclusion: conclusion, URL: url, LogTail: log}
}

func prBodyRound(narration *FactoryNarration) FactoryRound {
	r := FactoryRound{Number: 2, HeadSHA: "head", BaseSHA: "base"}
	r.CI = &FactoryCI{HeadSHA: "head", Complete: true, Checks: []FactoryCheck{prBodyCheck("build", "success", "", "")}}
	if narration != nil {
		r.Implementation = &FactoryReport{OperationID: "op", Outcome: "complete", Summary: "implemented", Narration: narration}
	} else {
		r.Implementation = &FactoryReport{OperationID: "op", Outcome: "complete", Summary: "implemented"}
	}
	return r
}

func fullNarration() *FactoryNarration {
	return &FactoryNarration{
		Issue:             "issue body",
		Approach:          "approach body",
		ExistingBehaviour: "existing body",
		Alternatives:      "alternatives body",
		Tradeoffs:         "tradeoffs body",
		Choice:            "choice body",
		Verification:      "verification body",
		Limitations:       "limitations body",
		Excerpts:          []FactoryExcerpt{{Path: "internal/app/x.go", Lang: "go", Code: "func A() {}"}},
	}
}

func TestFactoryPRBodyComposesCIThenNarration(t *testing.T) {
	body, err := composeFactoryPRBody(prBodyRound(fullNarration()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "## CI") {
		t.Fatalf("body does not lead with CI: %q", body)
	}
	headings := []string{"## CI", "## Issue", "## Approach", "## Existing behaviour", "## Alternatives and trade-offs", "## Implementation choice", "## Verification", "## Limitations", "## Selected code"}
	last := -1
	for _, h := range headings {
		at := strings.Index(body, h)
		if at < 0 {
			t.Fatalf("missing heading %q in:\n%s", h, body)
		}
		if at <= last {
			t.Fatalf("heading %q out of order in:\n%s", h, body)
		}
		last = at
	}
}

func TestFactoryPRBodyOmitsEmptySections(t *testing.T) {
	body, err := composeFactoryPRBody(prBodyRound(&FactoryNarration{Issue: "only issue", Approach: "only approach"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"## Issue", "## Approach"} {
		if !strings.Contains(body, h) {
			t.Fatalf("missing %q in:\n%s", h, body)
		}
	}
	for _, h := range []string{"## Existing behaviour", "## Alternatives and trade-offs", "## Implementation choice", "## Verification", "## Limitations", "## Selected code"} {
		if strings.Contains(body, h) {
			t.Fatalf("empty section %q rendered in:\n%s", h, body)
		}
	}
}

func TestFactoryPRBodyRendersExcerptsAsFencedBlocks(t *testing.T) {
	n := fullNarration()
	n.Excerpts = []FactoryExcerpt{
		{Path: "internal/app/x.go", Lang: "go", Code: "func A() {}"},
		{Path: ".gitignore", Code: "bin/"},
		{Path: "docs/note.md", Lang: "markdown", Code: "a ``` run"},
	}
	body, err := composeFactoryPRBody(prBodyRound(n))
	if err != nil {
		t.Fatal(err)
	}
	first := "```go\ninternal/app/x.go\nfunc A() {}\n```"
	second := "```\n.gitignore\nbin/\n```"
	third := "````markdown\ndocs/note.md\na ``` run\n````"
	at := strings.Index(body, first)
	if at < 0 {
		t.Fatalf("missing first excerpt block in:\n%s", body)
	}
	got := strings.Index(body, second)
	if got < at {
		t.Fatalf("excerpt order or bare fence wrong in:\n%s", body)
	}
	last := strings.Index(body, third)
	if last < got {
		t.Fatalf("computed fence or excerpt order wrong in:\n%s", body)
	}
}

// A language tag is only a fence tag, never a fence length, so a tag that
// carries its own fence must not close the block early.
func TestFactoryPRBodyFenceSurvivesAnUnvalidatedLanguage(t *testing.T) {
	n := fullNarration()
	n.Excerpts = []FactoryExcerpt{{Path: "internal/app/x.go", Lang: "go\n```", Code: "func A() {}"}}
	body, err := composeFactoryPRBody(prBodyRound(n))
	if err != nil {
		t.Fatal(err)
	}
	block := "````go\n```\ninternal/app/x.go\nfunc A() {}\n````"
	at := strings.Index(body, block)
	if at < 0 {
		t.Fatalf("excerpt is not one block fenced over its language:\n%s", body)
	}
	if strings.Contains(body, "\n```go\n") {
		t.Fatalf("the language closed the fence early:\n%s", body)
	}
	if strings.Contains(body[at+len(block):], "internal/app/x.go") {
		t.Fatalf("the excerpt reopened outside its block:\n%s", body)
	}
}

func TestFactoryPRBodyBucketsChecksByConclusion(t *testing.T) {
	failing := []struct{ name, conclusion, url, log string }{
		{"build", "failure", "https://ci.example/build", "compile failed"},
		{"unit", "error", "https://ci.example/unit", "panic: nil map"},
		{"integration", "cancelled", "https://ci.example/integration", "cancelled by user"},
		{"e2e", "timed_out", "https://ci.example/e2e", "timed out after 30m"},
		{"approval", "action_required", "https://ci.example/approval", "waiting for approval"},
	}
	checks := []FactoryCheck{prBodyCheck("ok", "success", "https://ci.example/ok", "")}
	for _, f := range failing {
		checks = append(checks, prBodyCheck(f.name, f.conclusion, f.url, f.log))
	}
	checks = append(checks,
		prBodyCheck("scan", "neutral", "https://ci.example/scan", ""),
		prBodyCheck("docs", "skipped", "https://ci.example/docs", ""),
		FactoryCheck{Key: FactoryCheckKey{Kind: "check_run", Name: "queued"}, SHA: "head", Status: "in_progress"},
	)
	r := prBodyRound(fullNarration())
	r.CI = &FactoryCI{HeadSHA: "head", Complete: true, Checks: checks}
	body, err := composeFactoryPRBody(r)
	if err != nil {
		t.Fatal(err)
	}
	open, closed := strings.Index(body, "<details>"), strings.Index(body, "</details>")
	if open < 0 || closed < open {
		t.Fatalf("missing the collapsed passing block in:\n%s", body)
	}
	collapsed := body[open:closed]
	if !strings.Contains(collapsed, "<summary>1 passing checks</summary>") || !strings.Contains(collapsed, "- ok: success") {
		t.Fatalf("the passing bucket is not the success check alone:\n%s", collapsed)
	}
	unpassed := body[:open] + body[closed:]
	for _, f := range failing {
		heading := "### " + f.name + ": " + f.conclusion
		for _, want := range []string{heading, f.url, f.log} {
			if !strings.Contains(unpassed, want) {
				t.Fatalf("failed check %s not expanded with %q in:\n%s", f.name, want, body)
			}
		}
	}
	for _, want := range []string{"- scan: neutral", "- docs: skipped", "- queued: in_progress"} {
		if !strings.Contains(unpassed, want) {
			t.Fatalf("unresolved check not listed with %q in:\n%s", want, body)
		}
	}
	for _, name := range []string{"build", "unit", "integration", "e2e", "approval", "scan", "docs", "queued"} {
		if strings.Contains(collapsed, name) {
			t.Fatalf("%s was counted as passing:\n%s", name, collapsed)
		}
	}
}

func TestFactoryPRBodyAlwaysCarriesCISection(t *testing.T) {
	cases := []struct {
		name string
		ci   *FactoryCI
		head string
		want []string
		deny []string
	}{
		{name: "missing evidence", ci: nil, want: []string{"## CI", "CI evidence is unavailable for this revision."}},
		{name: "other revision", ci: &FactoryCI{HeadSHA: "other", Complete: true}, want: []string{"CI evidence is unavailable for this revision."}},
		{name: "incomplete evidence", ci: &FactoryCI{HeadSHA: "head", Complete: false}, want: []string{"CI evidence is incomplete for this revision."}},
		{name: "no checks", ci: &FactoryCI{HeadSHA: "head", Complete: true}, want: []string{"CI evidence is complete with no reported checks."}},
		{
			name: "failing check",
			ci:   &FactoryCI{HeadSHA: "head", Complete: true, Checks: []FactoryCheck{prBodyCheck("build", "failure", "https://ci.example/1", "compile failed"), prBodyCheck("lint", "success", "", "")}},
			want: []string{"build", "failure", "https://ci.example/1", "compile failed", "<details><summary>1 passing checks</summary>"},
		},
		{
			name: "passing checks only",
			ci:   &FactoryCI{HeadSHA: "head", Complete: true, Checks: []FactoryCheck{prBodyCheck("build", "success", "", ""), prBodyCheck("lint", "success", "", "")}},
			want: []string{"<details><summary>2 passing checks</summary>", "- build: success", "- lint: success"},
			deny: []string{"### build"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := prBodyRound(fullNarration())
			r.CI = tc.ci
			body, err := composeFactoryPRBody(r)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, "## CI") {
				t.Fatalf("missing CI section in:\n%s", body)
			}
			want := tc.want
			if len(want) == 0 {
				want = []string{"CI evidence is unavailable for this revision."}
			}
			for _, s := range want {
				if !strings.Contains(body, s) {
					t.Fatalf("missing %q in:\n%s", s, body)
				}
			}
			for _, s := range tc.deny {
				if strings.Contains(body, s) {
					t.Fatalf("unexpected %q in:\n%s", s, body)
				}
			}
		})
	}
}

func TestFactoryPRBodyRendersTheGivenRound(t *testing.T) {
	older := prBodyRound(&FactoryNarration{Issue: "older issue"})
	newer := prBodyRound(&FactoryNarration{Issue: "newer issue"})
	body, err := composeFactoryPRBody(older)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "older issue") || strings.Contains(body, "newer issue") {
		t.Fatalf("composer did not render the round it was given:\n%s", body)
	}
	body, err = composeFactoryPRBody(newer)
	if err != nil || !strings.Contains(body, "newer issue") || strings.Contains(body, "older issue") {
		t.Fatalf("composer did not render the round it was given: %v\n%s", err, body)
	}
}

func TestFactoryPRBodyRefusesARoundWithoutNarration(t *testing.T) {
	r := prBodyRound(nil)
	if _, err := composeFactoryPRBody(r); err == nil {
		t.Fatal("round without a narration composed a body")
	}
	r.Implementation = nil
	if _, err := composeFactoryPRBody(r); err == nil {
		t.Fatal("round without an implementation report composed a body")
	}
}
