package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/db"
)

func TestRunWithConfigNilStartRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdin bytes.Buffer
	var stdout bytes.Buffer

	err := runWithConfig(ctx, &stdin, &stdout, runConfig{
		// startRuntime intentionally nil.
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
	})
	if err == nil {
		t.Fatal("runWithConfig should return error when startRuntime is nil")
	}
	if !strings.Contains(err.Error(), "startRuntime") {
		t.Fatalf("error should mention startRuntime, got: %v", err)
	}
}

func TestRunInitializeCapabilities(t *testing.T) {
	t.Run("basic capabilities", func(t *testing.T) {
		in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")
		out := &bytes.Buffer{}

		cfg := runConfig{
			startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
				return &app.Runtime{}, nil
			},
			closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
			shutdown:     0,
		}

		if err := runWithConfig(context.Background(), in, out, cfg); err != nil {
			t.Fatalf("runWithConfig() = %v", err)
		}

		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		if !scan.Scan() {
			t.Fatalf("no response; output=%q", out.String())
		}
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("response error: %+v", resp.Error)
		}

		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatalf("result type = %T", resp.Result)
		}

		// protocolVersion
		if result["protocolVersion"] != float64(1) {
			t.Fatalf("protocolVersion = %v, want 1", result["protocolVersion"])
		}

		// agentCapabilities
		caps, ok := result["agentCapabilities"].(map[string]any)
		if !ok {
			t.Fatalf("agentCapabilities missing or wrong type: %T", result["agentCapabilities"])
		}
		if caps["loadSession"] != true {
			t.Fatalf("loadSession = %v, want true", caps["loadSession"])
		}

		// sessionCapabilities.close is an object (not null)
		sessionCaps, ok := caps["sessionCapabilities"].(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities missing or wrong type: %T", caps["sessionCapabilities"])
		}
		// Every subsystem advertises a capability so clients can discover
		// it. session/diff, /merge, /discard and /worktree_prune exist, so
		// worktreeIsolation must be advertised too — otherwise the
		// "truthful capability advertisement" property is broken and no
		// client can find the isolation surface.
		isoCap, ok := sessionCaps["worktreeIsolation"]
		if !ok {
			t.Fatalf("sessionCapabilities.worktreeIsolation missing; capabilities = %v", sessionCaps)
		}
		if isoObj, ok := isoCap.(map[string]any); !ok || len(isoObj) != 0 {
			t.Fatalf("sessionCapabilities.worktreeIsolation = %v, want an empty object", isoCap)
		}

		closeCap, ok := sessionCaps["close"]
		if !ok {
			t.Fatalf("sessionCapabilities.close missing")
		}
		closeObj, ok := closeCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.close is not an object: %T", closeCap)
		}
		if len(closeObj) != 0 {
			t.Fatalf("sessionCapabilities.close = %v, want empty object", closeObj)
		}

		// sessionCapabilities.list is an empty object.
		listCap, ok := sessionCaps["list"]
		if !ok {
			t.Fatalf("sessionCapabilities.list missing")
		}
		listObj, ok := listCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.list is not an object: %T", listCap)
		}
		if len(listObj) != 0 {
			t.Fatalf("sessionCapabilities.list = %v, want empty object", listObj)
		}

		// sessionCapabilities.resume is an empty object.
		resumeCap, ok := sessionCaps["resume"]
		if !ok {
			t.Fatalf("sessionCapabilities.resume missing")
		}
		resumeObj, ok := resumeCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.resume is not an object: %T", resumeCap)
		}
		if len(resumeObj) != 0 {
			t.Fatalf("sessionCapabilities.resume = %v, want empty object", resumeObj)
		}

		// sessionCapabilities.additionalDirectories is an empty object.
		adCap, ok := sessionCaps["additionalDirectories"]
		if !ok {
			t.Fatalf("sessionCapabilities.additionalDirectories missing")
		}
		adObj, ok := adCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.additionalDirectories is not an object: %T", adCap)
		}
		if len(adObj) != 0 {
			t.Fatalf("sessionCapabilities.additionalDirectories = %v, want empty object", adObj)
		}

		// sessionCapabilities.delete is an empty object.
		deleteCap, ok := sessionCaps["delete"]
		if !ok {
			t.Fatalf("sessionCapabilities.delete missing")
		}
		deleteObj, ok := deleteCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.delete is not an object: %T", deleteCap)
		}
		if len(deleteObj) != 0 {
			t.Fatalf("sessionCapabilities.delete = %v, want empty object", deleteObj)
		}

		// Must NOT have image/audio/embeddedContext/mcp
		forbidden := []string{"image", "audio", "embeddedContext", "mcp"}
		for _, key := range forbidden {
			if _, exists := caps[key]; exists {
				t.Fatalf("unexpected capability: %s", key)
			}
		}

		// agentInfo
		info, ok := result["agentInfo"].(map[string]any)
		if !ok {
			t.Fatalf("agentInfo missing or wrong type: %T", result["agentInfo"])
		}
		if info["name"] != "marshal" {
			t.Fatalf("agentInfo.name = %v, want marshal", info["name"])
		}
		if info["title"] != "Marshal" {
			t.Fatalf("agentInfo.title = %v, want Marshal", info["title"])
		}

		// authMethods: empty array (not null)
		authMethods, ok := result["authMethods"].([]any)
		if !ok {
			t.Fatalf("authMethods wrong type: %T (want []any)", result["authMethods"])
		}
		if len(authMethods) != 0 {
			t.Fatalf("authMethods should be empty, got %v", authMethods)
		}
	})

	t.Run("missing version rejected", func(t *testing.T) {
		in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
		out := &bytes.Buffer{}

		cfg := runConfig{
			startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
				return &app.Runtime{}, nil
			},
			closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
			shutdown:     0,
		}

		if err := runWithConfig(context.Background(), in, out, cfg); err != nil {
			t.Fatalf("runWithConfig() = %v", err)
		}

		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		if !scan.Scan() {
			t.Fatalf("no response; output=%q", out.String())
		}
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Error == nil {
			t.Fatal("expected error for missing protocolVersion")
		}
		if resp.Error.Code != invalidParams {
			t.Fatalf("error code = %d, want %d", resp.Error.Code, invalidParams)
		}
	})

	t.Run("version negotiation", func(t *testing.T) {
		in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}` + "\n")
		out := &bytes.Buffer{}

		cfg := runConfig{
			startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
				return &app.Runtime{}, nil
			},
			closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
			shutdown:     0,
		}

		if err := runWithConfig(context.Background(), in, out, cfg); err != nil {
			t.Fatalf("runWithConfig() = %v", err)
		}

		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		if !scan.Scan() {
			t.Fatalf("no response; output=%q", out.String())
		}
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("response error: %+v", resp.Error)
		}

		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatalf("result type = %T", resp.Result)
		}
		if result["protocolVersion"] != float64(1) {
			t.Fatalf("protocolVersion = %v, want 1", result["protocolVersion"])
		}
	})
}

// initializeResult sends an initialize request over stdin and returns the
// decoded result map. It follows the existing run_test pattern: a stub
// startRuntime, a single JSON-RPC frame, and a scan of the response.
func initializeResult(t *testing.T) map[string]any {
	t.Helper()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")
	out := &bytes.Buffer{}

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
		shutdown:     0,
	}

	if err := runWithConfig(context.Background(), in, out, cfg); err != nil {
		t.Fatalf("runWithConfig() = %v", err)
	}

	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scan.Scan() {
		t.Fatalf("no response; output=%q", out.String())
	}
	var resp Response
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("response error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", resp.Result)
	}
	return result
}

func TestInitializeReportsAnAgentVersion(t *testing.T) {
	// The bridge cannot detect a stale derived image without this. The
	// version is stamped at build time; the test sets it explicitly since
	// the package default is empty.
	AgentVersion = "v1.2.3"
	t.Cleanup(func() { AgentVersion = "" })
	res := initializeResult(t)
	info, _ := res["agentInfo"].(map[string]any)
	if info["version"] == nil || info["version"] == "" {
		t.Fatalf("agentInfo carries no version: %+v", info)
	}
	if info["version"] != "v1.2.3" {
		t.Fatalf("agentInfo.version = %v, want v1.2.3", info["version"])
	}
}

func TestRunEOFClosesAllSessionsExactlyOnce(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}

	var (
		mu         sync.Mutex
		closeCalls []string
	)
	var idSeq atomic.Int64

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			id := idSeq.Add(1)
			return &app.Runtime{SessionID: "sess_" + strconv.FormatInt(id, 10)}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error {
			mu.Lock()
			closeCalls = append(closeCalls, rt.SessionID)
			mu.Unlock()
			return nil
		},
		shutdown: time.Second,
	}

	ctx := context.Background()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(ctx, pr, out, cfg)
	}()

	absCwd := t.TempDir()

	// Send two session/new requests
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	// Wait for two session responses
	pollUntil(t, 2*time.Second, func() bool {
		return strings.Count(out.String(), `"sessionId"`) >= 2
	})

	// Close writer to produce EOF
	pw.Close()

	select {
	case err := <-runErr:
		if err != nil {
			t.Logf("runWithConfig returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return after pipe close")
	}

	// Verify close calls
	mu.Lock()
	defer mu.Unlock()

	if len(closeCalls) != 2 {
		t.Fatalf("expected 2 close calls, got %d: %v", len(closeCalls), closeCalls)
	}

	counts := map[string]int{}
	for _, id := range closeCalls {
		counts[id]++
	}
	for id, count := range counts {
		if count != 1 {
			t.Fatalf("session %s closed %d times, want 1", id, count)
		}
	}
}

func TestRunContextCancelClosesSessions(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}

	var (
		mu         sync.Mutex
		closeCalls int
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{SessionID: "sess_1"}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error {
			mu.Lock()
			closeCalls++
			mu.Unlock()
			return nil
		},
		shutdown: time.Second,
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(ctx, pr, out, cfg)
	}()

	absCwd := t.TempDir()

	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	// Wait for session response
	pollUntil(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"sessionId"`)
	})

	// Cancel context
	cancel()

	select {
	case err := <-runErr:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("runWithConfig error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return after context cancellation")
	}

	mu.Lock()
	if closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", closeCalls)
	}
	mu.Unlock()
}

