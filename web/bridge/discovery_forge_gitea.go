package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// discoveryGiteaForge reads a Gitea/Forgejo instance's API for discovery. It
// shares the auth and base conventions of giteaForge — "<host>/api/v1" as the
// base and "token <pat>" as the header — but implements only the read-only
// probe surface. Unlike GitHub, Gitea has no public API default: an empty
// APIBase is missing configuration for this instance, not a forge failure.
type discoveryGiteaForge struct {
	client *http.Client
}

// apiRoot returns the instance API root. The versioned prefix is already part
// of Repo.APIBase, exactly as giteaForge.apiBase returns it.
//
// An empty base is reported as errDiscoveryUnconfigured with a detail naming
// the setting, before any request is built: a request to an empty base would
// hit a URL that cannot exist and would classify as a transport failure rather
// than as the configuration gap it is.
func (g *discoveryGiteaForge) apiRoot(repo Repo) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(repo.APIBase), "/")
	if base == "" {
		return "", fmt.Errorf(
			"gitea repository %q: the instance API base is not configured; set apiBase to https://<host>/api/v1: %w",
			repo.URL, errDiscoveryUnconfigured)
	}
	return base, nil
}

// authHeader returns the Authorization header value for a Gitea PAT.
func (g *discoveryGiteaForge) authHeader(cred Credential) string {
	return "token " + cred.literal
}

// repoPath returns "/repos/{owner}/{repo}" for the repo's remote URL.
func (g *discoveryGiteaForge) repoPath(repo Repo) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	return "/repos/" + owner + "/" + name, nil
}

// newRequest builds one authenticated GET against the instance API root.
func (g *discoveryGiteaForge) newRequest(repo Repo, path string, cred Credential) (*http.Request, error) {
	base, err := g.apiRoot(repo)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", g.authHeader(cred))
	return req, nil
}

// RepositoryMetadata reads GET /repos/{owner}/{repo}. Gitea has no topics or
// dominant-language field on the repository object, so those facts are simply
// omitted here rather than reported as absent. The size is in kilobytes, so it
// is shifted to bytes.
func (g *discoveryGiteaForge) RepositoryMetadata(ctx context.Context, repo Repo, cred Credential) (DiscoveryRepositoryMetadata, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return DiscoveryRepositoryMetadata{}, err
	}
	req, err := g.newRequest(repo, base, cred)
	if err != nil {
		return DiscoveryRepositoryMetadata{}, err
	}
	var resp struct {
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		Description   string `json:"description"`
		Size          int64  `json:"size"` // KB
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return DiscoveryRepositoryMetadata{}, err
	}
	visibility := "public"
	if resp.Private {
		visibility = "private"
	}
	return DiscoveryRepositoryMetadata{
		DefaultBranch: resp.DefaultBranch,
		Visibility:    visibility,
		Description:   resp.Description,
		SizeBytes:     resp.Size << 10,
	}, nil
}

// Branches reads GET /repos/{owner}/{repo}/branches. Gitea names the head
// revision commit.id; older shapes use commit.sha, so both are accepted.
func (g *discoveryGiteaForge) Branches(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryBranch, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, base+"/branches?limit=100", cred)
	if err != nil {
		return nil, err
	}
	var resp []struct {
		Name   string `json:"name"`
		Commit struct {
			ID  string `json:"id"`
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	branches := make([]DiscoveryBranch, 0, len(resp))
	for _, b := range resp {
		sha := b.Commit.ID
		if sha == "" {
			sha = b.Commit.SHA
		}
		branches = append(branches, DiscoveryBranch{Name: b.Name, SHA: sha})
	}
	return branches, nil
}

// PullRequests reads GET /repos/{owner}/{repo}/pulls?state=open. Gitea names
// the PR identifier "index"; "number" is accepted too because both appear in
// the wild.
func (g *discoveryGiteaForge) PullRequests(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryPullRequest, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, base+"/pulls?state=open&limit=100", cred)
	if err != nil {
		return nil, err
	}
	var resp []struct {
		Number int    `json:"number"`
		Index  int    `json:"index"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	prs := make([]DiscoveryPullRequest, 0, len(resp))
	for _, p := range resp {
		number := p.Number
		if number == 0 {
			number = p.Index
		}
		prs = append(prs, DiscoveryPullRequest{
			Number:  number,
			State:   p.State,
			Title:   p.Title,
			HeadSHA: p.Head.SHA,
		})
	}
	return prs, nil
}

// Checks reads GET /repos/{owner}/{repo}/commits/{ref}/statuses. A commit
// status carries its identity in "context" and its conclusion in "status"; the
// newest status per context wins, so an older pass cannot mask a newer failure.
func (g *discoveryGiteaForge) Checks(ctx context.Context, repo Repo, ref string, cred Credential) ([]DiscoveryCheck, error) {
	statuses, err := g.statuses(ctx, repo, ref, cred)
	if err != nil {
		return nil, err
	}
	newest := newestStatusPerContext(statuses)
	checks := make([]DiscoveryCheck, 0, len(newest))
	for _, s := range newest {
		checks = append(checks, DiscoveryCheck{
			Name:       s.Context,
			Conclusion: s.Status,
			Status:     s.Status,
			URL:        s.TargetURL,
			Context:    s.Context,
		})
	}
	return checks, nil
}

// CheckLogs reports what a Gitea commit status offers: the status identity and
// its target link, bounded by checkLogLimit. A commit status has no generic log
// endpoint, so no further request is made and nothing is invented.
func (g *discoveryGiteaForge) CheckLogs(ctx context.Context, repo Repo, check DiscoveryCheck, cred Credential) (string, error) {
	// Still require a configured instance: an unconfigured probe must report
	// the configuration gap, not a plausible-looking summary.
	if _, err := g.apiRoot(repo); err != nil {
		return "", err
	}
	return discoveryCheckSummary(check), nil
}

// giteaStatus is one commit status as Gitea reports it.
type giteaStatus struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// statuses reads GET /repos/{owner}/{repo}/commits/{ref}/statuses.
func (g *discoveryGiteaForge) statuses(ctx context.Context, repo Repo, ref string, cred Credential) ([]giteaStatus, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, base+"/commits/"+url.PathEscape(ref)+"/statuses", cred)
	if err != nil {
		return nil, err
	}
	var resp []giteaStatus
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// newestStatusPerContext keeps the highest-id status for each context. Contexts
// are emitted in sorted order so the result is deterministic even when a forge
// returns statuses in an arbitrary order.
func newestStatusPerContext(statuses []giteaStatus) []giteaStatus {
	winner := make(map[string]giteaStatus, len(statuses))
	contexts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		current, seen := winner[s.Context]
		if !seen {
			contexts = append(contexts, s.Context)
		}
		if !seen || s.ID > current.ID {
			winner[s.Context] = s
		}
	}
	sort.Strings(contexts)
	out := make([]giteaStatus, 0, len(contexts))
	for _, context := range contexts {
		out = append(out, winner[context])
	}
	return out
}

// compile-time proof that the probe keeps its promise: the Gitea adapter
// implements exactly the read-only discovery surface.
var _ DiscoveryForge = (*discoveryGiteaForge)(nil)
