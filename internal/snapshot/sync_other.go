//go:build !darwin

package snapshot

import "os"

// Outside macOS, os.File.Sync is already the strongest flush the platform
// offers per file, so batched durability changes nothing: each writeout sync is
// a full sync and the barrier has nothing left to flush. See sync_darwin.go.

func writeoutSync(f *os.File) error { return f.Sync() }

func writeoutSyncDir(dir string) error { return syncDir(dir) }

func fullBarrier(string) error { return nil }
