package oauthworker

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// missingDataDir returns a data dir path that does not exist yet, so each
// test also exercises the dir creation a first run needs.
func missingDataDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "data")
}

func TestTheLeaseExcludesASecondHolderUntilReleased(t *testing.T) {
	dir := missingDataDir(t)
	release, held, err := TryLease(dir)
	if err != nil || !held || release == nil {
		t.Fatalf("first TryLease = held %v, release %v, err %v; want held", held, release != nil, err)
	}

	// flock belongs to the open file description, so a second open in this
	// process stands in for a second process.
	again, held, err := TryLease(dir)
	if err != nil || held || again != nil {
		t.Fatalf("second TryLease while held = held %v, release %v, err %v; want not held, no error", held, again != nil, err)
	}

	release()
	release() // a second release is a no-op

	after, held, err := TryLease(dir)
	if err != nil || !held || after == nil {
		t.Fatalf("TryLease after release = held %v, release %v, err %v; want held", held, after != nil, err)
	}
	after()
}

func TestTheLeaseFileIsPrivate(t *testing.T) {
	dir := missingDataDir(t)
	release, held, err := TryLease(dir)
	if err != nil || !held {
		t.Fatalf("TryLease = held %v, err %v; want held", held, err)
	}
	defer release()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir mode = %#o; want 0700", perm)
	}

	fileInfo, err := os.Stat(filepath.Join(dir, LeaseFile))
	if err != nil {
		t.Fatalf("stat lease file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("lease file mode = %#o; want 0600", perm)
	}
}

func TestTheLeaseReportsAnUnopenableFile(t *testing.T) {
	// A regular file where the data dir belongs: the lease file cannot be
	// created inside it. That is an error, not "someone else holds it".
	dir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, held, err := TryLease(dir)
	if err == nil || held || release != nil {
		t.Fatalf("TryLease(regular file) = held %v, release %v, err %v; want an error", held, release != nil, err)
	}
}

// TestAHolderExitingLetsAnotherTakeOver is W9: a holder that dies without
// cleaning up must not strand the lease.
func TestAHolderExitingLetsAnotherTakeOver(t *testing.T) {
	dir := missingDataDir(t)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), leaseHolderEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lease holder: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	firstLine := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			firstLine <- ""
			return
		}
		firstLine <- line
	}()
	select {
	case line := <-firstLine:
		if line != "held\n" {
			t.Fatalf("holder said %q; want %q", line, "held\n")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the lease holder to report it holds the lease")
	}

	if release, held, err := TryLease(dir); err != nil || held || release != nil {
		t.Fatalf("TryLease while another process holds it = held %v, release %v, err %v; want not held, no error", held, release != nil, err)
	}

	// Kill it so no deferred release runs; only the kernel dropping the
	// process's descriptors can free the lease.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill lease holder: %v", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for {
		release, held, err := TryLease(dir)
		if err != nil {
			t.Fatalf("TryLease after holder died: %v", err)
		}
		if held {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lease was still held 5s after the holder was killed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
