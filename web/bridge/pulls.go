package bridge

import (
	"errors"
	"fmt"
	"sort"

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
