package bridge

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"marshal/web/bridge/contract"
)

// Pull record errors.
var (
	// ErrUnknownPull is returned for an id no record holds.
	ErrUnknownPull = errors.New("bridge: unknown pull request")
	// ErrPullFixRunning is returned when a fix is already dispatched for a
	// pull request. The running fix's id rides the error's message.
	ErrPullFixRunning = errors.New("bridge: a fix is already running for this pull request")
)

// pullID is a record's id: the repo it belongs to and its number.
func pullID(repoID string, number int) string { return fmt.Sprintf("%s#%d", repoID, number) }

// pullDelta is the "pull" fleet delta: contract.PullDelta plus the envelope
// every delta on the stream carries.
type pullDelta struct {
	contract.PullDelta
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
}

// emitPull announces one record, added or changed.
func (f *Fleet) emitPull(rec contract.PullRecord) {
	r := rec
	f.emit(pullDelta{PullDelta: contract.PullDelta{Pull: &r},
		Kind: string(contract.DeltaPull), SessionID: studioOwner})
}

// emitPullRemoved announces that a record is gone.
func (f *Fleet) emitPullRemoved(id string) {
	f.emit(pullDelta{PullDelta: contract.PullDelta{Removed: id},
		Kind: string(contract.DeltaPull), SessionID: studioOwner})
}

// recordPull writes one pull request record, keyed by <repoId>#<number>.
//
// It is idempotent by id: recording the same pull request again replaces the
// record rather than adding a second one, so a writer that runs twice — a
// retried exit, a reopened record — leaves one entry. The caller owns the
// evidence; the store fills the identity and the timestamps.
func (f *Fleet) recordPull(rec contract.PullRecord) error {
	if rec.RepoID == "" || rec.Number <= 0 {
		return errors.New("bridge: a pull record needs a repo id and a number")
	}
	rec.ID = pullID(rec.RepoID, rec.Number)
	now := f.now().UnixMilli()
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = now
	}
	if rec.ObservedAt == 0 {
		rec.ObservedAt = now
	}
	if rec.State == "" {
		rec.State = contract.PullInProgress
	}
	if rec.Checks == nil {
		rec.Checks = []contract.PullCheck{}
	}
	if err := f.ws.PutPull(rec); err != nil {
		return err
	}
	f.emitPull(rec)
	return nil
}

// pull returns one record.
func (f *Fleet) pull(id string) (contract.PullRecord, bool) {
	return f.ws.Pull(id)
}

