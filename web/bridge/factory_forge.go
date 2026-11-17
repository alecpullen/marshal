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

// FactoryPRDescription is optional: a forge that cannot update a pull request
// description must not pretend it did.
type FactoryPRDescription interface {
	UpdateFactoryPRDescription(ctx context.Context, repo Repo, number int, body string, cred Credential) error
}

var ErrFactoryPRDescriptionUnsupported = errors.New("forge does not support pull request descriptions")

func FactoryPRDescriptionFor(f Forge) (FactoryPRDescription, error) {
	v, ok := f.(FactoryPRDescription)
	if !ok {
		return nil, ErrFactoryPRDescriptionUnsupported
	}
	return v, nil
}

// ErrFactoryMergeUnsupported reports a forge with no pull request merge
// capability. It is never simulated as success.
var ErrFactoryMergeUnsupported = errors.New("forge does not support merging factory pull requests")

// ErrFactoryMergeRejected reports a merge the forge refused, or evidence that
// proves no merge happened. Nothing was applied and the batch may report a
// failure.
var ErrFactoryMergeRejected = errors.New("factory pull request merge was refused")

// ErrFactoryMergeUncertain reports a merge whose outcome cannot be proven. The
// coordinator must reconcile with the forge before any further mutation.
var ErrFactoryMergeUncertain = errors.New("factory pull request merge outcome is uncertain")

func FactoryPRMergerFor(f Forge) (FactoryPRMerger, error) {
	v, ok := f.(FactoryPRMerger)
	if !ok {
		return nil, ErrFactoryMergeUnsupported
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
