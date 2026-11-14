//go:build latencyprobe && !windows

package agent

// Opt-in measurement harness. This file adds no production instrumentation or
// performance thresholds. All providers, homes, databases and stores are owned
// by the probe; no daemon or configured provider is contacted.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/interaction"
	"marshal/internal/llm/schema"
	"marshal/internal/sandbox"
	"marshal/internal/snapshot"
	"marshal/internal/testenv"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestMain(m *testing.M) { testenv.Run(m) }

type latencyProbeRow struct {
	SnapshotCalls       int       `json:"snapshot_calls"`
	HandlerCalls        int       `json:"handler_calls"`
	RequestStartsMS     []float64 `json:"request_starts_ms,omitempty"`
	RequestContentBytes []int     `json:"request_content_bytes,omitempty"`
	Kind                string    `json:"kind"`
	Case                string    `json:"case"`
	Sample              int       `json:"sample"`
	Files               int       `json:"files,omitempty"`
	TotalMS             float64   `json:"total_ms"`
	SnapshotMS          float64   `json:"snapshot_ms"`
	GateMS              float64   `json:"gate_ms"`
	HandlerMS           float64   `json:"handler_ms"`
	AuditMS             float64   `json:"legacy_audit_ms"`
	MetricsMS           int64     `json:"metrics_ms"`
	FirstProviderCallMS float64   `json:"first_provider_call_ms"`
	ProviderCalls       int       `json:"provider_calls,omitempty"`
	RequestBytes        int       `json:"first_request_content_bytes"`
}

func probeMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func probeLog(t *testing.T, row latencyProbeRow) {
	t.Helper()
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if row.Kind != "turn" {
		for _, k := range []string{"metrics_ms", "first_provider_call_ms", "first_request_content_bytes"} {
			delete(fields, k)
		}
	}
	if row.Kind != "tool" {
		delete(fields, "handler_ms")
		delete(fields, "handler_calls")
	}
	if row.Kind != "tool" && row.Kind != "controlled" {
		for _, k := range []string{"snapshot_ms", "snapshot_calls", "gate_ms", "legacy_audit_ms"} {
			delete(fields, k)
		}
	}
	b, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LATENCY_PROBE %s", b)
}

type latencyProbeSnapshot struct {
	inner    Snapshotter
	delay    time.Duration
	elapsed  time.Duration
	calls    int
	failures int
}

func (s *latencyProbeSnapshot) Track(ctx context.Context) (string, error) {
	start := time.Now()
	defer func() { s.elapsed += time.Since(start); s.calls++ }()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.inner == nil {
		return "probe-synthetic", nil
	}
	hash, err := s.inner.Track(ctx)
	if err != nil || hash == "" {
		s.failures++
	}
	return hash, err
}
func (s *latencyProbeSnapshot) Diff(context.Context, string) (string, error) { return "", nil }
func (s *latencyProbeSnapshot) Restore(context.Context, string) error        { return nil }

type latencyProbeGate struct{ delay, elapsed time.Duration }

func (g *latencyProbeGate) Acquire() func() {
	start := time.Now()
	time.Sleep(g.delay)
	g.elapsed += time.Since(start)
	return func() {}
}

