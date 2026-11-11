package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrFactoryInvalidResult = errors.New("factory worker result invalid")
var ErrFactoryWorkerFailed = errors.New("factory worker turn failed")
var ErrFactoryRefused = errors.New("factory effect refused before dispatch")
var ErrFactoryUncertain = errors.New("factory effect outcome uncertain")

type FactoryEffects interface {
	Execute(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error)
	Observe(context.Context, FactoryItem) (FactoryObservation, error)
}
type factoryEffects struct {
	fleet     *Fleet
	workers   *factoryWorkers
	git       *factoryGit
	openForge func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error)
}

func NewFactoryEffects(fleet *Fleet) FactoryEffects {
	return &factoryEffects{fleet: fleet, workers: newFactoryWorkers(fleet), git: newFactoryGit(fleet)}
}
func (f *factoryEffects) forge(ctx context.Context, item FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
	if f.openForge != nil {
		return f.openForge(ctx, item)
	}
	if f.fleet == nil {
		return Repo{}, nil, nil, Credential{}, fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	repo, ok := f.fleet.ws.Repo(item.RepoID)
	if !ok {
		return Repo{}, nil, nil, Credential{}, fmt.Errorf("%w: %w", ErrFactoryRefused, ErrUnknownRepo)
	}
	base, cred, err := f.fleet.forgeFor(ctx, repo)
	if err != nil {
		return Repo{}, nil, nil, Credential{}, err
	}
	factory, err := FactoryForgeFor(base)
	return repo, base, factory, cred, err
}
func (f *factoryEffects) Observe(ctx context.Context, item FactoryItem) (FactoryObservation, error) {
	if f.fleet == nil {
		return FactoryObservation{}, fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	o, err := f.workers.Observe(ctx, item)
	if err != nil {
		return o, err
	}
	// A spawn response can be lost before the worker is copied into the
	// item. The persisted agent tag is the only acceptable recovery match.
	for _, e := range item.Effects {
		if e.Kind != "spawn" || (e.Status != "dispatched" && e.Status != "uncertain") {
			continue
		}
		for _, a := range f.fleet.ws.Agents() {
			if a.FactoryItemID == item.ID && a.FactorySpawnEffectID == e.ID && a.FactoryRole == e.WorkerRole {
				o.Workers = append(o.Workers, FactoryWorker{AgentID: a.ID, SessionID: a.SessionID, Role: a.FactoryRole, SpawnEffectID: e.ID, Parked: a.FactoryParked})
			}
		}
	}
	repo, _, forge, cred, err := f.forge(ctx, item)
	if err != nil {
		o.Error = err.Error()
		return o, err
	}
	if item.BaseSHA == "" && len(item.Approvals) > 0 && f.fleet.git != nil {
		branch := item.Approvals[len(item.Approvals)-1].Config.TargetBranch
		if branch == "" {
			branch = repo.Branch
		}
		if branch != "" {
			raw, e := f.fleet.git.runCtx(ctx, "", cred, "ls-remote", repo.URL, "refs/heads/"+branch)
			if e != nil {
				o.Error = e.Error()
			} else {
				fields := strings.Fields(string(raw))
				if len(fields) == 2 && fields[1] == "refs/heads/"+branch {
					o.BaseSHA = fields[0]
				}
			}
		}
	}
	if len(item.PRs) > 0 {
		p, e := forge.GetFactoryPR(ctx, repo, item.PRs[len(item.PRs)-1].Number, cred)
		if e != nil {
			o.Error = e.Error()
		} else {
			o.PR = &p
		}
	} else if item.Branch != "" {
		base := ""
		if len(item.Approvals) > 0 {
			base = item.Approvals[len(item.Approvals)-1].Config.TargetBranch
		}
		p, e := forge.FindFactoryPR(ctx, repo, item.Branch, base, cred)
		if e != nil {
			o.Error = e.Error()
		} else {
			o.PR = p
		}
	}
	sha := o.RemoteHead
	if o.PR != nil {
		sha = o.PR.HeadSHA
	}
	if sha != "" {
		ci, e := forge.ObserveFactoryCI(ctx, repo, sha, cred)
		if e != nil {
			o.Error = e.Error()
			return o, e
		} else {
			o.CI = &ci
		}
	}
	return o, nil
}
func (f *factoryEffects) Execute(ctx context.Context, item FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	out := FactoryOutcome{EffectID: e.ID}
	if f.fleet == nil && f.openForge == nil {
		return out, fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	switch e.Kind {
	case "spawn":
		w, err := f.fleet.SpawnFactoryWorker(ctx, item, e.WorkerRole, e.ID)
		if err != nil {
			return out, fmt.Errorf("%w: spawn: %v", ErrFactoryUncertain, err)
		}
		out.Worker = &w
	case "seed":
		if e.Worker == nil {
			return out, fmt.Errorf("%w: seed worker missing", ErrFactoryRefused)
		}
		head, tree, err := f.git.Seed(ctx, item, *e.Worker)
		if err != nil {
			return out, fmt.Errorf("%w: seed: %v", ErrFactoryUncertain, err)
		}
		out.HeadSHA = head
		out.TreeSHA = tree
	case "publish":
		if e.Worker == nil {
			return out, fmt.Errorf("%w: publish worker missing", ErrFactoryRefused)
		}
		if len(item.Rounds) > 0 {
			_, rt, err := f.git.checkout(item, *e.Worker)
			if err != nil {
				return out, fmt.Errorf("%w: %v", ErrFactoryRefused, err)
			}
			if _, err = f.fleet.commitSession(ctx, rt, "Implement factory work for "+item.ID); err != nil {
				return out, fmt.Errorf("%w: commit: %v", ErrFactoryUncertain, err)
			}
		}
		expected := ""
		if len(item.Rounds) > 0 {
			expected = item.Rounds[len(item.Rounds)-1].BaseSHA
		} else {
			expected = ""
		}
		if len(item.PRs) > 0 {
			expected = item.PRs[len(item.PRs)-1].HeadSHA
		}
		head, tree, err := f.git.Publish(ctx, item, *e.Worker, expected)
		if err != nil {
			return out, fmt.Errorf("%w: publish: %v", ErrFactoryUncertain, err)
		}
		out.HeadSHA = head
		out.TreeSHA = tree
	case "create_pr":
		repo, base, forge, cred, err := f.forge(ctx, item)
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrFactoryRefused, err)
		}
		conf := item.Approvals[len(item.Approvals)-1].Config
		p, err := forge.FindFactoryPR(ctx, repo, item.Branch, conf.TargetBranch, cred)
		if err != nil {
			return out, fmt.Errorf("%w: lookup PR: %v", ErrFactoryUncertain, err)
		}
		if p == nil {
			_, err = base.CreatePR(ctx, repo, PRRequest{Title: item.Title, Body: "Factory plan and specification are seeded. Implementation is in progress.", Head: item.Branch, Base: conf.TargetBranch, Draft: true}, cred)
			if err != nil {
				p, _ = forge.FindFactoryPR(ctx, repo, item.Branch, conf.TargetBranch, cred)
				if p == nil {
					return out, fmt.Errorf("%w: create PR: %v", ErrFactoryUncertain, err)
				}
			} else {
				p, err = forge.FindFactoryPR(ctx, repo, item.Branch, conf.TargetBranch, cred)
				if err != nil || p == nil {
					return out, fmt.Errorf("%w: created PR confirmation unavailable", ErrFactoryUncertain)
				}
			}
		}
		if !p.Draft || p.HeadSHA == "" {
			return out, fmt.Errorf("%w: draft PR evidence invalid", ErrFactoryUncertain)
		}
		out.PR = p
	case "implement", "review":
		o, err := f.workers.Execute(ctx, item, e)
		if err != nil {
			return o, err
		}
		return o, nil
	case "park":
		if e.Worker == nil {
			return out, fmt.Errorf("%w: park worker missing", ErrFactoryRefused)
		}
		if err := f.fleet.ParkFactoryWorker(ctx, *e.Worker); err != nil {
			return out, fmt.Errorf("%w: park: %v", ErrFactoryUncertain, err)
		}
	case "resume":
		if e.Worker == nil {
			return out, fmt.Errorf("%w: resume worker missing", ErrFactoryRefused)
		}
		if factoryFreshResume(item, e) {
			w, err := f.fleet.ResumeFactoryWorkerFresh(ctx, item, *e.Worker)
			if err != nil {
				return out, fmt.Errorf("%w: fresh resume: %v", ErrFactoryUncertain, err)
			}
			out.Worker = &w
		} else if err := f.fleet.Resume(ctx, e.Worker.AgentID); err != nil {
			return out, fmt.Errorf("%w: resume: %v", ErrFactoryUncertain, err)
		}
	case "set_ready", "set_draft":
		repo, _, forge, cred, err := f.forge(ctx, item)
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrFactoryRefused, err)
		}
		if len(item.PRs) == 0 {
			return out, fmt.Errorf("%w: missing PR", ErrFactoryRefused)
		}
		before, err := forge.GetFactoryPR(ctx, repo, item.PRs[len(item.PRs)-1].Number, cred)
		if err != nil {
			return out, fmt.Errorf("%w: PR precheck: %v", ErrFactoryUncertain, err)
		}
		if e.Kind == "set_ready" && (before.HeadSHA != e.ExpectedHead || before.BaseSHA != e.ExpectedBase || !before.Draft) {
			return out, fmt.Errorf("%w: PR revision changed", ErrFactoryRefused)
		}
		draft := e.Kind == "set_draft"
		if err := forge.SetFactoryDraft(ctx, repo, before, draft, cred); err != nil {
			return out, fmt.Errorf("%w: PR mutation: %v", ErrFactoryUncertain, err)
		}
		after, err := forge.GetFactoryPR(ctx, repo, before.Number, cred)
		if err != nil {
			return out, fmt.Errorf("%w: PR postcheck: %v", ErrFactoryUncertain, err)
		}
		out.PR = &after
		if !draft {
			ci, ciErr := forge.ObserveFactoryCI(ctx, repo, e.ExpectedHead, cred)
			conf := item.Approvals[len(item.Approvals)-1].Config
			ciState, _ := EvaluateFactoryCI(ci, conf.ExpectedChecks, conf.NoRequiredCIAcknowledged)
			if after.HeadSHA != e.ExpectedHead || after.BaseSHA != e.ExpectedBase || ciErr != nil || ciState != "passed" {
				if err := forge.SetFactoryDraft(ctx, repo, after, true, cred); err != nil {
					return out, fmt.Errorf("%w: ready race, draft compensation failed: %v", ErrFactoryUncertain, err)
				}
				after.Draft = true
				out.PR = &after
				out.Error = "PR revision or CI changed while marking ready; returned to draft"
				out.Confirmed = true
				return out, nil
			}
		}
	default:
		return out, fmt.Errorf("%w: unsupported effect %s", ErrFactoryRefused, e.Kind)
	}
	out.Confirmed = true
	return out, nil
}

// Cancel sends the existing ACP turn cancellation after the stop command is durable.
func (f *factoryEffects) Cancel(ctx context.Context, item FactoryItem) error {
	if f.fleet == nil {
		return fmt.Errorf("%w: fleet unavailable", ErrFactoryRefused)
	}
	for _, w := range item.Workers {
		if w.Parked {
			continue
		}
		a, ok := f.fleet.ws.Agent(w.AgentID)
		if !ok || a.FactoryItemID != item.ID || a.FactoryRole != w.Role || a.SessionID != w.SessionID {
			return fmt.Errorf("%w: worker ownership unconfirmed", ErrFactoryUncertain)
		}
		rt, err := f.fleet.runtimeForAgent(w.AgentID)
		if err != nil {
			return fmt.Errorf("%w: runtime unavailable: %v", ErrFactoryUncertain, err)
		}
		if err = rt.reg.Cancel(ctx, w.SessionID); err != nil {
			return fmt.Errorf("%w: ACP cancel: %v", ErrFactoryUncertain, err)
		}
	}
	return nil
}
