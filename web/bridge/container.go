package bridge

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// containerSocketDir is where the per-agent socket volume is mounted
// inside the container, and containerSocketName the socket within it.
const (
	containerSocketDir  = "/run/marshal"
	containerSocketName = "agent.sock"
	containerWorkDir    = "/work"
)

// dialTimeout bounds how long we wait for a freshly started container to
// bind its socket before giving up.
const dialTimeout = 30 * time.Second

// ContainerConfig describes one agent's container. Every field is
// supplied explicitly — nothing is inherited from the host environment,
// because an agent must receive exactly the credentials its spawn record
// grants it.
type ContainerConfig struct {
	// Runtime is the absolute path to docker or podman.
	Runtime string
	// Image is the agent image reference.
	Image string
	// Name is the container name, derived from the agent id so it can be
	// found again after a control-plane restart.
	Name string
	// WorkspaceDir is the host directory bind-mounted at /work.
	WorkspaceDir string
	// SocketDir is the host directory bind-mounted at /run/marshal.
	SocketDir string
	// CPUs and MemoryMB cap the container. Zero means unlimited.
	CPUs     float64
	MemoryMB int
	// Env is injected verbatim. Never populate this from os.Environ().
	Env map[string]string
}

// containerTransport runs one agent inside a long-lived container and
// speaks JSON-RPC to it over a unix socket on a shared volume.
type containerTransport struct {
	cfg ContainerConfig

	mu   sync.Mutex
	run  *exec.Cmd
	conn net.Conn
}

func newContainerTransport(cfg ContainerConfig) *containerTransport {
	return &containerTransport{cfg: cfg}
}

// socketPath is the host-side path of the agent's control socket.
func (c *containerTransport) socketPath() string {
	return filepath.Join(c.cfg.SocketDir, containerSocketName)
}

// buildRunArgs assembles the runtime invocation. It deliberately grants
// no host escape: no docker socket mount, no --privileged, no host
// networking. Adding any of them defeats the isolation boundary.
func (c *containerTransport) buildRunArgs() []string {
	args := []string{
		"run", "--rm", "-d",
		"--name", c.cfg.Name,
		"-v", c.cfg.WorkspaceDir + ":" + containerWorkDir,
		"-v", c.cfg.SocketDir + ":" + containerSocketDir,
		"-w", containerWorkDir,
	}
	if c.cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(c.cfg.CPUs, 'f', -1, 64))
	}
	if c.cfg.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(c.cfg.MemoryMB)+"m")
	}
	// Sorted so the argument vector is deterministic and testable.
	keys := make([]string, 0, len(c.cfg.Env))
	for k := range c.cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+c.cfg.Env[k])
	}
	args = append(args,
		c.cfg.Image,
		"marshal", "acp",
		"--listen", "unix://"+containerSocketDir+"/"+containerSocketName,
	)
	return args
}

// Open starts the container and dials its socket, returning half-closers
// over the single duplex connection so that closing the write side does
// not tear down the read side.
func (c *containerTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	if err := os.MkdirAll(c.cfg.SocketDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("bridge: create socket dir: %w", err)
	}
	// A socket left by a previous generation would be dialed instead of
	// the new container's.
	_ = os.Remove(c.socketPath())

	cmd := exec.Command(c.cfg.Runtime, c.buildRunArgs()...)
	cmd.Env = []string{} // never inherit
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, nil, fmt.Errorf("bridge: start container: %w (%s)", err, out)
	}

	conn, err := c.dialSocket()
	if err != nil {
		_ = c.Kill()
		return nil, nil, nil, err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	return writeHalf{conn}, readHalf{conn}, io.NopCloser(emptyReader{}), nil
}

// dialSocket polls until the container binds its socket or the timeout
// expires. A freshly started container has not yet listened.
func (c *containerTransport) dialSocket() (net.Conn, error) {
	deadline := time.Now().Add(dialTimeout)
	for {
		conn, err := net.Dial("unix", c.socketPath())
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("bridge: dial agent socket %s: %w", c.socketPath(), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Wait blocks until the container exits.
func (c *containerTransport) Wait() error {
	cmd := exec.Command(c.cfg.Runtime, "wait", c.cfg.Name)
	cmd.Env = []string{}
	return cmd.Run()
}

// Signal asks the container to stop. Docker has no general signal verb
// for graceful shutdown here, so SIGTERM maps to `stop` (which sends
// SIGTERM then escalates) and anything else is ignored.
func (c *containerTransport) Signal(sig os.Signal) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		// Closing the connection ends the current ACP session cleanly;
		// the agent itself keeps running and can be reattached.
		_ = conn.Close()
	}
	cmd := exec.Command(c.cfg.Runtime, "stop", c.cfg.Name)
	cmd.Env = []string{}
	return cmd.Run()
}

// Kill force-removes the container.
func (c *containerTransport) Kill() error {
	cmd := exec.Command(c.cfg.Runtime, "rm", "-f", c.cfg.Name)
	cmd.Env = []string{}
	return cmd.Run()
}

// writeHalf and readHalf expose one duplex conn as the two independent
// closers Child expects. Closing either shuts down only its direction.
type writeHalf struct{ c net.Conn }

func (w writeHalf) Write(p []byte) (int, error) { return w.c.Write(p) }
func (w writeHalf) Close() error {
	if cw, ok := w.c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

type readHalf struct{ c net.Conn }

func (r readHalf) Read(p []byte) (int, error) { return r.c.Read(p) }
func (r readHalf) Close() error {
	if cr, ok := r.c.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// emptyReader is the container transport's stderr: diagnostics go to
// container logs, not a pipe.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
