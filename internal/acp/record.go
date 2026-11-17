package acp

import (
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/acpwire"
)

// The reasons a stale child gives, in the fixed order stale reports them: the
// build first, then the user config, then the project's.
const (
	reasonNewBuild      = "a new marshal build"
	reasonUserConfig    = "config.toml changed"
	reasonProjectConfig = ".marshal/config.toml changed"
)

// reasonQuietMoment is pending's waiting-for when nothing is busy.
const reasonQuietMoment = "a quiet moment"

// executableFingerprint is the executable as the record keeps it: its path and
// that path's device, inode, size and modification time, taken from the
// system's stat. A fingerprint with no info is no entry: the record could not
// read the executable at start (decision 2).
type executableFingerprint struct {
	path string
	info os.FileInfo
}

// kept reports whether the record holds an entry for the executable.
func (e executableFingerprint) kept() bool { return e.info != nil }

// changed reports whether the file at path is no longer the executable the
// record took. The comparison is the portable one internal/snapshot uses: the
// same file, the same size and the same modification time. Device, inode, size
// and modification time are covered without OS-specific stat fields.
func (e executableFingerprint) changed() bool {
	if !e.kept() {
		return false
	}
	cur, err := os.Stat(e.path)
	if err != nil {
		// The executable the record took is not readable at its path any
		// more: whatever is there now is not it, absence included.
		return true
	}
	return !os.SameFile(e.info, cur) || e.info.Size() != cur.Size() || !e.info.ModTime().Equal(cur.ModTime())
}

// configFingerprint is one config file as the record keeps it: the SHA-256 of
// the file's content, or absent when it did not exist. An absent file is
// recorded as absent, so creating one later is a change.
type configFingerprint struct {
	path   string
	reason string // the fixed reason this file gives
	exists bool
	sum    [sha256.Size]byte
}

// changed reports whether the file at path is not the file recorded: a
// presence that differs from the record, or a different content. A file put
// back with the content it started with is therefore unchanged.
func (c configFingerprint) changed() bool {
	if c.path == "" {
		return false
	}
	sum, exists, err := hashFile(c.path)
	if err != nil {
		// Content the record cannot read now is not content it can vouch
		// for, so it counts as a change.
		return true
	}
	if exists != c.exists {
		return true
	}
	return exists && sum != c.sum
}

// recordFile reads path into the fingerprint the record keeps of it.
func recordFile(path, reason string) (configFingerprint, error) {
	fp := configFingerprint{path: path, reason: reason}
	if path == "" {
		return fp, nil
	}
	sum, exists, err := hashFile(path)
	if err != nil {
		return fp, err
	}
	fp.exists, fp.sum = exists, sum
	return fp, nil
}

// hashFile is one file's SHA-256, and whether the file is there at all. An
// absent file is not an error.
func hashFile(path string) ([sha256.Size]byte, bool, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sum, false, nil
		}
		return sum, false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, false, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, true, nil
}

// cleanPath is a path as the record keeps and compares it, so the path the
// record took and the path a write reports match whatever form each arrived
// in. An empty path stays empty: it names no file.
func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// startRecord is what a project child started from: its executable and the two
// config files it read (decision 1). It answers stale's reasons and remembers
// the last answer it gave, which the status reads (decision 4).
//
// mu guards everything below it. Nothing but wrote changes the fingerprints
// after construction, and the status forwarder reads the remembered answer on
// its own goroutine while the session's project/state handler writes it.
type startRecord struct {
	log *slog.Logger

	mu      sync.Mutex
	exec    executableFingerprint
	configs []configFingerprint
	// reasons and busy are the last answer remember kept: the status reads
	// pending instead of re-reading the files, so a status read never hashes.
	reasons []string
	busy    []string
}

// newStartRecord records executable, userConfig and projectConfig now. A
// config path that is empty is not watched, and a config file that is absent
// is recorded as absent. An executable the record cannot read keeps no entry
// and is logged once; the config entries still work (decision 2).
func newStartRecord(executable, userConfig, projectConfig string, log *slog.Logger) *startRecord {
	if log == nil {
		log = slog.Default()
	}
	r := &startRecord{
		log:  log,
		exec: executableFingerprint{path: cleanPath(executable)},
	}
	if r.exec.path != "" {
		info, err := os.Stat(r.exec.path)
		if err != nil {
			log.Warn("acp: the start record could not read the executable", "path", r.exec.path, "err", err)
		} else {
			r.exec.info = info
		}
	}
	for _, c := range []struct{ path, reason string }{
		{cleanPath(userConfig), reasonUserConfig},
		{cleanPath(projectConfig), reasonProjectConfig},
	} {
		if c.path == "" {
			continue
		}
		fp, err := recordFile(c.path, c.reason)
		if err != nil {
			// An entry the record could not read is no entry: it would
			// otherwise report the file as changed for ever.
			log.Warn("acp: the start record could not read a config file", "path", c.path, "err", err)
			continue
		}
		r.configs = append(r.configs, fp)
	}
	return r
}

// wrote re-records path when it is one the record keeps: the child's own write
// (decision 3). Both paths are compared after cleaning them. A path the record
// does not keep, or one it cannot read now, changes nothing.
func (r *startRecord) wrote(path string) {
	cleaned := cleanPath(path)
	if cleaned == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exec.kept() && r.exec.path == cleaned {
		if info, err := os.Stat(cleaned); err == nil {
			r.exec.info = info
		}
		return
	}
	for i, c := range r.configs {
		if c.path != cleaned {
			continue
		}
		if fp, err := recordFile(cleaned, c.reason); err == nil {
			r.configs[i] = fp
		}
		return
	}
}

// stale compares the record with the files as they are now: the build first,
// then config.toml, then .marshal/config.toml. Every comparison is against the
// record, so a file put back as it was gives no reason.
func (r *startRecord) stale() []string {
	r.mu.Lock()
	exec := r.exec
	configs := append([]configFingerprint(nil), r.configs...)
	r.mu.Unlock()

	var reasons []string
	if exec.changed() {
		reasons = append(reasons, reasonNewBuild)
	}
	for _, c := range configs {
		if c.changed() {
			reasons = append(reasons, c.reason)
		}
	}
	return reasons
}

// remember keeps an answer for the status to read (decision 4). The slices are
// copied, so a caller may reuse its own.
func (r *startRecord) remember(stale, busy []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append([]string(nil), stale...)
	r.busy = append([]string(nil), busy...)
}

// pending is the remembered answer as a session shows it; nil when current. It
// reads the remembered answer only, never the files: the reason is the first
// remembered stale reason and the waiting-for is the first remembered busy
// line, or "a quiet moment" when nothing is busy.
func (r *startRecord) pending() *acpwire.PendingRestart {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reasons) == 0 {
		return nil
	}
	waitingFor := reasonQuietMoment
	if len(r.busy) > 0 {
		waitingFor = r.busy[0]
	}
	return &acpwire.PendingRestart{Reason: r.reasons[0], WaitingFor: waitingFor}
}
