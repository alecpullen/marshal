package catalog

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestLookupKnownModel(t *testing.T) {
	w, out := Lookup("qwen2.5-coder:14b", nil)
	if w == 0 {
		t.Fatal("known model qwen2.5-coder:14b should resolve a context window")
	}
	if out == 0 {
		t.Fatal("known model should resolve max output tokens")
	}
}

func TestLookupUnknownModelWithNilLoggerNoWarn(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, maxOut := Lookup("unknown-model-xyz", nil)
	if ctx != 8192 {
		t.Errorf("unknown model contextWindow = %d, want 8192", ctx)
	}
	if maxOut != 4096 {
		t.Errorf("unknown model maxOutput = %d, want 4096", maxOut)
	}
	if buf.Len() > 0 {
		t.Errorf("expected no log output with nil logger, got: %s", buf.String())
	}
}

func TestLookupUnknownModelWithLoggerWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ctx, maxOut := Lookup("unknown-model-xyz", logger)
	if ctx != 8192 {
		t.Errorf("unknown model contextWindow = %d, want 8192", ctx)
	}
	if maxOut != 4096 {
		t.Errorf("unknown model maxOutput = %d, want 4096", maxOut)
	}
	if !bytes.Contains(buf.Bytes(), []byte("unknown-model-xyz")) {
		t.Errorf("expected warning log mentioning model name, got: %s", buf.String())
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	w1, _ := Lookup("Qwen2.5-Coder:14B", nil)
	w2, _ := Lookup("qwen2.5-coder:14b", nil)
	if w1 != w2 || w1 == 0 {
		t.Fatalf("case-insensitive lookup mismatch: %d vs %d", w1, w2)
	}
}
