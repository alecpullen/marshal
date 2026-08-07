package limits

import (
	"regexp"
	"strings"
)

var (
	// dateSuffixRe matches a trailing 8-digit datestamp, optionally
	// separated: claude-sonnet-4-5-20250929, gemini-2.0-flash@20250101.
	dateSuffixRe = regexp.MustCompile(`[-@_]?\d{8}$`)
	dashRunRe    = regexp.MustCompile(`-{2,}`)
)

// tagSuffixes are routing/availability tags that carry no capability
// information and so must not prevent a match. Ollama size tags (":7b",
// ":32b") are deliberately absent — those identify different models.
var tagSuffixes = []string{":free", ":beta", ":latest", ":nitro", ":extended", "-latest"}

// roleSuffixes are fine-tune role markers stripped only by the last-resort
// prefix rung in Table.Lookup, never by norm itself.
var roleSuffixes = []string{"-instruct", "-chat", "-it"}

// norm reduces a model id to a comparable form so ids naming the same model
// across providers collide: lowercase, no vendor prefix, no datestamp, no
// availability tag, dots folded to dashes.
func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	for trimmed := true; trimmed; {
		trimmed = false
		for _, tag := range tagSuffixes {
			if strings.HasSuffix(s, tag) {
				s = strings.TrimSuffix(s, tag)
				trimmed = true
			}
		}
	}
	s = dateSuffixRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, ".", "-")
	s = dashRunRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// stripRoleSuffix removes a trailing fine-tune role marker. Used only by the
// prefix rung, which is already guarded against ambiguity.
func stripRoleSuffix(s string) string {
	for _, suffix := range roleSuffixes {
		if strings.HasSuffix(s, suffix) {
			return strings.TrimSuffix(s, suffix)
		}
	}
	return s
}
