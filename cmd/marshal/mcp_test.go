package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// scriptedMCPPoster is the scripted half of the relay: it answers each line
// with answer and remembers what it was asked.
type scriptedMCPPoster struct {
	answer func(ctx context.Context, credential string, body []byte) (int, []byte, error)

	mu    sync.Mutex
	asked []string
}

func (p *scriptedMCPPoster) PostMCP(ctx context.Context, credential string, body []byte) (int, []byte, error) {
	p.mu.Lock()
	p.asked = append(p.asked, string(body))
	p.mu.Unlock()
	return p.answer(ctx, credential, body)
}

// bodies answers the lines the poster was asked to relay.
func (p *scriptedMCPPoster) bodies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

// mcpCredentialFile writes credential to a private file and answers its path.
func mcpCredentialFile(t *testing.T, credential string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatalf("write credential file: %v", err)
	}
	return path
}

// mcpRun runs the command against a scripted poster and answers what it wrote.
func mcpRun(t *testing.T, poster mcpPoster, stdin string, stdout io.Writer) (string, error) {
	t.Helper()
	old := mcpClientFor
	t.Cleanup(func() { mcpClientFor = old })
	mcpClientFor = func(string) mcpPoster { return poster }

	var stderr bytes.Buffer
	err := runMCP(context.Background(), []string{
		"--credential-file", mcpCredentialFile(t, "cred-1"),
		"--socket", "/run/marshal/marshal.sock",
	}, strings.NewReader(stdin), stdout, &stderr)
	return stderr.String(), err
}

