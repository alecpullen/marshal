package logging

import (
	"io"
	"log/slog"
)

// New creates a slog logger writing to w at the given level. When addSource
// is true, each log record includes source file and line (useful for debug
// builds). When false, source info is omitted for cleaner production logs.
func New(w io.Writer, level slog.Level, addSource bool) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:     level,
		AddSource: addSource,
	})
	return slog.New(handler)
}
