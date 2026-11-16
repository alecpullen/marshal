package fleetprofile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"marshal/internal/credentials"
)

// keychainService is the credentials service fleet tokens live under.
const keychainService = "marshal"

// KeychainKey is the credentials key a server's token is stored under.
func KeychainKey(name string) string {
	return "marshal:fleet:" + name
}

// OpenStore opens the keychain; it is called only when a token is read from
// it.
type OpenStore func() (credentials.Store, error)

// CommandRunner runs a token_cmd script and answers its stdout. Its stderr is
// written to stderr as it runs.
type CommandRunner func(ctx context.Context, script string, stderr io.Writer) ([]byte, error)

// tokenLimit is how long token_cmd may run. A test shortens it so the
// deadline is reached quickly.
var tokenLimit = 10 * time.Second

// tokenLimitText is decision 4's figure, written into the sentence a timeout
// produces.
const tokenLimitText = "10s"

// maxKeptStderr bounds the stderr kept for the failure sentence.
const maxKeptStderr = 4096

// RunShell is the production CommandRunner: /bin/sh -c, with the terminal's
// stdin (decision 4).
func RunShell(ctx context.Context, script string, stderr io.Writer) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Stdin = os.Stdin

	var out bytes.Buffer
	cmd.Stdout = &out
	kept := &firstLine{}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, kept)
	} else {
		cmd.Stderr = kept
	}

	if err := cmd.Run(); err != nil {
		if line := kept.line(); line != "" {
			return nil, fmt.Errorf("/bin/sh -c: %w: %s", err, line)
		}
		return nil, fmt.Errorf("/bin/sh -c: %w", err)
	}
	return out.Bytes(), nil
}

// Token resolves s's token: token_cmd through run, else the keychain through
// open (decision 4).
func Token(ctx context.Context, s Server, open OpenStore, run CommandRunner, stderr io.Writer) (string, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	if s.TokenCmd != "" {
		if run == nil {
			run = RunShell
		}
		return commandToken(ctx, s, run, stderr)
	}
	return keychainToken(s, open)
}

// commandToken runs s's token_cmd under the 10s limit (decision 4).
func commandToken(ctx context.Context, s Server, run CommandRunner, stderr io.Writer) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenLimit)
	defer cancel()

	kept := &firstLine{}
	out, err := run(ctx, s.TokenCmd, io.MultiWriter(stderr, kept))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("token_cmd for %s did not finish in %s", s.Name, tokenLimitText)
		}
		if line := kept.line(); line != "" {
			return "", fmt.Errorf("token_cmd for %s failed: %s", s.Name, line)
		}
		return "", fmt.Errorf("token_cmd for %s failed: %w", s.Name, err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("token_cmd for %s printed nothing", s.Name)
	}
	return token, nil
}

// keychainToken reads s's token from the OS keychain (decision 4).
func keychainToken(s Server, open OpenStore) (string, error) {
	if open == nil {
		open = func() (credentials.Store, error) { return credentials.New(keychainService) }
	}
	store, err := open()
	switch {
	case errors.Is(err, credentials.ErrKeyringUnavailable), err == nil && store == nil:
		return "", fmt.Errorf("no token for %s: this system has no keychain; set token_cmd in fleet.toml", s.Name)
	case err != nil:
		return "", fmt.Errorf("no token for %s: %w", s.Name, err)
	}

	value, err := store.Get(KeychainKey(s.Name))
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return "", fmt.Errorf("no token for %s: run `marshal fleet login %s`", s.Name, s.Name)
		}
		return "", fmt.Errorf("no token for %s: %w", s.Name, err)
	}
	token := strings.TrimSpace(string(value))
	if token == "" {
		return "", fmt.Errorf("no token for %s: run `marshal fleet login %s`", s.Name, s.Name)
	}
	return token, nil
}

// firstLine keeps the beginning of what is written to it, and answers its
// first non-empty line: decision 4's failure sentence.
type firstLine struct {
	buf bytes.Buffer
}

func (f *firstLine) Write(p []byte) (int, error) {
	if room := maxKeptStderr - f.buf.Len(); room > 0 {
		kept := p
		if len(kept) > room {
			kept = kept[:room]
		}
		f.buf.Write(kept)
	}
	return len(p), nil
}

// line answers the first non-blank line written, or "".
func (f *firstLine) line() string {
	for _, line := range strings.Split(f.buf.String(), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
