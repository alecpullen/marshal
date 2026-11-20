package tunnels

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"marshal/internal/fleetprofile"
)

// maxSocketPath is the longest unix socket path Run makes (macOS's limit).
const maxSocketPath = 103

// sshProgram is the ssh command: the host's own when it names one, else the
// one on this machine's PATH.
func sshProgram(ssh string) string {
	if ssh == "" {
		return "ssh"
	}
	return ssh
}

// sshOptions are the options both commands share: no PTY, never a password
// prompt, a bounded connect, and a connection of their own (a forward's
// control socket would otherwise be inherited by the status command).
func sshOptions() []string {
	return []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
	}
}

// statusArgv is the ssh command that prints h's daemon status document.
func statusArgv(ssh string, h fleetprofile.Host) []string {
	argv := []string{sshProgram(ssh)}
	argv = append(argv, sshOptions()...)
	argv = append(argv, h.SSH, remoteCommand(h.Marshal))
	return argv
}

// forwardArgv is the ssh -N command that forwards local to remote on h, and
// its preview listener to previewPort on this machine when previewAddr is set.
func forwardArgv(ssh string, h fleetprofile.Host, local, remote string, previewPort int, previewAddr string) []string {
	argv := []string{sshProgram(ssh), "-N"}
	argv = append(argv, sshOptions()...)
	argv = append(argv,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", local+":"+remote,
	)
	if previewAddr != "" {
		argv = append(argv, "-L", "127.0.0.1:"+strconv.Itoa(previewPort)+":"+bracketed(previewAddr))
	}
	argv = append(argv, h.SSH)
	return argv
}

// bracketed is addr with an IPv6 host in brackets, as net.JoinHostPort writes
// it; an address it cannot split is left as it came.
func bracketed(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort(host, port)
}

// remoteCommand is `<marshal> daemon status --json` as the remote shell reads
// it: the binary single-quoted, keeping a leading "~/" outside the quotes.
func remoteCommand(marshal string) string {
	binary := marshal
	prefix := ""
	if strings.HasPrefix(binary, "~/") {
		// The remote shell expands "~" only unquoted.
		prefix, binary = "~/", strings.TrimPrefix(binary, "~/")
	}
	if binary == "" {
		prefix, binary = "", "marshal"
	}
	return prefix + "'" + strings.ReplaceAll(binary, "'", `'\''`) + "'" + " daemon status --json"
}

// statusDoc is what `marshal daemon status --json` writes, as far as a tunnel
// reads it.
type statusDoc struct {
	SchemaVersion int  `json:"schemaVersion"`
	Answering     bool `json:"answering"`
	Daemon        struct {
		Socket      string `json:"socket"`
		PreviewAddr string `json:"previewAddr"`
	} `json:"daemon"`
}

// remoteDaemon reads the answering daemon's socket and preview address from a
// status document. The preview address is "" when the remote serves no
// previews, or names one that is not loopback: the remote only ever binds
// loopback, so a forward to anything else is not one.
func remoteDaemon(name string, doc []byte) (socket, previewAddr string, err error) {
	var status statusDoc
	if err := json.Unmarshal(doc, &status); err != nil {
		return "", "", fmt.Errorf("read %s's status document: %w", name, err)
	}
	if status.SchemaVersion != 1 {
		return "", "", fmt.Errorf("%s's marshal writes status schema %d; this marshal reads 1",
			name, status.SchemaVersion)
	}
	if !status.Answering {
		return "", "", fmt.Errorf("the daemon on %s is not answering; start it there with marshal daemon start", name)
	}
	if status.Daemon.Socket == "" {
		return "", "", fmt.Errorf("%s's daemon names no socket", name)
	}
	return status.Daemon.Socket, loopbackAddr(status.Daemon.PreviewAddr), nil
}

// loopbackAddr is addr when its host is loopback, and "" otherwise.
func loopbackAddr(addr string) string {
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch {
	case host == "127.0.0.1", host == "::1":
		return net.JoinHostPort(host, port)
	case strings.EqualFold(host, "localhost"):
		return net.JoinHostPort(host, port)
	}
	return ""
}

// freeLoopbackPort answers a port free on 127.0.0.1 at the time of the call.
func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a free loopback port for the preview forward: %w", err)
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("find a free loopback port for the preview forward: %s is not a TCP address", listener.Addr())
	}
	return addr.Port, nil
}

// socketPath is dir/<name>.sock, or an error when it is longer than
// maxSocketPath.
func socketPath(dir, name string) (string, error) {
	path := filepath.Join(dir, name+".sock")
	if len(path) > maxSocketPath {
		return "", fmt.Errorf("the tunnel socket %s is longer than a unix socket allows; give the host a shorter name", path)
	}
	return path, nil
}
