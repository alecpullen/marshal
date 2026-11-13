package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFactoryPromptCompletion(t *testing.T) {
	valid := "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"Implemented and tested\",\"narration\":{\"issue\":\"the issue\",\"approach\":\"the approach\"}}\n```"
	for _, tc := range []struct {
		name, text, role, base, head string
		ok                           bool
	}{
		{"implementation", valid, "implementer", "", "", true},
		{"wrong operation", strings.Replace(valid, "op-1", "op-old", 1), "implementer", "", "", false},
		{"missing summary", strings.Replace(valid, "Implemented and tested", "", 1), "implementer", "", "", false},
		{"needs input", "```json\n{\"operationId\":\"op-1\",\"outcome\":\"needs_input\",\"question\":\"What next?\",\"reason\":\"infrastructure_suspected\"}\n```", "implementer", "", "", true},
		{"malformed review", "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"reviewed\",\"baseSha\":\"base\",\"headSha\":\"head\",\"inputsAccessible\":true}\n```", "reviewer", "base", "head", false},
		{"review inaccessible", "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"reviewed\",\"baseSha\":\"base\",\"headSha\":\"head\",\"inputsAccessible\":false,\"findings\":[]}\n```", "reviewer", "base", "head", false},
		{"review complete", "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"reviewed\",\"baseSha\":\"base\",\"headSha\":\"head\",\"inputsAccessible\":true,\"findings\":[]}\n```", "reviewer", "base", "head", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFactoryReport(tc.text, "op-1", tc.role, tc.base, tc.head)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v, want success=%v", err, tc.ok)
			}
		})
	}
}

func TestFactoryPromptResultStopReason(t *testing.T) {
	for _, reason := range []string{"end_turn", "cancelled", "max_tokens", "unexpected"} {
		t.Run(reason, func(t *testing.T) {
			tr := &captureTransport{results: map[string]json.RawMessage{"session/prompt": json.RawMessage(`{"stopReason":"` + reason + `"}`)}}
			r := newStackTestRegistry(t, tr)
			r.track("s-1", AgentPath(t.TempDir()))
			got, err := r.PromptResult(context.Background(), "s-1", "hello")
			if err != nil || got.StopReason != reason {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if err := r.Prompt(context.Background(), "s-1", "ordinary caller"); err != nil {
				t.Fatalf("Prompt changed signature behavior: %v", err)
			}
		})
	}
}
func TestFactoryPromptDispatchedRepairUsesPreviousRound(t *testing.T) {
	i := loopItem()
	i.Rounds = []FactoryRound{{Number: 1, HeadSHA: "prior-head", CI: &FactoryCI{HeadSHA: "prior-head", Complete: true, Checks: []FactoryCheck{{Key: FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 1}, Conclusion: "failure", LogTail: "compile failed"}}}, Review: &FactoryReport{Findings: []ReviewFinding{{Title: "broken API", Severity: "blocking"}}}}}
	e := factoryNext(&i, "implement", "implementer", 2, time.Now())
	prompt, err := BuildFactoryPrompt(i, *e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "prior-head") || !strings.Contains(prompt, "compile failed") || !strings.Contains(prompt, "broken API") {
		t.Fatalf("dispatched repair lost evidence: %s", prompt)
	}
}

// narrationReportText builds a completed implementer report whose narration
// field is the given raw JSON, omitted when empty.
func narrationReportText(narration string) string {
	field := ""
	if narration != "" {
		field = `,"narration":` + narration
	}
	return "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"implemented\"" + field + "}\n```"
}

func narrationReportFor(t *testing.T, n *FactoryNarration) string {
	t.Helper()
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return narrationReportText(string(raw))
}

func excerptPathReport(t *testing.T, path string) string {
	t.Helper()
	quoted, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	return narrationReportText(`{"issue":"i","approach":"a","excerpts":[{"path":` + string(quoted) + `,"code":"x"}]}`)
}

