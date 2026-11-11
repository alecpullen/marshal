package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFactoryPromptCompletion(t *testing.T) {
	valid := "```json\n{\"operationId\":\"op-1\",\"outcome\":\"complete\",\"summary\":\"Implemented and tested\"}\n```"
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
