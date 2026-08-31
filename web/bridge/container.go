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
	"strings"
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

// defaultDialTimeout bounds how long we wait for a freshly started
// container to bind its socket before giving up.
const defaultDialTimeout = 30 * time.Second

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
	// RuntimeName is "docker" or "podman" — selects the volume-subpath
	// option spelling. Empty defaults to docker syntax.
	RuntimeName string
	// StateVolume is the name of the shared state volume.
	StateVolume string
	// WorkSubpath is the subpath within the state volume for the agent's
	// workspace (e.g. "work/<agentID>").
	WorkSubpath string
	// SocketSubpath is the subpath within the state volume for the
	// agent's socket directory (e.g. "sockets/<agentID>").
	SocketSubpath string
	// LocalMount, when non-empty, is a host path bind-mounted at /work
	// for LocalPath agents. When empty, the workspace is a volume
	// subpath. The source is the daemon's view of the path, so a
	// containerized bridge must hand it the translated host path.
	LocalMount string
	// CPUs and MemoryMB cap the container. Zero means unlimited.
	CPUs     float64
	MemoryMB int
	// Env is injected verbatim. Never populate this from os.Environ().
	Env map[string]string
}

// commandRunner executes a container-runtime command and returns its
// combined output.
type commandRunner func(name string, args ...string) ([]byte, error)

// containerTransport runs one agent inside a long-lived container and
// speaks JSON-RPC to it over a unix socket on a shared volume.
type containerTransport struct {
	cfg ContainerConfig

	mu   sync.Mutex
	conn net.Conn

	// run executes one runtime command. Nil means the real runner.
	// Tests inject a fake so the docker-shelling paths are exercisable
	// without a daemon.
	run commandRunner
	// dialTimeout overrides how long Open waits for the agent to bind
	// its socket. Zero means defaultDialTimeout.
	dialTimeout time.Duration
}

func newContainerTransport(cfg ContainerConfig) *containerTransport {
	return &containerTransport{cfg: cfg}
}

// exec runs one runtime command through the seam.
func (c *containerTransport) exec(args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(c.cfg.Runtime, args...)
	}
	cmd := exec.Command(c.cfg.Runtime, args...)
	cmd.Env = clientEnv()
	return cmd.CombinedOutput()
}

// dialWait is how long Open waits for a freshly started container.
func (c *containerTransport) dialWait() time.Duration {
	if c.dialTimeout > 0 {
		return c.dialTimeout
	}
	return defaultDialTimeout
}

// reattachWait is how long Reattach waits for an existing container's
// socket. It is shorter than the fresh-start wait because a container we
// are reattaching to has already bound its socket: a slow dial means the
// container is gone, not still starting. An injected dialTimeout wins,
// so tests need not burn real seconds.
func (c *containerTransport) reattachWait() time.Duration {
	if c.dialTimeout > 0 {
		return c.dialTimeout
	}
	return reattachDialTimeout
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
		"-w", containerWorkDir,
	}
	if c.cfg.LocalMount != "" {
		// A LocalPath agent works on the host checkout itself, so its
		// workspace is a bind mount of the (translated) host path rather
		// than a volume subpath.
		args = append(args, "-v", c.cfg.LocalMount+":"+containerWorkDir)
	} else {
		args = append(args,
			volumeMount(c.cfg.RuntimeName, c.cfg.StateVolume, containerWorkDir, c.cfg.WorkSubpath)...)
	}
	args = append(args,
		volumeMount(c.cfg.RuntimeName, c.cfg.StateVolume, containerSocketDir, c.cfg.SocketSubpath)...)
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
		"acp",
		"--listen", "unix://"+containerSocketDir+"/"+containerSocketName,
	)
	return args
}

// Open starts the container and dials its socket, returning half-closers
// over the single duplex connection so that closing the write side does
// not tear down the read side.
func (c *containerTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	// A container under this name means the agent outlived the control
	// plane. Reconnect to it rather than starting a duplicate.
	if running, err := c.listAgentContainers(); err == nil {
		for _, name := range running {
			if name == c.cfg.Name {
				return c.Reattach()
			}
		}
	}
	return c.start()
}

