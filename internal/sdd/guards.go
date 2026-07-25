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

// EditGuard checks the pipeline branch for source-file modifications since
// pipelineBase. The orchestrator should never edit source files directly
// (workers do, but only in their worktrees — those are the implementer's job
// and are NOT flagged here). "Source file" = any path not under
// .marshal/sdd/. Returns ORCHESTRATOR_EDIT with the violating paths when any
// source file was modified on the pipeline branch.
func EditGuard(git GitOps, pipelineBranch, pipelineBase string, dag *DAG) GuardResult {
	diff, err := git.DiffStat(pipelineBase, pipelineBranch)
	if err != nil {
		return GuardResult{Pass: false, Event: "ORCHESTRATOR_EDIT", Reason: "git diff --stat failed: " + err.Error()}
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
		if strings.HasPrefix(path, ".marshal/sdd/") {
			continue // SDD workspace files are allowed
		}
		violations = append(violations, path)
	}
	if len(violations) == 0 {
		return GuardResult{Pass: true, Event: "EDIT_GUARD_CLEAN"}
	}
	return GuardResult{Pass: false, Event: "ORCHESTRATOR_EDIT", Files: violations, Reason: "orchestrator edited source files on the pipeline branch"}
}

// BranchBaseGuard verifies the pipeline branch is not behind the target
// branch (spec §9). Returns BASE_OK when the target HEAD is an ancestor of
// the pipeline HEAD; WRONG_BASE otherwise. Called before task-merge and
// task-finalize.
func BranchBaseGuard(git GitOps, rs *RepoState) GuardResult {
	if rs.Branch == "" || rs.TargetBranch == "" {
		return GuardResult{Pass: false, Event: "WRONG_BASE", Reason: "branch or target_branch not set"}
	}
	pipelineHead, err := git.RevParse(rs.Branch)
	if err != nil {
		return GuardResult{Pass: false, Event: "WRONG_BASE", Reason: "rev-parse pipeline branch: " + err.Error()}
	}
	targetHead, err := git.RevParse(rs.TargetBranch)
	if err != nil {
		return GuardResult{Pass: false, Event: "WRONG_BASE", Reason: "rev-parse target branch: " + err.Error()}
	}
	if pipelineHead == targetHead {
		return GuardResult{Pass: true, Event: "BASE_OK"}
	}
	ok, err := git.IsAncestor(targetHead, pipelineHead)
	if err != nil {
		return GuardResult{Pass: false, Event: "WRONG_BASE", Reason: "merge-base check failed: " + err.Error()}
	}
	if ok {
		return GuardResult{Pass: true, Event: "BASE_OK"}
	}
	return GuardResult{Pass: false, Event: "WRONG_BASE", Reason: "pipeline branch does not contain target HEAD"}
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
