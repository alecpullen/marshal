package acp

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"marshal/internal/acp/service"
	"marshal/internal/app"
)

// EmbeddingConfig configures the embedding's agent host.
type EmbeddingConfig struct {
	// Logger receives the server's and the host's lines: the runtime's
	// logger, so they land in the session log (P3 R11).
	Logger *slog.Logger
	// MaxFrameBytes is the largest frame the agent's end reads: the pipe's
	// cap (P3 R2).
	MaxFrameBytes int
}

// Embedding is the agent host embedded mode runs in its own process (P3 R1,
// R11): marshal acp's host, serving runtimes the embedding started and still
// owns, to the embedding's own client over one connection.
//
// It adopts a runtime with the service pair the embedding built, so the trust
// prompt and its decision are the embedding's, never a pair built from the
// runtime's ServiceDeps (decision 5). A handler's panic ends the connection
// (decision 6). Close releases every session's service and closes no runtime:
// the embedding closes its own afterwards, as it does today.
type Embedding struct {
	host          *agentHost
	logger        *slog.Logger
	maxFrameBytes int
}

// NewEmbedding builds the host.
func NewEmbedding(cfg EmbeddingConfig) (*Embedding, error) {
	if cfg.Logger == nil {
		return nil, errors.New("acp: NewEmbedding requires a logger")
	}
	if cfg.MaxFrameBytes <= 0 {
		return nil, errors.New("acp: NewEmbedding requires a positive MaxFrameBytes")
	}
	host, err := newAgentHost(runConfig{
		// The embedding starts no runtime: every runtime it serves is one the
		// embedding started and adopted.
		startRuntime: func(context.Context, ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("acp: embedded mode starts no runtime")
		},
		closeRuntime: func(context.Context, *app.Runtime) error { return nil },
		logger:       cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &Embedding{host: host, logger: cfg.Logger, maxFrameBytes: cfg.MaxFrameBytes}, nil
}

// Adopt serves rt under its session ID through the service pair the embedding
// built for it.
func (e *Embedding) Adopt(rt *app.Runtime, host *service.Host, sess *service.Session) error {
	return e.host.manager.adopt(rt, host, sess)
}

// Service is the service serving a session now: the adopted one, or the one a
// replacement installed.
func (e *Embedding) Service(sessionID string) (*service.Session, bool) {
	return e.host.manager.Service(sessionID)
}

// Serve serves one connection until it ends or ctx does.
func (e *Embedding) Serve(ctx context.Context, conn io.ReadWriter) error {
	return e.host.serveConnWith(ctx, conn, conn, func(srv *Server) {
		srv.Logger = e.logger
		srv.MaxLineBytes = e.maxFrameBytes
		srv.EndOnPanic = true
	})
}

// Close releases every session's service and closes no runtime.
func (e *Embedding) Close(ctx context.Context) error {
	return e.host.close(ctx)
}
