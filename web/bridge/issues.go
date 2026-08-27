package bridge

import (
	"context"
	"fmt"
)

// SubmitIssue turns one issue into a submission.
//
// It goes through Intake.Submit rather than Fleet.Spawn directly, so
// confirmation, per-client caps and the registered-repo allowlist apply
// to issue intake exactly as they do to MCP intake. An adapter that
// bypassed the seam would be a policy hole.
func (f *Fleet) SubmitIssue(ctx context.Context, repoID string, number int) (SubmitResult, error) {
	repo, ok := f.ws.Repo(repoID)
	if !ok {
		return SubmitResult{}, fmt.Errorf("%w: %s", ErrUnregisteredRepo, repoID)
	}
	forge, cred, err := f.forgeFor(repo)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("issue intake for %s: %w", repoID, err)
	}
	issue, err := forge.GetIssue(ctx, repo, number, cred)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("fetch issue %d: %w", number, err)
	}

	return f.Submit(ctx, SpawnRequest{
		Origin:      OriginIssue,
		RepoID:      repoID,
		Title:       fmt.Sprintf("#%d %s", issue.Number, issue.Title),
		Prompt:      issuePrompt(issue),
		IssueNumber: issue.Number,
		IssueURL:    issue.URL,
	})
}

// ListRepoIssues lists open issues for a repo through its forge.
func (f *Fleet) ListRepoIssues(ctx context.Context, repoID string) ([]Issue, error) {
	repo, ok := f.ws.Repo(repoID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnregisteredRepo, repoID)
	}
	forge, cred, err := f.forgeFor(repo)
	if err != nil {
		return nil, fmt.Errorf("list issues for %s: %w", repoID, err)
	}
	return forge.ListIssues(ctx, repo, IssueQuery{}, cred)
}

// issuePrompt renders an issue as the agent's task. The issue body is
// untrusted input from whoever filed it, so it is delimited rather than
// interpolated bare — the agent should treat it as a report to act on,
// not as instructions that outrank its own.
func issuePrompt(i Issue) string {
	return fmt.Sprintf("Resolve issue #%d: %s\n\n--- issue body ---\n%s\n--- end issue body ---",
		i.Number, i.Title, i.Body)
}