// start launches a fresh container and dials its socket, returning
// half-closers over the single duplex connection so that closing the
// write side does not tear down the read side.
func (c *containerTransport) start() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	if err := os.MkdirAll(c.cfg.SocketDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("bridge: create socket dir: %w", err)
	}
	// A socket left by a previous generation would be dialed instead of
	// the new container's.
	_ = os.Remove(c.socketPath())

	out, err := c.exec(c.buildRunArgs()...)
	if err != nil {
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
	deadline := time.Now().Add(c.dialWait())
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
	_, err := c.exec("wait", c.cfg.Name)
	return err
}

// Signal terminates the container. Docker has no general signal verb
// here, so SIGTERM maps to `stop` (which sends SIGTERM then escalates)
// and anything else is ignored. This is Child.Stop's graceful step;
// Kill's `rm -f` is only the backstop. To release the agent without
// ending it, use Detach.
func (c *containerTransport) Signal(sig os.Signal) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	_, err := c.exec("stop", c.cfg.Name)
	return err
}

// Detach drops the connection and leaves the container running, so the
// agent outlives the bridge and Open can reattach to it by name.
func (c *containerTransport) Detach() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// Kill force-removes the container.
func (c *containerTransport) Kill() error {
	_, err := c.exec("rm", "-f", c.cfg.Name)
	return err
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

// containerNamePrefix marks containers this bridge owns, so a restart
// can find its own agents without touching anything else on the host.
const containerNamePrefix = "marshal-agent-"

// containerNameFor derives a container name from an agent id. It must be
// deterministic: after a restart the name is the only handle the bridge
// has on a running agent.
func containerNameFor(agentID string) string {
	return containerNamePrefix + agentID
}

// reattachDialTimeout is shorter than dialTimeout: a container we are
// reattaching to has already bound its socket, so a slow dial means it
// is gone rather than still starting.
const reattachDialTimeout = 3 * time.Second

// Reattach connects to an already-running container's socket without
// starting anything. It is the restart path: the agent kept working
// while the control plane was down.
//
// Notifications emitted while detached were dropped by the agent's
// notify sink, so the caller must re-sync session state with
// session/resume after reattaching.
func (c *containerTransport) Reattach() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	deadline := time.Now().Add(c.reattachWait())
	for {
		conn, err := net.Dial("unix", c.socketPath())
		if err == nil {
			c.mu.Lock()
			c.conn = conn
			c.mu.Unlock()
			return writeHalf{conn}, readHalf{conn}, io.NopCloser(emptyReader{}), nil
		}
		if time.Now().After(deadline) {
			return nil, nil, nil, fmt.Errorf("bridge: reattach to %s: %w", c.cfg.Name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// listAgentContainers returns the names of running containers this
// bridge owns, newest first.
func (c *containerTransport) listAgentContainers() ([]string, error) {
	// c.exec uses CombinedOutput, so stderr is mixed in. The HasPrefix
	// post-filter below keeps only valid container names, so diagnostic
	// lines from the runtime CLI are harmlessly dropped.
	out, err := c.exec("ps",
		"--filter", "name="+containerNamePrefix,
		"--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("bridge: list agent containers: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		// Docker's --filter name= does a substring match, not a prefix
		// match, so a foreign container like "foo-marshal-agent-bar" would
		// appear here. Post-filter with HasPrefix to keep only ours.
		if name != "" && strings.HasPrefix(name, containerNamePrefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

// agentIDFromContainer is the inverse of containerNameFor. The second
// return is false for a name that is not ours.
func agentIDFromContainer(name string) (string, bool) {
	if !strings.HasPrefix(name, containerNamePrefix) {
		return "", false
	}
	return strings.TrimPrefix(name, containerNamePrefix), true
}
