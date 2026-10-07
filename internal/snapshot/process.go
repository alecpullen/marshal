package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// defaultMaxCommandOutput bounds how much of a managed child's stdout and
// stderr is retained (each separately). A child that floods its output cannot
// exhaust the parent's memory; the retained prefix is enough for diagnostics.
const defaultMaxCommandOutput = 1 << 20 // 1 MiB

// processTreeKillTimeout bounds how long we wait for a killed process tree to
// disappear before reporting failure. Ownership must not be released while a
// writer could still be alive, so a tree that survives this budget is an error
// rather than something to shrug off.
const processTreeKillTimeout = 5 * time.Second

// limitedBuffer is an io.Writer that keeps at most max bytes and records
// whether it had to drop anything. It always reports a full write so a chatty
// child is never blocked on a full pipe.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.max > 0 {
		remaining := b.max - b.buf.Len()
		if remaining <= 0 {
			b.truncated = true
			return n, nil
		}
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
	}
	b.buf.Write(p)
	return n, nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// treeOutput is where a child's two streams go.
//
// The fields are io.Writer rather than *limitedBuffer so a caller can stream a
// child's stdout straight into a destination file (restore reads a blob this
// way) without first buffering it in memory. A *limitedBuffer satisfies
// io.Writer, so every existing caller is unaffected.
type treeOutput struct {
	stdout io.Writer
	stderr io.Writer
}

// combined returns a treeOutput that merges both streams into one buffer, which
// is what diagnostics want.
func combinedOutput(maxOutput int) (treeOutput, func() []byte) {
	buf := &limitedBuffer{max: maxOutput}
	return treeOutput{stdout: buf, stderr: buf}, buf.Bytes
}

// runProcessTree starts cmd in its own process tree, captures its combined
// output, and guarantees that no process in that tree is still running when it
// returns.
//
// Cancellation kills the whole tree — the direct child, its children, and its
// grandchildren — and reaps the direct child before returning. That is what
// lets a caller release store ownership afterwards: a cancelled subprocess
// cannot keep writing into the store.
func runProcessTree(ctx context.Context, cmd *exec.Cmd, maxOutput int) ([]byte, error) {
	tree, err := newProcessTree(cmd)
	if err != nil {
		return nil, err
	}
	out, bytesFn := combinedOutput(normalizeMaxOutput(maxOutput))
	err = runProcessTreeWith(ctx, cmd, tree, out)
	return bytesFn(), err
}

// runProcessTreeSplit is runProcessTree with stdout and stderr kept apart. It is
// used where stdout is data (a path list) and stderr is diagnostics, so a
// warning on stderr cannot corrupt the data.
func runProcessTreeSplit(ctx context.Context, cmd *exec.Cmd, maxOutput int) (stdout, stderr []byte, err error) {
	maxOutput = normalizeMaxOutput(maxOutput)
	tree, err := newProcessTree(cmd)
	if err != nil {
		return nil, nil, err
	}
	outBuf := &limitedBuffer{max: maxOutput}
	errBuf := &limitedBuffer{max: maxOutput}
	err = runProcessTreeWith(ctx, cmd, tree, treeOutput{stdout: outBuf, stderr: errBuf})
	return outBuf.Bytes(), errBuf.Bytes(), err
}

func normalizeMaxOutput(n int) int {
	if n <= 0 {
		return defaultMaxCommandOutput
	}
	return n
}

// runProcessTreeWith starts cmd under an already-created tree, streams its
// output into out, and does not return while any process in the tree is alive.
func runProcessTreeWith(ctx context.Context, cmd *exec.Cmd, tree *processTree, out treeOutput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer tree.close()
	cmd.Stdout = out.stdout
	cmd.Stderr = out.stderr
	// Stdin is left exactly as the caller set it. A nil Stdin already means
	// /dev/null, and clearing it here would silently starve a command whose
	// input is data (git check-ignore reads its path list from stdin).

	if err := cmd.Start(); err != nil {
		return err
	}
	if err := tree.afterStart(cmd.Process.Pid); err != nil {
		// We started a process we cannot control. Kill it and reap it rather
		// than leaking an untracked writer.
		_ = tree.kill()
		_ = cmd.Wait()
		return err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		killErr := tree.kill()
		<-done
		goneErr := tree.waitGone()
		if goneErr != nil {
			// Fail closed: reporting success here would let the caller free
			// the store lock while a writer may still be alive.
			return fmt.Errorf("process tree survived cancellation: %w", goneErr)
		}
		if killErr != nil {
			return fmt.Errorf("kill process tree: %w", killErr)
		}
		return ctx.Err()
	}
}