// TestMCPRelaysARequestAndItsAnswer: one line in, one POST, one compacted line
// out.
func TestMCPRelaysARequestAndItsAnswer(t *testing.T) {
	const line = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	var credential string
	poster := &scriptedMCPPoster{answer: func(_ context.Context, cred string, body []byte) (int, []byte, error) {
		credential = cred
		if string(body) != line {
			t.Errorf("posted body = %q, want %q", body, line)
		}
		return http.StatusOK, []byte("{\n  \"jsonrpc\": \"2.0\",\n  \"id\": 1,\n  \"result\": {\"tools\": []}\n}\n"), nil
	}}

	var stdout bytes.Buffer
	stderr, err := mcpRun(t, poster, line+"\n", &stdout)
	if err != nil {
		t.Fatalf("runMCP: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
	if want := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if credential != "cred-1" {
		t.Errorf("credential = %q, want the file's contents %q", credential, "cred-1")
	}
}

// TestMCPWritesNothingForANotification: the endpoint answers a notification 202
// with no body, and the relay passes that silence on.
func TestMCPWritesNothingForANotification(t *testing.T) {
	poster := &scriptedMCPPoster{answer: func(context.Context, string, []byte) (int, []byte, error) {
		return http.StatusAccepted, nil, nil
	}}

	var stdout bytes.Buffer
	stderr, err := mcpRun(t, poster, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n", &stdout)
	if err != nil {
		t.Fatalf("runMCP: %v\nstderr: %s", err, stderr)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
	if len(poster.bodies()) != 1 {
		t.Errorf("posted %d lines, want the notification to be relayed once", len(poster.bodies()))
	}
}

// TestMCPAnswersAFailureWithAnError: a refusal is answered to a request as a
// JSON-RPC error carrying that request's own id, and to a notification — which
// expects no answer — only on stderr.
func TestMCPAnswersAFailureWithAnError(t *testing.T) {
	const sentence = `{"error":"unauthorized"}`

	t.Run("a request", func(t *testing.T) {
		poster := &scriptedMCPPoster{answer: func(context.Context, string, []byte) (int, []byte, error) {
			return http.StatusUnauthorized, []byte(sentence), nil
		}}

		var stdout bytes.Buffer
		stderr, err := mcpRun(t, poster, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n", &stdout)
		if err != nil {
			t.Fatalf("runMCP: %v\nstderr: %s", err, stderr)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing: a request is answered on stdout", stderr)
		}
		if got := strings.Count(stdout.String(), "\n"); got != 1 {
			t.Fatalf("stdout = %q, want exactly one line", stdout.String())
		}

		var answer struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Error   struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &answer); err != nil {
			t.Fatalf("decode answer %q: %v", stdout.String(), err)
		}
		if answer.JSONRPC != "2.0" {
			t.Errorf("jsonrpc = %q, want 2.0", answer.JSONRPC)
		}
		if answer.ID != 7 {
			t.Errorf("id = %d, want the request's 7", answer.ID)
		}
		if answer.Error.Code != -32603 {
			t.Errorf("code = %d, want -32603", answer.Error.Code)
		}
		for _, want := range []string{"marshal mcp: the daemon answered 401", "unauthorized"} {
			if !strings.Contains(answer.Error.Message, want) {
				t.Errorf("message = %q, want it to carry %q", answer.Error.Message, want)
			}
		}
	})

	t.Run("a notification", func(t *testing.T) {
		poster := &scriptedMCPPoster{answer: func(context.Context, string, []byte) (int, []byte, error) {
			return http.StatusUnauthorized, []byte(sentence), nil
		}}

		var stdout bytes.Buffer
		stderr, err := mcpRun(t, poster, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n", &stdout)
		if err != nil {
			t.Fatalf("runMCP: %v", err)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: a notification has no id to answer", stdout.String())
		}
		if !strings.Contains(stderr, "marshal mcp: the daemon answered 401") {
			t.Errorf("stderr = %q, want the failure on it", stderr)
		}
	})

	t.Run("a transport failure", func(t *testing.T) {
		poster := &scriptedMCPPoster{answer: func(context.Context, string, []byte) (int, []byte, error) {
			return 0, nil, fmt.Errorf("daemon at /run/marshal/marshal.sock: %w", os.ErrNotExist)
		}}

		var stdout bytes.Buffer
		stderr, err := mcpRun(t, poster, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`+"\n", &stdout)
		if err != nil {
			t.Fatalf("runMCP: %v\nstderr: %s", err, stderr)
		}
		var answer struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &answer); err != nil {
			t.Fatalf("decode answer %q: %v", stdout.String(), err)
		}
		if answer.ID != 3 || answer.Error.Code != -32603 {
			t.Errorf("answer = %+v, want the request's id and -32603", answer)
		}
		if !strings.Contains(answer.Error.Message, "marshal mcp: daemon at") {
			t.Errorf("message = %q, want it to name the failure", answer.Error.Message)
		}
	})
}

// TestMCPAnswersABodylessFailure: a non-2xx answer with no body is still a
// failure. net/http answers a panicked /mcp handler with exactly that — a
// bodyless 500 — so dropping it would leave a request carrying an id
// unanswered for good.
func TestMCPAnswersABodylessFailure(t *testing.T) {
	poster := func() *scriptedMCPPoster {
		return &scriptedMCPPoster{answer: func(context.Context, string, []byte) (int, []byte, error) {
			return http.StatusInternalServerError, nil, nil
		}}
	}

	t.Run("a request", func(t *testing.T) {
		var stdout bytes.Buffer
		stderr, err := mcpRun(t, poster(), `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`+"\n", &stdout)
		if err != nil {
			t.Fatalf("runMCP: %v\nstderr: %s", err, stderr)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing: a request is answered on stdout", stderr)
		}
		var answer struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &answer); err != nil {
			t.Fatalf("decode answer %q: %v", stdout.String(), err)
		}
		if answer.ID != 5 || answer.Error.Code != -32603 {
			t.Errorf("answer = %+v, want the request's id and -32603", answer)
		}
		if !strings.Contains(answer.Error.Message, "the daemon answered 500") {
			t.Errorf("message = %q, want it to name the status", answer.Error.Message)
		}
	})

	t.Run("a notification", func(t *testing.T) {
		var stdout bytes.Buffer
		stderr, err := mcpRun(t, poster(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n", &stdout)
		if err != nil {
			t.Fatalf("runMCP: %v", err)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: a notification has no id to answer", stdout.String())
		}
		if !strings.Contains(stderr, "the daemon answered 500") {
			t.Errorf("stderr = %q, want the failure on it", stderr)
		}
	})
}

// TestMCPNeedsAPrivateCredentialFile: the flag is required, the file must be
// 0600, and an empty file is not a credential.
func TestMCPNeedsAPrivateCredentialFile(t *testing.T) {
	poster := &scriptedMCPPoster{answer: func(context.Context, string, []byte) (int, []byte, error) {
		return http.StatusAccepted, nil, nil
	}}
	old := mcpClientFor
	t.Cleanup(func() { mcpClientFor = old })
	mcpClientFor = func(string) mcpPoster {
		t.Error("the daemon was reached for a refused credential")
		return poster
	}

	readable := filepath.Join(t.TempDir(), "readable")
	if err := os.WriteFile(readable, []byte("cred-1\n"), 0o644); err != nil {
		t.Fatalf("write readable credential file: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty credential file: %v", err)
	}

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "no flag",
			args: nil,
			want: []string{"--credential-file is required"},
		},
		{
			name: "a readable file",
			args: []string{"--credential-file", readable},
			want: []string{"is mode 0644", "must be 0600"},
		},
		{
			name: "an empty file",
			args: []string{"--credential-file", empty},
			want: []string{"is empty"},
		},
		{
			name: "a missing file",
			args: []string{"--credential-file", filepath.Join(t.TempDir(), "gone")},
			want: []string{"marshal mcp:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runMCP(context.Background(), tc.args, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("runMCP(%v) returned no error, want a refusal", tc.args)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("runMCP(%v) error = %q, want it to name %q", tc.args, err, want)
				}
			}
		})
	}
}

// mcpRecordingWriter records each write and reports it, so a test can release
// a held answer only after another has landed.
type mcpRecordingWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// wrote carries each write's text; it is buffered so a write never waits
	// for a reader.
	wrote chan string
}

func newMCPRecordingWriter() *mcpRecordingWriter {
	return &mcpRecordingWriter{wrote: make(chan string, 4)}
}

func (w *mcpRecordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	w.wrote <- string(p)
	return n, err
}

// lines answers what was written, split into lines.
func (w *mcpRecordingWriter) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Split(strings.TrimSuffix(w.buf.String(), "\n"), "\n")
}

// TestMCPHandlesConcurrentRequests: answers come back in the order the daemon
// gives them, not the order the requests went out, and each is one whole line.
func TestMCPHandlesConcurrentRequests(t *testing.T) {
	release := make(chan struct{})
	poster := &scriptedMCPPoster{answer: func(_ context.Context, _ string, body []byte) (int, []byte, error) {
		if strings.Contains(string(body), `"id":1`) {
			// The first request's answer is held until the second's has been
			// written, so the two arrive in reverse order.
			<-release
			return http.StatusOK, []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`), nil
		}
		return http.StatusOK, []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`), nil
	}}

	stdout := newMCPRecordingWriter()
	go func() {
		first := <-stdout.wrote
		if !strings.Contains(first, `"id":2`) {
			t.Errorf("the first answer written = %q, want the second request's", first)
		}
		if got := strings.Count(first, "\n"); got != 1 {
			t.Errorf("the first answer written = %q, want exactly one line", first)
		}
		close(release)
	}()

	stderr, err := mcpRun(t, poster,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n",
		stdout)
	if err != nil {
		t.Fatalf("runMCP: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}

	lines := stdout.lines()
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want two lines", lines)
	}
	ids := make([]string, 0, 2)
	for _, line := range lines {
		var answer struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &answer); err != nil {
			t.Fatalf("decode answer %q: %v", line, err)
		}
		ids = append(ids, fmt.Sprint(answer.ID))
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "1,2" {
		t.Errorf("answer ids = %v, want both requests answered", ids)
	}
}
