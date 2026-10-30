package snapshot

import (
	"bytes"
	"context"
	"errors"
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

// treeTerminationUnknownError reports that a managed process tree was started
// but its exit could not be proved within the existing bound.
//
// It is returned only for a tree that exists. Cancellation whose kill was
// confirmed returns ctx.Err(), and an ordinary child exit or start failure keeps
// its existing error, so a caller can treat this type as the one signal that
// ownership is still held on its behalf.
type treeTerminationUnknownError struct {
	Cause error
}

func (e *treeTerminationUnknownError) Error() string {
	if e.Cause == nil {
		return "process tree termination could not be confirmed"
	}
	return fmt.Sprintf("process tree termination could not be confirmed: %v", e.Cause)
}

func (e *treeTerminationUnknownError) Unwrap() error { return e.Cause }

// treeState is how far a managed tree has progressed, from the point of view of
// a release drain that may have to kill it.
type treeState int

const (
	// treeStarting: registered, start outcome not known yet. The tree may have
	// no group id or job handle at all, so nothing may be concluded from
	// probing it.
	treeStarting treeState = iota
	// treeLive: the child is running and, on Windows, assigned to its job.
	treeLive
	// treeGone: no process in the tree survives. The platform handle is closed
	// and the tree may be forgotten.
	treeGone
	// treeUnknown: the tree may still have members. The handle stays open and
	// the tree stays registered so a later drain can retry.
	treeUnknown
	// treeNotStarted: the start failed, so no process was created.
	treeNotStarted
)

// ownedTree is one process tree plus the lifecycle a release drain needs to
// decide whether it may close the platform handle and forget it.
//
// A tree is registered before its child is started, so a drain can encounter it
// while the start is still in flight. started closes once the start outcome is
// known, and a drain must wait for that rather than probe a tree that has no
// group id yet: "no group id" reads as "gone", which would let the child start
// writing after the lock had been handed on.
type ownedTree struct {
	// started is closed exactly once, when the start outcome is known.
	started   chan struct{}
	startOnce sync.Once

	mu         sync.Mutex
	tree       *processTree
	state      treeState
	owned      bool
	closeCount int
}

// newStartingTree returns a registered-but-not-yet-started tree lifecycle.
func newStartingTree() *ownedTree {
	return &ownedTree{started: make(chan struct{}), state: treeStarting}
}

// newLiveTree wraps a tree whose child is already running and assigned. The
// legacy migration reader starts its own child and registers it afterwards, so
// there is no start boundary left to wait for.
func newLiveTree(tree *processTree) *ownedTree {
	life := &ownedTree{started: make(chan struct{}), tree: tree, state: treeLive, owned: true}
	close(life.started)
	return life
}

// attach records the platform tree once it has been created.
func (t *ownedTree) attach(tree *processTree) {
	t.mu.Lock()
	t.tree = tree
	t.mu.Unlock()
}

// markOwned records that a manager tracks this tree, so a release drain will
// terminate it. An unowned tree has to clean up after itself.
func (t *ownedTree) markOwned() {
	t.mu.Lock()
	t.owned = true
	t.mu.Unlock()
}

// live reports whether a manager is responsible for this tree's lifetime.
func (t *ownedTree) live() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.owned
}

// platformTree returns the tree, which is nil until attach has run.
func (t *ownedTree) platformTree() *processTree {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tree
}

// resolveStart records a successful start and lets a drain proceed. It must be
// called exactly once for every registered tree; repeated calls are ignored.
func (t *ownedTree) resolveStart() {
	t.startOnce.Do(func() {
		t.mu.Lock()
		t.state = treeLive
		t.mu.Unlock()
		close(t.started)
	})
}

// failStart records that no process was created. The handle, if the platform
// tree had already been built, is closed here so a failed start leaks nothing.
// The drain then sees a tree that needs no killing.
func (t *ownedTree) failStart() {
	t.startOnce.Do(func() {
		t.mu.Lock()
		t.state = treeNotStarted
		t.mu.Unlock()
		t.closeTree()
		close(t.started)
	})
}

// closeTree closes the platform handle at most once.
func (t *ownedTree) closeTree() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tree == nil || t.closeCount > 0 {
		return
	}
	t.closeCount++
	t.tree.close()
}

// closes reports how many times the platform handle was closed.
func (t *ownedTree) closes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeCount
}

