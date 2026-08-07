package session

import (
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestFinishSDDRunStoresError(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{
		Active:     true,
		DoneTasks:  1,
		TotalTasks: 3,
		StartedAt:  time.Now().Add(-time.Minute),
	})
	runErr := errors.New("transient timeout\nsecond line detail")
	ended := time.Now()
	s.FinishSDDRun(false, ended, runErr)

	p := s.SDDProgress()
	if p.Active {
		t.Error("Active = true, want false")
	}
	if p.Succeeded {
		t.Error("Succeeded = true, want false")
	}
	if p.Error != "transient timeout" {
		t.Errorf("Error = %q, want first line", p.Error)
	}
}

func TestFinishSDDRunSuccessClearsError(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetSDDProgress(SDDProgress{Active: true, Error: "old error"})
	s.FinishSDDRun(true, time.Now(), nil)
	if p := s.SDDProgress(); p.Error != "" {
		t.Errorf("Error = %q, want empty on success", p.Error)
	}
}

func TestSDDProgressCarriesOutcomePaths(t *testing.T) {
	s := newTestState()
	s.SetSDDProgress(SDDProgress{
		Branch:       "pipeline/add-retries",
		BaseRef:      "main",
		LedgerPath:   "/repo/.marshal/pipeline/add-retries/ledger.md",
		ArtifactsDir: "/repo/.marshal/pipeline/add-retries",
	})

	p := s.SDDProgress()
	if p.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want main — the diff needs a base to compare against", p.BaseRef)
	}
	if p.LedgerPath == "" || p.ArtifactsDir == "" {
		t.Errorf("outcome paths lost: %#v", p)
	}
}

func TestFinishSDDRunKeepsOutcomePaths(t *testing.T) {
	s := newTestState()
	s.SetSDDProgress(SDDProgress{
		Branch: "pipeline/p", BaseRef: "main",
		LedgerPath: "/l.md", ArtifactsDir: "/a",
		TotalTasks: 3, DoneTasks: 3,
	})

	s.FinishSDDRun(true, time.Now(), nil)

	p := s.SDDProgress()
	if p.Branch == "" || p.BaseRef == "" || p.LedgerPath == "" || p.ArtifactsDir == "" {
		t.Errorf("FinishSDDRun must keep the outcome reachable after the run ends: %#v", p)
	}
}

func TestSDDProgressCarriesTaskTimings(t *testing.T) {
	start := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	s := newTestState()
	s.SetSDDProgress(SDDProgress{
		TotalTasks: 3,
		TaskTimings: []TaskTiming{
			{StartedAt: start, EndedAt: start.Add(2 * time.Minute)},
			{StartedAt: start.Add(2 * time.Minute)},
			{},
		},
	})

	got := s.SDDProgress().TaskTimings
	if len(got) != 3 {
		t.Fatalf("got %d timings, want 3", len(got))
	}
	if d := got[0].EndedAt.Sub(got[0].StartedAt); d != 2*time.Minute {
		t.Errorf("task 1 duration = %v, want 2m", d)
	}
	if !got[1].EndedAt.IsZero() {
		t.Error("an in-flight task must have a zero EndedAt")
	}
	if !got[2].StartedAt.IsZero() {
		t.Error("a not-yet-started task must have a zero StartedAt")
	}
}

func TestFinishSDDRunKeepsTaskTimings(t *testing.T) {
	start := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	s := newTestState()
	s.SetSDDProgress(SDDProgress{
		TotalTasks:  2,
		TaskTimings: []TaskTiming{{StartedAt: start, EndedAt: start.Add(time.Minute)}, {}},
	})

	s.FinishSDDRun(true, start.Add(5*time.Minute), nil)

	if len(s.SDDProgress().TaskTimings) != 2 {
		t.Error("FinishSDDRun must keep timings so /run can show durations after the run")
	}
}
