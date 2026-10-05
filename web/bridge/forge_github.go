package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// forgeAPIError carries the HTTP response so callers (the poller's
// rate-limit logic) can inspect headers like Retry-After. The Error()
// string never includes request headers, which carry the credential.
type forgeAPIError struct {
	statusCode int
	message    string
	retryAfter string // raw Retry-After header value, "" if absent
}

func (e *forgeAPIError) Error() string {
	if e.message != "" {
		return fmt.Sprintf("forge API returned %d: %s", e.statusCode, e.message)
	}
	return fmt.Sprintf("forge API returned %d", e.statusCode)
}

// retryAfterDuration parses the Retry-After header on a forgeAPIError.
// Returns ok=false when the error is not a forgeAPIError or the header
// is absent/unparseable.
func retryAfterDuration(err error) (time.Duration, bool) {
	var fae *forgeAPIError
	if !errors.As(err, &fae) || fae.retryAfter == "" {
		return 0, false
	}
	// Retry-After can be seconds or an HTTP-date; handle the common
	// seconds form.
	if secs, perr := strconv.Atoi(fae.retryAfter); perr == nil {
		return time.Duration(secs) * time.Second, true
	}
	return 0, false
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
		msg := ""
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			msg = apiErr.Message
		}
		return &forgeAPIError{
			statusCode: resp.StatusCode,
			message:    msg,
			retryAfter: resp.Header.Get("Retry-After"),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode forge response: %w", err)
	}
	return nil
}

// RepoSize reports the repository's size in bytes. GitHub reports the
// size in kilobytes on the repository object, so it is shifted to bytes.
func (g *githubForge) RepoSize(ctx context.Context, repo Repo, cred Credential) (int64, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequest("GET", g.apiBase(repo)+"/repos/"+owner+"/"+name, nil)
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Authorization", g.authHeader(cred))
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	var resp struct {
		Size int64 `json:"size"` // KB
	}
	if err := doJSON(ctx, g.client, httpReq, &resp); err != nil {
		return 0, err
	}
	return resp.Size << 10, nil
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
	u := g.apiBase(repo) + "/repos/" + owner + "/" + name + "/issues?state=open&per_page=100"
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

// githubJobRe finds the Actions job id in a check run's details_url.
var githubJobRe = regexp.MustCompile(`/actions/runs/\d+/job/(\d+)`)

type githubPR struct {
	Number    int       `json:"number"`
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

func (p githubPR) info() PRInfo {
	i := PRInfo{
		Number: p.Number, Title: p.Title, URL: p.HTMLURL,
		HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseRef: p.Base.Ref,
		Author: p.User.Login, Draft: p.Draft, UpdatedAt: p.UpdatedAt,
	}
	for _, l := range p.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i
}

// filterPRs applies the client-side parts of a PRQuery.
func filterPRs(prs []PRInfo, q PRQuery) []PRInfo {
	out := prs[:0:0]
	for _, p := range prs {
		if q.Label != "" && !containsString(p.Labels, q.Label) {
			continue
		}
		if !q.Since.IsZero() && p.UpdatedAt.Before(q.Since) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (g *githubForge) newRequest(repo Repo, method, path string, body io.Reader, cred Credential) (*http.Request, error) {
	req, err := http.NewRequest(method, g.apiBase(repo)+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", g.authHeader(cred))
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (g *githubForge) ListPRs(ctx context.Context, repo Repo, q PRQuery, cred Credential) ([]PRInfo, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/pulls?state=open&per_page=100&sort=updated&direction=desc", nil, cred)
	if err != nil {
		return nil, err
	}
	var resp []githubPR
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	prs := make([]PRInfo, 0, len(resp))
	for _, p := range resp {
		prs = append(prs, p.info())
	}
	return filterPRs(prs, q), nil
}

func (g *githubForge) GetPR(ctx context.Context, repo Repo, number int, cred Credential) (PRInfo, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return PRInfo{}, err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/pulls/"+strconv.Itoa(number), nil, cred)
	if err != nil {
		return PRInfo{}, err
	}
	var resp githubPR
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return PRInfo{}, err
	}
	return resp.info(), nil
}

func (g *githubForge) PostReview(ctx context.Context, repo Repo, number int, review Review, cred Credential) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	type comment struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	comments := make([]comment, 0, len(review.Comments))
	for _, c := range review.Comments {
		comments = append(comments, comment{Path: c.Path, Line: c.Line, Side: "RIGHT", Body: c.Body})
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

func (g *githubForge) ListFailedChecks(ctx context.Context, repo Repo, ref string, cred Credential) ([]CheckInfo, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return nil, err
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/commits/"+url.PathEscape(ref)+"/check-runs?per_page=100", nil, cred)
	if err != nil {
		return nil, err
	}
	var resp struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			DetailsURL string `json:"details_url"`
		} `json:"check_runs"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return nil, err
	}
	var out []CheckInfo
	for _, c := range resp.CheckRuns {
		if c.Conclusion != "failure" {
			continue
		}
		ci := CheckInfo{ID: c.ID, Name: c.Name, Conclusion: c.Conclusion, URL: c.HTMLURL, Ref: ref}
		if m := githubJobRe.FindStringSubmatch(c.DetailsURL); m != nil {
			ci.JobID, _ = strconv.ParseInt(m[1], 10, 64)
		}
		out = append(out, ci)
	}
	return out, nil
}

func (g *githubForge) CheckLog(ctx context.Context, repo Repo, check CheckInfo, cred Credential) (string, error) {
	owner, name, err := parseOwnerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	if check.JobID != 0 {
		req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/actions/jobs/"+strconv.FormatInt(check.JobID, 10)+"/logs", nil, cred)
		if err != nil {
			return "", err
		}
		// The API answers with a redirect to a signed log URL. The client
		// follows it and drops the Authorization header across hosts.
		return doTail(ctx, g.client, req, checkLogLimit)
	}
	req, err := g.newRequest(repo, "GET", "/repos/"+owner+"/"+name+"/check-runs/"+strconv.FormatInt(check.ID, 10), nil, cred)
	if err != nil {
		return "", err
	}
	var resp struct {
		Output struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
	}
	if err := doJSON(ctx, g.client, req, &resp); err != nil {
		return "", err
	}
	text := resp.Output.Text
	if text == "" {
		text = strings.TrimSpace(resp.Output.Title + "\n" + resp.Output.Summary)
	}
	return tailString(text, checkLogLimit), nil
}

// doTail performs one call and returns the last limit bytes of a plain
// text body. Errors carry the status only, as doJSON's do.
func doTail(ctx context.Context, client *http.Client, req *http.Request, limit int) (string, error) {
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("forge API call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &forgeAPIError{statusCode: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
	}
	// Keep a rolling window rather than the whole log in memory.
	buf := make([]byte, 0, limit)
	chunk := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if len(buf) > 2*limit {
			buf = append(buf[:0], buf[len(buf)-limit:]...)
		}
		if rerr != nil {
			if rerr != io.EOF {
				return "", fmt.Errorf("read forge response: %w", rerr)
			}
			break
		}
	}
	return tailString(string(buf), limit), nil
}
