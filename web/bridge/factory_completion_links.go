package bridge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FactoryCompletionCommand is the structured payload of the completion
// commands. Each kind reads only the fields it needs, so a command those
// kinds do not carry marshals as it always did.
type FactoryCompletionCommand struct {
	// Link is the pull request a link_pr command names: a number or a URL.
	Link string `json:"link,omitempty"`
	// PRs is the reviewed, ordered set a merge_pr or merge_and_complete
	// command authorizes.
	PRs []FactoryCompletionPR `json:"prs,omitempty"`
	// Strategy is the reviewed merge strategy.
	Strategy string `json:"strategy,omitempty"`
	// CompleteAfter completes the item once every reviewed merge confirms.
	CompleteAfter bool `json:"completeAfter,omitempty"`
	// RunChoice is continue or request_stop when the item's own run is
	// active or its state cannot be established.
	RunChoice string `json:"runChoice,omitempty"`
}

// FactoryCompletionPR is one pull request a user reviewed for a merge batch.
type FactoryCompletionPR struct {
	RepoID  string `json:"repoId"`
	Number  int    `json:"number"`
	NodeID  string `json:"nodeId"`
	HeadSHA string `json:"headSha"`
	BaseRef string `json:"baseRef"`
}

// factoryCompletionEvidence is what the controller learned from the forge
// before the store update: the one pull request a link_pr command resolved, or
// the observation a refresh_pr_evidence command made of every linked pull
// request. It travels to the apply as an unexported parameter and never into
// the journaled command bytes, so a replay depends on no forge being reachable.
type factoryCompletionEvidence struct {
	// RepoID is the configured repository the resolved link belongs to.
	RepoID string
	// Link is the pull request a link_pr command resolved, with its
	// immutable identity and the observation made now.
	Link FactoryPR
	// Fetched holds the refreshed observation of a linked pull request,
	// keyed by the node ID the link names. A link missing from it is stale.
	Fetched map[string]FactoryPR
}

// factoryForgeOpener is the forge the completion commands reach through the
// effects value. Production passes *factoryEffects, which has this method; a
// value that does not implement it has no forge, and the evidence commands
// treat it as unavailable rather than guessing at what a pull request is.
type factoryForgeOpener interface {
	forge(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error)
}

// completionLinkEvidence resolves a link_pr command against the configured
// repository before the store update. The forge call stays outside the atomic
// mutation on purpose: a store update is the boundary the journal needs, and
// nothing on the network belongs inside it.
func (c *FactoryController) completionLinkEvidence(ctx context.Context, item FactoryItem, cmd FactoryCommand) (*factoryCompletionEvidence, error) {
	link := ""
	if cmd.Completion != nil {
		link = strings.TrimSpace(cmd.Completion.Link)
	}
	if link == "" {
		return nil, fmt.Errorf("%w: link_pr requires a pull request number or URL", errFactoryInput)
	}
	repoID := factoryConfiguredRepoID(item)
	if repoID == "" {
		return nil, fmt.Errorf("%w: the work item names no repository to link a pull request in", errFactoryConfig)
	}
	repo, forge, cred, err := c.completionForge(ctx, item)
	if err != nil {
		return nil, err
	}
	number, err := factoryLinkNumber(link, repo)
	if err != nil {
		return nil, err
	}
	pr, err := forge.GetFactoryPR(ctx, repo, number, cred)
	if err != nil {
		return nil, fmt.Errorf("factory: pull request %s could not be observed: %w", link, err)
	}
	// The node ID is the identity a link dedups and a rename cannot change;
	// a response without one, or about a different number, is not evidence.
	if strings.TrimSpace(pr.NodeID) == "" || pr.Number != number {
		return nil, fmt.Errorf("%w: pull request %s did not come back with its immutable identity", errFactoryInput, link)
	}
	return &factoryCompletionEvidence{RepoID: repoID, Link: pr}, nil
}

// completionRefreshEvidence re-observes every linked pull request. It never
// fails on a fetch: stale evidence is a state the surfaces render, so a failed
// fetch keeps the previous snapshot for the apply to mark stale. An
// unavailable forge is the same statement about every link.
func (c *FactoryController) completionRefreshEvidence(ctx context.Context, item FactoryItem) *factoryCompletionEvidence {
	ev := &factoryCompletionEvidence{Fetched: map[string]FactoryPR{}}
	completion := factoryCompletionOf(item)
	if len(completion.Links) == 0 {
		return ev
	}
	repo, forge, cred, err := c.completionForge(ctx, item)
	if err != nil {
		return ev
	}
	for _, l := range completion.Links {
		if ctx.Err() != nil || strings.TrimSpace(l.PR.NodeID) == "" {
			continue
		}
		pr, err := forge.GetFactoryPR(ctx, repo, l.PR.Number, cred)
		if err != nil {
			continue
		}
		// A renumbered or replaced pull request is a different pull request:
		// the link keeps the identity it named and goes stale rather than
		// silently following the number to something else.
		if pr.NodeID != l.PR.NodeID {
			continue
		}
		ev.Fetched[l.PR.NodeID] = pr
	}
	return ev
}

