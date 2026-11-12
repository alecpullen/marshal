package bridge

import (
	"context"
	"errors"
)

// FactoryForge is optional: unsupported forges must not simulate successful evidence.
type FactoryForge interface {
	ObserveFactoryCI(context.Context, Repo, string, Credential) (FactoryCI, error)
	FindFactoryPR(context.Context, Repo, string, string, Credential) (*FactoryPR, error)
	GetFactoryPR(context.Context, Repo, int, Credential) (FactoryPR, error)
	SetFactoryDraft(context.Context, Repo, FactoryPR, bool, Credential) error
}

var ErrFactoryForgeUnsupported = errors.New("forge does not support factory CI and draft lifecycle")

func FactoryForgeFor(f Forge) (FactoryForge, error) {
	v, ok := f.(FactoryForge)
	if !ok {
		return nil, ErrFactoryForgeUnsupported
	}
	return v, nil
}

// EvaluateFactoryCI never treats incomplete or revision-mismatched evidence as passing.
func EvaluateFactoryCI(ci FactoryCI, expected []FactoryCheckKey, acknowledged bool) (string, []FactoryCheck) {
	if !ci.Complete || ci.HeadSHA == "" {
		return "unavailable", nil
	}
	if len(expected) == 0 && !acknowledged {
		return "blocked", nil
	}
	required := map[FactoryCheckKey]bool{}
	for _, k := range expected {
		required[k] = true
	}
	seen := map[FactoryCheckKey]bool{}
	state := "passed"
	var actionable []FactoryCheck
	rank := map[string]int{"passed": 0, "waiting": 1, "failed": 2, "blocked": 3, "unavailable": 4}
	mark := func(s string, c FactoryCheck) {
		if rank[s] > rank[state] {
			state = s
		}
		actionable = append(actionable, c)
	}
	for _, c := range ci.Checks {
		if c.SHA != ci.HeadSHA {
			mark("unavailable", c)
			continue
		}
		seen[c.Key] = true
		if c.Status != "completed" {
			mark("waiting", c)
			continue
		}
		switch c.Conclusion {
		case "success":
		case "failure", "error":
			mark("failed", c)
		case "skipped", "neutral":
			if required[c.Key] {
				mark("blocked", c)
			}
		case "cancelled", "timed_out", "action_required", "stale", "startup_failure":
			mark("blocked", c)
		default:
			mark("blocked", c)
		}
	}
	for k := range required {
		if !seen[k] {
			mark("waiting", FactoryCheck{Key: k, SHA: ci.HeadSHA, Status: "missing"})
		}
	}
	return state, actionable
}
