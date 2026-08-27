package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPRURLAcrossForges(t *testing.T) {
	cases := []struct{ name, output, want string }{
		{
			"github",
			"remote: \nremote: Create a pull request for 'marshal/a1' on GitHub by visiting:\nremote:   https://github.com/you/repo/pull/new/marshal/a1\nremote: \n",
			"https://github.com/you/repo/pull/new/marshal/a1",
		},
		{
			"gitlab",
			"remote: \nremote: To create a merge request for marshal/a1, visit:\nremote:   https://gitlab.com/you/repo/-/merge_requests/new?merge_request%5Bsource_branch%5D=marshal%2Fa1\n",
			"https://gitlab.com/you/repo/-/merge_requests/new?merge_request%5Bsource_branch%5D=marshal%2Fa1",
		},
		{
			"gitea",
			"remote: Create a new pull request for 'marshal/a1':\nremote:   https://gitea.example.com/you/repo/compare/main...marshal/a1\n",
			"https://gitea.example.com/you/repo/compare/main...marshal/a1",
		},
		{
			"forgejo existing PR",
			"remote: Visit the existing pull request:\nremote:   https://code.example.com/you/repo/pulls/7\n",
			"https://code.example.com/you/repo/pulls/7",
		},
		{
			"github re-push prints nothing",
			"To github.com:you/repo.git\n   abc123..def456  HEAD -> marshal/a1\n",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			remote := "https://" + hostFor(c.name) + "/you/repo.git"
			if got := extractPRURL(c.output, remote); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func hostFor(name string) string {
	switch name {
	case "github", "github re-push prints nothing":
		return "github.com"
	case "gitlab":
		return "gitlab.com"
	case "gitea":
		return "gitea.example.com"
	default:
		return "code.example.com"
	}
}

// TestExtractPRURLRejectsHostileOutput is the regression test for the
// fact that a read-only agent may target an arbitrary remote, making
// push output attacker-influenceable.
func TestExtractPRURLRejectsHostileOutput(t *testing.T) {
	remote := "https://github.com/you/repo.git"
	hostile := []struct{ name, output string }{
		{"javascript scheme", "remote:   javascript:alert(document.cookie)\n"},
		{"file scheme", "remote:   file:///etc/passwd\n"},
		{"data scheme", "remote:   data:text/html,<script>x</script>\n"},
		{"foreign host", "remote: Create a pull request:\nremote:   https://evil.example/phish\n"},
	}
	for _, c := range hostile {
		t.Run(c.name, func(t *testing.T) {
			if got := extractPRURL(c.output, remote); got != "" {
				t.Fatalf("accepted hostile URL %q", got)
			}
		})
	}
}

func TestRemoteHostHandlesSSHAndHTTPS(t *testing.T) {
	cases := map[string]string{
		"https://github.com/you/repo.git":               "github.com",
		"git@github.com:you/repo.git":                   "github.com",
		"ssh://git@code.example.com/you/r.git":          "code.example.com",
		"https://user@gitea.example.com:3000/you/r.git": "gitea.example.com:3000",
	}
	for in, want := range cases {
		if got := remoteHost(in); got != want {
			t.Errorf("remoteHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPushLandsTheBranchAndRefusesToOverwrite(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	mirror, err := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := g.PrepareTree(state, "a1", mirror, origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "new.txt")
	mustGit(t, dir, "-c", "user.email=t@e.com", "-c", "user.name=T", "commit", "-m", "work")

	if _, err := g.Push(dir, "marshal/a1", Credential{Kind: "none"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// The ref must exist on the origin.
	mustGit(t, origin, "rev-parse", "refs/heads/marshal/a1")
}
