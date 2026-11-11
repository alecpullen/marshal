package oauthworker

import (
	"fmt"
	"io"
	"os"
	"testing"

	"marshal/internal/testenv"
)

// leaseHolderEnv names a data dir. A process started with it set takes the
// refresh lease there and idles instead of running this package's tests;
// lease_test.go starts it that way to prove the lease excludes a second
// process, and that a holder which never cleans up still releases it.
const leaseHolderEnv = "OAUTHWORKER_LEASE_HOLDER"

func TestMain(m *testing.M) {
	if dir := os.Getenv(leaseHolderEnv); dir != "" {
		holdLeaseUntilStdinCloses(dir)
		return
	}
	os.Exit(testenv.Run(m))
}

// holdLeaseUntilStdinCloses takes the lease on dir and reports it, so the
// parent knows the child holds the lease before it checks. It then blocks
// until stdin closes. The test kills this process instead, so the deferred
// release never runs: that is the case the lease has to survive.
func holdLeaseUntilStdinCloses(dir string) {
	release, held, err := TryLease(dir)
	if err != nil || !held {
		fmt.Fprintf(os.Stderr, "lease holder: held %v, err %v\n", held, err)
		os.Exit(2)
	}
	defer release()
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