// completionForge opens the item's forge through the effects value. A value
// without the unexported forge method — a test double, or no effects at all —
// has no forge, which the callers state rather than paper over.
func (c *FactoryController) completionForge(ctx context.Context, item FactoryItem) (Repo, FactoryForge, Credential, error) {
	opener, ok := c.effects.(factoryForgeOpener)
	if !ok {
		return Repo{}, nil, Credential{}, fmt.Errorf("%w: no forge is available to verify pull request evidence", errFactoryConfig)
	}
	repo, _, forge, cred, err := opener.forge(ctx, item)
	if err != nil {
		return Repo{}, nil, Credential{}, err
	}
	if forge == nil {
		return Repo{}, nil, Credential{}, fmt.Errorf("%w: no forge is available to verify pull request evidence", errFactoryConfig)
	}
	return repo, forge, cred, nil
}

// factoryConfiguredRepoID is the repository a completion's links belong to:
// the latest approval's when there is one, else the draft's, else the item's
// own, which is the same rule every other reader of the configuration uses.
func factoryConfiguredRepoID(i FactoryItem) string {
	if n := len(i.Approvals); n > 0 {
		if id := strings.TrimSpace(i.Approvals[n-1].Config.RepoID); id != "" {
			return id
		}
	}
	if id := strings.TrimSpace(i.Draft.Config.RepoID); id != "" {
		return id
	}
	return strings.TrimSpace(i.RepoID)
}

// factoryLinkIndex finds a link by its immutable identity: the repository it
// belongs to and the pull request's node ID, which a rename or a renumber
// cannot change.
func factoryLinkIndex(links []FactoryPRLink, repoID, nodeID string) (int, bool) {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(nodeID) == "" {
		return 0, false
	}
	for n := range links {
		if links[n].RepoID == repoID && links[n].PR.NodeID == nodeID {
			return n, true
		}
	}
	return 0, false
}

// factoryLinkNumber reads the pull request a link command names. A bare number
// is always the configured repository's; a URL has to name that same
// repository, and is refused before any request is made if it does not.
func factoryLinkNumber(link string, repo Repo) (int, error) {
	if n, err := strconv.Atoi(link); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("%w: pull request number must be positive", errFactoryInput)
		}
		return n, nil
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return 0, fmt.Errorf("%w: %s is not a pull request number or URL", errFactoryInput, link)
	}
	host, path, ok := factoryRepoIdentity(link)
	repoHost, repoPath, repoOK := factoryRepoIdentity(repo.URL)
	if !ok || !repoOK || host != repoHost || path != repoPath {
		return 0, fmt.Errorf("%w: %s is not a pull request of %s", errFactoryInput, link, repo.URL)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for n := 0; n+1 < len(segs); n++ {
		if segs[n] != "pull" {
			continue
		}
		number, err := strconv.Atoi(segs[n+1])
		if err != nil || number <= 0 {
			break
		}
		return number, nil
	}
	return 0, fmt.Errorf("%w: %s does not name a pull request", errFactoryInput, link)
}

// factoryRepoIdentity reads a repository URL as the host and the owner/repo
// path it names, lowercased, ignoring a .git suffix, a trailing slash and
// anything after the repository path — so a pull request URL and its
// repository compare as the same repository.
func factoryRepoIdentity(raw string) (string, string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", false
	}
	host := ""
	path := trimmed
	if u, err := url.Parse(trimmed); err == nil && u.Host != "" {
		host = strings.ToLower(u.Host)
		path = u.Path
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segs := strings.Split(path, "/")
	if host == "" && len(segs) >= 3 {
		// A scheme-less "host/owner/repo" names a host the parser cannot see.
		// A bare "owner/repo" names none, and only matches itself.
		host = strings.ToLower(segs[0])
		segs = segs[1:]
	}
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" || strings.ToLower(segs[1]) == "pull" {
		return "", "", false
	}
	return host, strings.ToLower(segs[0] + "/" + segs[1]), true
}

// factoryApplyCompletionLinks applies the pull request evidence commands. Both
// are Studio commands: an MCP client's grant is over named actions of a run,
// and naming evidence about the business outcome is not one of them.
func factoryApplyCompletionLinks(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, ev *factoryCompletionEvidence, now time.Time) error {
	if p != nil {
		return fmt.Errorf("%w: %s is not an action an MCP client may take", ErrFactoryConflict, c.Kind)
	}
	switch c.Kind {
	case "link_pr":
		return factoryApplyLinkPR(i, p, ev, now)
	case "refresh_pr_evidence":
		return factoryApplyRefreshPRs(i, ev, now)
	}
	return ErrFactoryConflict
}

