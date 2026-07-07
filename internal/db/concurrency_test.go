package db

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

// TestConcurrentWritesDoNotLock reproduces the "database is locked"
// (SQLITE_BUSY) failure that surfaced when the agent persisted tool calls and
// messages from parallel goroutines. It uses a file-based database because the
// lock contention only manifests with an on-disk journal, not with
// :memory:. Without serialized writers / a busy timeout, several of these
// concurrent writes return SQLITE_BUSY.
func TestConcurrentWritesDoNotLock(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "concurrent.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-concurrent"
	if err := db.CreateSession(sessionID, projectID, "concurrency test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"command": "go test"})

	const writers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, writers*2)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			event := registry.AuditEvent{
				Timestamp: time.Now().UTC(),
				ToolName:  "shell.exec",
				Args:      args,
				Risk:      registry.RiskCommand,
				Approval:  registry.ApprovalApproved,
			}
			if err := db.SaveToolCall(sessionID, event); err != nil {
				errCh <- err
			}
			if err := db.SaveMessage(sessionID, "assistant", "hello", "markdown", time.Now().UTC(), "", 0, true); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent write failed: %v", err)
	}
}
