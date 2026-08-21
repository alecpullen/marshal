package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/db"
)

func TestRunCalibrateTokens_FromDBEmpty(t *testing.T) {
	tmp := t.TempDir()
	if err := setupCalibrateDB(t, tmp); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runCalibrateTokens([]string{"--from-db", "--project", tmp}, &out)
	if err != nil {
		t.Fatalf("runCalibrateTokens: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("No calibration samples")) {
		t.Errorf("expected 'No calibration samples' in output, got:\n%s", out.String())
	}
}

func TestRunCalibrateTokens_FromDBWithSamples(t *testing.T) {
	tmp := t.TempDir()
	database, pid := setupCalibrateDBWithProject(t, tmp)
	for _, s := range []struct{ est, prov int }{
		{100, 120}, {200, 250},
	} {
		if _, err := database.InsertCalibrationSample(db.CalibrationSample{
			ProjectID: pid, SessionID: "s1", Provider: "ollama", Model: "qwen",
			EstimatorTokens: s.est, ProviderTokens: s.prov, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()

	var out bytes.Buffer
	if err := runCalibrateTokens([]string{"--from-db", "--project", tmp}, &out); err != nil {
		t.Fatalf("runCalibrateTokens: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("samples:")) {
		t.Errorf("expected 'samples:' in report, got:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("ratio")) {
		t.Errorf("expected 'ratio' in report, got:\n%s", out.String())
	}
}

func setupCalibrateDB(t *testing.T, dir string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		return err
	}
	database, err := db.Open(db.Path(dir))
	if err != nil {
		return err
	}
	defer database.Close()
	return database.Migrate()
}

func setupCalibrateDBWithProject(t *testing.T, dir string) (*db.DB, int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, err := database.GetOrCreateProject(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return database, pid
}

func TestRunCalibrateTokensNoCtxParam(t *testing.T) {
	// Verify the function works without a context parameter.
	// --from-db not set, so it should print usage and return nil.
	var stdout bytes.Buffer
	if err := runCalibrateTokens([]string{}, &stdout); err != nil {
		t.Fatalf("runCalibrateTokens: %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("calibrate-tokens")) {
		t.Errorf("expected usage output, got: %s", stdout.String())
	}
}