// pulls returns every record, ordered by id.
func (f *Fleet) pulls() []contract.PullRecord {
	out := f.ws.Pulls()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// updatePull applies mutate to one record under the store's lock and
// announces the result. The mutation's timestamp is the fleet's clock.
func (f *Fleet) updatePull(id string, mutate func(*contract.PullRecord) error) (contract.PullRecord, error) {
	rec, err := f.ws.UpdatePull(id, func(rec *contract.PullRecord) error {
		if err := mutate(rec); err != nil {
			return err
		}
		rec.UpdatedAt = f.now().UnixMilli()
		return nil
	})
	if err != nil {
		return contract.PullRecord{}, err
	}
	f.emitPull(rec)
	return rec, nil
}

// removePull drops one record and announces it.
func (f *Fleet) removePull(id string) error {
	_, ok, err := f.ws.RemovePull(id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	f.emitPullRemoved(id)
	return nil
}

// pullNumberFromURL reads a pull request's number out of its forge URL: the
// trailing /pull/<n> or /pulls/<n> both forges print. A URL without one has no
// number to record.
var pullURLNumberRe = regexp.MustCompile(`/pulls?/(\d+)/?$`)

func pullNumberFromURL(u string) (int, bool) {
	m := pullURLNumberRe.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// recordExitPull records the pull request an exit path opened. The record's
// origin is the agent that asked, unless an automation started that agent: the
// CI fixer's and the review bot's pull requests are the automation's, not the
// worker's that happened to push. A failure here never fails the exit — the
// work is already on the remote — so it is logged and dropped.
func (f *Fleet) recordExitPull(a Agent, branch string, pr PR) {
	if pr.Number <= 0 {
		return
	}
	repoID := a.SourceRef
	if r, ok := f.ws.Repo(repoID); ok {
		repoID = r.ID
	}
	rec := contract.PullRecord{
		RepoID:      repoID,
		ProjectRoot: a.Project,
		Number:      pr.Number,
		URL:         pr.URL,
		Title:       a.Name,
		HeadRef:     branch,
		BaseRef:     a.TargetBranch,
		Origin:      contract.PullOriginAgent,
		AgentID:     a.ID,
	}
	switch a.Origin {
	case OriginCI:
		rec.Origin, rec.Automation = contract.PullOriginAutomation, "ci-fixer"
	case OriginReviewBot:
		rec.Origin, rec.Automation = contract.PullOriginAutomation, "review-bot"
	}
	if err := f.recordPull(rec); err != nil {
		slog.Default().Warn("webbridge: record pull request", "agent", a.ID, "number", pr.Number, "err", err)
	}
}

// recordFactoryPull records a pull request a factory item opened. It is the
// controller's recorder: the factory writes its item, and this keeps the one
// index of pull requests in step with it.
func (f *Fleet) recordFactoryPull(item FactoryItem, pr FactoryPR) {
	if pr.Number <= 0 {
		return
	}
	if _, ok := f.pull(pullID(item.RepoID, pr.Number)); ok {
		return
	}
	rec := contract.PullRecord{
		RepoID:      item.RepoID,
		ProjectRoot: item.ProjectRoot,
		Number:      pr.Number,
		URL:         pr.URL,
		Title:       item.Title,
		HeadRef:     pr.HeadRef,
		BaseRef:     pr.BaseRef,
		HeadSHA:     pr.HeadSHA,
		Draft:       pr.Draft,
		Origin:      contract.PullOriginFactory,
		FactoryItem: item.ID,
	}
	if err := f.recordPull(rec); err != nil {
		slog.Default().Warn("webbridge: record factory pull request", "item", item.ID, "number", pr.Number, "err", err)
	}
}

// backfillPulls records what the fleet already knew before it kept records: an
// agent's exit pull request URL, and each factory item's open pull requests. It
// runs once per source, whichever start hook comes first, and adds only what is
// missing, so running it again changes nothing.
func (f *Fleet) backfillPulls() {
	f.backfillAgentsOnce.Do(f.backfillAgentPulls)
	f.backfillFactoryOnce.Do(f.backfillFactoryPulls)
}

// backfillAgentPulls records the pull request each agent's exit already
// reported.
func (f *Fleet) backfillAgentPulls() {
	for _, a := range f.ws.Agents() {
		if a.PRUrl == "" {
			continue
		}
		number, ok := pullNumberFromURL(a.PRUrl)
		if !ok {
			continue
		}
		repoID := a.SourceRef
		if r, ok := f.ws.Repo(repoID); ok {
			repoID = r.ID
		}
		if repoID == "" {
			continue
		}
		if _, ok := f.pull(pullID(repoID, number)); ok {
			continue
		}
		rec := contract.PullRecord{
			RepoID:      repoID,
			ProjectRoot: a.Project,
			Number:      number,
			URL:         a.PRUrl,
			Title:       a.Name,
			HeadRef:     a.Branch,
			BaseRef:     a.TargetBranch,
			Origin:      contract.PullOriginAgent,
			AgentID:     a.ID,
		}
		switch a.Origin {
		case OriginCI:
			rec.Origin, rec.Automation = contract.PullOriginAutomation, "ci-fixer"
		case OriginReviewBot:
			rec.Origin, rec.Automation = contract.PullOriginAutomation, "review-bot"
		}
		if err := f.recordPull(rec); err != nil {
			slog.Default().Warn("webbridge: backfill pull request", "agent", a.ID, "err", err)
		}
	}
}

// backfillFactoryPulls records each factory item's open pull requests.
func (f *Fleet) backfillFactoryPulls() {
	if f.factory == nil {
		return
	}
	for _, item := range f.factory.Items() {
		for _, pr := range item.PRs {
			if pr.State == "closed" || pr.State == "merged" {
				continue
			}
			if pr.Number <= 0 {
				continue
			}
			if _, ok := f.pull(pullID(item.RepoID, pr.Number)); ok {
				continue
			}
			rec := contract.PullRecord{
				RepoID:      item.RepoID,
				ProjectRoot: item.ProjectRoot,
				Number:      pr.Number,
				URL:         pr.URL,
				Title:       item.Title,
				HeadRef:     pr.HeadRef,
				BaseRef:     pr.BaseRef,
				HeadSHA:     pr.HeadSHA,
				Draft:       pr.Draft,
				Origin:      contract.PullOriginFactory,
				FactoryItem: item.ID,
			}
			if err := f.recordPull(rec); err != nil {
				slog.Default().Warn("webbridge: backfill factory pull request", "item", item.ID, "err", err)
			}
		}
	}
}
