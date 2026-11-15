package bridge

import (
	"fmt"
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

// The preview section's fixed strings, so a failure reads as a wording change
// rather than a mystery diff.
const (
	previewSectionHeading = "## Preview and test environments"
	previewUnreadable     = "Preview and test environments could not be read when this description was written."
	previewEmpty          = "No preview deployments or test environments are configured for this revision."
	previewScopeHead      = "Deployment records for `feature/x`, PR head `8e4c1b2` at write time."
	previewLinksHeading   = "Links reported by commit statuses:"
)

func previewOKReport() *PreviewReport {
	return &PreviewReport{RepoID: "repo-1", Forge: "github", Status: "ok", Ref: "feature/x", SHA: "8e4c1b2"}
}

// previewRound is a round whose CI evidence is complete for its own head and
// carries no checks, so a status-link case opts in by adding them. It carries a
// narration so the composer accepts it.
func previewRound() FactoryRound {
	r := FactoryRound{Number: 3, HeadSHA: "8e4c1b2"}
	r.CI = &FactoryCI{HeadSHA: "8e4c1b2", Complete: true}
	r.Implementation = &FactoryReport{OperationID: "op", Outcome: "complete", Summary: "implemented", Narration: &FactoryNarration{Issue: "issue body"}}
	return r
}

// assertSubstringOrder fails unless every want appears in body in the given
// order.
func assertSubstringOrder(t *testing.T, body string, wants ...string) {
	t.Helper()
	last := -1
	for _, want := range wants {
		at := strings.Index(body, want)
		if at < 0 {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
		if at <= last {
			t.Fatalf("%q out of order in:\n%s", want, body)
		}
		last = at
	}
}

func TestFactoryPRBodyComposesCIThenNarration(t *testing.T) {
	body, err := composeFactoryPRBody(prBodyRound(fullNarration()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "## CI") {
		t.Fatalf("body does not lead with CI: %q", body)
	}
	headings := []string{"## CI", "## Preview and test environments", "## Issue", "## Approach", "## Existing behaviour", "## Alternatives and trade-offs", "## Implementation choice", "## Verification", "## Limitations", "## Selected code"}
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
	body, err := composeFactoryPRBody(prBodyRound(&FactoryNarration{Issue: "only issue", Approach: "only approach"}), nil, "")
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
	body, err := composeFactoryPRBody(prBodyRound(n), nil, "")
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
	body, err := composeFactoryPRBody(prBodyRound(n), nil, "")
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
	body, err := composeFactoryPRBody(r, nil, "")
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
			body, err := composeFactoryPRBody(r, nil, "")
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
	body, err := composeFactoryPRBody(older, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "older issue") || strings.Contains(body, "newer issue") {
		t.Fatalf("composer did not render the round it was given:\n%s", body)
	}
	body, err = composeFactoryPRBody(newer, nil, "")
	if err != nil || !strings.Contains(body, "newer issue") || strings.Contains(body, "older issue") {
		t.Fatalf("composer did not render the round it was given: %v\n%s", err, body)
	}
}

func TestFactoryPRBodyRefusesARoundWithoutNarration(t *testing.T) {
	r := prBodyRound(nil)
	if _, err := composeFactoryPRBody(r, nil, ""); err == nil {
		t.Fatal("round without a narration composed a body")
	}
	r.Implementation = nil
	if _, err := composeFactoryPRBody(r, nil, ""); err == nil {
		t.Fatal("round without an implementation report composed a body")
	}
}

// The badge compares the provider's revision with the confirmed PR head by
// case-insensitive equality: no prefix matching, no ancestry, no distance.
func TestFactoryPreviewBodyBadgesAgainstTheHead(t *testing.T) {
	cases := []struct {
		name      string
		deploySHA string
		head      string
		row       string
		deny      []string
	}{
		{
			name:      "equal revisions match",
			deploySHA: "8e4c1b2",
			head:      "8e4c1b2",
			row:       "- `production` — `success` — `8e4c1b2` matches the PR head",
			deny:      []string{"not the PR head", "revision not reported by the provider"},
		},
		{
			name:      "different revisions do not match",
			deploySHA: "7a3f9c1",
			head:      "8e4c1b2",
			row:       "- `production` — `success` — `7a3f9c1` not the PR head",
			deny:      []string{"matches the PR head", "revision not reported by the provider"},
		},
		{
			name:      "a case difference still matches",
			deploySHA: "8E4C1B2",
			head:      "8e4c1b2",
			row:       "- `production` — `success` — `8E4C1B2` matches the PR head",
			deny:      []string{"not the PR head", "revision not reported by the provider"},
		},
		{
			name:      "no provider revision claims no equality",
			deploySHA: "",
			head:      "8e4c1b2",
			row:       "- `production` — `success` — revision not reported by the provider",
			deny:      []string{"matches the PR head", "not the PR head"},
		},
		{
			name:      "no head revision does not match",
			deploySHA: "7a3f9c1",
			head:      "",
			row:       "- `production` — `success` — `7a3f9c1` not the PR head",
			deny:      []string{"matches the PR head", "revision not reported by the provider"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := previewOKReport()
			report.Deployments = []PreviewDeployment{{ID: "1", Environment: "production", SHA: tc.deploySHA, State: "success"}}
			body := factoryPreviewBody(previewRound(), report, tc.head)
			if !strings.Contains(body, tc.row) {
				t.Fatalf("missing row %q in:\n%s", tc.row, body)
			}
			for _, s := range tc.deny {
				if strings.Contains(body, s) {
					t.Fatalf("unexpected %q in:\n%s", s, body)
				}
			}
		})
	}
	if got, want := factoryPreviewBadge("", "8e4c1b2"), "revision not reported by the provider"; got != want {
		t.Fatalf("factoryPreviewBadge with no provider revision = %q, want %q", got, want)
	}
}

func TestFactoryPreviewBodyRowsFollowTheRenderingContract(t *testing.T) {
	// Fields the report carries for fidelity and the description never renders.
	neverRendered := []string{"target.example.internal", "task-name-1", "orig-env-1", "deploy-id-1", "2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z", "true"}
	cases := []struct {
		name string
		dep  PreviewDeployment
		row  string
		deny []string
	}{
		{
			name: "a full row joins its segments with a spaced em dash",
			dep: PreviewDeployment{
				ID: "deploy-id-1", Environment: "production", OriginalEnvironment: "orig-env-1",
				Ref: "feature/x", SHA: "8e4c1b2", Task: "task-name-1", State: "success",
				EnvironmentURL: "https://preview.example.internal", TargetURL: "https://target.example.internal",
				LogURL: "https://ci.example/run/42/log", Description: "preview up",
				Transient: true, Production: true,
				CreatedAt: "2024-01-01T00:00:00Z", UpdatedAt: "2024-01-02T00:00:00Z",
			},
			row: "- `production` — `success` — `8e4c1b2` matches the PR head — https://preview.example.internal (logs: https://ci.example/run/42/log) — `preview up`",
		},
		{
			name: "an empty state states the provider reported none",
			dep:  PreviewDeployment{Environment: "staging", SHA: "7a3f9c1"},
			row:  "- `staging` — (no status reported by the provider) — `7a3f9c1` not the PR head",
		},
		{
			name: "an empty revision replaces the revision and badge together",
			dep:  PreviewDeployment{Environment: "qa", State: "pending"},
			row:  "- `qa` — `pending` — revision not reported by the provider",
		},
		{
			name: "no environment link drops the logs with it",
			dep:  PreviewDeployment{Environment: "production", State: "success", SHA: "8e4c1b2", LogURL: "https://ci.example/run/42/log"},
			row:  "- `production` — `success` — `8e4c1b2` matches the PR head",
			deny: []string{"https://ci.example/run/42/log", "(logs:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := previewOKReport()
			report.Deployments = []PreviewDeployment{tc.dep}
			body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
			if want := previewScopeHead + "\n\n" + tc.row; body != want {
				t.Fatalf("row mismatch:\ngot  %q\nwant %q", body, want)
			}
			for _, s := range append(append([]string(nil), neverRendered...), tc.deny...) {
				if strings.Contains(body, s) {
					t.Fatalf("%q rendered from a field the contract never renders:\n%s", s, body)
				}
			}
		})
	}
}

func TestFactoryPreviewBodyGroupsGapsByDetail(t *testing.T) {
	cases := []struct {
		name  string
		gaps  []DiscoveryGap
		lines []string
	}{
		{
			name: "one shared detail renders one line",
			gaps: []DiscoveryGap{
				{Key: "preview_environments", Section: "environments", State: DiscoveryCapabilityUnconfigured, Detail: "no token"},
				{Key: "preview_deployments", Section: "deployments", State: DiscoveryCapabilityUnconfigured, Detail: "no token"},
				{Key: "preview_statuses", Section: "statuses", State: DiscoveryCapabilityUnconfigured, Detail: "no token"},
			},
			lines: []string{"Not fully read — `preview_environments`, `preview_deployments`, `preview_statuses`: no token"},
		},
		{
			name: "keys join in frozen order whatever the report order",
			gaps: []DiscoveryGap{
				{Key: "preview_deployments", Detail: "no token"},
				{Key: "preview_environments", Detail: "no token"},
			},
			lines: []string{"Not fully read — `preview_environments`, `preview_deployments`: no token"},
		},
		{
			name: "distinct details render separate lines",
			gaps: []DiscoveryGap{
				{Key: "preview_environments", Detail: "environments unreadable"},
				{Key: "preview_deployments", Detail: "deployments unreadable"},
				{Key: "preview_statuses", Detail: "statuses unreadable"},
			},
			lines: []string{
				"Not fully read — `preview_environments`: environments unreadable",
				"Not fully read — `preview_deployments`: deployments unreadable",
				"Not fully read — `preview_statuses`: statuses unreadable",
			},
		},
		{
			name: "interleaved details keep each group's first key order",
			gaps: []DiscoveryGap{
				{Key: "preview_environments", Detail: "later"},
				{Key: "preview_deployments", Detail: "sooner"},
				{Key: "preview_statuses", Detail: "later"},
			},
			lines: []string{
				"Not fully read — `preview_environments`, `preview_statuses`: later",
				"Not fully read — `preview_deployments`: sooner",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := previewOKReport()
			report.Gaps = tc.gaps
			body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
			if want := previewScopeHead + "\n\n" + strings.Join(tc.lines, "\n"); body != want {
				t.Fatalf("gap lines mismatch:\ngot  %q\nwant %q", body, want)
			}
			if strings.Contains(body, previewEmpty) {
				t.Fatalf("a report with gaps rendered the empty line:\n%s", body)
			}
		})
	}
}

func TestFactoryPreviewBodyStatusLinksFilterSortAndGuard(t *testing.T) {
	round := previewRound()
	round.CI = &FactoryCI{HeadSHA: round.HeadSHA, Complete: true, Checks: []FactoryCheck{
		{Key: FactoryCheckKey{Kind: "check_run", Name: "alpha"}, ID: "0", SHA: round.HeadSHA, Conclusion: "success", URL: "https://ci.example/run/0"},
		{Key: FactoryCheckKey{Kind: "commit_status", Name: "deploy/preview"}, ID: "2", SHA: round.HeadSHA, Status: "pending", URL: "https://deploy.example/b"},
		{Key: FactoryCheckKey{Kind: "commit_status", Name: "deploy/preview"}, ID: "1", SHA: round.HeadSHA, Status: "completed", Conclusion: "success", URL: "https://deploy.example/a"},
		{Key: FactoryCheckKey{Kind: "commit_status", Name: "alpha"}, ID: "4", SHA: round.HeadSHA, Status: "in_progress", URL: "https://deploy.example/alpha"},
		{Key: FactoryCheckKey{Kind: "commit_status", Name: "unlinked"}, ID: "3", SHA: round.HeadSHA, Conclusion: "success"},
		{Key: FactoryCheckKey{Kind: "commit_status", Name: "zeta"}, ID: "5", SHA: round.HeadSHA, URL: "https://deploy.example/zeta"},
	}}
	body := factoryPreviewBody(round, previewOKReport(), "8e4c1b2")
	want := previewScopeHead + "\n\n" + previewEmpty + "\n\n" + previewLinksHeading + "\n\n" + strings.Join([]string{
		"- `alpha` (`in_progress`): https://deploy.example/alpha",
		"- `deploy/preview` (`success`): https://deploy.example/a",
		"- `deploy/preview` (`pending`): https://deploy.example/b",
		"- `zeta` (`pending`): https://deploy.example/zeta",
	}, "\n")
	if body != want {
		t.Fatalf("status-link body mismatch:\ngot  %q\nwant %q", body, want)
	}
	if strings.Contains(body, "https://ci.example/run/0") {
		t.Fatalf("a check run rendered as a status link:\n%s", body)
	}
	if strings.Contains(body, "unlinked") {
		t.Fatalf("a check without a URL rendered:\n%s", body)
	}

	guards := []struct {
		name string
		ci   *FactoryCI
	}{
		{name: "no CI evidence", ci: nil},
		{
			name: "CI evidence for another revision",
			ci: &FactoryCI{HeadSHA: "other", Complete: true, Checks: []FactoryCheck{
				{Key: FactoryCheckKey{Kind: "commit_status", Name: "deploy/preview"}, ID: "1", Status: "completed", Conclusion: "success", URL: "https://deploy.example/a"},
			}},
		},
	}
	for _, guard := range guards {
		t.Run(guard.name, func(t *testing.T) {
			r := previewRound()
			r.CI = guard.ci
			if links := factoryPreviewStatusLinks(r); len(links) != 0 {
				t.Fatalf("links reported without CI evidence for the round's head: %+v", links)
			}
			body := factoryPreviewBody(r, previewOKReport(), "8e4c1b2")
			if strings.Contains(body, previewLinksHeading) {
				t.Fatalf("status-link subsection rendered without CI evidence:\n%s", body)
			}
			if want := previewScopeHead + "\n\n" + previewEmpty; body != want {
				t.Fatalf("body mismatch:\ngot  %q\nwant %q", body, want)
			}
		})
	}
}

func TestFactoryPreviewBodyStatesEmptyAndUnreadable(t *testing.T) {
	t.Run("no report at all", func(t *testing.T) {
		if got := factoryPreviewBody(previewRound(), nil, "8e4c1b2"); got != previewUnreadable {
			t.Fatalf("unreadable body = %q, want %q", got, previewUnreadable)
		}
		body, err := composeFactoryPRBody(previewRound(), nil, "8e4c1b2")
		if err != nil {
			t.Fatal(err)
		}
		at := strings.Index(body, previewSectionHeading)
		if at < 0 {
			t.Fatalf("missing the preview heading in:\n%s", body)
		}
		if suffix := previewSectionHeading + "\n\n" + previewUnreadable + "\n"; !strings.HasPrefix(body[at:], suffix) {
			t.Fatalf("the unreadable section is not exactly the heading and the line:\n%s", body[at:])
		}
		if strings.Contains(body, "Deployment records for") {
			t.Fatalf("the unreadable section claimed a scope:\n%s", body)
		}
	})

	t.Run("an unavailable report", func(t *testing.T) {
		report := previewOKReport()
		report.Status = "unavailable"
		report.Error = "forge refused the read"
		body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
		if body != previewUnreadable {
			t.Fatalf("unavailable body = %q, want %q", body, previewUnreadable)
		}
		if strings.Contains(body, report.Error) {
			t.Fatalf("the report's internal error entered the body:\n%s", body)
		}
	})

	t.Run("nothing configured", func(t *testing.T) {
		body := factoryPreviewBody(previewRound(), previewOKReport(), "8e4c1b2")
		if want := previewScopeHead + "\n\n" + previewEmpty; body != want {
			t.Fatalf("empty body:\ngot  %q\nwant %q", body, want)
		}
	})

	t.Run("nothing configured but status links", func(t *testing.T) {
		r := previewRound()
		r.CI.Checks = []FactoryCheck{{Key: FactoryCheckKey{Kind: "commit_status", Name: "deploy/preview"}, ID: "1", SHA: r.HeadSHA, Conclusion: "success", URL: "https://preview.example.internal"}}
		body := factoryPreviewBody(r, previewOKReport(), "8e4c1b2")
		want := previewScopeHead + "\n\n" + previewEmpty + "\n\n" + previewLinksHeading + "\n\n- `deploy/preview` (`success`): https://preview.example.internal"
		if body != want {
			t.Fatalf("hybrid body:\ngot  %q\nwant %q", body, want)
		}
	})

	t.Run("gaps and nothing else", func(t *testing.T) {
		report := previewOKReport()
		report.Gaps = []DiscoveryGap{{Key: "preview_deployments", Section: "deployments", State: DiscoveryCapabilityTemporarilyUnavailable, Detail: "the forge timed out"}}
		body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
		if want := previewScopeHead + "\n\nNot fully read — `preview_deployments`: the forge timed out"; body != want {
			t.Fatalf("gaps-only body:\ngot  %q\nwant %q", body, want)
		}
		if strings.Contains(body, previewEmpty) {
			t.Fatalf("a report with gaps rendered the empty line:\n%s", body)
		}
	})
}

func TestFactoryPreviewBodyTruncatesAtTenRowsPerList(t *testing.T) {
	t.Run("deployments", func(t *testing.T) {
		report := previewOKReport()
		for i := 0; i < 11; i++ {
			report.Deployments = append(report.Deployments, PreviewDeployment{
				ID: fmt.Sprintf("id-%02d", i), Environment: fmt.Sprintf("deploy-%02d", i), SHA: "8e4c1b2", State: "success",
			})
		}
		body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
		if last := "- `deploy-09` — `success` — `8e4c1b2` matches the PR head"; !strings.Contains(body, last+"\n- …and 1 more") {
			t.Fatalf("missing the truncation line after the tenth row:\n%s", body)
		}
		if strings.Count(body, "- …and 1 more") != 1 {
			t.Fatalf("wrong truncation count:\n%s", body)
		}
		if strings.Contains(body, "deploy-10") {
			t.Fatalf("the eleventh deployment rendered:\n%s", body)
		}
		order := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			order = append(order, fmt.Sprintf("`deploy-%02d`", i))
		}
		assertSubstringOrder(t, body, order...)
	})

	t.Run("environments", func(t *testing.T) {
		report := previewOKReport()
		for i := 0; i < 11; i++ {
			report.Environments = append(report.Environments, PreviewEnvironment{
				Name: fmt.Sprintf("env-%02d", i), ID: fmt.Sprintf("env-id-%02d", i), HTMLURL: fmt.Sprintf("https://env.example/%02d", i),
			})
		}
		body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
		if last := "- `env-09` — https://env.example/09"; !strings.Contains(body, last+"\n- …and 1 more") {
			t.Fatalf("missing the truncation line after the tenth environment:\n%s", body)
		}
		if strings.Count(body, "- …and 1 more") != 1 {
			t.Fatalf("wrong truncation count:\n%s", body)
		}
		if strings.Contains(body, "env-10") {
			t.Fatalf("the eleventh environment rendered:\n%s", body)
		}
		if !strings.Contains(body, "Configured environments:\n\n- `env-00` — https://env.example/00") {
			t.Fatalf("the environment block is not headed and ordered as reported:\n%s", body)
		}
		order := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			order = append(order, fmt.Sprintf("`env-%02d`", i))
		}
		assertSubstringOrder(t, body, order...)
	})

	t.Run("status links", func(t *testing.T) {
		r := previewRound()
		for i := 0; i < 11; i++ {
			r.CI.Checks = append(r.CI.Checks, FactoryCheck{
				Key:        FactoryCheckKey{Kind: "commit_status", Name: fmt.Sprintf("link-%02d", i)},
				ID:         fmt.Sprintf("%02d", i),
				SHA:        r.HeadSHA,
				Conclusion: "success",
				URL:        fmt.Sprintf("https://link.example/%02d", i),
			})
		}
		body := factoryPreviewBody(r, previewOKReport(), "8e4c1b2")
		if last := "- `link-09` (`success`): https://link.example/09"; !strings.Contains(body, last+"\n- …and 1 more") {
			t.Fatalf("missing the truncation line after the tenth link:\n%s", body)
		}
		if strings.Count(body, "- …and 1 more") != 1 {
			t.Fatalf("wrong truncation count:\n%s", body)
		}
		if strings.Contains(body, "link-10") {
			t.Fatalf("the eleventh status link rendered:\n%s", body)
		}
		order := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			order = append(order, fmt.Sprintf("`link-%02d`", i))
		}
		assertSubstringOrder(t, body, order...)
	})
}

