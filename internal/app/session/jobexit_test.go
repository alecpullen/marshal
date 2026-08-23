package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestJobExitAppearsInTranscript(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.AddJobExit(JobExit{ID: "job-1", Command: "go test", ExitCode: 0, Duration: time.Second})
	var found bool
	for _, item := range s.Transcript() {
		if item.Kind == KindJobExit {
			if item.JobExit == nil {
				t.Fatal("KindJobExit item has a nil JobExit payload")
			}
			if item.JobExit.ID != "job-1" {
				t.Fatalf("wrong job: %q", item.JobExit.ID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("job exit did not reach the transcript")
	}
}

func TestJobExitStampsTime(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.AddJobExit(JobExit{ID: "job-1"})
	items := s.Transcript()
	if items[0].Timestamp.IsZero() {
		t.Fatal("a zero At must be stamped, or transcript ordering breaks")
	}
}
