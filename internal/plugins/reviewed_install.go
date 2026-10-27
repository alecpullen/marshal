package plugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReviewedInstall is one installation of a plugin the user has reviewed: the
// identity shown on the confirmation screen, the hash of the exact clone
// contents that were shown, and the scoped store/lockfile pair it lands in.
type ReviewedInstall struct {
	Name, Source, Commit, ReviewedHash string
	CloneDir, StoreDir, LockPath       string
}

// InstallReviewed writes a reviewed plugin into its store and records it in
// the scoped lockfile.
//
// The install is staged rather than written in place: the clone is copied and
// hashed into a temporary directory inside the store, the copy that is already
// installed (if any) is moved aside to a backup, the staged copy is renamed
// onto the destination, and only then is the prepared lockfile renamed over
// the real one. A copy, hash, rename or lockfile failure therefore leaves the
// previously installed plugin and the previous lockfile exactly as they were,
// instead of destroying one and silently altering the other.
//
// The caller owns in.CloneDir: this function never writes to or removes it.
func InstallReviewed(ctx context.Context, in ReviewedInstall) error {
	return installReviewed(ctx, in, defaultInstallOps())
}

// installOps is the set of filesystem operations one install performs. It is
// passed in as an argument rather than held in a package variable, so a test
// can inject one failure at one call without mutating shared state.
type installOps struct {
	copyDir   func(src, dst string) error
	hashDir   func(dir string) (string, error)
	readFile  func(path string) ([]byte, error)
	mkdirAll  func(path string, perm os.FileMode) error
	rename    func(oldpath, newpath string) error
	removeAll func(path string) error
	lstat     func(path string) (os.FileInfo, error)
	lockWrite func(l *Lockfile, path string) error
}

func defaultInstallOps() installOps {
	return installOps{
		copyDir:   CopyDir,
		hashDir:   HashDir,
		readFile:  os.ReadFile,
		mkdirAll:  os.MkdirAll,
		rename:    os.Rename,
		removeAll: os.RemoveAll,
		lstat:     os.Lstat,
		lockWrite: func(l *Lockfile, path string) error { return l.Write(path) },
	}
}

