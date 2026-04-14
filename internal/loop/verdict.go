package loop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/alecpullen/marshal/internal/reasoning"
)

// Verdict is the structured response from the critic model.
type Verdict struct {
	Verdict  string   `json:"verdict"`  // "PASS" | "FAIL"
	Summary  string   `json:"summary"`
	Issue    string   `json:"issue"`
	Fix      string   `json:"fix"`
	Concerns []string `json:"concerns"`
}

// IsPASS reports whether the verdict is a passing verdict (case-insensitive).
func (v *Verdict) IsPASS() bool {
	return strings.EqualFold(v.Verdict, "PASS")
}

// markdownFenceRe strips leading/trailing ``` fences that some models wrap
// their JSON in (e.g. ```json\n{...}\n```).
var markdownFenceRe = regexp.MustCompile("(?s)^```[a-zA-Z]*\\s*(.*?)\\s*```$")

// trailingCommaRe removes trailing commas before } or ] (common JSON mistake
// in model output).
var trailingCommaRe = regexp.MustCompile(`,\s*([}\]])`)

// ParseVerdict extracts a Verdict from raw critic output.
// It:
//  1. Strips <think>…</think> blocks (returns them separately for storage).
//  2. Strips markdown fences around the JSON.
//  3. Removes trailing commas.
//  4. Unmarshals the remaining text.
func ParseVerdict(raw string) (*Verdict, []string, error) {
	// Strip think blocks first so they don't confuse the JSON extractor.
	clean, thinks := reasoning.Strip(raw)

	// Strip markdown fences.
	if m := markdownFenceRe.FindStringSubmatch(clean); m != nil {
		clean = m[1]
	}

	// Try to extract just the JSON object if there's surrounding prose.
	if idx := strings.Index(clean, "{"); idx > 0 {
		if end := strings.LastIndex(clean, "}"); end > idx {
			clean = clean[idx : end+1]
		}
	}

	// Remove trailing commas.
	clean = trailingCommaRe.ReplaceAllString(clean, "$1")

	var v Verdict
	if err := json.Unmarshal([]byte(clean), &v); err != nil {
		return nil, thinks, fmt.Errorf("verdict JSON parse: %w (raw: %.200s)", err, clean)
	}

	// Normalise verdict field to upper-case.
	v.Verdict = strings.ToUpper(strings.TrimSpace(v.Verdict))
	if v.Verdict != "PASS" && v.Verdict != "FAIL" {
		return nil, thinks, fmt.Errorf("verdict must be PASS or FAIL, got %q", v.Verdict)
	}

	return &v, thinks, nil
}
