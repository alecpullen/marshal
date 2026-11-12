package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseFactoryReport accepts only the final structured block for this effect.
// The caller is responsible for proving that the message came from this turn.
func ParseFactoryReport(text, operationID, role, baseSHA, headSHA string) (FactoryReport, error) {
	block, ok := lastJSONBlock(text)
	if !ok {
		return FactoryReport{}, fmt.Errorf("factory: missing final JSON report")
	}
	var wire struct {
		OperationID      string           `json:"operationId"`
		Outcome          string           `json:"outcome"`
		Summary          string           `json:"summary"`
		Question         string           `json:"question"`
		Reason           string           `json:"reason"`
		BaseSHA          string           `json:"baseSha"`
		HeadSHA          string           `json:"headSha"`
		InputsAccessible *bool            `json:"inputsAccessible"`
		Findings         *[]ReviewFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(block), &wire); err != nil {
		return FactoryReport{}, fmt.Errorf("factory: malformed report: %w", err)
	}
	if operationID == "" || wire.OperationID != operationID {
		return FactoryReport{}, fmt.Errorf("factory: report operation does not match")
	}
	if wire.Outcome != "complete" && wire.Outcome != "needs_input" {
		return FactoryReport{}, fmt.Errorf("factory: unknown report outcome")
	}
	if wire.Outcome == "needs_input" && (strings.TrimSpace(wire.Question) == "" || strings.TrimSpace(wire.Reason) == "") {
		return FactoryReport{}, fmt.Errorf("factory: needs_input requires question and reason")
	}
	if wire.Outcome == "complete" && strings.TrimSpace(wire.Summary) == "" {
		return FactoryReport{}, fmt.Errorf("factory: completed report requires summary")
	}
	if role != "implementer" && role != "reviewer" {
		return FactoryReport{}, fmt.Errorf("factory: unknown worker role")
	}
	if role == "reviewer" && wire.Outcome == "complete" {
		if wire.InputsAccessible == nil || !*wire.InputsAccessible || wire.Findings == nil || wire.BaseSHA != baseSHA || wire.HeadSHA != headSHA || baseSHA == "" || headSHA == "" {
			return FactoryReport{}, fmt.Errorf("factory: review evidence incomplete or mismatched")
		}
		for _, finding := range *wire.Findings {
			if strings.TrimSpace(finding.Title) == "" {
				return FactoryReport{}, fmt.Errorf("factory: review finding requires title")
			}
			switch finding.Severity {
			case "blocking", "should-fix", "nit":
			default:
				return FactoryReport{}, fmt.Errorf("factory: invalid review finding severity")
			}
		}
	}
	return FactoryReport{OperationID: wire.OperationID, Outcome: wire.Outcome, Summary: wire.Summary, Question: wire.Question, Reason: wire.Reason, BaseSHA: wire.BaseSHA, HeadSHA: wire.HeadSHA, InputsAccessible: wire.InputsAccessible != nil && *wire.InputsAccessible, Findings: func() []ReviewFinding {
		if wire.Findings == nil {
			return nil
		}
		return *wire.Findings
	}()}, nil
}

func BuildFactoryPrompt(item FactoryItem, effect FactoryEffect) (string, error) {
	if effect.ID == "" || (effect.Kind != "implement" && effect.Kind != "review") {
		return "", fmt.Errorf("factory: unsupported prompt effect")
	}
	if len(item.Approvals) == 0 {
		return "", fmt.Errorf("factory: missing approval")
	}
	a := item.Approvals[len(item.Approvals)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "Factory operation ID: %s\nTask: %s\nApproved plan:\n%s\nApproved spec:\n%s\n", effect.ID, item.Title, a.Plan, a.Spec)
	if effect.Kind == "review" {
		fmt.Fprintf(&b, "Review base SHA: %s\nCandidate head SHA: %s\nReviewer instructions:\n%s\nRead the diff and approved inputs. Use ask mode; do not edit files. Report inputsAccessible, baseSha, headSha, and findings (possibly []).\n", effect.ExpectedBase, effect.ExpectedHead, a.Config.ReviewerInstructions)
	} else {
		b.WriteString("Implement the approved scope, run relevant tests, and provide a concise engineering report. Do not push or merge. If blocked, return needs_input with a question and reason.\n")
		if effect.Round > 1 {
			var prev FactoryRound
			found := false
			for _, r := range item.Rounds {
				if r.Number == effect.Round-1 {
					prev = r
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("factory: missing previous round evidence")
			}
			fmt.Fprintf(&b, "Repair round %d from reviewed head %s. Diagnose and address the combined evidence below.\n", effect.Round, prev.HeadSHA)
			if prev.CI != nil {
				for _, check := range prev.CI.Checks {
					if check.Conclusion == "failure" || check.Conclusion == "error" {
						fmt.Fprintf(&b, "CI failure %s (%s): %s\n", check.Key.Name, check.URL, check.LogTail)
					}
				}
			}
			if prev.Review != nil {
				for _, finding := range prev.Review.Findings {
					fmt.Fprintf(&b, "Review finding [%s] %s %s:%d: %s\n", finding.Severity, finding.Title, finding.Path, finding.Line, finding.Body)
				}
			}
			b.WriteString("For failed checks, reproduce or investigate before changing code. If not reproducible or infrastructure is suspected, return needs_input with reason not_reproducible or infrastructure_suspected. Do not disable checks to claim success.\n")
		}
	}
	b.WriteString("Finish with a fenced JSON block containing operationId, outcome (complete or needs_input), summary, question, reason, baseSha, headSha, inputsAccessible, and findings.\n")
	return b.String(), nil
}
