package native

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

func TestJobManagerStartAndKill(t *testing.T) {
	jm := NewJobManager(defaultCommandRunner(), "", 25, 8*time.Hour)
	t.Cleanup(jm.Shutdown)
	id, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	info, _, err := jm.Output(id, 10)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("status = %q, want running", info.Status)
	}
	if err := jm.Kill(id); err != nil {
		t.Fatalf("kill: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	info, _, _ = jm.Output(id, 10)
	if info.Status != StatusKilled {
		t.Fatalf("status after kill = %q, want killed", info.Status)
	}
}

func TestJobManagerEnforcesMaxJobs(t *testing.T) {
	jm := NewJobManager(defaultCommandRunner(), "", 2, 8*time.Hour)
	t.Cleanup(jm.Shutdown)
	_, _ = jm.Start(context.Background(), "sleep 30", 5*time.Second)
	_, _ = jm.Start(context.Background(), "sleep 30", 5*time.Second)
	_, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err == nil {
		t.Fatal("expected too many jobs error")
	}
}

func TestJobManagerPublishesViaBroker(t *testing.T) {
	jm := NewJobManager(defaultCommandRunner(), "", 25, 8*time.Hour)
	t.Cleanup(jm.Shutdown)
	broker := pubsub.NewBroker[JobEvent]()
	t.Cleanup(broker.Close)
	subCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := broker.Subscribe(subCtx)
	jm.SetBroker(broker)

	// Start a long-running job; the first notifyChange publishes a
	// +1 delta. Read it from the subscription and assert the count + delta.
	id, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = jm.Kill(id) })

	select {
	case ev := <-ch:
		if ev.Type != "jobs" {
			t.Fatalf("type = %q, want jobs", ev.Type)
		}
		if ev.Payload.Count != 1 || ev.Payload.Delta != 1 {
			t.Fatalf("payload = %+v, want count=1 delta=+1", ev.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no job-event published within 1s")
	}
}

func TestJobManagerRunsInWorkspaceDir(t *testing.T) {
	root := t.TempDir()
	runner := &recordingProcessRunner{}
	jm := NewJobManager(runner, root, 25, 8*time.Hour)
	t.Cleanup(jm.Shutdown)

	id, err := jm.Start(context.Background(), "echo hi", 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-jm.jobs[id].done

	var startReq *CommandRequest
	for i := len(runner.requests) - 1; i >= 0; i-- {
		if runner.requests[i].Command == "echo hi" {
			startReq = &runner.requests[i]
			break
		}
	}
	if startReq == nil {
		t.Fatal("Start did not call runner.Start")
	}
	if startReq.Dir != root {
		t.Fatalf("Start Dir = %q, want %q", startReq.Dir, root)
	}
}

func TestJobManagerEvictsCompletedJobsByRetention(t *testing.T) {
	root := t.TempDir()
	jm := NewJobManager(defaultCommandRunner(), root, 25, 100*time.Millisecond)
	t.Cleanup(jm.Shutdown)

	id, err := jm.Start(context.Background(), "echo hello", 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-jm.jobs[id].done

	if _, _, err := jm.Output(id, 0); err != nil {
		t.Fatalf("output right after completion: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	if jobs := jm.List(); len(jobs) != 0 {
		t.Fatalf("List returned %d jobs, want 0", len(jobs))
	}
	if _, _, err := jm.Output(id, 0); err == nil {
		t.Fatal("expected job to be evicted")
	}
}

func TestShellRunBackgroundStartsJob(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("shell.run background returned error: %v", err)
	}
	if !strings.Contains(res.Content, "job_id: job-") {
		t.Fatalf("content = %q, want job_id", res.Content)
	}

	if state.RunningJobsCount() != 1 {
		t.Fatalf("running jobs = %d, want 1", state.RunningJobsCount())
	}
}

func TestShellRunBackgroundUsesWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"touch bg-file.txt","background":true,"timeout_seconds":5}`); err != nil {
		t.Fatalf("shell.run background returned error: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if _, err := os.Stat(filepath.Join(root, "bg-file.txt")); err != nil {
		t.Fatalf("expected file in workspace: %v", err)
	}
}

func TestRegisterAllHonorsConfigBackgroundLimits(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Tools.Shell.MaxBackgroundJobs = 1
	cfg.Tools.Shell.BackgroundRetention = 30 * time.Minute

	reg := registry.New()
	state := session.New(cfg, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, Config: cfg, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("first background job: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`); err == nil {
		t.Fatal("expected too many jobs error")
	}

	jobID := strings.TrimPrefix(strings.TrimSpace(res.Content), "job_id: ")
	if _, err := invokeTool(t, reg, "job.kill", fmt.Sprintf(`{"job_id":"%s"}`, jobID)); err != nil {
		t.Fatalf("job.kill: %v", err)
	}
}

func TestJobOutputAndListTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("start background job: %v", err)
	}
	jobID := strings.TrimPrefix(strings.TrimSpace(res.Content), "job_id: ")

	listRes, err := invokeTool(t, reg, "job.list", `{}`)
	if err != nil {
		t.Fatalf("job.list: %v", err)
	}
	if !strings.Contains(listRes.Content, jobID) {
		t.Fatalf("job.list content = %q, want %q", listRes.Content, jobID)
	}

	outRes, err := invokeTool(t, reg, "job.output", fmt.Sprintf(`{"job_id":"%s"}`, jobID))
	if err != nil {
		t.Fatalf("job.output: %v", err)
	}
	if !strings.Contains(outRes.Content, "status: running") {
		t.Fatalf("job.output content = %q, want status running", outRes.Content)
	}

	if _, err := invokeTool(t, reg, "job.kill", fmt.Sprintf(`{"job_id":"%s"}`, jobID)); err != nil {
		t.Fatalf("job.kill: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	outRes, err = invokeTool(t, reg, "job.output", fmt.Sprintf(`{"job_id":"%s"}`, jobID))
	if err != nil {
		t.Fatalf("job.output after kill: %v", err)
	}
	if !strings.Contains(outRes.Content, "status: killed") {
		t.Fatalf("job.output content after kill = %q, want status killed", outRes.Content)
	}
}

func defaultCommandRunner() ProcessRunner {
	return execRunner{}
}

type recordingProcessRunner struct {
	requests []CommandRequest
}

func (f *recordingProcessRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	f.requests = append(f.requests, req)
	return CommandResult{}, nil
}

func (f *recordingProcessRunner) Start(req CommandRequest) (*runningCmd, error) {
	f.requests = append(f.requests, req)
	cmd := exec.Command("true")
	cmd.Dir = req.Dir
	rc := &runningCmd{cmd: cmd}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return rc, nil
}
