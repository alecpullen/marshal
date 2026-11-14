//go:build latencyprobe && !windows

package usability

// Opt-in real-binary measurement. Its only writable workspace must be a
// disposable fixture marked by the preparation script, never a live checkout.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/registry"
	"marshal/test/usability/harness"
	"marshal/test/usability/screen"
)

type realRepoRequest struct {
	AtMS     float64 `json:"at_ms"`
	Bytes    int     `json:"bytes"`
	Messages int     `json:"messages"`
	Tools    int     `json:"tools"`
}

type realRepoResources struct {
	AtMS             float64           `json:"at_ms"`
	ClientCPUMS      float64           `json:"client_cpu_ms"`
	ClientRSSKiB     int64             `json:"client_rss_kib"`
	DescendantRSSKiB int64             `json:"descendant_rss_kib"`
	Descendants      int               `json:"descendants"`
	Processes        []realRepoProcess `json:"processes,omitempty"`
}

type realRepoProcess struct {
	PID     int     `json:"pid"`
	Command string  `json:"command"`
	CPUMS   float64 `json:"cpu_ms"`
	RSSKiB  int64   `json:"rss_kib"`
}

func realRepoLog(t *testing.T, kind string, value any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"kind": kind, "value": value})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("REAL_REPO_PROBE %s", b)
}

func realRepoCPU(s string) float64 {
	var seconds float64
	for _, part := range strings.Split(s, ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0
		}
		seconds = seconds*60 + n
	}
	return seconds * 1000
}

// ps samples only numeric process metadata. Other processes' arguments, names
// and resource readings are neither retained nor emitted.
func realRepoSample(pid int, started time.Time) (realRepoResources, error) {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,rss=,time=,comm=").Output()
	if err != nil {
		return realRepoResources{}, err
	}
	type proc struct {
		id, parent int
		rss        int64
		cpu        float64
		command    string
	}
	var processes []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		id, _ := strconv.Atoi(f[0])
		parent, _ := strconv.Atoi(f[1])
		rss, _ := strconv.ParseInt(f[2], 10, 64)
		processes = append(processes, proc{id, parent, rss, realRepoCPU(f[3]), filepath.Base(strings.Join(f[4:], " "))})
	}
	owned := map[int]bool{pid: true}
	for changed := true; changed; {
		changed = false
		for _, p := range processes {
			if !owned[p.id] && owned[p.parent] {
				owned[p.id] = true
				changed = true
			}
		}
	}
	row := realRepoResources{AtMS: float64(time.Since(started)) / float64(time.Millisecond)}
	for _, p := range processes {
		if owned[p.id] {
			row.Processes = append(row.Processes, realRepoProcess{p.id, p.command, p.cpu, p.rss})
		}
		if p.id == pid {
			row.ClientCPUMS = p.cpu
			row.ClientRSSKiB = p.rss
		} else if owned[p.id] {
			row.DescendantRSSKiB += p.rss
			row.Descendants++
		}
	}
	if row.ClientRSSKiB == 0 {
		return row, fmt.Errorf("client PID %d unavailable", pid)
	}
	return row, nil
}

func realRepoShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func realRepoEnv(source []string) []string {
	allowed := map[string]bool{}
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR", "TERM", "LANG", "LC_ALL"} {
		allowed[key] = true
	}
	var result []string
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			result = append(result, entry)
		}
	}
	return result
}