// factoryApplyLinkPR links one pull request the user attached. Re-linking the
// pull request already linked under that immutable identity is a refresh of
// its observation, not a second entry: the link is the identity.
func factoryApplyLinkPR(i *FactoryItem, p *factoryPrincipal, ev *factoryCompletionEvidence, now time.Time) error {
	if ev == nil || strings.TrimSpace(ev.RepoID) == "" || strings.TrimSpace(ev.Link.NodeID) == "" {
		return fmt.Errorf("%w: link_pr requires a resolved pull request", errFactoryInput)
	}
	now = now.UTC()
	completion := factoryCompletionOf(*i)
	if n, ok := factoryLinkIndex(completion.Links, ev.RepoID, ev.Link.NodeID); ok {
		completion.Links[n].PR = ev.Link
		completion.Links[n].ObservedAt = now
		completion.Links[n].Fresh = true
		i.Completion = &completion
		return nil
	}
	completion.Links = append(completion.Links, FactoryPRLink{RepoID: ev.RepoID, PR: ev.Link, Origin: "user", ObservedAt: now, Fresh: true})
	i.Completion = &completion
	i.History = append(i.History, p.event("completion_pr_linked", fmt.Sprintf("pull request #%d linked", ev.Link.Number), "", now))
	return nil
}

// factoryApplyRefreshPRs replaces the snapshot of every link that was fetched
// and marks the rest stale. A stale link keeps its last successful observation
// — the snapshot and its timestamp — because the evidence that it existed then
// is still true; only "this is current" stopped being true.
func factoryApplyRefreshPRs(i *FactoryItem, ev *factoryCompletionEvidence, now time.Time) error {
	completion := factoryCompletionOf(*i)
	if len(completion.Links) == 0 {
		return nil
	}
	now = now.UTC()
	for n := range completion.Links {
		l := &completion.Links[n]
		pr, ok := factoryFetched(ev, l.PR.NodeID)
		if !ok || pr.NodeID != l.PR.NodeID {
			l.Fresh = false
			continue
		}
		l.PR = pr
		l.ObservedAt = now
		l.Fresh = true
	}
	i.Completion = &completion
	return nil
}

// factoryFetched reads one refreshed observation, treating an absent evidence
// set — an unavailable forge — as no observation at all.
func factoryFetched(ev *factoryCompletionEvidence, nodeID string) (FactoryPR, bool) {
	if ev == nil || strings.TrimSpace(nodeID) == "" {
		return FactoryPR{}, false
	}
	pr, ok := ev.Fetched[nodeID]
	return pr, ok
}

// factoryAssociateDelivery observes the item's own delivery pull request as
// completion evidence. It is the factory's link rather than the user's, and it
// never touches delivery ownership: FactoryItem.PRs, factoryPRMatches, the
// effects, the workers, the grants and State belong to the scheduler, and
// linking a pull request for observation changes none of them.
//
// Association is churn-free. The snapshot moves only when the observed
// identity, state, head or base actually changed, and the observation
// timestamps belong to refresh_pr_evidence, so observing the same pull request
// again neither journals nor returns a different item.
func factoryAssociateDelivery(i *FactoryItem, observed FactoryObservation, now time.Time) {
	if observed.PR == nil {
		return
	}
	pr := *observed.PR
	if strings.TrimSpace(pr.NodeID) == "" || !factoryPRMatches(*i, pr) {
		return
	}
	repoID := factoryConfiguredRepoID(*i)
	if repoID == "" {
		return
	}
	now = now.UTC()
	completion := factoryCompletionOf(*i)
	if n, ok := factoryLinkIndex(completion.Links, repoID, pr.NodeID); ok {
		if factoryPRAgrees(completion.Links[n].PR, pr) {
			return
		}
		completion.Links[n].PR = pr
		i.Completion = &completion
		return
	}
	completion.Links = append(completion.Links, FactoryPRLink{RepoID: repoID, PR: pr, Origin: "factory", ObservedAt: now, Fresh: true})
	i.Completion = &completion
	i.History = append(i.History, FactoryEvent{At: now, Kind: "completion_pr_linked", Message: fmt.Sprintf("delivery pull request #%d linked", pr.Number)})
}

// factoryPRAgrees reports whether two observations of one pull request agree on
// every field a link snapshots.
func factoryPRAgrees(a, b FactoryPR) bool {
	return a.Number == b.Number && a.URL == b.URL && a.NodeID == b.NodeID && a.HeadRef == b.HeadRef && a.BaseRef == b.BaseRef && a.HeadSHA == b.HeadSHA && a.BaseSHA == b.BaseSHA && a.State == b.State && a.Draft == b.Draft
}
