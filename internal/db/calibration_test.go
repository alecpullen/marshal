package db

import (
	"testing"
	"time"
)

func TestInsertAndSummarizeCalibration(t *testing.T) {
	d := newTestDB(t)
	defer d.Close()
	pid, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateSession("s1", pid, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ est, prov int }{
		{100, 120}, {200, 250}, {400, 390},
	} {
		if _, err := d.InsertCalibrationSample(CalibrationSample{
			ProjectID: pid, SessionID: "s1", Provider: "ollama", Model: "qwen",
			EstimatorTokens: s.est, ProviderTokens: s.prov, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	sum, err := d.CalibrationSummary(pid, "s1")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Samples != 3 {
		t.Fatalf("samples = %d, want 3", sum.Samples)
	}
	// ratios: 100/120=0.833, 200/250=0.8, 400/390=1.025
	wantMean := (0.833333 + 0.8 + 1.025641) / 3
	if !approxEqual(sum.MeanRatio, wantMean, 0.01) {
		t.Errorf("mean ratio = %.4f, want ~%.4f", sum.MeanRatio, wantMean)
	}
	if sum.MaxRatio < 1.0 {
		t.Errorf("max ratio = %.4f, want >= 1.0", sum.MaxRatio)
	}
	if sum.MinRatio > 0.85 {
		t.Errorf("min ratio = %.4f, want < 0.85", sum.MinRatio)
	}
}

func TestCalibrationSummaryEmpty(t *testing.T) {
	d := newTestDB(t)
	defer d.Close()
	pid, err := d.GetOrCreateProject("/repo2", "repo2")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := d.CalibrationSummary(pid, "s-none")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Samples != 0 {
		t.Fatalf("samples = %d, want 0", sum.Samples)
	}
}

func TestInsertCalibrationSamplePrunesOldRows(t *testing.T) {
	d := newTestDB(t)
	defer d.Close()
	pid, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateSession("s1", pid, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxCalibrationRows+2; i++ {
		_, err := d.InsertCalibrationSample(CalibrationSample{
			ProjectID:       pid,
			SessionID:       "s1",
			Provider:        "p",
			Model:           "m",
			EstimatorTokens: i,
			ProviderTokens:  i + 1,
			CreatedAt:       time.Now(),
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	sum, err := d.CalibrationSummary(pid, "")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Samples != maxCalibrationRows {
		t.Fatalf("samples = %d, want %d", sum.Samples, maxCalibrationRows)
	}
}

func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < tol
}