func TestParseFactoryReportRequiresNarrationForCompletedImplementer(t *testing.T) {
	for _, tc := range []struct{ name, narration, want string }{
		{"missing", "", "narration"},
		{"null", "null", "narration"},
		{"empty object", "{}", "issue"},
		{"empty issue", `{"issue":"","approach":"a"}`, "issue"},
		{"empty approach", `{"issue":"i","approach":""}`, "approach"},
		{"whitespace approach", `{"issue":"i","approach":"  "}`, "approach"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFactoryReport(narrationReportText(tc.narration), "op-1", "implementer", "", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestParseFactoryReportNarrationRequirementByRole(t *testing.T) {
	review := "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"reviewed\",\"baseSha\":\"base\",\"headSha\":\"head\",\"inputsAccessible\":true,\"findings\":[]}\n```"
	if _, err := ParseFactoryReport(review, "op-1", "reviewer", "base", "head"); err != nil {
		t.Fatalf("reviewer report required a narration: %v", err)
	}
	needsInput := "```json\n{\"operationId\":\"op-1\",\"outcome\":\"needs_input\",\"question\":\"What next?\",\"reason\":\"infrastructure_suspected\"}\n```"
	if _, err := ParseFactoryReport(needsInput, "op-1", "implementer", "", ""); err != nil {
		t.Fatalf("implementer needs_input required a narration: %v", err)
	}
}

func TestParseFactoryReportRejectsInvalidExcerptPaths(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		ok         bool
	}{
		{"relative", "x.go", true},
		{"nested relative", "internal/app/x.go", true},
		{"dotfile", ".gitignore", true},
		{"dot directory", ".github/ci.yml", true},
		{"absolute", "/etc/passwd", false},
		{"leading slash", "/x.go", false},
		{"leading tilde", "~/x.go", false},
		{"parent", "../x.go", false},
		{"mid parent", "internal/../x.go", false},
		{"backslash", `internal\x.go`, false},
		{"windows drive", `C:\work\x.go`, false},
		{"forward drive", "C:/work/x.go", false},
		{"empty", "", false},
		{"double slash", "internal//x.go", false},
		{"dot segment", "./x.go", false},
		{"trailing slash", "internal/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFactoryReport(excerptPathReport(t, tc.path), "op-1", "implementer", "", "")
			if (err == nil) != tc.ok {
				t.Fatalf("path %q: err=%v, want accepted=%v", tc.path, err, tc.ok)
			}
		})
	}
}

func TestParseFactoryReportAcceptsNarrationAtBounds(t *testing.T) {
	section := strings.Repeat("a", factoryNarrationMaxSectionRunes)
	code := strings.Repeat("x", factoryNarrationMaxExcerptRunes)
	eight := make([]FactoryExcerpt, factoryNarrationMaxExcerpts)
	for n := range eight {
		eight[n] = FactoryExcerpt{Path: "x.go", Code: "x"}
	}
	full := &FactoryNarration{
		Issue: section, Approach: section, ExistingBehaviour: section,
		Alternatives: section, Tradeoffs: section, Choice: section,
		Verification: section, Limitations: section,
	}
	for _, tc := range []struct {
		name string
		n    *FactoryNarration
	}{
		{"section at bound", &FactoryNarration{Issue: section, Approach: "a"}},
		{"excerpt at bound", &FactoryNarration{Issue: "i", Approach: "a", Excerpts: []FactoryExcerpt{{Path: "x.go", Code: code}}}},
		{"excerpt count at bound", &FactoryNarration{Issue: "i", Approach: "a", Excerpts: eight}},
		{"total at bound", full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseFactoryReport(narrationReportFor(t, tc.n), "op-1", "implementer", "", ""); err != nil {
				t.Fatalf("narration at its bound rejected: %v", err)
			}
		})
	}
}

func TestParseFactoryReportRejectsOversizedNarration(t *testing.T) {
	section := strings.Repeat("a", factoryNarrationMaxSectionRunes)
	full := &FactoryNarration{
		Issue: section, Approach: section, ExistingBehaviour: section,
		Alternatives: section, Tradeoffs: section, Choice: section,
		Verification: section, Limitations: section,
	}
	excess := *full
	excess.Excerpts = []FactoryExcerpt{{Path: "x.go", Code: "x"}}
	excessSection := &FactoryNarration{Issue: strings.Repeat("a", factoryNarrationMaxSectionRunes+1), Approach: "a"}
	excessExcerpt := &FactoryNarration{Issue: "i", Approach: "a", Excerpts: []FactoryExcerpt{{Path: "x.go", Code: strings.Repeat("x", factoryNarrationMaxExcerptRunes+1)}}}
	tooMany := &FactoryNarration{Issue: "i", Approach: "a"}
	for n := 0; n < factoryNarrationMaxExcerpts+1; n++ {
		tooMany.Excerpts = append(tooMany.Excerpts, FactoryExcerpt{Path: "x.go", Code: "x"})
	}
	for _, tc := range []struct {
		name, want string
		n          *FactoryNarration
	}{
		{"section over bound", "issue", excessSection},
		{"excerpt over bound", "excerpt", excessExcerpt},
		{"too many excerpts", "excerpt", tooMany},
		{"total over bound", "narration total", &excess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFactoryReport(narrationReportFor(t, tc.n), "op-1", "implementer", "", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestFactoryPromptNamesNarrationFields(t *testing.T) {
	i := loopItem()
	implement, err := BuildFactoryPrompt(i, *factoryNext(&i, "implement", "implementer", 1, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"narration", "issue", "approach", "existingBehaviour", "alternatives", "tradeoffs", "choice", "verification", "limitations", "excerpts", "path", "lang", "code"} {
		if !strings.Contains(implement, field) {
			t.Fatalf("implementer prompt does not name %q: %s", field, implement)
		}
	}
	review, err := BuildFactoryPrompt(i, *factoryNext(&i, "review", "reviewer", 1, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(review, "narration") || strings.Contains(review, "existingBehaviour") {
		t.Fatalf("reviewer prompt asks for a narration: %s", review)
	}
}
