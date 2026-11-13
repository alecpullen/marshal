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

// factoryPage follows only same-origin pagination, so credentials cannot leak via Link.
func (g *githubForge) factoryPage(ctx context.Context, repo Repo, path string, cred Credential, out any) (string, error) {
	req, err := g.newRequest(repo, "GET", path, nil, cred)
	if err != nil {
		return "", err
	}
	resp, err := g.client.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &forgeAPIError{statusCode: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
	}
	if err = json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("decode factory evidence: %w", err)
	}
	for _, link := range strings.Split(resp.Header.Get("Link"), ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 || !strings.Contains(strings.Join(parts[1:], ";"), `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(parts[0]), "<>"))
		if err != nil {
			return "", err
		}
		u = req.URL.ResolveReference(u)
		base, _ := url.Parse(g.apiBase(repo))
		if u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(u.Path, strings.TrimSuffix(base.Path, "/")+"/") {
			return "", fmt.Errorf("unsafe factory pagination URL")
		}
		return strings.TrimPrefix(u.String(), strings.TrimSuffix(g.apiBase(repo), "/")), nil
	}
	return "", nil
}
func factoryRepoPath(repo Repo) (string, error) {
	o, n, e := parseOwnerRepo(repo.URL)
	return "/repos/" + url.PathEscape(o) + "/" + url.PathEscape(n), e
}

type factoryRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	SHA        string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"details_url"`
	Started    time.Time `json:"started_at"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

func (g *githubForge) ObserveFactoryCI(ctx context.Context, repo Repo, sha string, cred Credential) (FactoryCI, error) {
	ci := FactoryCI{HeadSHA: sha, ObservedAt: time.Now().UTC()}
	root, err := factoryRepoPath(repo)
	if err != nil {
		return ci, err
	}
	if sha == "" {
		return ci, fmt.Errorf("missing head SHA")
	}
	runs := map[FactoryCheckKey]factoryRun{}
	path := root + "/commits/" + url.PathEscape(sha) + "/check-runs?per_page=100&filter=all"
	visited := map[string]bool{}
	count := 0
	total := 0
	for path != "" {
		if visited[path] {
			return ci, fmt.Errorf("pagination cycle")
		}
		visited[path] = true
		var page struct {
			Total int          `json:"total_count"`
			Runs  []factoryRun `json:"check_runs"`
		}
		next, e := g.factoryPage(ctx, repo, path, cred, &page)
		if e != nil {
			return ci, e
		}
		if page.Runs == nil {
			return ci, fmt.Errorf("missing check_runs evidence")
		}
		count += len(page.Runs)
		if page.Total > total {
			total = page.Total
		}
		for _, r := range page.Runs {
			k := FactoryCheckKey{Kind: "check_run", Name: r.Name, AppID: r.App.ID}
			old, ok := runs[k]
			// Check run IDs reflect creation order; a queued retry may not have started_at yet.
			if !ok || r.ID > old.ID {
				runs[k] = r
			}
		}
		path = next
	}
	if count < total {
		return ci, fmt.Errorf("truncated check runs: got %d of %d", count, total)
	}
	for k, r := range runs {
		c := FactoryCheck{Key: k, ID: strconv.FormatInt(r.ID, 10), SHA: r.SHA, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL, ObservedAt: ci.ObservedAt}
		if r.Status == "completed" && r.Conclusion == "failure" {
			check := CheckInfo{ID: r.ID, Name: r.Name, Conclusion: r.Conclusion, URL: r.URL, Ref: sha}
			if m := githubJobRe.FindStringSubmatch(r.URL); len(m) > 1 {
				check.JobID, _ = strconv.ParseInt(m[1], 10, 64)
			}
			log, e := g.CheckLog(ctx, repo, check, cred)
			if e != nil {
				return ci, e
			}
			c.LogTail = log
		}
		ci.Checks = append(ci.Checks, c)
	}
	type status struct {
		ID      int64     `json:"id"`
		Context string    `json:"context"`
		State   string    `json:"state"`
		URL     string    `json:"target_url"`
		Created time.Time `json:"created_at"`
	}
	statuses := map[string]status{}
	path = root + "/commits/" + url.PathEscape(sha) + "/statuses?per_page=100"
	visited = map[string]bool{}
	for path != "" {
		if visited[path] {
			return ci, fmt.Errorf("pagination cycle")
		}
		visited[path] = true
		var page []status
		next, e := g.factoryPage(ctx, repo, path, cred, &page)
		if e != nil {
			return ci, e
		}
		if page == nil {
			return ci, fmt.Errorf("missing commit status evidence")
		}
		for _, s := range page {
			old, ok := statuses[s.Context]
			if !ok || s.Created.After(old.Created) || (s.Created.Equal(old.Created) && s.ID > old.ID) {
				statuses[s.Context] = s
			}
		}
		path = next
	}
	for name, s := range statuses {
		state := "completed"
		conclusion := s.State
		if s.State == "pending" {
			state = "pending"
			conclusion = ""
		}
		ci.Checks = append(ci.Checks, FactoryCheck{Key: FactoryCheckKey{Kind: "commit_status", Name: name}, ID: strconv.FormatInt(s.ID, 10), SHA: sha, Status: state, Conclusion: conclusion, URL: s.URL, ObservedAt: ci.ObservedAt})
	}
	ci.Complete = true
	return ci, nil
}

type factoryGitHubPR struct {
	Number   int        `json:"number"`
	URL      string     `json:"html_url"`
	Node     string     `json:"node_id"`
	State    string     `json:"state"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	Draft    bool       `json:"draft"`
	Head     struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

func (p factoryGitHubPR) factory() FactoryPR {
	state := p.State
	if p.Merged || p.MergedAt != nil {
		state = "merged"
	}
	return FactoryPR{Number: p.Number, URL: p.URL, NodeID: p.Node, State: state, Draft: p.Draft, HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseRef: p.Base.Ref, BaseSHA: p.Base.SHA}
}
func (g *githubForge) FindFactoryPR(ctx context.Context, repo Repo, head, base string, cred Credential) (*FactoryPR, error) {
	root, err := factoryRepoPath(repo)
	if err != nil {
		return nil, err
	}
	owner, _, _ := parseOwnerRepo(repo.URL)
	path := root + "/pulls?state=all&per_page=100&head=" + url.QueryEscape(owner+":"+head) + "&base=" + url.QueryEscape(base)
	var found *FactoryPR
	seen := map[string]bool{}
	for path != "" {
		if seen[path] {
			return nil, fmt.Errorf("pagination cycle")
		}
		seen[path] = true
		var page []factoryGitHubPR
		next, e := g.factoryPage(ctx, repo, path, cred, &page)
		if e != nil {
			return nil, e
		}
		for _, p := range page {
			if p.Head.Ref != head || p.Base.Ref != base {
				continue
			}
			if p.Head.Repo.FullName != "" && !strings.EqualFold(p.Head.Repo.FullName, strings.TrimPrefix(root, "/repos/")) {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("ambiguous factory PR for %s", head)
			}
			v := p.factory()
			found = &v
		}
		path = next
	}
	return found, nil
}
func (g *githubForge) GetFactoryPR(ctx context.Context, repo Repo, number int, cred Credential) (FactoryPR, error) {
	root, err := factoryRepoPath(repo)
	if err != nil {
		return FactoryPR{}, err
	}
	var p factoryGitHubPR
	_, err = g.factoryPage(ctx, repo, root+"/pulls/"+strconv.Itoa(number), cred, &p)
	if err != nil {
		return FactoryPR{}, err
	}
	if p.Number != number || p.Node == "" {
		return FactoryPR{}, fmt.Errorf("incomplete factory PR response")
	}
	return p.factory(), nil
}
func (g *githubForge) SetFactoryDraft(ctx context.Context, repo Repo, pr FactoryPR, draft bool, cred Credential) error {
	if pr.NodeID == "" {
		return fmt.Errorf("factory PR node ID required")
	}
	mutation := "markPullRequestReadyForReview"
	if draft {
		mutation = "convertPullRequestToDraft"
	}
	payload, _ := json.Marshal(map[string]any{"query": "mutation($id: ID!) { mutation: " + mutation + "(input:{pullRequestId:$id}) { pullRequest { id isDraft } } }", "variables": map[string]string{"id": pr.NodeID}})
	// Enterprise REST roots end in /api/v3; their GraphQL endpoint is /api/graphql.
	endpoint := "/graphql"
	apiRepo := repo
	if strings.HasSuffix(g.apiBase(repo), "/api/v3") {
		apiRepo.APIBase = strings.TrimSuffix(g.apiBase(repo), "/v3")
		endpoint = "/graphql"
	}
	req, err := g.newRequest(apiRepo, "POST", endpoint, bytes.NewReader(payload), cred)
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			Mutation *struct {
				PR *struct {
					ID    string `json:"id"`
					Draft bool   `json:"isDraft"`
				} `json:"pullRequest"`
			} `json:"mutation"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err = doJSON(ctx, g.client, req, &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 || response.Data.Mutation == nil || response.Data.Mutation.PR == nil {
		return fmt.Errorf("GitHub draft mutation failed or returned no confirmation")
	}
	if response.Data.Mutation.PR.ID != pr.NodeID || response.Data.Mutation.PR.Draft != draft {
		return fmt.Errorf("GitHub draft mutation returned mismatched PR confirmation")
	}
	return nil
}

// UpdateFactoryPRDescription writes the composed description and confirms the
// response names the pull request it was asked to update.
func (g *githubForge) UpdateFactoryPRDescription(ctx context.Context, repo Repo, number int, body string, cred Credential) error {
	root, err := factoryRepoPath(repo)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	req, err := g.newRequest(repo, "PATCH", root+"/pulls/"+strconv.Itoa(number), bytes.NewReader(payload), cred)
	if err != nil {
		return err
	}
	var p factoryGitHubPR
	if err = doJSON(ctx, g.client, req, &p); err != nil {
		return err
	}
	if p.Number != number {
		return fmt.Errorf("GitHub description update returned mismatched pull request confirmation")
	}
	return nil
}
