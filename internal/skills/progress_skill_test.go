package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/progress"
)

// progressSkillBody returns the embedded progress-reporting skill's body.
func progressSkillBody(t *testing.T) string {
	t.Helper()
	idx := NewIndex()
	if err := loadBuiltIns(idx); err != nil {
		t.Fatalf("loadBuiltIns: %v", err)
	}
	sk, ok := idx.Load("progress-reporting")
	if !ok {
		t.Fatal("built-in progress-reporting skill is missing")
	}
	return sk.Body
}

// producerBlocks maps each embedded producer's path to its source. A block is
// a fenced python block whose first line is the path comment the skill uses.
func producerBlocks(body string) map[string]string {
	const marker = "# .marshal/progress/"
	blocks := make(map[string]string)
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```python" {
			continue
		}
		start := i + 1
		end := start
		for end < len(lines) && strings.TrimSpace(lines[end]) != "```" {
			end++
		}
		if start < len(lines) {
			first := strings.TrimSpace(lines[start])
			if strings.HasPrefix(first, marker) {
				name := strings.TrimSpace(strings.TrimPrefix(first, marker))
				blocks[name] = strings.Join(lines[start:end], "\n")
			}
		}
		i = end
	}
	return blocks
}

// TestProgressSkillExampleRecordsDecode proves every record the body shows the
// model is a record Marshal itself accepts.
func TestProgressSkillExampleRecordsDecode(t *testing.T) {
	body := progressSkillBody(t)
	found := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, progress.Prefix) {
			continue
		}
		found++
		if _, err := progress.DecodeEvent([]byte(line), 0, time.Now()); err != nil {
			t.Errorf("example record does not decode: %v\n%s", err, line)
		}
	}
	if found == 0 {
		t.Fatal("skill body has no example record line starting with the progress prefix")
	}
}

func TestProgressSkillHasEveryProducer(t *testing.T) {
	blocks := producerBlocks(progressSkillBody(t))
	want := []string{"marshal_progress.py", "download.py", "transfer.py", "custom.py"}
	for _, name := range want {
		if _, ok := blocks[name]; !ok {
			t.Errorf("skill body has no producer block for %s", name)
		}
	}
	if len(blocks) != len(want) {
		names := make([]string, 0, len(blocks))
		for name := range blocks {
			names = append(names, name)
		}
		t.Errorf("producer blocks = %v, want exactly %v", names, want)
	}
}

// runProducer runs one producer from root and returns its stdout and exit code.
func runProducer(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("python3", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run %v: %v", args, err)
	}
	return string(out), exitErr.ExitCode()
}

// decodeRecords decodes every control record in stdout, threading the previous
// sequence so a replay or a regression in ordering fails.
func decodeRecords(t *testing.T, stdout string) []*progress.Snapshot {
	t.Helper()
	var snaps []*progress.Snapshot
	var prev uint64
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(line, progress.Prefix) {
			continue
		}
		snap, err := progress.DecodeEvent([]byte(line), prev, time.Now())
		if err != nil {
			t.Fatalf("record does not decode: %v\n%s", err, line)
		}
		prev = snap.Sequence
		snaps = append(snaps, snap)
	}
	return snaps
}

func metricByID(snap *progress.Snapshot, id string) *progress.Metric {
	for i := range snap.Metrics {
		if snap.Metrics[i].ID == id {
			return &snap.Metrics[i]
		}
	}
	return nil
}

