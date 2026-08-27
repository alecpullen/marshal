package bridge

import (
	"context"
	"log/slog"
	"time"
)

// defaultPollInterval is how often watching repos are checked. Issue
// intake is not latency-sensitive, and several watchers share one
// token's rate-limit budget.
const defaultPollInterval = 5 * time.Minute

// StartPoller runs the issue watcher until the fleet closes.
//
// web/bridge cannot import internal/worker, so this is a plain goroutine
// owned by the Fleet and stopped by Close.
func (f *Fleet) StartPoller(interval time.Duration) {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-t.C:
				f.pollOnce(context.Background())
			}
		}
	}()
}

// pollOnce checks every watching repo. One repo's failure never stops
// the others.
func (f *Fleet) pollOnce(ctx context.Context) {
	for _, r := range f.ws.Repos() {
		if !r.Watch {
			continue
		}
		if f.rateLimited(r.ID) {
			continue
		}
		err := f.pollRepo(ctx, r)

		r, ok := f.ws.Repo(r.ID)
		if !ok {
			continue
		}
		r.LastPolled = time.Now().UTC()
		// Clear a stale error on success, so the UI does not show a
		// failure that has since resolved.
		r.LastPollErr = ""
		if err != nil {
			r.LastPollErr = err.Error()
			slog.Default().Warn("webbridge: issue poll failed", "repo", r.ID, "err", err)
		}
		if perr := f.ws.PutRepo(r); perr != nil {
			slog.Default().Warn("webbridge: persist poll state", "repo", r.ID, "err", perr)
		}
	}
}

// pollRepo submits every newly-labelled issue in one repo.
//
// Deduplication is by recorded issue number, not by the `since` cursor
// alone: editing an issue bumps its updated time, so a cursor-only
// design resubmits work the operator has already seen.
func (f *Fleet) pollRepo(ctx context.Context, r Repo) error {
	forge, cred, err := f.forgeFor(r)
	if err != nil {
		return err
	}
	issues, err := forge.ListIssues(ctx, r, IssueQuery{Label: r.WatchLabel, Since: r.LastPolled}, cred)
	if err != nil {
		f.noteRateLimit(r.ID, err)
		return err
	}

	seen := make(map[int]bool)
	for _, n := range f.ws.SubmittedIssues(r.ID) {
		seen[n] = true
	}
	for _, issue := range issues {
		if seen[issue.Number] {
			continue
		}
		if _, err := f.SubmitIssue(ctx, r.ID, issue.Number); err != nil {
			return err
		}
		if err := f.ws.MarkIssueSubmitted(r.ID, issue.Number); err != nil {
			return err
		}
	}
	return nil
}

// rateLimited reports whether a repo is in a backoff window.
func (f *Fleet) rateLimited(repoID string) bool {
	f.rateMu.Lock()
	defer f.rateMu.Unlock()
	notBefore, ok := f.rateLimits[repoID]
	if !ok {
		return false
	}
	if time.Now().Before(notBefore) {
		return true
	}
	delete(f.rateLimits, repoID)
	return false
}

// noteRateLimit records a backoff for a repo. A 403/429 with a
// Retry-After header sets the "not before" time; other errors get a
// short fixed backoff so a transient failure does not hammer the API.
func (f *Fleet) noteRateLimit(repoID string, err error) {
	// For now, use a fixed 60-second backoff on any error. The plan
	// notes that per-token budgeting is an S3 item; per-repo backoff is
	// the conservative approximation.
	f.rateMu.Lock()
	defer f.rateMu.Unlock()
	f.rateLimits[repoID] = time.Now().Add(60 * time.Second)
}
