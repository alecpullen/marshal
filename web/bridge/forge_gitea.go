package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

// RepoSize reports the repository's size in bytes. Gitea reports the
// size in kilobytes on the repository object, so it is shifted to bytes.
func (g *giteaForge) RepoSize(ctx context.Context, repo Repo, cred Credential) (int64, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequest("GET", g.apiBase(repo)+"/repos/"+owner+"/"+name, nil)
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	var resp struct {
		Size int64 `json:"size"` // KB
	}
	if err := doJSON(ctx, g.client, httpReq, &resp); err != nil {
		return 0, err
	}
	return resp.Size << 10, nil
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

type giteaPR struct {
	Number    int       `json:"number"`
	Index     int       `json:"index"`
	Title     string    `json:"title"`
	HTMLURL   string    `json:"html_url"`
	Draft     bool      `json:"draft"`
	UpdatedAt time.Time `json:"updated_at"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (p giteaPR) info() PRInfo {
	n := p.Number
	if n == 0 {
		n = p.Index
	}
	i := PRInfo{
		Number: n, Title: p.Title, URL: p.HTMLURL,
		HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseRef: p.Base.Ref,
		Author: p.User.Login, Draft: p.Draft, UpdatedAt: p.UpdatedAt,
	}
	// Older Gitea marks a draft with a title prefix instead.
	upper := strings.ToUpper(p.Title)
	if strings.HasPrefix(upper, "WIP:") || strings.HasPrefix(upper, "[WIP]") {
		i.Draft = true
	}
	for _, l := range p.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i
}

func (g *giteaForge) newRequest(repo Repo, method, path string, body io.Reader, cred Credential) (*http.Request, error) {
	req, err := http.NewRequest(method, g.apiBase(repo)+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", g.authHeader(cred))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (g *giteaForge) ListPRs(ctx context.Context, repo Repo, q PRQuery, cred Credential) ([]PRInfo, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/pulls?state=open&limit=50", nil, cred)
	if err != nil {
		return nil, err
	}
	var resp []giteaPR
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	prs := make([]PRInfo, 0, len(resp))
	for _, p := range resp {
		prs = append(prs, p.info())
	}
	return filterPRs(prs, q), nil
}

func (g *giteaForge) GetPR(ctx context.Context, repo Repo, number int, cred Credential) (PRInfo, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return PRInfo{}, err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/pulls/"+strconv.Itoa(number), nil, cred)
	if err != nil {
		return PRInfo{}, err
	}
	var resp giteaPR
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return PRInfo{}, err
	}
	return resp.info(), nil
}

func (g *giteaForge) PostReview(ctx context.Context, repo Repo, number int, review Review, cred Credential) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	type comment struct {
		Path        string `json:"path"`
		NewPosition int    `json:"new_position"`
		Body        string `json:"body"`
	}
	comments := make([]comment, 0, len(review.Comments))
	for _, c := range review.Comments {
		comments = append(comments, comment{Path: c.Path, NewPosition: c.Line, Body: c.Body})
	}
	payload, _ := json.Marshal(map[string]any{"body": review.Body, "event": "COMMENT", "comments": comments})
	req, err := g.newRequest(repo, "POST", "/repos/"+owner+"/"+name+"/pulls/"+strconv.Itoa(number)+"/reviews", bytes.NewReader(payload), cred)
	if err != nil {
		return "", err
	}
	var resp struct {
		HTMLURL string `json:"html_url"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return "", err
	}
	return resp.HTMLURL, nil
}

func (g *giteaForge) ListFailedChecks(ctx context.Context, repo Repo, ref string, cred Credential) ([]CheckInfo, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/commits/"+url.PathEscape(ref)+"/statuses", nil, cred)
	if err != nil {
		return nil, err
	}
	var resp []struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	// A context can report several times; only its newest status counts.
	latest := map[string]int{}
	for i, s := range resp {
		if j, ok := latest[s.Context]; !ok || s.ID > resp[j].ID {
			latest[s.Context] = i
		}
	}
	var out []CheckInfo
	for i, s := range resp {
		if latest[s.Context] != i || s.Status != "failure" {
			continue
		}
		out = append(out, CheckInfo{ID: s.ID, Name: s.Context, Conclusion: s.Status, URL: s.TargetURL, Ref: ref})
	}
	return out, nil
}

// CheckLog returns the status description and its link: a Gitea commit
// status has no generic log endpoint.
func (g *giteaForge) CheckLog(ctx context.Context, repo Repo, check CheckInfo, cred Credential) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/commits/"+url.PathEscape(check.Ref)+"/statuses", nil, cred)
	if err != nil {
		return "", err
	}
	var resp []struct {
		ID          int64  `json:"id"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return "", err
	}
	for _, s := range resp {
		if s.ID == check.ID {
			text := s.Description
			if s.TargetURL != "" {
				text += "\n" + s.TargetURL
			}
			return tailString(strings.TrimSpace(text), checkLogLimit), nil
		}
	}
	return tailString(strings.TrimSpace(check.Name+"\n"+check.URL), checkLogLimit), nil
}
