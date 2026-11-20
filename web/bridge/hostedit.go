package bridge

// hostedit.go is POST /api/hosts and DELETE /api/hosts/{name}: adding a host to,
// and removing one from, fleet.toml on this daemon's machine (one-daemon D15,
// plan 7).
//
// The daemon does not edit the file itself. Everything under web/ is standard
// library only, so it cannot import internal/fleetprofile, and the CLI already
// owns that write: `marshal fleet add` and `remove` keep the file's comments,
// refuse what cannot be a host, and wait for the tunnel helper's report to
// catch up with the file. Both routes run it, one at a time, and then answer
// the hosts the daemon now reports.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"marshal/web/bridge/contract"
)

// fleetCommand runs `<marshal-bin> fleet <args…>`, and answers nil, or the
// command's last line of stderr as the error. Tests replace it.
type fleetCommand func(ctx context.Context, args ...string) error

// hostEditTimeout bounds one fleet.toml edit. The CLI itself waits up to five
// seconds for the daemon's report to pick the change up; the rest is the write,
// and a command still running after this has hung.
const hostEditTimeout = 30 * time.Second

// The CLI names itself in front of every failure it writes, and the answer a
// caller gets is the reason alone (D15): the caller already knows which command
// it ran.
const (
	fleetAddPrefix    = "marshal fleet add: "
	fleetRemovePrefix = "marshal fleet remove: "
)

// fleetStartFailure is what a caller reads when the fleet command could not be
// started at all: the raw error names a binary path and a syscall, which is the
// daemon machine's business, not the caller's.
const fleetStartFailure = "could not run marshal fleet"

// addHost answers POST /api/hosts: it runs `fleet add` and answers 201 with the
// HostList after it.
func (s *Server) addHost(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this bridge keeps no fleet file to edit"})
		return
	}
	var body contract.HostAdd
	if !decodeStrictJSON(w, r, &body) {
		return
	}
	for _, field := range []struct{ name, value string }{
		{"name", body.Name},
		{"ssh", body.SSH},
	} {
		if reason := hostFieldRefusal(field.name, field.value); reason != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": reason})
			return
		}
	}
	// The binary on the host is optional — empty is the CLI's own default — so
	// it is not required here, but a flag-shaped path never reaches the CLI
	// either.
	if reason := hostFlagRefusal("marshal", body.Marshal); reason != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": reason})
		return
	}
	args := []string{"add", body.Name, body.SSH}
	if body.Marshal != "" {
		args = append(args, "--marshal", body.Marshal)
	}
	if err := s.editHosts(r.Context(), args...); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fleetReason(err)})
		return
	}
	writeJSON(w, http.StatusCreated, contract.HostList{Hosts: s.reportedHosts()})
}

// removeHost answers DELETE /api/hosts/{name}: it runs `fleet remove` and
// answers 200 with the HostList after it.
func (s *Server) removeHost(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this bridge keeps no fleet file to edit"})
		return
	}
	name := r.PathValue("name")
	if reason := hostFieldRefusal("name", name); reason != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": reason})
		return
	}
	if err := s.editHosts(r.Context(), "remove", name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fleetReason(err)})
		return
	}
	writeJSON(w, http.StatusOK, contract.HostList{Hosts: s.reportedHosts()})
}

// hostFieldRefusal is why a host edit cannot name field, or "" when it can. An
// empty name or destination is nothing the CLI could write, and a value that
// starts with a dash would be read as a flag. The CLI refuses both too; this is
// the check that keeps anything flag-shaped from reaching it at all.
func hostFieldRefusal(field, value string) string {
	if value == "" {
		return field + " is required"
	}
	return hostFlagRefusal(field, value)
}

// hostFlagRefusal is the same check for a field the caller may leave empty: a
// value that starts with a dash would be read as a flag.
func hostFlagRefusal(field, value string) string {
	if strings.HasPrefix(value, "-") {
		return field + " must not start with -"
	}
	return ""
}

// editHosts runs one fleet.toml edit, under hostsEdits: two edits at once would
// interleave their writes to the one file (D15).
func (s *Server) editHosts(ctx context.Context, args ...string) error {
	s.hostsEdits.Lock()
	defer s.hostsEdits.Unlock()
	// The budget is one edit's, so it starts once this edit holds the lock: a
	// request queued behind another has not spent it on waiting. Its cancel
	// runs before the unlock, since a defer taken later runs first.
	ctx, cancel := context.WithTimeout(ctx, hostEditTimeout)
	defer cancel()
	return s.runFleet(ctx, args...)
}

// runFleet runs the fleet command this server has: the one a test replaced, or
// the daemon's own marshal binary.
func (s *Server) runFleet(ctx context.Context, args ...string) error {
	if s.fleetCmd != nil {
		return s.fleetCmd(ctx, args...)
	}
	return s.defaultFleetCommand(ctx, args...)
}

// defaultFleetCommand is the fleet command a server without one runs: the
// daemon's own marshal binary, in the daemon's environment, so fleet.toml and
// the ssh config a host is reached through are the ones this machine's owner
// set up.
func (s *Server) defaultFleetCommand(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, s.fleet.marshalBin, append([]string{"fleet"}, args...)...)
	cmd.Env = os.Environ()
	// Nothing is typed at the CLI, and its report — "Added box. …" — is for a
	// terminal: only a failure's reason crosses the wire.
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if line := fleetLastLine(stderr.String()); line != "" {
			return errors.New(line)
		}
		// Nothing on stderr: the exit status is what there is to say. A
		// command that never started — no binary there, no permission to run
		// it — carries a path and a syscall this caller has no business
		// reading, so it is answered as a sentence.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return errors.New(exit.ProcessState.String())
		}
		return errors.New(fleetStartFailure)
	}
	return nil
}

// fleetLastLine is the last line of s that says anything: the CLI writes its
// report over several lines, and the one that failed is the last one it wrote.
func fleetLastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// fleetReason is a failed command's reason as a caller reads it: its last line,
// without the CLI's name for itself. The default command already strips the
// prefix, so this is idempotent over what it answers and what a replacement
// command reports.
func fleetReason(err error) string {
	if err == nil {
		return ""
	}
	line := fleetLastLine(err.Error())
	for _, prefix := range []string{fleetAddPrefix, fleetRemovePrefix} {
		line = strings.TrimPrefix(line, prefix)
	}
	return strings.TrimSpace(line)
}
