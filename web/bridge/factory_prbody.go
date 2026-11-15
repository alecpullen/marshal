package bridge

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// composeFactoryPRBody renders one round's CI evidence, the preview and test
// environment section, and the narration as the pull request description. The
// composer renders the round it is given; choosing the newest round is the
// effect's job.
func composeFactoryPRBody(round FactoryRound, preview *PreviewReport, head string) (string, error) {
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
	// The section states, never omits (spec D7), so the heading and the line
	// factoryPreviewBody always returns render whatever the report holds.
	fmt.Fprintf(&b, "\n## Preview and test environments\n\n%s\n", factoryPreviewBody(round, preview, head))
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
			// The fence covers every line the block renders, so neither the
			// language tag nor the path nor the code can close it early.
			fence := codeFence(excerpt.Lang + "\n" + excerpt.Path + "\n" + excerpt.Code)
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
	var failing, passing, other []FactoryCheck
	for _, check := range round.CI.Checks {
		switch {
		case check.Conclusion == "success":
			passing = append(passing, check)
		case check.Conclusion != "" && check.Conclusion != "neutral" && check.Conclusion != "skipped":
			failing = append(failing, check)
		default:
			other = append(other, check)
		}
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
			fmt.Fprintf(&b, "- %s: %s\n", check.Key.Name, factoryCheckConclusion(check))
		}
		b.WriteString("\n</details>\n")
	}
	if len(other) > 0 {
		b.WriteString("\n")
		for _, check := range other {
			fmt.Fprintf(&b, "- %s: %s\n", check.Key.Name, factoryCheckConclusion(check))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// factoryCheckConclusion states what a check reported. A check that neither
// passed nor settled is listed with its conclusion, or its status while it
// has none.
func factoryCheckConclusion(check FactoryCheck) string {
	if check.Conclusion != "" {
		return check.Conclusion
	}
	if check.Status != "" {
		return check.Status
	}
	return "pending"
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

// previewRowLimit is how many rows of each list the description renders before
// stating how many it left out (spec D9).
const previewRowLimit = 10

// factoryPreviewBody renders the content below the preview heading: the scope
// line, the gaps the report found, the deployment rows, the configured
// environments and the links the commit statuses reported. It states what
// exists rather than omitting the section, so it never returns an empty
// string: a report that could not be read yields one fixed line and no scope
// line (spec D7, D11).
func factoryPreviewBody(round FactoryRound, preview *PreviewReport, head string) string {
	if preview == nil || preview.Status == "unavailable" {
		return "Preview and test environments could not be read when this description was written."
	}
	blocks := []string{factoryPreviewScopeLine(preview.Ref, head)}
	if gaps := factoryPreviewGapLines(preview.Gaps); gaps != "" {
		blocks = append(blocks, gaps)
	}
	if len(preview.Deployments) == 0 && len(preview.Environments) == 0 && len(preview.Gaps) == 0 {
		// The line stands where the missing rows would be, so it reports the
		// absence of deployment and environment records even when links
		// render below it; they are never described as previews.
		blocks = append(blocks, "No preview deployments or test environments are configured for this revision.")
	}
	if len(preview.Deployments) > 0 {
		blocks = append(blocks, factoryPreviewDeploymentRows(preview.Deployments, head))
	}
	if len(preview.Environments) > 0 {
		blocks = append(blocks, "Configured environments:\n\n"+factoryPreviewEnvironmentRows(preview.Environments))
	}
	if links := factoryPreviewStatusLinks(round); len(links) > 0 {
		blocks = append(blocks, "Links reported by commit statuses:\n\n"+factoryPreviewStatusRows(links))
	}
	return strings.Join(blocks, "\n\n")
}

// factoryPreviewScopeLine names the revision scope the report resolved and the
// PR head the description was written against, so the snapshot stays truthful
// after the head moves.
func factoryPreviewScopeLine(ref, head string) string {
	scope := "every revision the forge reports"
	if ref != "" {
		scope = factoryInlineCode(ref)
	}
	return fmt.Sprintf("Deployment records for %s, PR head %s at write time.", scope, factoryInlineCode(factoryShortSHA(head)))
}

// factoryPreviewGapLines groups the gaps by identical detail: one line per
// group, keys joined in frozen capability order, and the groups in the order
// each first appears in the report's already-ordered gaps.
func factoryPreviewGapLines(gaps []DiscoveryGap) string {
	if len(gaps) == 0 {
		return ""
	}
	var order []string
	grouped := make(map[string][]string, len(gaps))
	for _, gap := range gaps {
		if _, seen := grouped[gap.Detail]; !seen {
			order = append(order, gap.Detail)
		}
		grouped[gap.Detail] = append(grouped[gap.Detail], gap.Key)
	}
	lines := make([]string, 0, len(order))
	for _, detail := range order {
		keys := grouped[detail]
		sort.SliceStable(keys, func(i, j int) bool {
			return previewCapabilityRank(keys[i]) < previewCapabilityRank(keys[j])
		})
		spans := make([]string, 0, len(keys))
		for _, key := range keys {
			spans = append(spans, factoryInlineCode(key))
		}
		lines = append(lines, fmt.Sprintf("Not fully read — %s: %s", strings.Join(spans, ", "), factoryInlineCode(detail)))
	}
	return strings.Join(lines, "\n")
}

// previewCapabilityRank is one capability key's position in the frozen order. A
// key outside the list sorts after every frozen one, keeping the report's
// order for it.
func previewCapabilityRank(key string) int {
	for at, candidate := range previewCapabilityOrder {
		if candidate == key {
			return at
		}
	}
	return len(previewCapabilityOrder)
}

// factoryPreviewDeploymentRows renders the deployment rows, capped, followed by
// the truncated count when the report holds more (spec D9).
func factoryPreviewDeploymentRows(deployments []PreviewDeployment, head string) string {
	rows := make([]string, 0, previewRowLimit)
	for _, deployment := range deployments {
		if len(rows) == previewRowLimit {
			break
		}
		rows = append(rows, factoryPreviewDeploymentRow(deployment, head))
	}
	return factoryPreviewTruncated(rows, len(deployments))
}

// factoryPreviewDeploymentRow renders one deployment as the segments the report
// has: the environment and its state, the provider revision with its badge,
// the link with its logs, and the description. Fields the report carries for
// fidelity — target URL, task, transient, production, original environment, id
// and the timestamps — are never rendered.
func factoryPreviewDeploymentRow(deployment PreviewDeployment, head string) string {
	segments := []string{factoryInlineCode(deployment.Environment)}
	if deployment.State == "" {
		segments = append(segments, "(no status reported by the provider)")
	} else {
		segments = append(segments, factoryInlineCode(deployment.State))
	}
	if deployment.SHA == "" {
		segments = append(segments, "revision not reported by the provider")
	} else {
		segments = append(segments, factoryInlineCode(factoryShortSHA(deployment.SHA))+" "+factoryPreviewBadge(deployment.SHA, head))
	}
	if deployment.EnvironmentURL != "" {
		link := factoryPreviewURL(deployment.EnvironmentURL)
		if deployment.LogURL != "" {
			// Logs never render unattached to the link they belong to.
			link += " (logs: " + factoryPreviewURL(deployment.LogURL) + ")"
		}
		segments = append(segments, link)
	}
	if deployment.Description != "" {
		segments = append(segments, factoryInlineCode(deployment.Description))
	}
	return "- " + strings.Join(segments, " — ")
}

// factoryPreviewEnvironmentRows renders the configured-environment rows, capped
// and counted like the other lists.
func factoryPreviewEnvironmentRows(environments []PreviewEnvironment) string {
	rows := make([]string, 0, previewRowLimit)
	for _, environment := range environments {
		if len(rows) == previewRowLimit {
			break
		}
		row := "- " + factoryInlineCode(environment.Name)
		if environment.HTMLURL != "" {
			row += " — " + factoryPreviewURL(environment.HTMLURL)
		}
		rows = append(rows, row)
	}
	return factoryPreviewTruncated(rows, len(environments))
}

// factoryPreviewStatusLinks returns the round's commit-status checks that carry
// a provider link, sorted by name then by the provider id string so the body is
// deterministic. Check runs are excluded: their URLs are CI pages the CI
// section already presents. The whole set is unavailable when the round's CI
// evidence is not for the round's head.
func factoryPreviewStatusLinks(round FactoryRound) []FactoryCheck {
	if round.CI == nil || round.CI.HeadSHA != round.HeadSHA {
		return nil
	}
	var links []FactoryCheck
	for _, check := range round.CI.Checks {
		if check.Key.Kind == "commit_status" && check.URL != "" {
			links = append(links, check)
		}
	}
	sort.SliceStable(links, func(i, j int) bool {
		if links[i].Key.Name != links[j].Key.Name {
			return links[i].Key.Name < links[j].Key.Name
		}
		return links[i].ID < links[j].ID
	})
	return links
}

// factoryPreviewStatusRows renders the status-link rows, capped and counted.
func factoryPreviewStatusRows(links []FactoryCheck) string {
	rows := make([]string, 0, previewRowLimit)
	for _, link := range links {
		if len(rows) == previewRowLimit {
			break
		}
		rows = append(rows, fmt.Sprintf("- %s (%s): %s", factoryInlineCode(link.Key.Name), factoryInlineCode(factoryCheckConclusion(link)), factoryPreviewURL(link.URL)))
	}
	return factoryPreviewTruncated(rows, len(links))
}

// factoryPreviewTruncated joins rendered rows and states how many the list left
// out, so a truncated list never reads as complete.
func factoryPreviewTruncated(rows []string, total int) string {
	if omitted := total - len(rows); omitted > 0 {
		rows = append(rows, fmt.Sprintf("- …and %d more", omitted))
	}
	return strings.Join(rows, "\n")
}

// factoryPreviewBadge compares the provider-reported revision with the PR head
// by case-insensitive equality: no prefix matching, no ancestry, no distance.
func factoryPreviewBadge(deploymentSHA, head string) string {
	if deploymentSHA == "" {
		return "revision not reported by the provider"
	}
	if strings.EqualFold(deploymentSHA, head) {
		return "matches the PR head"
	}
	return "not the PR head"
}

// factoryShortSHA truncates a revision to at most seven characters, verbatim
// when it is already that short.
func factoryShortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// factoryDisplayBound caps each displayed provider text at 512 Unicode code
// points (spec D10).
const factoryDisplayBound = 512

// factoryDisplayText prepares one provider string for display: newlines become
// single spaces, every other control character becomes a space, and the result
// is capped at 512 Unicode code points with an ellipsis. Only the rendered
// string is changed; the underlying report keeps its full value.
func factoryDisplayText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	text := b.String()
	if runes := []rune(text); len(runes) > factoryDisplayBound {
		return string(runes[:factoryDisplayBound]) + "…"
	}
	return text
}

// factoryInlineCode renders a provider string as one protected inline span: its
// control characters become spaces and are capped for display, and the wrapping
// run is one backtick longer than the longest run inside it, padded where the
// value begins or ends with a backtick so no provider string can close the span
// early.
func factoryInlineCode(value string) string {
	value = factoryDisplayText(value)
	longest, run := 0, 0
	for _, r := range value {
		if r != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}
	fence := strings.Repeat("`", longest+1)
	var b strings.Builder
	b.WriteString(fence)
	if strings.HasPrefix(value, "`") {
		b.WriteString(" ")
	}
	b.WriteString(value)
	if strings.HasSuffix(value, "`") {
		b.WriteString(" ")
	}
	b.WriteString(fence)
	return b.String()
}

// factoryProviderURLBound caps a clickable provider URL in bytes (spec D10).
const factoryProviderURLBound = 2048

// factoryURLClickable reports whether a provider-reported URL may render as a
// Markdown autolink: an absolute HTTP or HTTPS URL with a nonempty host, at
// most 2048 bytes, and no whitespace, control character, angle bracket or
// backtick. An invalid value is judged as the provider reported it — nothing is
// trimmed or repaired into validity (spec D10).
func factoryURLClickable(value string) bool {
	if value == "" || len(value) > factoryProviderURLBound {
		return false
	}
	for _, r := range value {
		if r <= ' ' || unicode.IsControl(r) || unicode.IsSpace(r) || r == '<' || r == '>' || r == '`' {
			return false
		}
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != ""
}

// factoryPreviewURL renders one provider-reported URL (spec D10). A valid URL
// renders as a Markdown autolink; a rejected nonempty value renders as bounded
// protected inline code prefixed "Untrusted URL:" — never an href or autolink.
// An empty value renders as nothing, so a caller that drops empty segments
// keeps dropping them.
func factoryPreviewURL(value string) string {
	if value == "" {
		return ""
	}
	if factoryURLClickable(value) {
		return "<" + value + ">"
	}
	return factoryInlineCode("Untrusted URL: " + value)
}
