package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// githubForge implements Forge against the GitHub REST API.
type githubForge struct {
	client *http.Client
}

func newGitHubForge(client *http.Client) Forge {
	return &githubForge{client: client}
}

// apiBase returns the API root, defaulting to the public GitHub API.
func (g *githubForge) apiBase(repo Repo) string {
	if repo.APIBase != "" {
		return repo.APIBase
	}
	return "https://api.github.com"
}

// authHeader returns the Authorization header value for a GitHub PAT.
func (g *githubForge) authHeader(cred Credential) string {
	return "Bearer " + cred.literal
}

// doJSON performs one API call and decodes into out.
//
// The error path deliberately reports the forge's own message and the
// status code, never the request headers: those carry the credential,
// and an error string tends to end up in a log.
func doJSON(ctx context.Context, client *http.Client, req *http.Request, out any) error {
	req = req.WithContext(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("forge API call: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// Try to extract the forge's own error message.
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return fmt.Errorf("forge API returned %d: %s", resp.StatusCode, apiErr.Message)
		}
		return fmt.Errorf("forge API returned %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode forge response: %w", err)
	}
	return nil
}

func (g *githubForge) CreatePR(ctx context.Context, repo Repo, req PRRequest, cred Credential) (PR, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return PR{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"title": req.Title,
		"body":  req.Body,
		"head":  req.Head,
		"base":  req.Base,
		"draft": req.Draft,
	})
	httpReq, err := http.NewRequest("POST", g.apiBase(repo)+"/repos/"+owner+"/"+name+"/pulls", bytes.NewReader(body))
	if err != nil {
		return PR{}, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	var resp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := doJSON(ctx, g.client, httpReq, &resp); err != nil {
		return PR{}, err
	}
	return PR{Number: resp.Number, URL: resp.HTMLURL}, nil
}

func (g *githubForge) GetIssue(ctx context.Context, repo Repo, number int, cred Credential) (Issue, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return Issue{}, err
	}
	httpReq, err := http.NewRequest("GET", g.apiBase(repo)+"/repos/"+owner+"/"+name+"/issues/"+fmt.Sprint(number), nil)
	if err != nil {
		return Issue{}, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	var resp struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := doJSON(ctx, g.client, httpReq, &resp); err != nil {
		return Issue{}, err
	}
	i := Issue{Number: resp.Number, Title: resp.Title, Body: resp.Body, URL: resp.HTMLURL}
	for _, l := range resp.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i, nil
}

func (g *githubForge) ListIssues(ctx context.Context, repo Repo, q IssueQuery, cred Credential) ([]Issue, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return nil, err
	}
	u := g.apiBase(repo) + "/repos/" + owner + "/" + name + "/issues?state=open"
	if q.Label != "" {
		u += "&labels=" + q.Label
	}
	if !q.Since.IsZero() {
		u += "&since=" + q.Since.UTC().Format("2006-01-02T15:04:05Z")
	}
	httpReq, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	var resp []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := doJSON(ctx, g.client, httpReq, &resp); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(resp))
	for _, r := range resp {
		i := Issue{Number: r.Number, Title: r.Title, Body: r.Body, URL: r.HTMLURL}
		for _, l := range r.Labels {
			i.Labels = append(i.Labels, l.Name)
		}
		issues = append(issues, i)
	}
	return issues, nil
}

func (g *githubForge) CommentIssue(ctx context.Context, repo Repo, number int, body string, cred Credential) error {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	httpReq, err := http.NewRequest("POST", g.apiBase(repo)+"/repos/"+owner+"/"+name+"/issues/"+fmt.Sprint(number)+"/comments", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	return doJSON(ctx, g.client, httpReq, nil)
}