func installReviewed(ctx context.Context, in ReviewedInstall, ops installOps) error {
	if err := validateReviewedInstall(in); err != nil {
		return err
	}
	commit := in.Commit
	if strings.TrimSpace(commit) == "" {
		commit = "unknown"
	}

	// Snapshot the lockfile before anything is staged. These bytes are the
	// token that proves nobody else edited it while the plugin was copied.
	lockBefore, lockExisted, err := readLockSnapshot(ops, in.LockPath)
	if err != nil {
		return err
	}
	lf, err := ReadLockfile(in.LockPath)
	if err != nil {
		return err
	}

	if err := ops.mkdirAll(in.StoreDir, 0o755); err != nil {
		return fmt.Errorf("create plugin store %s: %w", in.StoreDir, err)
	}
	// The stage lives inside the store so placing it is a rename on the same
	// filesystem, never a copy that can be interrupted half way.
	stage, err := os.MkdirTemp(in.StoreDir, "."+in.Name+".stage-*")
	if err != nil {
		return fmt.Errorf("create a staging directory in %s: %w", in.StoreDir, err)
	}
	staged := false
	defer func() {
		// Every path that did not place the stage must not leave it in the
		// store: a leftover stage is a plugin directory the loader can see.
		if !staged {
			ops.removeAll(stage)
		}
	}()

	if err := ops.copyDir(in.CloneDir, stage); err != nil {
		return fmt.Errorf("copy plugin %q into the store: %w", in.Name, err)
	}
	stagedHash, err := ops.hashDir(stage)
	if err != nil {
		return fmt.Errorf("hash the staged copy of %q: %w", in.Name, err)
	}
	if stagedHash != in.ReviewedHash {
		return fmt.Errorf("plugin %q contents changed since the review (staged %s, reviewed %s)",
			in.Name, stagedHash, in.ReviewedHash)
	}

	// Prepare the new lockfile as a sibling of the real one, so committing it
	// is a single rename. Unrelated entries come from the lockfile just read
	// and are left untouched; Write owns the ordering.
	lf.Upsert(LockEntry{
		Name:        in.Name,
		Source:      in.Source,
		Commit:      commit,
		ContentHash: in.ReviewedHash,
	})
	lockDir := filepath.Dir(in.LockPath)
	if err := ops.mkdirAll(lockDir, 0o755); err != nil {
		return fmt.Errorf("create the lockfile directory %s: %w", lockDir, err)
	}
	tmpLock, err := reserveLockTemporary(lockDir, filepath.Base(in.LockPath))
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpLock)
		}
	}()
	if err := ops.lockWrite(lf, tmpLock); err != nil {
		return fmt.Errorf("write the prepared lockfile for %q: %w", in.Name, err)
	}

	// The one cancellation check: everything above is reversible without
	// touching the installed plugin or the real lockfile.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("install %q cancelled before commit: %w", in.Name, err)
	}

	// A lockfile that changed since the snapshot belongs to someone else's
	// decision. Aborting here is what keeps this function from silently
	// rewriting an entry the user (or another scope's install) just wrote.
	lockNow, nowExisted, err := readLockSnapshot(ops, in.LockPath)
	if err != nil {
		return err
	}
	if nowExisted != lockExisted || !bytes.Equal(lockNow, lockBefore) {
		return fmt.Errorf("the lockfile %s changed while plugin %q was being staged; nothing was installed",
			in.LockPath, in.Name)
	}

	// in.Name passed ValidName, so it is a single path element: neither path
	// below can escape the store.
	dest := filepath.Join(in.StoreDir, in.Name)
	backup := ""
	if _, statErr := ops.lstat(dest); statErr == nil {
		// Claim a unique sibling name before moving anything, so the old copy
		// is never renamed onto a path another install could also pick.
		if backup, err = reserveBackupPath(in.StoreDir, in.Name); err != nil {
			return err
		}
		if err := ops.rename(dest, backup); err != nil {
			// The destination never moved: there is no backup to claim, and
			// the installed copy is still in place.
			return fmt.Errorf("move the installed copy of %q aside: %w", in.Name, err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect the plugin directory %s: %w", dest, statErr)
	}

	// rollback undoes a half-committed swap. It removes whatever a failed
	// rename may have left at the destination and puts the backup back. When
	// the restore itself fails the backup is deliberately retained and named,
	// because it is then the only copy of the previous plugin.
	rollback := func(cause error) error {
		var problems []string
		if rmErr := ops.removeAll(dest); rmErr != nil {
			problems = append(problems, fmt.Sprintf("remove the new copy: %v", rmErr))
		}
		if backup != "" {
			if restoreErr := ops.rename(backup, dest); restoreErr != nil {
				return fmt.Errorf("%w; rollback failed: %v; the previous copy of %q is retained at %s",
					cause, restoreErr, in.Name, backup)
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("%w; rollback incomplete: %s", cause, strings.Join(problems, "; "))
		}
		return cause
	}

	if err := ops.rename(stage, dest); err != nil {
		return rollback(fmt.Errorf("place plugin %q in the store: %w", in.Name, err))
	}
	staged = true
	if err := ops.rename(tmpLock, in.LockPath); err != nil {
		return rollback(fmt.Errorf("commit the lockfile %s: %w", in.LockPath, err))
	}
	committed = true

	if backup != "" {
		if err := ops.removeAll(backup); err != nil {
			// The install is committed; saying otherwise would send the user
			// looking for a failure that did not happen. Name the directory
			// that was left behind instead.
			return fmt.Errorf("plugin %q is installed and its lockfile committed, but the previous copy could not be removed and is retained at %s: %w",
				in.Name, backup, err)
		}
	}
	return nil
}

// validateReviewedInstall rejects anything that could name a path outside the
// store, and anything an install cannot be held to. It runs before any path is
// created, so a rejected install leaves no trace on disk.
func validateReviewedInstall(in ReviewedInstall) error {
	if !ValidName(in.Name) {
		return fmt.Errorf("invalid plugin name %q: a name must be one directory inside the plugin store", in.Name)
	}
	if in.ReviewedHash == "" {
		return fmt.Errorf("plugin %q has no reviewed content hash to install against", in.Name)
	}
	if in.StoreDir == "" || in.LockPath == "" || in.CloneDir == "" {
		return fmt.Errorf("plugin %q install is missing a store, lockfile or clone path", in.Name)
	}
	return nil
}

// readLockSnapshot returns the lockfile's raw bytes and whether it exists,
// treating a missing file as the empty lockfile.
func readLockSnapshot(ops installOps, path string) ([]byte, bool, error) {
	raw, err := ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read the lockfile %s: %w", path, err)
	}
	return raw, true, nil
}

// reserveLockTemporary claims a sibling path for the prepared lockfile.
func reserveLockTemporary(dir, base string) (string, error) {
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("reserve a lockfile temporary in %s: %w", dir, err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("reserve a lockfile temporary in %s: %w", dir, err)
	}
	return path, nil
}

// reserveBackupPath claims a unique sibling name for the installed copy. The
// atomic way to claim a name is to create it; the empty directory is removed
// again so the destination can be renamed onto that path.
func reserveBackupPath(storeDir, name string) (string, error) {
	path, err := os.MkdirTemp(storeDir, "."+name+".bak-*")
	if err != nil {
		return "", fmt.Errorf("reserve a backup path for %q: %w", name, err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("reserve a backup path for %q: %w", name, err)
	}
	return path, nil
}
