package bridge

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// The supervisor's defaults, used when a tuning field of Helper is zero.
const (
	helperBackoff     = time.Second
	helperMaxBackoff  = time.Minute
	helperSteadyRun   = time.Minute
	helperStopTimeout = 5 * time.Second
)

// Helper runs one long-lived helper process and restarts it when it exits
// (P4 D12's `marshal daemon refresh`). The zero values of its tuning fields
// are the production defaults.
type Helper struct {
	Argv        []string      // the command and its arguments
	Stderr      io.Writer     // the helper's standard error; nil discards it
	Backoff     time.Duration // the first restart's delay; zero means helperBackoff
	MaxBackoff  time.Duration // the longest delay; zero means helperMaxBackoff
	SteadyRun   time.Duration // a run this long resets the delay; zero means helperSteadyRun
	StopTimeout time.Duration // SIGTERM to SIGKILL; zero means helperStopTimeout

	mu       sync.Mutex
	pid      int
	restarts int
}

// Run starts the helper and keeps it running until ctx ends, then stops it
// and returns once it has exited.
func (h *Helper) Run(ctx context.Context) {
	if len(h.Argv) == 0 {
		return
	}
	delay := h.backoff()
	for {
		began := time.Now()
		cmd, err := h.start()
		if err == nil {
			h.setPID(cmd.Process.Pid)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case err = <-exited:
			case <-ctx.Done():
				// The signal, and the kill if it is ignored or cannot be
				// sent, happen in stop; Run returns once the helper is gone.
				h.stop(cmd, exited)
				h.setPID(0)
				return
			}
		}
		if ctx.Err() != nil {
			// The run ended as the context did, so there is nothing to
			// restart.
			h.setPID(0)
			return
		}
		// A start that failed counts as an exit: it is logged and counted as
		// a restart like any other.
		slog.Default().Warn("webbridge: refresh helper exited",
			"status", exitStatus(err), "restartIn", delay)
		h.setPID(0)
		h.countRestart()
		if time.Since(began) >= h.steadyRun() {
			// A run that lasted was not a crash loop, so the delay starts
			// over at the first backoff.
			delay = h.backoff()
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, h.maxBackoff())
	}
}

// State is the running helper's process ID (zero between runs) and how many
// times it was restarted.
func (h *Helper) State() (pid, restarts int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pid, h.restarts
}

// start launches one generation of the helper: the bridge's environment and
// working directory, its stderr, and no standard input or output.
func (h *Helper) start() (*exec.Cmd, error) {
	cmd := exec.Command(h.Argv[0], h.Argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = h.Stderr
	// Stdin and Stdout stay nil, which exec maps to the null device.
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// stop ends a running helper: SIGTERM, then SIGKILL when it has not exited
// after StopTimeout or when the signal cannot be sent. It returns once the
// helper has exited.
func (h *Helper) stop(cmd *exec.Cmd, exited <-chan error) {
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
	}
	timer := time.NewTimer(h.stopTimeout())
	defer timer.Stop()
	select {
	case <-exited:
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-exited
	}
}

// setPID records the process ID of the current generation, zero between runs.
func (h *Helper) setPID(pid int) {
	h.mu.Lock()
	h.pid = pid
	h.mu.Unlock()
}

// countRestart records one restart.
func (h *Helper) countRestart() {
	h.mu.Lock()
	h.restarts++
	h.mu.Unlock()
}

// exitStatus is one run's outcome as it is logged.
func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

func (h *Helper) backoff() time.Duration {
	if h.Backoff > 0 {
		return h.Backoff
	}
	return helperBackoff
}

func (h *Helper) maxBackoff() time.Duration {
	if h.MaxBackoff > 0 {
		return h.MaxBackoff
	}
	return helperMaxBackoff
}

func (h *Helper) steadyRun() time.Duration {
	if h.SteadyRun > 0 {
		return h.SteadyRun
	}
	return helperSteadyRun
}

func (h *Helper) stopTimeout() time.Duration {
	if h.StopTimeout > 0 {
		return h.StopTimeout
	}
	return helperStopTimeout
}
