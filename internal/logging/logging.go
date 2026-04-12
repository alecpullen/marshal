// Package logging configures a structured slog logger for marshal.
//
// Call Init once at startup. After that, use slog.Default() or the standard
// slog package-level helpers everywhere.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// Options controls logger initialisation.
type Options struct {
	// Verbose enables DEBUG-level output. When false, INFO and above are logged.
	Verbose bool
	// File is an optional path for a JSON log sink alongside stderr.
	// Empty string disables the file sink.
	File string
}

// Init configures slog.Default() according to opts.
// It must be called before any logging.
func Init(opts Options) error {
	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}

	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})

	var handler slog.Handler = stderrHandler

	if opts.File != "" {
		f, err := openLogFile(opts.File)
		if err != nil {
			return err
		}
		fileHandler := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})
		handler = &multiHandler{handlers: []slog.Handler{stderrHandler, fileHandler}}
	}

	slog.SetDefault(slog.New(handler))
	return nil
}

func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
}

// multiHandler fans log records out to multiple slog.Handler implementations.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: hs}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: hs}
}

// ensure multiHandler implements slog.Handler at compile time.
var _ slog.Handler = (*multiHandler)(nil)

// MustInit is like Init but panics on error.
func MustInit(opts Options) {
	if err := Init(opts); err != nil {
		panic("logging.Init: " + err.Error())
	}
}

// WithComponent returns a logger annotated with a "component" attribute.
func WithComponent(name string) *slog.Logger {
	return slog.Default().With("component", name)
}

// Noop configures slog to discard all output. Useful in tests.
func Noop() {
	slog.SetDefault(slog.New(noopHandler{}))
}

type noopHandler struct{}

func (noopHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (noopHandler) Handle(context.Context, slog.Record) error { return nil }
func (noopHandler) WithAttrs([]slog.Attr) slog.Handler        { return noopHandler{} }
func (noopHandler) WithGroup(string) slog.Handler             { return noopHandler{} }

var _ slog.Handler = noopHandler{}

// Reader exposes stderr output. Mostly used via io.Writer indirection in tests.
func NewTestWriter(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
