package permissions

import (
	"path"
	"strings"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionAsk   Action = "ask"
	ActionDeny  Action = "deny"
)

type Rule struct {
	Permission string
	Pattern    string
	Action     Action
}

var SafeCommands = []Rule{
	{Permission: "shell", Pattern: "ls", Action: ActionAllow},
	{Permission: "shell", Pattern: "pwd", Action: ActionAllow},
	{Permission: "shell", Pattern: "date", Action: ActionAllow},
	{Permission: "shell", Pattern: "uname", Action: ActionAllow},
	{Permission: "shell", Pattern: "wc", Action: ActionAllow},
	{Permission: "shell", Pattern: "cat", Action: ActionAllow},
	{Permission: "shell", Pattern: "echo", Action: ActionAllow},
	{Permission: "shell", Pattern: "true", Action: ActionAllow},
}

func Matches(pattern, subject string) bool {
	if pattern == subject {
		return true
	}
	if strings.Contains(pattern, "/") {
		return matchPath(pattern, subject)
	}
	return matchGlob(pattern, subject)
}

func matchGlob(pattern, subject string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == subject
	}
	norm := strings.ReplaceAll(pattern, "**", "*")
	ok, err := path.Match(norm, subject)
	if err != nil {
		return false
	}
	if ok {
		return true
	}
	if strings.HasSuffix(norm, "*") {
		prefix := strings.TrimSuffix(norm, "*")
		return strings.HasPrefix(subject, prefix)
	}
	return false
}

func matchPath(pattern, subject string) bool {
	patSegs := strings.Split(pattern, "/")
	subSegs := strings.Split(subject, "/")
	return matchSegments(patSegs, subSegs)
}

func matchSegments(pat, sub []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(sub); i++ {
				if matchSegments(pat[1:], sub[i:]) {
					return true
				}
			}
			return false
		}
		if len(sub) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], sub[0])
		if err != nil || !ok {
			return false
		}
		pat = pat[1:]
		sub = sub[1:]
	}
	return len(sub) == 0
}

func PermissionForTool(toolName string) string {
	switch toolName {
	case "shell.run", "test.run":
		return "shell"
	case "file.write_patch":
		return "file.write_patch"
	default:
		return toolName
	}
}

func Evaluate(rules []Rule, permissionName, subject string) (Action, bool) {
	var matched Action
	found := false
	for _, r := range rules {
		if !permissionMatches(r.Permission, permissionName) {
			continue
		}
		if Matches(r.Pattern, subject) {
			matched = r.Action
			found = true
		}
	}
	return matched, found
}

func permissionMatches(rulePermission, toolPermission string) bool {
	if rulePermission == toolPermission {
		return true
	}
	if rulePermission == "shell" && (toolPermission == "shell.run" || toolPermission == "test.run") {
		return true
	}
	if strings.HasSuffix(rulePermission, ".*") {
		prefix := strings.TrimSuffix(rulePermission, ".*")
		return strings.HasPrefix(toolPermission, prefix+".")
	}
	return false
}