// finished reports whether the tree may be forgotten: it is confirmed gone or
// was never started.
func (t *ownedTree) finished() bool {
	select {
	case <-t.started:
	default:
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state == treeGone || t.state == treeNotStarted
}

// markGone records a confirmed disappearance and closes the handle.
func (t *ownedTree) markGone() {
	t.closeTree()
	t.mu.Lock()
	t.state = treeGone
	t.mu.Unlock()
}

// markUnknown records that the tree may still have members.
func (t *ownedTree) markUnknown(err error) {
	t.mu.Lock()
	t.state = treeUnknown
	t.mu.Unlock()
}

// awaitGone waits for every process in the tree to disappear without signalling
// it, and reports nil only when that is confirmed.
//
// A direct child that exited normally is not evidence that its descendants
// exited, which is why this runs on the ordinary-completion path too: untracking
// a tree whose grandchildren are still writing would let the store be handed on
// underneath them.
func (t *ownedTree) awaitGone() error {
	<-t.started
	tree := t.platformTree()
	t.mu.Lock()
	done := t.state == treeGone || t.state == treeNotStarted
	t.mu.Unlock()
	if done {
		return nil
	}
	if err := tree.waitGone(); err != nil {
		t.markUnknown(err)
		return err
	}
	t.markGone()
	return nil
}

// terminate kills the tree and waits for it to disappear, waiting first for an
// in-flight start to resolve so it never probes a tree that has no identity yet.
//
// It reports nil only when the tree is confirmed gone, in which case the handle
// is closed and the tree may be forgotten. Any other outcome — a kill that could
// not be delivered, a group that outlived the budget — leaves the tree and its
// handle in place, so the next release drain retries instead of racing a writer
// that may still be alive.
func (t *ownedTree) terminate() error {
	<-t.started
	tree := t.platformTree()
	t.mu.Lock()
	done := t.state == treeGone || t.state == treeNotStarted
	t.mu.Unlock()
	if done {
		t.closeTree()
		return nil
	}
	var errs []error
	if err := tree.kill(); err != nil {
		errs = append(errs, err)
	}
	if err := tree.waitGone(); err != nil {
		t.markUnknown(err)
		errs = append(errs, err)
	} else {
		t.markGone()
	}
	return errors.Join(errs...)
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
	life := newStartingTree()
	life.attach(tree)
	out, bytesFn := combinedOutput(normalizeMaxOutput(maxOutput))
	err = runProcessTreeWith(ctx, cmd, life, out)
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
	life := newStartingTree()
	life.attach(tree)
	outBuf := &limitedBuffer{max: maxOutput}
	errBuf := &limitedBuffer{max: maxOutput}
	err = runProcessTreeWith(ctx, cmd, life, treeOutput{stdout: outBuf, stderr: errBuf})
	return outBuf.Bytes(), errBuf.Bytes(), err
}

func normalizeMaxOutput(n int) int {
	if n <= 0 {
		return defaultMaxCommandOutput
	}
	return n
}

// runProcessTreeWith starts cmd under an already-created tree, streams its
// output into out, and does not return while any process in the tree may still
// be alive.
//
// It returns nil for the child only when the whole tree is confirmed gone. A
// tree that outlives the existing bound is reported as a
// treeTerminationUnknownError and stays registered with its platform handle
// open, so a release drain can kill and recheck it rather than forget it.
func runProcessTreeWith(ctx context.Context, cmd *exec.Cmd, life *ownedTree, out treeOutput) error {
	tree := life.platformTree()
	if err := ctx.Err(); err != nil {
		// Nothing was started, so there is no process to keep ownership for.
		life.failStart()
		return err
	}
	cmd.Stdout = out.stdout
	cmd.Stderr = out.stderr
	// Stdin is left exactly as the caller set it. A nil Stdin already means
	// /dev/null, and clearing it here would silently starve a command whose
	// input is data (git check-ignore reads its path list from stdin).

	if err := cmd.Start(); err != nil {
		life.failStart()
		return err
	}
	if err := tree.afterStart(cmd.Process.Pid); err != nil {
		// We started a process we cannot control. Kill it and reap it rather
		// than leaking an untracked writer. The start is resolved before the
		// kill so a release drain waits for this outcome instead of probing a
		// tree that is not assigned yet.
		life.resolveStart()
		_ = tree.kill()
		_ = cmd.Wait()
		if goneErr := life.awaitGone(); goneErr != nil {
			if !life.live() {
				// No manager will retry this tree, so close the handle instead
				// of leaking it; the uncertainty is still reported.
				life.closeTree()
			}
			return &treeTerminationUnknownError{Cause: errors.Join(err, goneErr)}
		}
		return err
	}
	life.resolveStart()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// A direct child exiting normally does not prove its descendants
		// exited.
		if goneErr := life.awaitGone(); goneErr != nil {
			// Under ownership the release drain kills and rechecks the tree, so
			// it stays registered with its handle open. Without an owner nothing
			// else will, so try once here before surfacing the uncertainty.
			if !life.live() {
				if killErr := life.terminate(); killErr == nil {
					return err
				}
				life.closeTree()
			}
			return &treeTerminationUnknownError{Cause: goneErr}
		}
		return err
	case <-ctx.Done():
		killErr := tree.kill()
		<-done
		if goneErr := life.awaitGone(); goneErr != nil {
			// Fail closed: reporting success here would let the caller free
			// the store lock while a writer may still be alive.
			if !life.live() {
				life.closeTree()
			}
			return &treeTerminationUnknownError{Cause: errors.Join(killErr, goneErr)}
		}
		if killErr != nil {
			return fmt.Errorf("kill process tree: %w", killErr)
		}
		return ctx.Err()
	}
}