func probeFixture(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < count; i++ {
		path := filepath.Join(root, fmt.Sprintf("d%02d", i%20), fmt.Sprintf("f%05d.go", i))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("package fixture\n// needle %d\n", i)+strings.Repeat("// fixture content\n", 20)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func probeState(t *testing.T, root string, persistent bool) (*session.State, *db.DB) {
	t.Helper()
	cfg := config.Default()
	p := session.Persistence{SessionID: "latency-probe", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if persistent {
		d, err := db.Open(filepath.Join(t.TempDir(), "probe.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err = d.Migrate(); err != nil {
			t.Fatal(err)
		}
		pid, err := d.GetOrCreateProject(root, "latency-probe")
		if err != nil {
			t.Fatal(err)
		}
		if err = d.CreateSession(p.SessionID, pid, "probe", time.Now()); err != nil {
			t.Fatal(err)
		}
		p.DB = d
	}
	st := session.New(cfg, root, time.Now(), p)
	st.SetTrusted(true)
	auto := true
	if _, err := st.ApplyInteraction(interaction.Patch{Auto: &auto}); err != nil {
		t.Fatal(err)
	}
	return st, p.DB
}

func TestLatencyProbe(t *testing.T) {
	ctx := context.Background()
	t.Run("tools", func(t *testing.T) {
		for _, files := range []int{100, 1000, 5000} {
			t.Run(fmt.Sprint(files), func(t *testing.T) {
				root := probeFixture(t, files)
				st, d := probeState(t, root, true)
				cfg := config.Default()
				reg := registry.New()
				pol := policy.NewEngine(&cfg, nil)
				pol.WithRegistry(reg)
				r := NewRunner(nil, reg, pol, st, "probe")
				r.Now = time.Now
				sb, err := sandbox.New(sandbox.FromConfig(cfg.Tools.Shell), nil)
				if err != nil {
					t.Fatal(err)
				}
				jm := native.NewCommandService(ctx, native.LaunchPolicy{Runner: sb, Dir: root, MaxOutputBytes: 10000})
				st.SetTurnStopService(jm)
				t.Cleanup(func() {
					c, cancel := context.WithTimeout(ctx, 2*time.Second)
					defer cancel()
					if err := jm.Shutdown(c); err != nil {
						t.Error(err)
					}
				})
				if err := native.RegisterAll(reg, native.Options{WorkspaceRoot: root, SessionState: st, Config: cfg, DB: d, CommandRunner: sb, JobManager: jm, CommandAdmission: r.Authorizer.BeginCommand}); err != nil {
					t.Fatal(err)
				}
				snap := &latencyProbeSnapshot{inner: snapshot.New(t.TempDir(), root, 1<<20, nil, nil)}
				r.Snapshotter = snap
				r.SnapshotRecorder = d
				gate := &latencyProbeGate{}
				r.WriteGate = gate
				cold := time.Now()
				if _, err := snap.Track(ctx); err != nil {
					t.Fatal(err)
				}
				probeLog(t, latencyProbeRow{Kind: "snapshot", Case: "cold", Files: files, TotalMS: probeMS(time.Since(cold))})
				cases := []struct{ name, args string }{
					{"file.read", `{"path":"d00/f00000.go"}`},
					{"repo.search", `{"query":"not-present-probe-needle"}`},
					{"todo.write", `{"todos":[{"content":"measure tool latency","status":"in_progress"}]}`},
					{"shell.run", `{"command":"cat d00/f00000.go"}`},
					{"shell.run", `{"command":"grep -n needle d00/f00000.go"}`},
				}
				for index, tc := range cases {
					tool, ok := reg.Lookup(tc.name)
					if !ok {
						t.Fatal(tc.name)
					}
					handler := tool.Handler
					var handlerElapsed time.Duration
					handlerCalls := 0
					tool.Handler = func(ctx context.Context, c registry.ToolCall) (registry.ToolResult, error) {
						start := time.Now()
						defer func() { handlerElapsed += time.Since(start); handlerCalls++ }()
						return handler(ctx, c)
					}
					if err := reg.Replace(tool); err != nil {
						t.Fatal(err)
					}
					for sample := 0; sample < 5; sample++ {
						snap.elapsed = 0
						snap.calls = 0
						gate.elapsed = 0
						handlerElapsed = 0
						handlerCalls = 0
						auditCount := len(st.AuditLog())
						start := time.Now()
						if _, err := r.executeToolCall(ctx, ModelAction{Type: ActionToolCall, Tool: tc.name, Args: json.RawMessage(tc.args)}); err != nil {
							t.Fatal(err)
						}
						total := time.Since(start)
						if snap.failures != 0 {
							t.Fatal("snapshot capture failed or skipped")
						}
						audits := st.AuditLog()
						if len(audits) != auditCount+1 || handlerCalls != 1 {
							t.Fatalf("expected one new audit and handler invocation: audits=%d handlers=%d", len(audits)-auditCount, handlerCalls)
						}
						audit := audits[len(audits)-1]
						if audit.Error != "" || audit.Approval == registry.ApprovalDenied {
							t.Fatalf("tool did not succeed: %+v", audit)
						}
						if tc.name == "shell.run" {
							if audit.CommandExitCode == nil || *audit.CommandExitCode != 0 || !strings.Contains(audit.ResultContent, "needle") {
								t.Fatalf("shell did not return the fixture contents successfully: %+v", audit)
							}
						}
						if tc.name == "todo.write" && len(st.Todos()) != 1 {
							t.Fatal("todo write did not update the fixture session")
						}
						probeLog(t, latencyProbeRow{Kind: "tool", SnapshotCalls: snap.calls, HandlerCalls: handlerCalls, Case: fmt.Sprintf("%s_%d", tc.name, index), Files: files, Sample: sample, TotalMS: probeMS(total), SnapshotMS: probeMS(snap.elapsed), GateMS: probeMS(gate.elapsed), HandlerMS: probeMS(handlerElapsed), AuditMS: probeMS(audit.Duration)})
					}
					tool.Handler = handler
					if err := reg.Replace(tool); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	})
	t.Run("controlled_hidden_waits", func(t *testing.T) {
		for _, tc := range []struct {
			name           string
			snapshot, gate time.Duration
		}{{"none", 0, 0}, {"snapshot_80ms", 80 * time.Millisecond, 0}, {"gate_80ms", 0, 80 * time.Millisecond}} {
			st, _ := probeState(t, t.TempDir(), false)
			reg := registry.New()
			if err := reg.Register(native.TodoWriteTool(st)); err != nil {
				t.Fatal(err)
			}
			r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), st, "probe")
			r.Now = time.Now
			snap := &latencyProbeSnapshot{delay: tc.snapshot}
			gate := &latencyProbeGate{delay: tc.gate}
			r.Snapshotter = snap
			r.WriteGate = gate
			start := time.Now()
			if _, err := r.executeToolCall(ctx, ModelAction{Type: ActionToolCall, Tool: "todo.write", Args: json.RawMessage(`{"todos":[]}`)}); err != nil {
				t.Fatal(err)
			}
			audits := st.AuditLog()
			if len(audits) != 1 || audits[0].Error != "" {
				t.Fatalf("unexpected audits: %+v", audits)
			}
			probeLog(t, latencyProbeRow{Kind: "controlled", SnapshotCalls: snap.calls, Case: tc.name, TotalMS: probeMS(time.Since(start)), SnapshotMS: probeMS(snap.elapsed), GateMS: probeMS(gate.elapsed), AuditMS: probeMS(audits[0].Duration)})
		}
	})
	t.Run("turn_entry", func(t *testing.T) {
		for _, tc := range []struct {
			name            string
			title, snapshot time.Duration
			history         int
			final           bool
		}{{"baseline", 0, 0, 0, true}, {"title_80ms", 80 * time.Millisecond, 0, 0, true}, {"snapshot_80ms", 0, 80 * time.Millisecond, 0, true}, {"history_100", 0, 0, 100, true}, {"history_1000", 0, 0, 1000, true}, {"orphan_history_1000", 0, 0, 1000, false}} {
			st, _ := probeState(t, t.TempDir(), false)
			for i := 0; i < tc.history; i++ {
				role := session.RoleUser
				if i%2 == 1 {
					role = session.RoleAssistant
				}
				if role == session.RoleAssistant && tc.final {
					st.AddMessageFinal(role, strings.Repeat("fixture ", 125), session.ContentTypePlain)
				} else {
					st.AddMessage(role, strings.Repeat("fixture ", 125), session.ContentTypePlain)
				}
			}
			p := &agenttest.ScriptedProvider{Responses: []string{`{"rationale":"done","action":{"type":"final","content":"Probe complete."}}`}}
			r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), st, "probe")
			r.Now = time.Now
			r.SetForceClass(string(ClassQuestion))
			var metrics TurnMetrics
			r.MetricsObserver = func(m TurnMetrics) { metrics = m }
			if tc.snapshot > 0 {
				r.Snapshotter = &latencyProbeSnapshot{delay: tc.snapshot}
			}
			if tc.title > 0 {
				tp := &agenttest.ScriptedProvider{Responses: []string{"Probe title"}, OnChat: func(int, schema.ChatRequest) { time.Sleep(tc.title) }}
				r.TitleManager = NewTitleManager(tp, "probe", st, time.Second)
			}
			var first time.Time
			bytes := 0
			var requestStarts []time.Time
			var requestSizes []int
			p.OnChat = func(_ int, req schema.ChatRequest) {
				requestStarts = append(requestStarts, time.Now())
				size := 0
				for _, m := range req.Messages {
					size += len(m.Content)
				}
				requestSizes = append(requestSizes, size)
				if first.IsZero() {
					first = time.Now()
					for _, m := range req.Messages {
						bytes += len(m.Content)
					}
				}
			}
			start := time.Now()
			if _, err := r.RunTask(ctx, "Explain the fixture"); err != nil {
				t.Fatal(err)
			}
			if first.IsZero() {
				t.Fatal("no provider request")
			}
			offsets := make([]float64, len(requestStarts))
			for i, at := range requestStarts {
				offsets[i] = probeMS(at.Sub(start))
			}
			probeLog(t, latencyProbeRow{Kind: "turn", RequestStartsMS: offsets, RequestContentBytes: requestSizes, Case: tc.name, TotalMS: probeMS(time.Since(start)), MetricsMS: metrics.DurationMs, FirstProviderCallMS: probeMS(first.Sub(start)), ProviderCalls: p.Calls, RequestBytes: bytes})
		}
	})
	t.Run("shell_startup", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("needle\n"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, slow := range []bool{false, true} {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if slow {
				if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("sleep 0.08\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			sb, err := sandbox.New(sandbox.Config{Backend: "restricted"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"direct_cat", "bash_nonlogin", "restricted_login"} {
				for sample := 0; sample < 5; sample++ {
					start := time.Now()
					var out []byte
					var err error
					switch mode {
					case "direct_cat":
						cmd := exec.CommandContext(ctx, "/bin/cat", "target.txt")
						cmd.Dir = root
						cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
						out, err = cmd.Output()
					case "bash_nonlogin":
						cmd := exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", "cat target.txt")
						cmd.Dir = root
						cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
						out, err = cmd.Output()
					case "restricted_login":
						var res native.CommandResult
						res, err = sb.Run(ctx, native.CommandRequest{Command: "cat target.txt", Dir: root, Timeout: 2 * time.Second, MaxOutputBytes: 1000})
						out = []byte(res.Stdout)
						if res.ExitCode != 0 {
							t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					if string(out) != "needle\n" {
						t.Fatalf("unexpected command output %q", out)
					}
					probeLog(t, latencyProbeRow{Kind: "shell", Case: fmt.Sprintf("%s_profile_slow=%t", mode, slow), Sample: sample, TotalMS: probeMS(time.Since(start))})
				}
			}
		}
	})
}
