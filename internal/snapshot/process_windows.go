//go:build windows

package snapshot

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree is a Windows job object. Assigning the child to a job created
// with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE means every descendant it spawns
// (which inherits the job unless it explicitly breaks away) is terminated when
// the job is terminated or its handle is closed.
//
// Documented limitation: the assignment happens immediately after the child is
// started, because exec.Cmd does not expose the primary thread needed to start
// a suspended process and resume it. A descendant created in that brief window
// could escape the job. Unix has no such window (the process group exists from
// fork), so this is recorded as a Windows-specific caveat rather than hidden.
type processTree struct {
	// mu guards job: it is written when the tree is created and read by
	// whichever goroutine releases ownership.
	mu  sync.Mutex
	job windows.Handle
}

// newProcessTree creates the job before the child is started so that
// afterStart has something to assign the child to.
func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return &processTree{job: job}, nil
}

// afterStart assigns the freshly started child to the job.
func (t *processTree) afterStart(pid int) error {
	job := t.handle()
	if job == 0 {
		return fmt.Errorf("job object is not open")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		return fmt.Errorf("AssignProcessToJobObject(%d): %w", pid, err)
	}
	return nil
}

// handle returns the job handle, or 0 when the job was closed.
func (t *processTree) handle() windows.Handle {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.job
}

// kill terminates every process in the job.
func (t *processTree) kill() error {
	job := t.handle()
	if job == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return fmt.Errorf("TerminateJobObject: %w", err)
	}
	return nil
}

// waitGone waits for the job's active process count to reach zero. A job handle
// is signaled only when every process in it has terminated, so a successful
// wait is the Win32 equivalent of "no surviving writer".
func (t *processTree) waitGone() error {
	job := t.handle()
	if job == 0 {
		return nil
	}
	timeoutMS := uint32(processTreeKillTimeout / 1e6)
	event, err := windows.WaitForSingleObject(job, timeoutMS)
	if err != nil {
		return fmt.Errorf("WaitForSingleObject(job): %w", err)
	}
	if event != 0 { // WAIT_OBJECT_0 == 0
		return fmt.Errorf("job still has live processes after %s (wait result %d)", processTreeKillTimeout, event)
	}
	return nil
}

// close releases the job handle. KILL_ON_JOB_CLOSE guarantees that any straggler
// still assigned to the job dies with it.
func (t *processTree) close() {
	t.mu.Lock()
	job := t.job
	t.job = 0
	t.mu.Unlock()
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
}
