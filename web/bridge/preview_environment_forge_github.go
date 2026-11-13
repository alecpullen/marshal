package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// previewGitHubForge reads GitHub's REST API for preview environments. It
// shares the auth and base conventions of githubForge but implements only the
// read-only preview surface: nothing here creates a deployment, posts a status,
// or edits an environment.
type previewGitHubForge struct {
	client *http.Client
}

// apiBase returns the API root, defaulting to the public GitHub API.
func (g *previewGitHubForge) apiBase(repo Repo) string {
	if repo.APIBase != "" {
		return repo.APIBase
	}
	return "https://api.github.com"
}

// authHeader returns the Authorization header value for a GitHub PAT.
func (g *previewGitHubForge) authHeader(cred Credential) string {
	return "Bearer " + cred.literal
}

// repoPath returns "/repos/{owner}/{repo}" for the repo's remote URL.
func (g *previewGitHubForge) repoPath(repo Repo) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	return "/repos/" + owner + "/" + name, nil
}

// newRequest builds one authenticated GET against the repo's API root.
func (g *previewGitHubForge) newRequest(repo Repo, path string, cred Credential) (*http.Request, error) {
	req, err := http.NewRequest("GET", g.apiBase(repo)+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", g.authHeader(cred))
	req.Header.Set("Accept", "application/vnd.github+json")
	return req, nil
}

// previewGitHubBranchPolicy is GitHub's deployment_branch_policy shape. The two
// modes are mutually exclusive; a policy the forge does not report is absent.
type previewGitHubBranchPolicy struct {
	ProtectedBranches    bool `json:"protected_branches"`
	CustomBranchPolicies bool `json:"custom_branch_policies"`
}

// previewBranchPolicy names the branch-policy mode verbatim, or "" when the
// forge reported no policy object.
func previewBranchPolicy(policy *previewGitHubBranchPolicy) string {
	if policy == nil {
		return ""
	}
	switch {
	case policy.ProtectedBranches:
		return "protected_branches"
	case policy.CustomBranchPolicies:
		return "custom_branch_policies"
	}
	return ""
}

// Environments reads GET /repos/{owner}/{repo}/environments, which answers with
// an object carrying an environments array rather than a bare array. It follows
// pages until a short page or the page bound; a full final page is truncation.
func (g *previewGitHubForge) Environments(ctx context.Context, repo Repo, cred Credential) ([]PreviewEnvironment, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	environments := make([]PreviewEnvironment, 0, discoveryForgePageSize)
	for page := 1; page <= discoveryForgeMaxPages; page++ {
		req, err := g.newRequest(repo, fmt.Sprintf("%s/environments?per_page=%d&page=%d",
			base, discoveryForgePageSize, page), cred)
		if err != nil {
			return nil, err
		}
		var resp struct {
			TotalCount   int `json:"total_count"`
			Environments []struct {
				ID                     int64                      `json:"id"`
				Name                   string                     `json:"name"`
				HTMLURL                string                     `json:"html_url"`
				ProtectionRules        []struct{}                 `json:"protection_rules"`
				DeploymentBranchPolicy *previewGitHubBranchPolicy `json:"deployment_branch_policy"`
			} `json:"environments"`
		}
		if err := doJSON(ctx, g.client, req, &resp); err != nil {
			return nil, err
		}
		for _, environment := range resp.Environments {
			environments = append(environments, PreviewEnvironment{
				Name:                environment.Name,
				ID:                  previewIDString(environment.ID),
				HTMLURL:             environment.HTMLURL,
				BranchPolicy:        previewBranchPolicy(environment.DeploymentBranchPolicy),
				ProtectionRuleCount: len(environment.ProtectionRules),
			})
		}
		if len(resp.Environments) < discoveryForgePageSize {
			return environments, nil
		}
		if page == discoveryForgeMaxPages {
			return environments, fmt.Errorf("environments for %s: %w", repo.URL, errDiscoveryTruncated)
		}
	}
	return environments, nil
}

// Deployments reads GET /repos/{owner}/{repo}/deployments with the resolved
// filters, following pages until a short page or the page bound. A filter
// dimension that is empty is omitted from the query, never sent as an empty
// value. The deployment's own description is deliberately not mapped onto the
// row: the row's description is the newest status's, per the contract.
func (g *previewGitHubForge) Deployments(ctx context.Context, repo Repo, filter PreviewFilter, cred Credential) ([]PreviewDeployment, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	deployments := make([]PreviewDeployment, 0, discoveryForgePageSize)
	for page := 1; page <= discoveryForgeMaxPages; page++ {
		query := url.Values{}
		query.Set("per_page", strconv.Itoa(discoveryForgePageSize))
		query.Set("page", strconv.Itoa(page))
		if filter.Ref != "" {
			query.Set("ref", filter.Ref)
		}
		if filter.SHA != "" {
			query.Set("sha", filter.SHA)
		}
		req, err := g.newRequest(repo, base+"/deployments?"+query.Encode(), cred)
		if err != nil {
			return nil, err
		}
		var resp []struct {
			ID                    int64  `json:"id"`
			SHA                   string `json:"sha"`
			Ref                   string `json:"ref"`
			Task                  string `json:"task"`
			Environment           string `json:"environment"`
			OriginalEnvironment   string `json:"original_environment"`
			CreatedAt             string `json:"created_at"`
			UpdatedAt             string `json:"updated_at"`
			TransientEnvironment  bool   `json:"transient_environment"`
			ProductionEnvironment bool   `json:"production_environment"`
		}
		if err := doJSON(ctx, g.client, req, &resp); err != nil {
			return nil, err
		}
		for _, deployment := range resp {
			deployments = append(deployments, PreviewDeployment{
				ID:                  previewIDString(deployment.ID),
				Environment:         deployment.Environment,
				OriginalEnvironment: deployment.OriginalEnvironment,
				Ref:                 deployment.Ref,
				SHA:                 deployment.SHA,
				Task:                deployment.Task,
				Transient:           deployment.TransientEnvironment,
				Production:          deployment.ProductionEnvironment,
				CreatedAt:           deployment.CreatedAt,
				UpdatedAt:           deployment.UpdatedAt,
			})
		}
		if len(resp) < discoveryForgePageSize {
			return deployments, nil
		}
		if page == discoveryForgeMaxPages {
			return deployments, fmt.Errorf("deployments for %s: %w", repo.URL, errDiscoveryTruncated)
		}
	}
	return deployments, nil
}

// DeploymentStatuses reads GET /repos/{owner}/{repo}/deployments/{id}/statuses,
// following pages until a short page or the page bound. The records are the
// provider's own: no state is normalised and no URL is rewritten.
func (g *previewGitHubForge) DeploymentStatuses(ctx context.Context, repo Repo, deploymentID int64, cred Credential) ([]PreviewDeploymentStatus, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	statuses := make([]PreviewDeploymentStatus, 0, discoveryForgePageSize)
	for page := 1; page <= discoveryForgeMaxPages; page++ {
		req, err := g.newRequest(repo, fmt.Sprintf("%s/deployments/%d/statuses?per_page=%d&page=%d",
			base, deploymentID, discoveryForgePageSize, page), cred)
		if err != nil {
			return nil, err
		}
		var resp []struct {
			ID             int64  `json:"id"`
			State          string `json:"state"`
			EnvironmentURL string `json:"environment_url"`
			TargetURL      string `json:"target_url"`
			LogURL         string `json:"log_url"`
			Description    string `json:"description"`
			CreatedAt      string `json:"created_at"`
			UpdatedAt      string `json:"updated_at"`
		}
		if err := doJSON(ctx, g.client, req, &resp); err != nil {
			return nil, err
		}
		for _, status := range resp {
			statuses = append(statuses, PreviewDeploymentStatus{
				ID:             previewIDString(status.ID),
				State:          status.State,
				EnvironmentURL: status.EnvironmentURL,
				TargetURL:      status.TargetURL,
				LogURL:         status.LogURL,
				Description:    status.Description,
				CreatedAt:      status.CreatedAt,
				UpdatedAt:      status.UpdatedAt,
			})
		}
		if len(resp) < discoveryForgePageSize {
			return statuses, nil
		}
		if page == discoveryForgeMaxPages {
			return statuses, fmt.Errorf("deployment statuses for %s: %w", repo.URL, errDiscoveryTruncated)
		}
	}
	return statuses, nil
}

// compile-time proof that the adapter is exactly the read-only preview surface.
var _ PreviewForge = (*previewGitHubForge)(nil)
