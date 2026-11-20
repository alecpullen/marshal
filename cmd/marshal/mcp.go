package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"marshal/internal/app/config"
	"marshal/internal/daemon"
)

// mcpPoster is the part of daemon.Client that marshal mcp needs.
type mcpPoster interface {
	PostMCP(ctx context.Context, credential string, body []byte) (int, []byte, error)
}

var mcpClientFor = func(socket string) mcpPoster { return daemon.NewClient(socket) }

// mcpInFlight is how many lines are relayed at once: a client that pipelines
// its requests is answered in parallel, and one that sends a thousand is not
// answered by a thousand connections.
const mcpInFlight = 8

// mcpInternalError is JSON-RPC's internal error, which is what a daemon that
// could not answer the request amounts to.
const mcpInternalError = -32603

// mcpErrorResponse is the JSON-RPC error a failed request is answered with, so
// a client that asked a question is told rather than left waiting.
type mcpErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   mcpResponseErr  `json:"error"`
}

// mcpResponseErr is the error object of an mcpErrorResponse.
type mcpResponseErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// runMCP implements `marshal mcp`: it relays a local MCP client's JSON-RPC
// messages to the daemon's /mcp endpoint over the socket, so the client needs
// no network listener and no address of its own.
//
// Usage: marshal mcp --credential-file PATH [--socket PATH]
func runMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	credentialFile := fs.String("credential-file", "", "the 0600 file holding this MCP client's credential")
	socket := fs.String("socket", "", "the daemon's socket (default: this user's daemon socket)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("marshal mcp: unexpected argument %q", rest[0])
	}
	if *credentialFile == "" {
		return errors.New("marshal mcp: --credential-file is required")
	}
	credential, err := readCredentialFile(*credentialFile)
	if err != nil {
		return err
	}
	path := *socket
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("marshal mcp: find home directory: %w", err)
		}
		path = config.DaemonSocketPath(home)
	}

	return mcpRelay(ctx, mcpClientFor(path), credential, stdin, stdout, stderr)
}

// mcpRelay is the loop: one line in, one POST, one line out. Every line is
// handled on its own goroutine so a slow request does not hold up the ones
// behind it, and at most mcpInFlight run at once.
func mcpRelay(ctx context.Context, poster mcpPoster, credential string, stdin io.Reader, stdout, stderr io.Writer) error {
	out := &mcpAnswers{mu: &sync.Mutex{}, stdout: stdout, stderr: stderr}

	// The reader is its own goroutine so a cancelled context ends the relay
	// rather than waiting on a stdin nobody is going to close. A line is read
	// with a bufio.Reader rather than a Scanner, so a long one has no limit.
	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		reader := bufio.NewReader(stdin)
		for {
			line, err := reader.ReadString('\n')
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				select {
				case lines <- trimmed:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				// io.EOF is stdin closing, which is how the relay ends.
				if !errors.Is(err, io.EOF) {
					readErr <- err
				}
				return
			}
		}
	}()

	slots := make(chan struct{}, mcpInFlight)
	var inFlight sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			inFlight.Wait()
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				// Stdin closed: the answers owed for what was already read
				// are still owed, so the relay waits for them.
				inFlight.Wait()
				select {
				case err := <-readErr:
					return fmt.Errorf("marshal mcp: read stdin: %w", err)
				default:
				}
				return nil
			}
			inFlight.Add(1)
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				inFlight.Done()
				inFlight.Wait()
				return ctx.Err()
			}
			go func(line string) {
				defer inFlight.Done()
				defer func() { <-slots }()
				mcpSend(ctx, poster, credential, line, out)
			}(line)
		}
	}
}

// mcpSend relays one line and writes what it answers.
func mcpSend(ctx context.Context, poster mcpPoster, credential, line string, out *mcpAnswers) {
	status, answer, err := poster.PostMCP(ctx, credential, []byte(line))

	switch {
	case err != nil:
		out.fail(line, "marshal mcp: "+err.Error())
	case status == http.StatusAccepted || status == http.StatusNoContent:
		// A notification, or a request the endpoint answered without a body:
		// there is nothing to relay.
	case len(bytes.TrimSpace(answer)) == 0:
		// Same, for a 200 that carried no message.
	case status != http.StatusOK:
		out.fail(line, fmt.Sprintf("marshal mcp: the daemon answered %d: %s", status, mcpDaemonDetail(answer)))
	default:
		var compact bytes.Buffer
		if err := json.Compact(&compact, answer); err != nil {
			// The daemon answered something that is not one JSON message; the
			// client is told rather than handed unparseable bytes.
			out.fail(line, "marshal mcp: the daemon's answer is not JSON: "+err.Error())
			return
		}
		out.write(compact.Bytes())
	}
}

// mcpDaemonDetail is the daemon's own sentence when its answer carried one, and
// the answer's text otherwise.
func mcpDaemonDetail(answer []byte) string {
	var refused struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(answer, &refused) == nil {
		if detail := strings.TrimSpace(refused.Error); detail != "" {
			return detail
		}
	}
	if detail := strings.TrimSpace(string(answer)); detail != "" {
		return detail
	}
	return http.StatusText(http.StatusInternalServerError)
}

// mcpAnswers writes answers to stdout and failures to stderr. Both go through
// one mutex, so an answer is one whole line however many are in flight.
type mcpAnswers struct {
	mu     *sync.Mutex
	stdout io.Writer
	stderr io.Writer
}

// write puts one answer on stdout as a single line.
func (a *mcpAnswers) write(answer []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.stdout.Write(append(bytes.TrimSpace(answer), '\n'))
}

// fail tells the client that its line could not be answered: a request is
// answered with a JSON-RPC error carrying its own id, and a notification —
// which expects no answer — only gets the line on stderr.
func (a *mcpAnswers) fail(line, message string) {
	if id, ok := mcpRequestID(line); ok {
		response := mcpErrorResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   mcpResponseErr{Code: mcpInternalError, Message: message},
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			message = fmt.Sprintf("%s (and its id could not be echoed: %v)", message, err)
		} else {
			a.write(encoded)
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintln(a.stderr, message)
}

// mcpRequestID answers a line's id as it was written, and whether it had one.
// A line that is not a JSON object has no id to answer, so its failure is a
// notification's.
func mcpRequestID(line string) (json.RawMessage, bool) {
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return nil, false
	}
	id := bytes.TrimSpace(envelope.ID)
	if len(id) == 0 || string(id) == "null" {
		return nil, false
	}
	return json.RawMessage(id), true
}

// readCredentialFile reads an MCP credential from a 0600 file. A file another
// user can read is refused rather than used: it is a credential, and a file the
// group or the world can read is not kept like one.
func readCredentialFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("marshal mcp: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("marshal mcp: %s is mode %04o; a credential file must be 0600", path, mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("marshal mcp: %w", err)
	}
	credential := strings.TrimSpace(string(raw))
	if credential == "" {
		return "", fmt.Errorf("marshal mcp: %s is empty; it must hold this MCP client's credential", path)
	}
	return credential, nil
}
