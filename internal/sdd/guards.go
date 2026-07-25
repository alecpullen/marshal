package sdd

import (
	"regexp"
	"strings"
)

// diffStatPathRe matches a line of `git diff --stat` output and captures the
// file path (everything before " | "). Format: " internal/foo.go | 2 +-".
var diffStatPathRe = regexp.MustCompile(`^\s*([^\s|]+(?:\s+[^\s|]+)*?)\s*\|`)

// AllowedFilesCheck compares the actual diff (git diff --stat base..branch)
// against the contract's Allowed Files list. Returns a GuardResult with
// Pass=false and the violating file paths in Files when any modified path is
// not in the allowed set.
func AllowedFilesCheck(git GitOps, taskBranch, baseSHA string, contract *Contract) GuardResult {
	diff, err := git.DiffStat(baseSHA, taskBranch)
	if err != nil {
		return GuardResult{Pass: false, Event: "ALLOWED_FILES_VIOLATION", Reason: "git diff --stat failed: " + err.Error()}
	}
	allowed := parseAllowedFiles(contract.AllowedFiles)
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	var violations []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := diffStatPathRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := strings.TrimSpace(m[1])
		if path == "" {
			continue
		}
		if !allowedSet[path] {
			violations = append(violations, path)
		}
	}
	if len(violations) == 0 {
		return GuardResult{Pass: true, Event: "ALLOWED_FILES_PASS"}
	}
	return GuardResult{Pass: false, Event: "ALLOWED_FILES_VIOLATION", Files: violations, Reason: "files outside contract Allowed Files were modified"}
}

// parseAllowedFiles extracts file paths from the Allowed Files section body,
// stripping bullet prefixes ("- ", "* ") and whitespace.
func parseAllowedFiles(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "-* \t")
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
