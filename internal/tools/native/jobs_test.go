package native

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestJobManagerStartAndKill(t *testing.T) {
	jm := NewJobManager(defaultCommandRunner(), 25, 8*time.Hour)
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
	jm := NewJobManager(defaultCommandRunner(), 2, 8*time.Hour)
	t.Cleanup(jm.Shutdown)
	_, _ = jm.Start(context.Background(), "sleep 30", 5*time.Second)
	_, _ = jm.Start(context.Background(), "sleep 30", 5*time.Second)
	_, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err == nil {
		t.Fatal("expected too many jobs error")
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