func realRepoCleanupDescendants(pid int) {
	// Sample the still-live root's current descendants before killing it. Never
	// signal an unrelated process, a system daemon or an earlier run's PID.
	for attempt := 0; attempt < 3; attempt++ {
		row, err := realRepoSample(pid, time.Now())
		if err != nil {
			return
		}
		for i := len(row.Processes) - 1; i >= 0; i-- {
			p := row.Processes[i]
			if p.PID != pid {
				_ = syscall.Kill(p.PID, syscall.SIGKILL)
			}
		}
		if row.Descendants == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRealRepoProbeDropsAmbientShellStartup(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "ambient-script-ran")
	script := filepath.Join(home, "startup.sh")
	if err := os.WriteFile(script, []byte("touch "+realRepoShellQuote(marker)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "BASH_ENV=" + script, "ENV=" + script, "NODE_OPTIONS=--inspect", "GOPLS_REMOTE=auto", "OPENAI_API_KEY=ambient-secret"}
	filtered := realRepoEnv(parent)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-o", "pipefail", "-lc", "printf probe-ok")
	cmd.Env = filtered
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "probe-ok" {
		t.Fatalf("isolated shell: %v %q", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("ambient shell script ran")
	}
	for _, e := range filtered {
		if strings.HasPrefix(e, "NODE_OPTIONS=") || strings.HasPrefix(e, "GOPLS_REMOTE=") || strings.HasPrefix(e, "OPENAI_API_KEY=") {
			t.Fatalf("ambient control survived filtering: %q", e)
		}
	}
}

func TestMarshalRepoLatency(t *testing.T) {
	root := os.Getenv("MARSHAL_LATENCY_REPO")
	if root == "" {
		t.Skip("set MARSHAL_LATENCY_REPO to a marked disposable Marshal checkout")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if marker, err := os.ReadFile(filepath.Join(root, ".git", "marshal-latency-fixture")); err != nil || string(marker) != "disposable Marshal latency fixture\n" {
		t.Fatal("refusing unmarked workspace")
	}
	bin := requireBinary(t)
	testCommand := "go test -json ./internal/tools/registry -count=1 -timeout 2m"
	var replies []scriptedReply
	add := func(name string, args any) {
		replies = append(replies, scriptedReply{ToolName: name, ToolArgs: mustJSON(t, args)})
	}
	for turn := 1; turn <= 2; turn++ {
		add("todo.write", map[string]any{"todos": []map[string]string{{"content": "Inspect the runner", "status": "in_progress"}, {"content": "Run registry tests", "status": "pending"}, {"content": "Report results", "status": "pending"}}})
		add("file.read", map[string]any{"path": "internal/agent/runner.go", "start_line": 1048, "end_line": 1205})
		add("repo.search", map[string]any{"query": "func (r *Runner) executeToolCall", "mode": "substring", "path": "internal/agent"})
		add("shell.run", map[string]any{"command": "git status --short"})
		add("shell.run", map[string]any{"command": testCommand, "timeout_seconds": 120})
		add("shell.run", map[string]any{"command": "git diff --stat"})
		add("todo.write", map[string]any{"todos": []map[string]string{{"content": "Inspect the runner", "status": "completed"}, {"content": "Run registry tests", "status": "completed"}, {"content": "Report results", "status": "completed"}}})
		replies = append(replies, scriptedReply{Text: fmt.Sprintf("REALREPO_DONE_%d. Inspection and registry tests are complete.", turn)})
	}
	readPaths := []string{"internal/agent/chat.go", "internal/agent/execute.go", "internal/agent/prompts.go", "internal/agent/metrics.go", "internal/agent/step.go", "internal/app/runtime.go", "internal/app/session/session.go", "internal/repo/scanner.go", "internal/snapshot/capture.go", "internal/viewmodel/project.go", "internal/acp/stack.go", "internal/tools/native/command.go"}
	for _, p := range readPaths {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("read workload file %s: %v", p, err)
		}
		add("file.read", map[string]any{"path": p, "start_line": 1, "end_line": 200})
	}
	replies = append(replies, scriptedReply{Text: "REALREPO_DONE_3. The source inspection is complete."})
	p := newFixtureProvider(t, replies)
	var requestsMu sync.Mutex
	var requests []realRepoRequest
	started := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "request read failed", 500)
				return
			}
			var req struct {
				Messages []json.RawMessage `json:"messages"`
				Tools    []json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "request decode failed", 400)
				return
			}
			requestsMu.Lock()
			requests = append(requests, realRepoRequest{float64(time.Since(started)) / float64(time.Millisecond), len(body), len(req.Messages), len(req.Tools)})
			requestsMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		p.serveHTTP(w, r)
	}))
	defer server.Close()
	env := isolatedAgentEnv(t, server.URL)
	env.Env = realRepoEnv(env.Env)
	config := strings.ReplaceAll(agentChildConfig(server.URL+"/v1"), "context_window = 32768", "context_window = 131072")
	config = strings.ReplaceAll(config, "auto = false", "auto = true")
	if os.Getenv("MARSHAL_LATENCY_LSP") == "off" {
		config += "\n[lsp]\nenabled = false\n"
	}
	if os.Getenv("MARSHAL_LATENCY_SNAPSHOTS") == "off" {
		config += "\n[snapshots]\nenabled = false\n"
	}
	config += "\n[tools.shell]\nforeground_wait_seconds = 120\n\n[tools.shell.sandbox]\nbackend = \"restricted\"\nenv_allowlist = [\"PATH\",\"HOME\",\"TMPDIR\",\"GOCACHE\",\"GOMODCACHE\",\"GOPATH\",\"GOPROXY\",\"GOSUMDB\",\"GOTOOLCHAIN\",\"CGO_ENABLED\"]\n"
	writeChildConfig(t, env.HomeDir, config)
	// Shared build and module caches make cold versus warm refer to Marshal
	// state, not to accidentally redownloading the Go toolchain and dependencies.
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		value, err := exec.Command("go", "env", key).Output()
		if err != nil {
			t.Fatal(err)
		}
		env.Env = append(env.Env, key+"="+strings.TrimSpace(string(value)))
	}
	env.Env = append(env.Env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "CGO_ENABLED=1")
	commandTmp := filepath.Join(root, ".git", "latency-tmp")
	if err := os.MkdirAll(commandTmp, 0700); err != nil {
		t.Fatal(err)
	}
	env.Env = append(env.Env, "TMPDIR="+commandTmp)
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte("[project]\nname = \"marshal-latency\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	trustTestWorkspace(t, env.HomeDir, root)
	profilePrefix := os.Getenv("MARSHAL_LATENCY_PROFILE_PREFIX")
	if profilePrefix != "" {
		env.Env = append(env.Env, "MARSHAL_LATENCY_PROFILE_PREFIX="+profilePrefix)
		if os.Getenv("MARSHAL_LATENCY_PROFILE_MODE") == "quiet" {
			env.Env = append(env.Env, "MARSHAL_LATENCY_PROFILE_MODE=quiet")
		}
	}
	// Capture a direct-command context baseline using the exact same shell,
	// private home, offline environment and build caches as the client.
	shellEnv := envutil.AllowList(env.Env)
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "CGO_ENABLED"} {
		for _, entry := range env.Env {
			k, v, _ := strings.Cut(entry, "=")
			if k == key {
				shellEnv = envutil.Set(shellEnv, k, v)
			}
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for sample := 0; sample < 2; sample++ {
		cmd := exec.Command(bash, "-o", "pipefail", "-lc", testCommand)
		cmd.Dir = root
		cmd.Env = shellEnv
		at := time.Now()
		out, err := cmd.CombinedOutput()
		elapsed := time.Since(at)
		if err != nil {
			t.Fatalf("direct tests: %v: %s", err, out)
		}
		rss := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
		if runtime.GOOS == "darwin" {
			rss /= 1024
		}
		realRepoLog(t, "direct_tests", map[string]any{"sample": sample, "elapsed_ms": float64(elapsed) / float64(time.Millisecond), "user_ms": float64(cmd.ProcessState.UserTime()) / float64(time.Millisecond), "system_ms": float64(cmd.ProcessState.SystemTime()) / float64(time.Millisecond), "wait4_peak_rss_kib": rss, "output_bytes": len(out)})
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	wrapper := writeHelperScript(t, "printf '%s\\n' \"$$\" > "+realRepoShellQuote(pidFile)+"\nexec "+realRepoShellQuote(bin)+" --embedded\n")
	requestsMu.Lock()
	started = time.Now()
	requestsMu.Unlock()
	sess, err := harness.New(harness.Config{BinaryPath: wrapper, Width: 80, Height: 24, WorkDir: root, Env: env.Env})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var pid int
	for deadline := time.Now().Add(3 * time.Second); pid == 0 && time.Now().Before(deadline); {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if pid == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("client PID not published")
	}
	defer func() {
		if t.Failed() {
			realRepoCleanupDescendants(pid)
		}
	}()
	var sampleMu sync.Mutex
	var samples []realRepoResources
	var sampleErrors []string
	sampleCtx, stopSamples := context.WithCancel(context.Background())
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				row, err := realRepoSample(pid, started)
				sampleMu.Lock()
				if err == nil {
					samples = append(samples, row)
				} else {
					sampleErrors = append(sampleErrors, err.Error())
				}
				sampleMu.Unlock()
			}
		}
	}()
	defer func() { stopSamples(); <-sampleDone }()
	waitForViewport(t, sess, 60*time.Second, "real-repo composer", func(scr screen.Screen) bool { return strings.Contains(scr.Content, "❯") })
	readyMS := float64(time.Since(started)) / float64(time.Millisecond)
	realRepoLog(t, "phase", map[string]any{"name": "ready", "at_ms": float64(time.Since(started)) / float64(time.Millisecond)})
	time.Sleep(2 * time.Second)
	var activeStartMS, quietStartMS float64
	for turn := 1; turn <= 3; turn++ {
		goal := fmt.Sprintf("Inspect Marshal and run registry tests (task %d).", turn)
		if err := sess.Paste(goal); err != nil {
			t.Fatal(err)
		}
		waitForViewport(t, sess, 10*time.Second, "benchmark goal pasted", func(scr screen.Screen) bool { return strings.Contains(scr.Content, fmt.Sprintf("task %d", turn)) })
		at := time.Now()
		if turn == 1 {
			activeStartMS = float64(at.Sub(started)) / float64(time.Millisecond)
		}
		realRepoLog(t, "phase", map[string]any{"name": fmt.Sprintf("turn_%d_start", turn), "at_ms": float64(at.Sub(started)) / float64(time.Millisecond)})
		if err := sess.SendKey("enter"); err != nil {
			t.Fatal(err)
		}
		marker := fmt.Sprintf("REALREPO_DONE_%d", turn)
		waitForViewport(t, sess, 3*time.Minute, marker, func(scr screen.Screen) bool { return strings.Contains(scr.Content, marker) })
		realRepoLog(t, "phase", map[string]any{"name": fmt.Sprintf("turn_%d_end", turn), "at_ms": float64(time.Since(started)) / float64(time.Millisecond), "elapsed_ms": float64(time.Since(at)) / float64(time.Millisecond)})
	}
	if profilePrefix != "" && os.Getenv("MARSHAL_LATENCY_PROFILE_MODE") == "quiet" {
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Signal(syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(profilePrefix + ".started"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("quiet profile did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	quietStartMS = float64(time.Since(started)) / float64(time.Millisecond)
	realRepoLog(t, "phase", map[string]any{"name": "quiet_idle_start", "at_ms": quietStartMS})
	time.Sleep(10 * time.Second)
	stopSamples()
	<-sampleDone
	sampleMu.Lock()
	realRepoLog(t, "resources", samples)
	realRepoLog(t, "resource_errors", sampleErrors)
	coverage := map[string]int{}
	for _, s := range samples {
		switch {
		case s.AtMS < readyMS:
			coverage["startup"]++
		case s.AtMS < activeStartMS:
			coverage["ready_idle"]++
		case s.AtMS < quietStartMS:
			coverage["active"]++
		default:
			coverage["quiet_idle"]++
		}
	}
	realRepoLog(t, "resource_coverage", coverage)
	sampleMu.Unlock()
	if len(sampleErrors) != 0 {
		t.Fatalf("resource sampling errors: %v", sampleErrors)
	}
	for _, phase := range []string{"startup", "ready_idle", "active", "quiet_idle"} {
		if coverage[phase] < 3 {
			t.Fatalf("insufficient resource coverage for %s: %v", phase, coverage)
		}
	}
	requestsMu.Lock()
	realRepoLog(t, "requests", requests)
	requestCount := len(requests)
	requestsMu.Unlock()
	p.AssertNoUnexpectedRequests(t)
	if requestCount != len(replies) {
		t.Fatalf("provider requests=%d, scripted=%d", requestCount, len(replies))
	}
	if profilePrefix != "" {
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Signal(syscall.SIGUSR2); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(profilePrefix + ".done"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("profile did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Quit cleanly so persistence and child cleanup complete before inspection.
	if err := sess.Send("/quit"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendKey("enter"); err != nil {
		t.Fatal(err)
	}
	quitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sess.Wait(quitCtx); err != nil {
		t.Fatalf("graceful exit: %v", err)
	}
	d, err := db.Open(filepath.Join(root, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, err := d.SQLDB().Query("SELECT id FROM agent_sessions ORDER BY started_at DESC LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	var sid string
	if !rows.Next() {
		t.Fatal("no persisted session")
	}
	if err := rows.Scan(&sid); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	audits, err := d.GetToolCalls(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 26 {
		t.Fatalf("tool audits=%d, want 26", len(audits))
	}
	var auditRows []map[string]any
	for _, a := range audits {
		if a.Error != "" || a.Approval == registry.ApprovalDenied {
			t.Fatalf("failed tool %s: %s", a.ToolName, a.Error)
		}
		if a.ToolName == "shell.run" && (a.CommandExitCode == nil || *a.CommandExitCode != 0) {
			t.Fatalf("command did not complete: %+v", a)
		}
		auditRows = append(auditRows, map[string]any{"tool": a.ToolName, "call_id": a.ToolCallID, "process_ms": a.Sandbox.DurationMS, "truncated": a.Sandbox.OutputTruncated})
	}
	realRepoLog(t, "audits", auditRows)
	var snapshotCount, indexedFiles int
	if err := d.SQLDB().QueryRow("SELECT COUNT(*) FROM snapshots WHERE session_id = ?", sid).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := d.SQLDB().QueryRow("SELECT COUNT(*) FROM files").Scan(&indexedFiles); err != nil {
		t.Fatal(err)
	}
	wantSnapshots := 6
	if os.Getenv("MARSHAL_LATENCY_SNAPSHOTS") == "off" {
		wantSnapshots = 0
	}
	if snapshotCount != wantSnapshots {
		t.Fatalf("persisted pre-write snapshots=%d, want %d", snapshotCount, wantSnapshots)
	}
	realRepoLog(t, "persistence", map[string]any{"pre_write_snapshots": snapshotCount, "indexed_files": indexedFiles})
	log, err := os.ReadFile(filepath.Join(root, ".marshal", "marshal.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "snapshot capture unavailable") {
		t.Fatal("a baseline snapshot capture failed or skipped")
	}
	var timings []map[string]string
	for _, line := range strings.Split(string(log), "\n") {
		if !strings.Contains(line, `msg="tool timing"`) {
			continue
		}
		fields := map[string]string{}
		for _, field := range strings.Fields(line) {
			k, v, ok := strings.Cut(field, "=")
			if ok && (k == "tool" || k == "call_id" || strings.HasSuffix(k, "_ns") || k == "cache_hit" || strings.HasSuffix(k, "_error")) {
				fields[k] = v
			}
		}
		timings = append(timings, fields)
	}
	if len(timings) != 26 {
		t.Fatalf("timing logs=%d, want 26", len(timings))
	}
	realRepoLog(t, "tool_timings", timings)
}
