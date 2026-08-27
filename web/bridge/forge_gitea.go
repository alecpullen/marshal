package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// The Gitea family (Gitea and Forgejo, which forked from it and keeps
// API compatibility) mirrors GitHub's route shape under an /api/v1
// prefix. Three things differ, and only three:
//
//  1. Base URL       <host>/api/v1        vs https://api.github.com
//  2. Auth header    "token <pat>"        vs "Bearer <pat>"
//  3. PR identifier  "index"              vs "number"
//
// Everything else — paths, verbs, the fields we read — is shared, which
// is why one interface serves both without contortion.
type giteaForge struct {
	client *http.Client
}

func newGiteaForge(client *http.Client) Forge {
	return &giteaForge{client: client}
}

// apiBase returns the API root. Gitea requires an explicit APIBase
// (e.g. https://code.example.com/api/v1); there is no public default.
func (g *giteaForge) apiBase(repo Repo) string {
	if repo.APIBase != "" {
		return repo.APIBase
	}
	return ""
}

func (g *giteaForge) authHeader(cred Credential) string {
	return "token " + cred.literal
}

func (g *giteaForge) CreatePR(ctx context.Context, repo Repo, req PRRequest, cred Credential) (PR, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return PR{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"title": req.Title,
		"body":  req.Body,
		"head":  req.Head,
		"base":  req.Base,
	})
	httpReq, err := http.NewRequest("POST", g.apiBase(repo)+"/repos/"+owner+"/"+name+"/pulls", bytes.NewReader(body))
	if err != nil {
		return PR{}, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	httpReq.Header.Set("Content-Type", "application/json")
	// Gitea names the PR identifier "index", not "number".
	var resp struct {
		Index   int    `json:"index"`
		HTMLURL string `json:"html_url"`
	}
	if err := doJSON(ctx, g.client, httpReq, &resp); err != nil {
		return PR{}, err
	}
	return PR{Number: resp.Index, URL: resp.HTMLURL}, nil
}

func (g *giteaForge) GetIssue(ctx context.Context, repo Repo, number int, cred Credential) (Issue, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return Issue{}, err
	}
	httpReq, err := http.NewRequest("GET", g.apiBase(repo)+"/repos/"+owner+"/"+name+"/issues/"+fmt.Sprint(number), nil)
	if err != nil {
		return Issue{}, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
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

func (g *giteaForge) ListIssues(ctx context.Context, repo Repo, q IssueQuery, cred Credential) ([]Issue, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return nil, err
	}
	u := g.apiBase(repo) + "/repos/" + owner + "/" + name + "/issues?state=open&type=issues&per_page=100"
	if q.Label != "" {
		u += "&labels=" + url.QueryEscape(q.Label)
	}
	if !q.Since.IsZero() {
		u += "&since=" + q.Since.UTC().Format("2006-01-02T15:04:05Z")
	}
	httpReq, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
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

func (g *giteaForge) CommentIssue(ctx context.Context, repo Repo, number int, body string, cred Credential) error {
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
	httpReq.Header.Set("Content-Type", "application/json")
	return doJSON(ctx, g.client, httpReq, nil)
}
