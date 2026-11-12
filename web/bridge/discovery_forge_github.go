package bridge

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// discoveryGitHubForge reads GitHub's REST API for discovery. It shares the
// auth and base conventions of githubForge but implements only the read-only
// probe surface: nothing here can create, comment, or change a repository.
type discoveryGitHubForge struct {
	client *http.Client
}

// apiBase returns the API root, defaulting to the public GitHub API.
func (g *discoveryGitHubForge) apiBase(repo Repo) string {
	if repo.APIBase != "" {
		return repo.APIBase
	}
	return "https://api.github.com"
}

// authHeader returns the Authorization header value for a GitHub PAT.
func (g *discoveryGitHubForge) authHeader(cred Credential) string {
	return "Bearer " + cred.literal
}

// repoPath returns "/repos/{owner}/{repo}" for the repo's remote URL.
func (g *discoveryGitHubForge) repoPath(repo Repo) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	return "/repos/" + owner + "/" + name, nil
}

// newRequest builds one authenticated GET against the repo's API root.
func (g *discoveryGitHubForge) newRequest(repo Repo, path string, cred Credential) (*http.Request, error) {
	req, err := http.NewRequest("GET", g.apiBase(repo)+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", g.authHeader(cred))
	req.Header.Set("Accept", "application/vnd.github+json")
	return req, nil
}

// RepositoryMetadata reads GET /repos/{owner}/{repo}. GitHub reports the size
// in kilobytes, so it is shifted to bytes.
func (g *discoveryGitHubForge) RepositoryMetadata(ctx context.Context, repo Repo, cred Credential) (DiscoveryRepositoryMetadata, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return DiscoveryRepositoryMetadata{}, err
	}
	req, err := g.newRequest(repo, base, cred)
	if err != nil {
		return DiscoveryRepositoryMetadata{}, err
	}
	var resp struct {
		DefaultBranch string   `json:"default_branch"`
		Private       bool     `json:"private"`
		Visibility    string   `json:"visibility"`
		Description   string   `json:"description"`
		Topics        []string `json:"topics"`
		Language      string   `json:"language"`
		Size          int64    `json:"size"` // KB
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return DiscoveryRepositoryMetadata{}, err
	}
	visibility := resp.Visibility
	if visibility == "" {
		visibility = "public"
		if resp.Private {
			visibility = "private"
		}
	}
	return DiscoveryRepositoryMetadata{
		DefaultBranch: resp.DefaultBranch,
		Visibility:    visibility,
		Description:   resp.Description,
		Topics:        resp.Topics,
		Language:      resp.Language,
		SizeBytes:     resp.Size << 10,
	}, nil
}

// Branches reads GET /repos/{owner}/{repo}/branches.
func (g *discoveryGitHubForge) Branches(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryBranch, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, base+"/branches?per_page=100", cred)
	if err != nil {
		return nil, err
	}
	var resp []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	branches := make([]DiscoveryBranch, 0, len(resp))
	for _, b := range resp {
		branches = append(branches, DiscoveryBranch{Name: b.Name, SHA: b.Commit.SHA})
	}
	return branches, nil
}

// PullRequests reads GET /repos/{owner}/{repo}/pulls?state=open.
func (g *discoveryGitHubForge) PullRequests(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryPullRequest, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, base+"/pulls?state=open&per_page=100", cred)
	if err != nil {
		return nil, err
	}
	var resp []struct {
		Number int    `json:"number"`
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
		prs = append(prs, DiscoveryPullRequest{
			Number:  p.Number,
			State:   p.State,
			Title:   p.Title,
			HeadSHA: p.Head.SHA,
		})
	}
	return prs, nil
}

// Checks reads GET /repos/{owner}/{repo}/commits/{ref}/check-runs. Every check
// on the ref is reported, not only the failed ones: discovery describes what
// the credential can see, and the caller decides what matters.
func (g *discoveryGitHubForge) Checks(ctx context.Context, repo Repo, ref string, cred Credential) ([]DiscoveryCheck, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, base+"/commits/"+url.PathEscape(ref)+"/check-runs?per_page=100", cred)
	if err != nil {
		return nil, err
	}
	var resp struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Status     string `json:"status"`
			HTMLURL    string `json:"html_url"`
			DetailsURL string `json:"details_url"`
			App        struct {
				ID int64 `json:"id"`
			} `json:"app"`
		} `json:"check_runs"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	checks := make([]DiscoveryCheck, 0, len(resp.CheckRuns))
	for _, c := range resp.CheckRuns {
		check := DiscoveryCheck{
			Name:       c.Name,
			Conclusion: c.Conclusion,
			Status:     c.Status,
			URL:        c.HTMLURL,
			AppID:      c.App.ID,
		}
		// An Actions check run links its job in details_url; that job id is
		// what the log endpoint needs.
		if m := githubJobRe.FindStringSubmatch(c.DetailsURL); m != nil {
			check.JobID, _ = strconv.ParseInt(m[1], 10, 64)
		}
		checks = append(checks, check)
	}
	return checks, nil
}

// CheckLogs returns the tail of an Actions job's log. A check that is not a
// GitHub Actions job has no log endpoint to read, so its identity and link are
// the whole artefact.
func (g *discoveryGitHubForge) CheckLogs(ctx context.Context, repo Repo, check DiscoveryCheck, cred Credential) (string, error) {
	if check.JobID == 0 {
		return discoveryCheckSummary(check), nil
	}
	base, err := g.repoPath(repo)
	if err != nil {
		return "", err
	}
	req, err := g.newRequest(repo, base+"/actions/jobs/"+strconv.FormatInt(check.JobID, 10)+"/logs", cred)
	if err != nil {
		return "", err
	}
	// The API answers with a redirect to a signed log URL. The client follows
	// it and drops the Authorization header across hosts.
	return doTail(ctx, g.client, req, checkLogLimit)
}

// compile-time proof that the probe keeps its promise: the GitHub adapter
// implements exactly the read-only discovery surface.
var _ DiscoveryForge = (*discoveryGitHubForge)(nil)