// TestProgressSkillProducersRun executes the embedded producers. The transfer
// producer needs SSH, so it is only proven to import.
func TestProgressSkillProducersRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not available")
	}
	blocks := producerBlocks(progressSkillBody(t))
	root := t.TempDir()
	dir := filepath.Join(root, ".marshal", "progress")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range blocks {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("download", func(t *testing.T) {
		src := filepath.Join(root, "source.bin")
		data := make([]byte, 1<<20)
		for i := range data {
			data[i] = byte(i*31 + i/251)
		}
		if err := os.WriteFile(src, data, 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, code := runProducer(t, root, ".marshal/progress/download.py", "file://"+src, "out.bin")
		if code != 0 {
			t.Fatalf("download exited %d\n%s", code, stdout)
		}
		snaps := decodeRecords(t, stdout)
		if len(snaps) < 2 {
			t.Fatalf("download emitted %d records, want at least 2", len(snaps))
		}
		received := metricByID(snaps[len(snaps)-1], "received")
		if received == nil {
			t.Fatalf("last record has no received metric: %+v", snaps[len(snaps)-1].Metrics)
		}
		if received.Unit != "bytes" {
			t.Errorf("received unit = %q, want bytes", received.Unit)
		}
		if received.Total == nil || *received.Total != float64(len(data)) || received.Current != float64(len(data)) {
			t.Errorf("received = %v/%v, want %d/%d", received.Current, received.Total, len(data), len(data))
		}
		got, err := os.ReadFile(filepath.Join(root, "out.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Error("out.bin does not match the source")
		}
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if last := lines[len(lines)-1]; !strings.HasPrefix(last, "Downloaded") {
			t.Errorf("last stdout line = %q, want it to start with Downloaded", last)
		}
		// A second run must refuse to overwrite the destination.
		if _, code := runProducer(t, root, ".marshal/progress/download.py", "file://"+src, "out.bin"); code == 0 {
			t.Error("second download exited 0, want a refusal")
		}
		again, err := os.ReadFile(filepath.Join(root, "out.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, data) {
			t.Error("second download changed out.bin")
		}
	})

	t.Run("custom", func(t *testing.T) {
		a := filepath.Join(root, "a.bin")
		b := filepath.Join(root, "b.bin")
		if err := os.WriteFile(a, []byte("alpha payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b, []byte("beta payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, code := runProducer(t, root, ".marshal/progress/custom.py", "manifest.json", "a.bin", "b.bin")
		if code != 0 {
			t.Fatalf("custom exited %d\n%s", code, stdout)
		}
		snaps := decodeRecords(t, stdout)
		if len(snaps) == 0 {
			t.Fatal("custom emitted no records")
		}
		sawHash := false
		for _, snap := range snaps {
			if p := snap.Phase; p != nil && p.ID == "hash" && p.Index == 1 && p.Total == 2 {
				sawHash = true
			}
		}
		if !sawHash {
			t.Error("no record has phase hash 1/2")
		}
		last := snaps[len(snaps)-1]
		if p := last.Phase; p == nil || p.ID != "manifest" || p.Index != 2 || p.Total != 2 {
			t.Errorf("last phase = %+v, want manifest 2/2", last.Phase)
		}
		files := metricByID(last, "files")
		if files == nil || files.Current != 2 || files.Total == nil || *files.Total != 2 {
			t.Errorf("files metric = %+v, want 2/2", files)
		}
		sawDone := false
		for _, row := range last.Rows {
			if row.ID == "manifest" && row.State == "done" {
				sawDone = true
			}
		}
		if !sawDone {
			t.Errorf("last record's manifest row is not done: %+v", last.Rows)
		}

		raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("manifest.json does not decode: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("manifest has %d entries, want 2", len(entries))
		}
		for i, path := range []string{a, b} {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if want := hex.EncodeToString(sum[:]); entries[i].SHA256 != want {
				t.Errorf("entry %d sha256 = %q, want %q", i, entries[i].SHA256, want)
			}
		}
		// The manifest is opened with mode "x", so a second run must fail.
		if _, code := runProducer(t, root, ".marshal/progress/custom.py", "manifest.json", "a.bin", "b.bin"); code == 0 {
			t.Error("second custom run exited 0, want a refusal")
		}
	})

	t.Run("transfer", func(t *testing.T) {
		if _, code := runProducer(t, root, ".marshal/progress/transfer.py", "--help"); code != 0 {
			t.Fatalf("transfer.py --help exited %d, want 0", code)
		}
	})
}
