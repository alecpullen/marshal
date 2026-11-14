package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// previewGiteaForge reads a Gitea/Forgejo instance's API for preview
// environments. It shares the auth and base conventions of giteaForge —
// "<host>/api/v1" as the base and "token <pat>" as the header — but implements
// only the read-only preview surface.
//
// Neither family serves a deployment or environment resource today, so the
// instance answers 404 and the adapter reports exactly that. The paths and
// shapes below are the natural mirror of GitHub's under the instance's own API
// root: the adapter asks the instance rather than consulting a family table,
// which is what makes a future instance that does serve them work unchanged.
type previewGiteaForge struct {
	client *http.Client
}

// apiRoot returns the instance API root. The versioned prefix is already part
// of Repo.APIBase, exactly as giteaForge.apiBase returns it.
//
// An empty base is reported as errDiscoveryUnconfigured with a detail naming the
// setting, before any request is built: a request to an empty base would hit a
// URL that cannot exist and would classify as a transport failure rather than
// as the configuration gap it is.
func (g *previewGiteaForge) apiRoot(repo Repo) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(repo.APIBase), "/")
	if base == "" {
		return "", fmt.Errorf(
			"gitea repository %q: the instance API base is not configured; set apiBase to https://<host>/api/v1: %w",
			repo.URL, errDiscoveryUnconfigured)
	}
	return base, nil
}

// authHeader returns the Authorization header value for a Gitea PAT.
func (g *previewGiteaForge) authHeader(cred Credential) string {
	return "token " + cred.literal
}

// repoPath returns "/repos/{owner}/{repo}" for the repo's remote URL.
func (g *previewGiteaForge) repoPath(repo Repo) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	return "/repos/" + owner + "/" + name, nil
}

// newRequest builds one authenticated GET against the instance API root.
func (g *previewGiteaForge) newRequest(repo Repo, path string, cred Credential) (*http.Request, error) {
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

// Environments reads GET /repos/{owner}/{repo}/environments, following pages
// until a short page or the page bound.
func (g *previewGiteaForge) Environments(ctx context.Context, repo Repo, cred Credential) ([]PreviewEnvironment, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	environments := make([]PreviewEnvironment, 0, discoveryForgePageSize)
	for page := 1; page <= discoveryForgeMaxPages; page++ {
		req, err := g.newRequest(repo, fmt.Sprintf("%s/environments?limit=%d&page=%d",
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
// filters, following pages until a short page or the page bound. An empty
// filter dimension is omitted from the query.
func (g *previewGiteaForge) Deployments(ctx context.Context, repo Repo, filter PreviewFilter, cred Credential) ([]PreviewDeployment, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	deployments := make([]PreviewDeployment, 0, discoveryForgePageSize)
	for page := 1; page <= discoveryForgeMaxPages; page++ {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(discoveryForgePageSize))
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
// following pages until a short page or the page bound.
func (g *previewGiteaForge) DeploymentStatuses(ctx context.Context, repo Repo, deploymentID int64, cred Credential) ([]PreviewDeploymentStatus, error) {
	base, err := g.repoPath(repo)
	if err != nil {
		return nil, err
	}
	statuses := make([]PreviewDeploymentStatus, 0, discoveryForgePageSize)
	for page := 1; page <= discoveryForgeMaxPages; page++ {
		req, err := g.newRequest(repo, fmt.Sprintf("%s/deployments/%d/statuses?limit=%d&page=%d",
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
var _ PreviewForge = (*previewGiteaForge)(nil)
