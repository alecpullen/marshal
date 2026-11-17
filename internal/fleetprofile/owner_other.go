//go:build !unix

package fleetprofile

import "os"

// checkPrivate is a no-op where the file mode carries no owner and group bits
// (decision 3).
func checkPrivate(_ string, _ os.FileInfo) error {
	return nil
}
