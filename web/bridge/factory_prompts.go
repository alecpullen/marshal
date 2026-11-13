package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The narration bounds are fixed, so an oversized report fails at parse time
// rather than at publish time.
const (
	// factoryNarrationMaxExcerpts is how many excerpts one narration may
	// carry. The narration is a selection, not a diff.
	factoryNarrationMaxExcerpts = 8
	// factoryNarrationMaxSectionRunes bounds each narration section.
	factoryNarrationMaxSectionRunes = 8_000
	// factoryNarrationMaxExcerptRunes bounds one excerpt's code.
	factoryNarrationMaxExcerptRunes = 4_000
	// factoryNarrationMaxTotalRunes bounds the whole narration.
	factoryNarrationMaxTotalRunes = 64_000
)

// ParseFactoryReport accepts only the final structured block for this effect.
// The caller is responsible for proving that the message came from this turn.
func ParseFactoryReport(text, operationID, role, baseSHA, headSHA string) (FactoryReport, error) {
	block, ok := lastJSONBlock(text)
	if !ok {
		return FactoryReport{}, fmt.Errorf("factory: missing final JSON report")
	}
	var wire struct {
		OperationID      string            `json:"operationId"`
		Outcome          string            `json:"outcome"`
		Summary          string            `json:"summary"`
		Question         string            `json:"question"`
		Reason           string            `json:"reason"`
		BaseSHA          string            `json:"baseSha"`
		HeadSHA          string            `json:"headSha"`
		InputsAccessible *bool             `json:"inputsAccessible"`
		Findings         *[]ReviewFinding  `json:"findings"`
		Narration        *FactoryNarration `json:"narration"`
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
	// Placed after the reviewer block so a reviewer report is never rejected
	// for a missing narration.
	if role == "implementer" && wire.Outcome == "complete" {
		if wire.Narration == nil {
			return FactoryReport{}, fmt.Errorf("factory: completed implementer report requires a narration")
		}
		if strings.TrimSpace(wire.Narration.Issue) == "" {
			return FactoryReport{}, fmt.Errorf("factory: narration issue is required")
		}
		if strings.TrimSpace(wire.Narration.Approach) == "" {
			return FactoryReport{}, fmt.Errorf("factory: narration approach is required")
		}
	}
	if wire.Narration != nil {
		if err := validateFactoryNarration(wire.Narration); err != nil {
			return FactoryReport{}, err
		}
	}
	return FactoryReport{OperationID: wire.OperationID, Outcome: wire.Outcome, Summary: wire.Summary, Question: wire.Question, Reason: wire.Reason, BaseSHA: wire.BaseSHA, HeadSHA: wire.HeadSHA, InputsAccessible: wire.InputsAccessible != nil && *wire.InputsAccessible, Findings: func() []ReviewFinding {
		if wire.Findings == nil {
			return nil
		}
		return *wire.Findings
	}(), Narration: wire.Narration}, nil
}

// validateFactoryNarration enforces the fixed bounds and the
// repository-relative excerpt path rule at parse time, so a malformed
// narration never reaches the store, the journal or the pull request.
func validateFactoryNarration(n *FactoryNarration) error {
	if len(n.Excerpts) > factoryNarrationMaxExcerpts {
		return fmt.Errorf("factory: narration excerpt count %d exceeds %d", len(n.Excerpts), factoryNarrationMaxExcerpts)
	}
	for _, section := range []struct{ name, body string }{
		{"issue", n.Issue},
		{"approach", n.Approach},
		{"existingBehaviour", n.ExistingBehaviour},
		{"alternatives", n.Alternatives},
		{"tradeoffs", n.Tradeoffs},
		{"choice", n.Choice},
		{"verification", n.Verification},
		{"limitations", n.Limitations},
	} {
		runes := len([]rune(section.body))
		if runes > factoryNarrationMaxSectionRunes {
			return fmt.Errorf("factory: narration %s is %d runes, at most %d allowed", section.name, runes, factoryNarrationMaxSectionRunes)
		}
	}
	total := 0
	for i, excerpt := range n.Excerpts {
		if !factoryNarrationPathOK(excerpt.Path) {
			return fmt.Errorf("factory: narration excerpt %d path %q is not repository-relative", i, excerpt.Path)
		}
		runes := len([]rune(excerpt.Code))
		if runes > factoryNarrationMaxExcerptRunes {
			return fmt.Errorf("factory: narration excerpt %d code is %d runes, at most %d allowed", i, runes, factoryNarrationMaxExcerptRunes)
		}
		total += runes
	}
	total += len([]rune(n.Issue)) + len([]rune(n.Approach)) + len([]rune(n.ExistingBehaviour)) + len([]rune(n.Alternatives)) + len([]rune(n.Tradeoffs)) + len([]rune(n.Choice)) + len([]rune(n.Verification)) + len([]rune(n.Limitations))
	if total > factoryNarrationMaxTotalRunes {
		return fmt.Errorf("factory: narration total is %d runes, at most %d allowed", total, factoryNarrationMaxTotalRunes)
	}
	return nil
}

// factoryNarrationPathOK accepts a repository-relative path. A dot-prefixed
// name such as .gitignore is a name, not a dot segment.
func factoryNarrationPathOK(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") || strings.Contains(path, `\`) {
		return false
	}
	if len(path) >= 2 && path[1] == ':' {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
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
		b.WriteString("Narrate the change for the pull request description: the issue and the proposed solution, the approach, the existing behaviour it changes, the alternatives and trade-offs, the implementation choice, how you verified it, and its limitations — with optional code excerpts (path, lang, code).\n")
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
	if effect.Kind == "implement" {
		b.WriteString("A complete report must also include narration with issue, approach, existingBehaviour, alternatives, tradeoffs, choice, verification, limitations, and excerpts, each excerpt carrying path, lang and code.\n")
	}
	return b.String(), nil
}
