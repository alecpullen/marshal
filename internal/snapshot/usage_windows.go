//go:build windows

package snapshot

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformSupportsOwnership reports whether this platform can provide safe
// advisory locking and process-tree control. Windows can.
const platformSupportsOwnership = true

// procGetCompressedFileSize is kernel32!GetCompressedFileSizeW. Binding it
// lazily keeps process start-up free of extra DLL loads and fails closed (the
// Call returns an error) if the entry point is missing.
var procGetCompressedFileSize = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCompressedFileSizeW")

// allocatedSize returns the bytes the filesystem actually allocated to path.
//
// Windows has no cheap fstat equivalent that reports the cluster-rounded
// allocation size for every file, so this is a documented conservative
// approximation:
//
//  1. Ask the kernel for the file's "compressed file size", which is its
//     allocation rounded up to a cluster boundary and returned as two 32-bit
//     halves. (The name is historical: it reports the on-disk allocation, not
//     the compressed length.)
//  2. Take the larger of that and the logical length. The caller rounds up to
//     an allocation unit, so a tiny or zero-length file still counts for at
//     least one cluster.
//  3. Fail closed if the API is unavailable or reports an impossible size: an
//     unknown file must never be counted as zero.
//
// A directory cannot be sized this way (the API reports 0 for directories and
// may refuse to open one), so it is charged a single allocation unit, applied
// by the caller's rounding of the logical length.
func allocatedSize(path string, info os.FileInfo) (int64, error) {
	logical := info.Size()
	if logical < 0 {
		logical = 0
	}
	if info.IsDir() {
		return 0, nil
	}
	compressed, err := getCompressedFileSize(path)
	if err != nil {
		return 0, fmt.Errorf("GetCompressedFileSize: %w", err)
	}
	if compressed < 0 {
		return 0, fmt.Errorf("GetCompressedFileSize returned negative size %d", compressed)
	}
	return maxInt64(compressed, logical), nil
}

// getCompressedFileSize calls kernel32!GetCompressedFileSizeW and combines its
// two 32-bit halves. A return of INVALID_FILE_SIZE together with a non-zero
// last error is reported as an error rather than as a 4 GiB-minus-one size.
func getCompressedFileSize(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode path: %w", err)
	}
	var high uint32
	low, _, lastErr := procGetCompressedFileSize.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&high)),
	)
	if uint32(low) == 0xFFFFFFFF {
		if errno, ok := lastErr.(syscall.Errno); ok && errno != 0 {
			return 0, errno
		}
	}
	return (int64(high) << 32) | int64(uint32(low)), nil
}

// freeSpaceBytes returns the bytes available to the caller on the filesystem
// holding path, via GetDiskFreeSpaceEx. FreeBytesAvailableToCaller already
// excludes quota the caller cannot use, so it is the conservative field. An
// error is reported rather than treated as zero free space.
func freeSpaceBytes(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode path: %w", err)
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("GetDiskFreeSpaceEx: %w", err)
	}
	if freeToCaller > uint64(1)<<62 {
		return 0, overflowError("free space", int64(freeToCaller>>32), int64(freeToCaller&0xFFFFFFFF))
	}
	return int64(freeToCaller), nil
}
