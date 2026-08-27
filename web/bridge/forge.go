package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PRRequest is what the exit path asks the forge to create.
type PRRequest struct {
	Title string
	Body  string
	Head  string
	Base  string
	Draft bool
}

// PR is the forge's response to a pull-request creation.
type PR struct {
	Number int
	URL    string
}

// Issue is a forge issue, normalised across providers.
type Issue struct {
	Number int
	Title  string
	Body   string
	URL    string
	Labels []string
}

// IssueQuery filters a list-issues call.
type IssueQuery struct {
	Label string
	Since time.Time
}

// Every method takes the credential explicitly rather than binding one
// at construction: a single client serves many repos, each with its own
// token, and a client that remembered one would silently use the wrong
// repo's credential.
type Forge interface {
	CreatePR(ctx context.Context, repo Repo, req PRRequest, cred Credential) (PR, error)
	GetIssue(ctx context.Context, repo Repo, number int, cred Credential) (Issue, error)
	ListIssues(ctx context.Context, repo Repo, q IssueQuery, cred Credential) ([]Issue, error)
	CommentIssue(ctx context.Context, repo Repo, number int, body string, cred Credential) error
}

// errNoForge signals the documented degradation path: the repo has no
// forge declared, or its credential cannot make HTTP API calls. It is
// not an error worth logging — the caller falls back to URL extraction.
var errNoForge = errors.New("bridge: no forge or no HTTP-capable credential for this repo")

// ForgeFor builds the client for a repo, or reports why none applies.
//
// A repo with no forge declared, or one whose credential cannot make
// HTTP calls, is not an error condition — it is the documented
// degradation path. Callers check this and fall back to S2b's URL
// extraction rather than failing the exit.
func ForgeFor(r Repo, c *http.Client) (Forge, error) {
	switch r.Forge {
	case "github":
		return newGitHubForge(c), nil
	// TODO(Task 3): add "gitea" -> newGiteaForge(c) once the Gitea
	// client lands. Until then an unhandled forge is the documented
	// degradation path.
	default:
		return nil, errNoForge
	}
}

// parseOwnerRepo extracts the owner and repo name from a git remote URL,
// handling both HTTPS and scp-like SSH forms.
func parseOwnerRepo(remoteURL string) (owner, repo string, err error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", "", fmt.Errorf("empty remote URL")
	}

	// scp-like: [user@]host:owner/repo[.git]
	if !strings.Contains(remoteURL, "://") {
		// Must contain a colon to be scp-like.
		colon := strings.Index(remoteURL, ":")
		if colon < 0 {
			return "", "", fmt.Errorf("unrecognised remote URL form: %s", remoteURL)
		}
		path := remoteURL[colon+1:]
		return parseOwnerRepoPath(path)
	}

	// URL form: scheme://[user@]host/owner/repo[.git]
	u, err := url.Parse(remoteURL)
	if err != nil {
		return "", "", fmt.Errorf("parse remote URL: %w", err)
	}
	return parseOwnerRepoPath(strings.TrimPrefix(u.Path, "/"))
}

// parseOwnerRepoPath splits "owner/repo.git" or "owner/repo" into its
// parts.
func parseOwnerRepoPath(path string) (owner, repo string, err error) {
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from %q", path)
	}
	return parts[0], parts[1], nil
}
