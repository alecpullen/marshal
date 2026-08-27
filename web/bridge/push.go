package bridge

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// urlPattern finds candidate URLs in push output. Matching any URL
// rather than a per-forge message is deliberate: GitHub, GitLab, Gitea
// and Forgejo all print different wording, and wording changes between
// versions. A URL is a URL.
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// Push sends the session's HEAD to a branch on origin and returns the
// combined output, which carries the forge's pull-request URL.
//
// There is deliberately no force flag. A first push onto an occupied
// branch is rejected by git as a non-fast-forward, which is exactly the
// wanted behaviour: never overwrite someone else's work. A send-back
// re-push onto this agent's own branch is a fast-forward and succeeds.
func (g *gitRunner) Push(dir, branch string, cred Credential) (string, error) {
	if strings.TrimSpace(branch) == "" {
		return "", fmt.Errorf("bridge: push requires a branch name")
	}
	out, err := g.run(dir, cred, "push", "origin", "HEAD:refs/heads/"+branch)
	if err != nil {
		return string(out), fmt.Errorf("push %s: %w", branch, err)
	}
	return string(out), nil
}

// extractPRURL pulls a pull-request URL out of push output, or returns
// "" when the forge printed none.
//
// The output is UNTRUSTED. A read-only agent may target an arbitrary
// remote, so a hostile server can print whatever it likes. Two checks
// apply: the scheme must be http or https, so a javascript: or data:
// URL can never reach a rendered link; and the host must match the
// remote's, so a hostile server cannot make the UI display a phishing
// link inside a trusted "open pull request" affordance.
func extractPRURL(pushOutput, remoteURL string) string {
	want := remoteHost(remoteURL)
	if want == "" {
		return ""
	}
	for _, candidate := range urlPattern.FindAllString(pushOutput, -1) {
		candidate = strings.TrimRight(candidate, ".,);")
		u, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		if u.Host != want {
			continue
		}
		return candidate
	}
	return ""
}

// remoteHost extracts the host from a git remote URL, handling both
// URL forms and the scp-like SSH shorthand (git@host:owner/repo.git),
// which url.Parse does not understand.
func remoteHost(remoteURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return ""
	}
	if strings.Contains(remoteURL, "://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return ""
		}
		return u.Host
	}
	// scp-like: [user@]host:path
	at := strings.LastIndex(remoteURL, "@")
	rest := remoteURL[at+1:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	return rest[:colon]
}
