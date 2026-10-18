//go:build darwin

package snapshot

import (
	"errors"
	"os"
	"syscall"
)

// On macOS, fsync(2) moves a file's data and metadata to the drive but does not
// flush the drive's own write cache; only fcntl(F_FULLFSYNC) does, and that is
// what os.File.Sync issues. A full flush costs milliseconds per file, so a
// capture of thousands of new objects spent most of its time in it.
//
// Batched durability follows Git's core.fsyncMethod=batch: each object (and
// each object directory) gets a writeout-only fsync, and a single full flush —
// which flushes the whole drive cache, not just one file — runs before anything
// depends on the objects being durable.

// writeoutSync issues fsync(2) on f. The data is durable only after a later
// fullBarrier returns.
func writeoutSync(f *os.File) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var syncErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		for {
			syncErr = syscall.Fsync(int(fd))
			if !errors.Is(syncErr, syscall.EINTR) {
				return
			}
		}
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return syncErr
}

// writeoutSyncDir issues fsync(2) on a directory, tolerating filesystems that
// cannot sync directories exactly as syncDir does.
func writeoutSyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := writeoutSync(f)
	closeErr := f.Close()
	if syncErr != nil && !isDirSyncUnsupported(syncErr) {
		return syncErr
	}
	return closeErr
}

// fullBarrier flushes the drive's write cache by issuing a full sync on path,
// making every earlier writeoutSync on the same device durable.
func fullBarrier(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
