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
