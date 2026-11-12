package bridge

import (
	"fmt"
	"strings"
)

// composeFactoryPRBody renders one round's CI evidence and narration as the
// pull request description. The composer renders the round it is given;
// choosing the newest round is the effect's job.
func composeFactoryPRBody(round FactoryRound) (string, error) {
	var narration *FactoryNarration
	if round.Implementation != nil {
		narration = round.Implementation.Narration
	}
	if narration == nil {
		return "", fmt.Errorf("factory: round %d carries no narration", round.Number)
	}
	var b strings.Builder
	b.WriteString("## CI\n\n")
	b.WriteString(factoryCIBody(round))
	b.WriteString("\n")
	section := func(heading, body string) {
		if body == "" {
			return
		}
		fmt.Fprintf(&b, "\n%s\n\n%s\n", heading, body)
	}
	section("## Issue", narration.Issue)
	section("## Approach", narration.Approach)
	section("## Existing behaviour", narration.ExistingBehaviour)
	if narration.Alternatives != "" || narration.Tradeoffs != "" {
		b.WriteString("\n## Alternatives and trade-offs\n")
		for _, paragraph := range []string{narration.Alternatives, narration.Tradeoffs} {
			if paragraph == "" {
				continue
			}
			fmt.Fprintf(&b, "\n%s\n", paragraph)
		}
	}
	section("## Implementation choice", narration.Choice)
	section("## Verification", narration.Verification)
	section("## Limitations", narration.Limitations)
	if len(narration.Excerpts) > 0 {
		b.WriteString("\n## Selected code\n")
		for _, excerpt := range narration.Excerpts {
			fence := codeFence(excerpt.Path + "\n" + excerpt.Code)
			fmt.Fprintf(&b, "\n%s%s\n%s\n%s\n%s\n", fence, excerpt.Lang, excerpt.Path, excerpt.Code, fence)
		}
	}
	return b.String(), nil
}

// factoryCIBody states CI evidence rather than omitting it: a revision whose
// CI is unavailable, incomplete or checkless says so in the body.
func factoryCIBody(round FactoryRound) string {
	if round.CI == nil || round.CI.HeadSHA != round.HeadSHA {
		return "CI evidence is unavailable for this revision."
	}
	if !round.CI.Complete {
		return "CI evidence is incomplete for this revision."
	}
	if len(round.CI.Checks) == 0 {
		return "CI evidence is complete with no reported checks."
	}
	var failing, passing []FactoryCheck
	for _, check := range round.CI.Checks {
		if check.Conclusion == "failure" || check.Conclusion == "error" {
			failing = append(failing, check)
			continue
		}
		passing = append(passing, check)
	}
	var b strings.Builder
	for _, check := range failing {
		fmt.Fprintf(&b, "### %s: %s\n", check.Key.Name, check.Conclusion)
		if check.URL != "" {
			fmt.Fprintf(&b, "%s\n", check.URL)
		}
		if check.LogTail != "" {
			fence := codeFence(check.LogTail)
			fmt.Fprintf(&b, "\n%s\n%s\n%s\n", fence, check.LogTail, fence)
		}
		b.WriteString("\n")
	}
	if len(passing) > 0 {
		fmt.Fprintf(&b, "<details><summary>%d passing checks</summary>\n\n", len(passing))
		for _, check := range passing {
			status := check.Status
			if check.Conclusion != "" {
				status = check.Conclusion
			}
			fmt.Fprintf(&b, "- %s: %s\n", check.Key.Name, status)
		}
		b.WriteString("\n</details>\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// codeFence is one backtick longer than the longest backtick run in the
// content, so content that carries its own fence cannot close the block
// early. It is never shorter than a triple backtick.
func codeFence(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}