func TestFactoryPreviewBodyProtectsProviderStrings(t *testing.T) {
	cases := []struct {
		name string
		dep  PreviewDeployment
		row  string
	}{
		{
			name: "a newline in the state becomes one space",
			dep:  PreviewDeployment{Environment: "staging", State: "queued\ndraft", SHA: "8e4c1b2"},
			row:  "- `staging` — `queued draft` — `8e4c1b2` matches the PR head",
		},
		{
			name: "a backtick run is one shorter than the wrap",
			dep:  PreviewDeployment{Environment: "staging", State: "a `b` c", SHA: "8e4c1b2"},
			row:  "- `staging` — ``a `b` c`` — `8e4c1b2` matches the PR head",
		},
		{
			name: "a double run widens the wrap again",
			dep:  PreviewDeployment{Environment: "staging", State: "x``y", SHA: "8e4c1b2"},
			row:  "- `staging` — ```x``y``` — `8e4c1b2` matches the PR head",
		},
		{
			name: "a value that begins and ends with a backtick is padded",
			dep:  PreviewDeployment{Environment: "`staging`", State: "success", SHA: "8e4c1b2"},
			row:  "- `` `staging` `` — `success` — `8e4c1b2` matches the PR head",
		},
		{
			name: "a newline in the description becomes one space",
			dep:  PreviewDeployment{Environment: "staging", State: "success", SHA: "8e4c1b2", Description: "line one\r\nline two"},
			row:  "- `staging` — `success` — `8e4c1b2` matches the PR head — `line one line two`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := previewOKReport()
			report.Deployments = []PreviewDeployment{tc.dep}
			body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
			if want := previewScopeHead + "\n\n" + tc.row; body != want {
				t.Fatalf("protected row mismatch:\ngot  %q\nwant %q", body, want)
			}
		})
	}

	t.Run("URLs render bare and never as markdown links", func(t *testing.T) {
		report := previewOKReport()
		report.Deployments = []PreviewDeployment{{
			Environment: "production", State: "success", SHA: "8e4c1b2",
			EnvironmentURL: "https://preview.example/a?x=1&y=2", LogURL: "https://ci.example/log/1",
		}}
		body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
		want := previewScopeHead + "\n\n- `production` — `success` — `8e4c1b2` matches the PR head — https://preview.example/a?x=1&y=2 (logs: https://ci.example/log/1)"
		if body != want {
			t.Fatalf("URL row mismatch:\ngot  %q\nwant %q", body, want)
		}
		if strings.Contains(body, "](") || strings.Contains(body, "<https://") {
			t.Fatalf("a URL rendered as link syntax:\n%s", body)
		}
	})

	t.Run("link syntax in provider text stays inside its span", func(t *testing.T) {
		report := previewOKReport()
		report.Deployments = []PreviewDeployment{{
			Environment: "production", State: "success", SHA: "8e4c1b2", Description: "[docs](https://evil.example)",
		}}
		body := factoryPreviewBody(previewRound(), report, "8e4c1b2")
		if !strings.Contains(body, "— `[docs](https://evil.example)`") {
			t.Fatalf("provider text escaped its span:\n%s", body)
		}
	})

	t.Run("factoryInlineCode protects a span over its own runs", func(t *testing.T) {
		cases := map[string]string{
			"plain":      "`plain`",
			"one\r\ntwo": "`one two`",
			"a ``` b":    "````a ``` b````",
			"`padded`":   "`` `padded` ``",
		}
		for in, want := range cases {
			if got := factoryInlineCode(in); got != want {
				t.Fatalf("factoryInlineCode(%q) = %q, want %q", in, got, want)
			}
		}
	})
}
