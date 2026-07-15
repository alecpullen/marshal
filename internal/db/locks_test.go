package db

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProjectLocksAreExclusive(t *testing.T) {
	locks := NewProjectLocks()
	unlockA := locks.Lock(1)

	done := make(chan struct{})
	go func() {
		unlockB := locks.Lock(1)
		defer unlockB()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("second Lock(1) returned while first was held")
	case <-time.After(50 * time.Millisecond):
		// expected: blocked
	}

	unlockA()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Lock(1) did not return after first unlock")
	}
}

func TestSaveFileIndexSerialisesByProject(t *testing.T) {
	db, projectID := openMetricsTestDB(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := db.SaveFileIndex(projectID, []FileIndex{
				{Path: fmt.Sprintf("f%d.go", i), Language: "go", Hash: "h", SizeBytes: 1, LastIndexedAt: time.Now().UTC()},
			}); err != nil {
				t.Errorf("save: %v", err)
			}
		}(i)
	}
	wg.Wait()

	files, err := db.GetFileIndex(projectID, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected exactly 1 file (last writer wins), got %d", len(files))
	}
}
