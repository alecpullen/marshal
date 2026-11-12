package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// errDiscoveryTruncated signals that a forge collection has more items than
// discovery reads. An adapter returns the items it did collect together with
// an error wrapping this sentinel, so the report can keep those items and
// record a gap instead of reporting the collection as absent.
var errDiscoveryTruncated = errors.New("bridge: discovery forge collection is truncated")

// discoveryForgePageSize and discoveryForgeMaxPages bound collection reads.
// A full final page means the forge has more items than discovery reads.
const (
	discoveryForgePageSize = 100
	discoveryForgeMaxPages = 5
)

// DiscoveryForge is the read-only forge surface discovery probes. It is
// deliberately a separate, optional interface: discovery must never gain a
// mutating call, and Forge must not widen to carry a probe that only the
// report needs. A repo whose forge is not implemented here simply has no
// DiscoveryForge, which the report represents as the Git-only path.
type DiscoveryForge interface {
	// RepositoryMetadata reports the repository's default branch, visibility,
	// description, topics, language, and size as the forge states them.
	RepositoryMetadata(ctx context.Context, repo Repo, cred Credential) (DiscoveryRepositoryMetadata, error)
	// Branches reports the repo's branches and their head revisions.
	Branches(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryBranch, error)
	// PullRequests reports the open pull/merge requests.
	PullRequests(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryPullRequest, error)
	// Checks reports the CI checks on ref. It never filters: an empty result
	// means the ref has no checks, which differs from an unreadable section.
	Checks(ctx context.Context, repo Repo, ref string, cred Credential) ([]DiscoveryCheck, error)
	// CheckLogs returns the bounded detail available for one check.
	CheckLogs(ctx context.Context, repo Repo, check DiscoveryCheck, cred Credential) (string, error)
}

// DiscoveryForgeFor builds the read-only probe adapter for a repo's declared
// forge, mirroring ForgeFor's switch. A repo with no declared forge, or one
// whose family has no read-only probe, is not an error condition: it is the
// documented Git-only path, signalled with errDiscoveryUnsupported so the
// classifier reports the forge capabilities as unsupported rather than as a
// failure.
func DiscoveryForgeFor(repo Repo, client *http.Client) (DiscoveryForge, error) {
	if client == nil {
		client = forgeHTTPClient
	}
	switch repo.Forge {
	case "github":
		return &discoveryGitHubForge{client: client}, nil
	case "gitea":
		// Gitea and Forgejo, which forked from it and keeps API compatibility.
		return &discoveryGiteaForge{client: client}, nil
	default:
		return nil, fmt.Errorf("forge %q: %w", repo.Forge, errDiscoveryUnsupported)
	}
}

// discoveryCheckSummary is what a probe can report for a check that has no
// fetchable log body. It keeps the check's identity and its link, bounded by
// checkLogLimit like any other log, instead of inventing a request the forge
// does not serve.
func discoveryCheckSummary(check DiscoveryCheck) string {
	return tailString(strings.TrimSpace(check.Name+"\n"+check.URL), checkLogLimit)
}
