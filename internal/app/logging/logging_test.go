package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewWithAddSource(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelDebug, true)
	logger.Debug("test message")

	output := buf.String()
	if !strings.Contains(output, "source=") {
		t.Fatalf("expected source= in output with AddSource=true, got: %q", output)
	}
}

func TestNewWithoutAddSource(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelDebug, false)
	logger.Debug("test message")

	output := buf.String()
	if strings.Contains(output, "source=") {
		t.Fatalf("expected no source= in output with AddSource=false, got: %q", output)
	}
}
