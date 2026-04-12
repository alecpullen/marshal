// Package reasoning strips model thinking blocks from completions.
// Ported from aider/reasoning_tags.py.
package reasoning

import (
	"regexp"
	"strings"
)

// thinkRe matches <think>…</think> blocks including newlines.
var thinkRe = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

// Strip removes all <think>…</think> blocks from content and returns the
// cleaned text plus each extracted block (trimmed). Blocks are stored
// separately in the SQLite rounds table and kept out of the verdict
// parse path.
func Strip(content string) (stripped string, thinks []string) {
	matches := thinkRe.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		thinks = append(thinks, strings.TrimSpace(m[1]))
	}
	stripped = thinkRe.ReplaceAllString(content, "")
	return strings.TrimSpace(stripped), thinks
}
