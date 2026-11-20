package tunnels

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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

// forwardArgv is the ssh -N command that forwards local to remote on h.
func forwardArgv(ssh string, h fleetprofile.Host, local, remote string) []string {
	argv := []string{sshProgram(ssh), "-N"}
	argv = append(argv, sshOptions()...)
	argv = append(argv,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", local+":"+remote,
		h.SSH,
	)
	return argv
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
		Socket string `json:"socket"`
	} `json:"daemon"`
}

// remoteSocket reads the answering daemon's socket from a status document.
func remoteSocket(name string, doc []byte) (string, error) {
	var status statusDoc
	if err := json.Unmarshal(doc, &status); err != nil {
		return "", fmt.Errorf("read %s's status document: %w", name, err)
	}
	if status.SchemaVersion != 1 {
		return "", fmt.Errorf("%s's marshal writes status schema %d; this marshal reads 1",
			name, status.SchemaVersion)
	}
	if !status.Answering {
		return "", fmt.Errorf("the daemon on %s is not answering; start it there with marshal daemon start", name)
	}
	if status.Daemon.Socket == "" {
		return "", fmt.Errorf("%s's daemon names no socket", name)
	}
	return status.Daemon.Socket, nil
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