func TestRunReturnsJoinedServeAndCleanupErrors(t *testing.T) {
	writeSentinel := errors.New("write failure")
	closeSentinel := errors.New("close failure")

	pr, pw := io.Pipe()
	wf := &writeFailer{err: writeSentinel}

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{SessionID: "sess_1"}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error {
			return closeSentinel
		},
		shutdown: time.Second,
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(context.Background(), pr, wf, cfg)
	}()

	absCwd := t.TempDir()

	// Send session/new — the response write will fail, causing Serve to exit
	// with writeSentinel, then CloseAll will return closeSentinel.
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("expected joined error, got nil")
		}
		if !errors.Is(err, writeSentinel) {
			t.Fatalf("err does not contain writeSentinel: %v", err)
		}
		if !errors.Is(err, closeSentinel) {
			t.Fatalf("err does not contain closeSentinel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return after write failure")
	}

	_ = pw.Close()
}

func TestRunPromptTurnNilRunnerReturnsError(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{SessionID: "sess_nilrunner"}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
		shutdown:     time.Second,
	}

	ctx := context.Background()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(ctx, pr, out, cfg)
	}()

	absCwd := t.TempDir()

	// Create a session (startRuntime returns a runtime with nil Runner).
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	// Wait for session response.
	pollUntil(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"sessionId"`)
	})

	// Send a prompt to the session whose runtime has a nil Runner.
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_nilrunner",
			"prompt":    []map[string]any{{"type": "text", "text": "hello"}},
		},
	})

	// Wait for the prompt response.
	pollUntil(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"id":2`)
	})

	pw.Close()
	<-runErr

	// Parse the response with id=2.
	scan := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var resp Response
	for scan.Scan() {
		line := scan.Text()
		var r Response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if r.ID != nil {
			var id float64
			if err := json.Unmarshal(*r.ID, &id); err == nil && id == 2 {
				resp = r
				break
			}
		}
	}
	if resp.Error == nil {
		t.Fatal("expected error for nil runner, got success")
	}
	if resp.Error.Code != internalError {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, internalError)
	}
}

func TestRunRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, bytes.NewBuffer(nil), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}

func TestRunSessionListWire(t *testing.T) {
	root := t.TempDir()
	absCwd, _ := filepath.Abs(root)
	if err := os.MkdirAll(filepath.Join(absCwd, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d, err := db.Open(filepath.Join(absCwd, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		t.Fatalf("migrate: %v", err)
	}
	pid, _ := d.GetOrCreateProject(absCwd, "p")
	if err := d.CreateSession("sess_wire", pid, "Wire", time.Now().UTC()); err != nil {
		_ = d.Close()
		t.Fatalf("create: %v", err)
	}
	_ = d.Close()

	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) { return &app.Runtime{}, nil },
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
		lister:       newPerCwdLister(),
		shutdown:     0,
	}
	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(context.Background(), pr, out, cfg)
	}()

	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{"cwd":"` + absCwd + `"}}` + "\n"))

	// Wait for response with the session list.
	var found bool
	pollUntil(t, 5*time.Second, func() bool {
		if strings.Contains(out.String(), `"sessions"`) {
			found = true
			return true
		}
		return false
	})
	pw.Close()
	<-runErr

	if !found {
		t.Fatalf("no session/list response; output=%q", out.String())
	}

	// Parse the response.
	scan := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var resp Response
	for scan.Scan() {
		line := scan.Text()
		if !strings.Contains(line, `"sessions"`) {
			continue
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		break
	}
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result %T", resp.Result)
	}
	sessions, ok := res["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions %T", res["sessions"])
	}
	var sessFound bool
	for _, s := range sessions {
		m, _ := s.(map[string]any)
		if m["sessionId"] == "sess_wire" {
			sessFound = true
		}
	}
	if !sessFound {
		t.Fatalf("sess_wire not in sessions: %+v", sessions)
	}
}
