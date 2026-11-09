package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"marshal/internal/acp"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/app"
)

// serverStopTimeout bounds how long a close waits for the agent's server
// goroutine to return.
const serverStopTimeout = 5 * time.Second

// agentConn is one pass's connection: the embedding serving the runtime, the
// server's goroutine, and the client the TUI runs on.
type agentConn struct {
	emb  *acp.Embedding
	host *rpc.Host
	sess *rpc.Session
	rt   *app.Runtime

	cancel context.CancelFunc
	done   chan struct{}
	lostCh chan struct{}

	mu       sync.Mutex
	serveErr error
	closed   bool
}

// loggerOf is the runtime's logger, or the default one when it has none.
func loggerOf(rt *app.Runtime) *slog.Logger {
	if rt != nil && rt.Logger != nil {
		return rt.Logger
	}
	return slog.Default()
}

// dialAgent adopts rt with the pair, serves it on the agent's end of a pipe
// (wrapped by wrap when it is not nil), dials the client's end and attaches
// rt's session.
func dialAgent(ctx context.Context, rt *app.Runtime, host *service.Host, sess *service.Session, wrap func(io.ReadWriteCloser) io.ReadWriteCloser) (*agentConn, error) {
	emb, err := acp.NewEmbedding(acp.EmbeddingConfig{Logger: loggerOf(rt), MaxFrameBytes: rpc.MaxFrameBytes})
	if err != nil {
		return nil, fmt.Errorf("launch: build the embedding: %w", err)
	}
	if err := emb.Adopt(rt, host, sess); err != nil {
		return nil, fmt.Errorf("launch: adopt the runtime: %w", err)
	}

	client, agent := rpc.Pipe()
	var agentEnd io.ReadWriteCloser = agent
	if wrap != nil {
		agentEnd = wrap(agent)
	}

	// The server's context does not end with the signal context: the TUI's own
	// shutdown still makes calls, and they must be answered (decision 7).
	serveCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &agentConn{emb: emb, rt: rt, cancel: cancel, done: make(chan struct{}), lostCh: make(chan struct{})}
	go func() {
		defer close(c.done)
		serveErr := emb.Serve(serveCtx, agentEnd)
		c.mu.Lock()
		c.serveErr = serveErr
		c.mu.Unlock()
	}()

	clientHost, err := rpc.Dial(serveCtx, client, rpc.DialOptions{SharedFilesystem: true, AgentVersion: acp.AgentVersion})
	if err != nil {
		_ = c.close(context.Background())
		return nil, fmt.Errorf("launch: dial the agent: %w", err)
	}
	c.host = clientHost

	clientSess, err := clientHost.Attach(serveCtx, rt.SessionID)
	if err != nil {
		_ = c.close(context.Background())
		return nil, fmt.Errorf("launch: attach %q: %w", rt.SessionID, err)
	}
	c.sess = clientSess

	// lost closes when the connection ends on its own, before close was called.
	go func() {
		select {
		case <-clientHost.Done():
			c.mu.Lock()
			if !c.closed {
				close(c.lostCh)
			}
			c.mu.Unlock()
		case <-c.done:
		}
	}()
	return c, nil
}

// lost is closed when the connection ended before close was called.
func (c *agentConn) lost() <-chan struct{} { return c.lostCh }

// err is the agent's error once the connection is lost: the server's, or else
// the client's.
func (c *agentConn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.serveErr != nil {
		return c.serveErr
	}
	if c.host != nil {
		return c.host.Err()
	}
	return nil
}

// close closes the client, stops the server and waits for it, then closes the
// embedding. A service never outlives the runtime it reads: the embedding's
// Close releases the service, and launch closes the runtime only afterwards.
func (c *agentConn) close(ctx context.Context) error {
	c.mu.Lock()
	alreadyClosed := c.closed
	c.closed = true
	c.mu.Unlock()
	if alreadyClosed {
		return nil
	}

	var errs []error
	if c.host != nil {
		if err := c.host.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(serverStopTimeout):
		loggerOf(c.rt).Warn("launch: the agent's server did not stop; closing anyway")
	}
	if err := c.emb.Close(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// withAgentConn wraps the agent's end of each pass's pipe.
func withAgentConn(wrap func(io.ReadWriteCloser) io.ReadWriteCloser) Option {
	return func(o *options) { o.agentConnWrap = wrap }
}

// withServed hands each pass's service pair to fn before the program runs.
func withServed(fn func(*service.Host, *service.Session)) Option {
	return func(o *options) { o.onServed = fn }
}
