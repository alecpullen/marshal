package screen

import (
	"regexp"
	"strings"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// StripANSI removes ANSI escape sequences.
func StripANSI(b []byte) string {
	return ansiRe.ReplaceAllString(string(b), "")
}

func extractState(content string, lines []string) UIState {
	state := UIState{}
	lower := strings.ToLower(content)

	if strings.Contains(lower, "marshal keys") {
		state.HelpOpen = true
	}
	if strings.Contains(content, "Agent wants to run:") {
		state.PendingApproval = true
	}
	if strings.Contains(content, "Pending question") || strings.Contains(content, "[Enter] answer") {
		state.PendingQuestion = true
	}
	if strings.Contains(content, "❯") {
		// input prompt visible; attempt to capture the current input line after the prompt
		for _, ln := range lines {
			if idx := strings.Index(ln, "❯"); idx >= 0 {
				state.InputValue = strings.TrimSpace(ln[idx+len("❯"):])
			}
		}
	}
	if strings.Contains(content, "busy") || strings.Contains(content, "running") {
		state.Busy = true
	}

	// Mode indicator from status line; look for known mode words at line start or in status bar.
	for _, m := range []string{"ask", "edit", "auto", "plan"} {
		if strings.Contains(lower, " "+m+" ") || strings.Contains(lower, "["+m+"]") {
			state.Mode = m
		}
	}

	state.LastAgentMsg = lastAgentMessage(lines)
	return state
}

func lastAgentMessage(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if strings.HasPrefix(ln, "agent:") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "agent:"))
		}
	}
	return ""
}
