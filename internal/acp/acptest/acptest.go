// Package acptest is the test agent: a test's state served over an
// in-memory pipe by the agent host embedded mode runs, and the client dialled
// to it (P3 V2). Only tests import it.
package acptest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// UnnamedSessionID is the ID a state built without one is served under.
const UnnamedSessionID = "acptest-session"

type options struct {
	unsettled bool
}

// Option adjusts Open.
type Option func(*options)

// Unsettled makes the session's reads the client's own, with no comparison
// and no sync, for a budget that measures the pipe itself.
func Unsettled() Option {
	return func(o *options) { o.unsettled = true }
}

// discardLogger swallows the host's and the server's lines.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Host is the client's host. NewSession wraps the replacement's session as
// Open wraps the first.
type Host struct {
	*rpc.Host

	emb   *acp.Embedding
	tb    testing.TB
	opts  options
	state *session.State
}

// Session is the client's session. Its ten mirror reads compare the mirror
// with the service's own answer and sync when they differ; everything else is
// the client's.
type Session struct {
	*rpc.Session

	emb   *acp.Embedding
	tb    testing.TB
	opts  options
	first *session.State

	mu    sync.Mutex
	syncs int
}

// Open serves d's state through an acp.Embedding over a bare runtime, dials
// it, attaches the state's session and returns the client's host and session.
// Everything closes with tb.
func Open(tb testing.TB, d service.Deps, opts ...Option) (*Host, *Session) {
	tb.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	st := d.State
	if st == nil {
		tb.Fatal("acptest: Open requires a state")
	}
	id := st.SessionID()
	if id == "" {
		// Every call names its session, so a state built without an ID is
		// served under a fixed one (decision 9).
		id = UnnamedSessionID
	}

	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	rt := &app.Runtime{
		State:       st,
		SessionID:   id,
		EventBroker: broker,
		Config:      st.Config,
		WorkingDir:  st.WorkingDir,
		Logger:      discardLogger(),
	}

	// A replacement points the runtime at the new state, as Runtime.NewSession
	// does, and carries the same broker, so the agent's own reads settle too.
	if d.NewSession != nil {
		inner := d.NewSession
		d.NewSession = func(name string) (*session.State, error) {
			next, err := inner(name)
			if err != nil {
				return nil, err
			}
			next.SetEventBroker(broker)
			rt.State = next
			rt.SessionID = next.SessionID()
			return next, nil
		}
	}

	servedHost, served := service.New(d)
	emb, err := acp.NewEmbedding(acp.EmbeddingConfig{Logger: discardLogger(), MaxFrameBytes: rpc.MaxFrameBytes})
	if err != nil {
		tb.Fatalf("acptest: NewEmbedding: %v", err)
	}
	if err := emb.Adopt(rt, servedHost, served); err != nil {
		tb.Fatalf("acptest: Adopt: %v", err)
	}

	client, agent := rpc.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = emb.Serve(ctx, agent)
	}()

	host, err := rpc.Dial(ctx, client, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		cancel()
		tb.Fatalf("acptest: dial: %v", err)
	}
	// One cleanup, in the order a connection closes: the client, the server,
	// then the embedding that releases the service.
	tb.Cleanup(func() {
		_ = host.Close()
		_ = client.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			tb.Error("acptest: the embedding did not stop")
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := emb.Close(closeCtx); err != nil {
			tb.Errorf("acptest: Close: %v", err)
		}
	})

	sess, err := host.Attach(context.Background(), id)
	if err != nil {
		tb.Fatalf("acptest: attach %q: %v", id, err)
	}
	return &Host{Host: host, emb: emb, tb: tb, opts: o, state: st},
		&Session{Session: sess, emb: emb, tb: tb, opts: o, first: st}
}

// NewSession wraps the client host's replacement as Open wraps the first.
func (h *Host) NewSession(ctx context.Context, name string) (acpclient.Session, error) {
	sess, err := h.Host.NewSession(ctx, name)
	if err != nil {
		return nil, err
	}
	rs, ok := sess.(*rpc.Session)
	if !ok {
		return nil, fmt.Errorf("acptest: the replacement is %T, want *rpc.Session", sess)
	}
	return &Session{Session: rs, emb: h.emb, tb: h.tb, opts: h.opts, first: h.state}, nil
}

// State is the state the session serves now.
func (s *Session) State() *session.State {
	if srv, ok := s.service(); ok {
		return srv.State()
	}
	return s.first
}

// Syncs is how many reads have synced so far, for acptest's own tests.
func (s *Session) Syncs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncs
}

// service is the service serving this session now, or false when none is.
func (s *Session) service() (*service.Session, bool) {
	return s.emb.Service(s.ID())
}

// settle is decision 4: read the service's own answer, compare it with the
// mirror's JSON encoding, and sync — up to three times — only when they
// differ. A read cannot fail, so the test's own assertions decide.
func settle[T any](s *Session, serviceRead func(*service.Session) T, clientRead func() T, same func(T, T) bool) T {
	got := clientRead()
	if s.opts.unsettled {
		return got
	}
	srv, ok := s.service()
	if !ok {
		// A retired or closed session answers the client as is.
		return got
	}
	if same(serviceRead(srv), got) {
		return got
	}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.Sync(ctx)
		cancel()
		if err != nil {
			break
		}
		s.mu.Lock()
		s.syncs++
		s.mu.Unlock()
		got = clientRead()
		srv, ok = s.service()
		if !ok {
			return got
		}
		if same(serviceRead(srv), got) {
			return got
		}
	}
	return got
}

// sameJSONValue compares two values by their JSON encodings.
func sameJSONValue[T any](a, b T) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aJSON, bJSON)
}

// sameStack compares a stack by its roots and nodes only: revisions differ by
// design.
func sameStack(a, b acpwire.StackSnapshot) bool {
	return sameJSONValue(a.Roots, b.Roots) && sameJSONValue(a.Nodes, b.Nodes)
}

// The pair comparisons marshal each part separately: a pair's own fields are
// unexported, so encoding the pair would compare "{}" with "{}" and every read
// would look settled.

func sameAccessPair(a, b accessPair) bool {
	return a.ok == b.ok && sameJSONValue(a.request, b.request)
}

func sameWorkPair(a, b workPair) bool {
	return a.ok == b.ok && sameJSONValue(a.snapshot, b.snapshot)
}

func sameToolsPair(a, b toolsPair) bool {
	return a.ok == b.ok && sameJSONValue(a.names, b.names)
}

func (s *Session) Status() acpwire.SessionStatus {
	return settle(s,
		func(srv *service.Session) acpwire.SessionStatus { return srv.Status() },
		s.Session.Status,
		sameJSONValue[acpwire.SessionStatus],
	)
}

func (s *Session) Stack(subagentID int64, busy bool) acpwire.StackSnapshot {
	return settle(s,
		func(srv *service.Session) acpwire.StackSnapshot { return srv.Stack(subagentID, busy) },
		func() acpwire.StackSnapshot { return s.Session.Stack(subagentID, busy) },
		sameStack,
	)
}

func (s *Session) Pending() acpwire.PendingRequests {
	return settle(s,
		func(srv *service.Session) acpwire.PendingRequests { return srv.Pending() },
		s.Session.Pending,
		sameJSONValue[acpwire.PendingRequests],
	)
}

// accessPair keeps ReconcileAccess's request and its flag together.
type accessPair struct {
	request acpwire.AccessRequest
	ok      bool
}

func (s *Session) ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool) {
	// The service's call settles a satisfied access request first, as the
	// TUI's call did in process and as the request forwarder does on each
	// pass.
	if srv, ok := s.service(); ok && !s.opts.unsettled {
		_, _ = srv.ReconcileAccess(ctx)
	}
	got := settle(s,
		func(srv *service.Session) accessPair {
			request, ok := srv.ReconcileAccess(ctx)
			return accessPair{request: request, ok: ok}
		},
		func() accessPair {
			request, ok := s.Session.ReconcileAccess(ctx)
			return accessPair{request: request, ok: ok}
		},
		sameAccessPair,
	)
	return got.request, got.ok
}

// workPair keeps Work's snapshot and its flag together.
type workPair struct {
	snapshot acpwire.WorkSnapshot
	ok       bool
}

func (s *Session) Work() (acpwire.WorkSnapshot, bool) {
	got := settle(s,
		func(srv *service.Session) workPair {
			snapshot, ok := srv.Work()
			return workPair{snapshot: snapshot, ok: ok}
		},
		func() workPair {
			snapshot, ok := s.Session.Work()
			return workPair{snapshot: snapshot, ok: ok}
		},
		sameWorkPair,
	)
	return got.snapshot, got.ok
}

func (s *Session) Config() config.Config {
	return settle(s,
		func(srv *service.Session) config.Config { return srv.Config() },
		s.Session.Config,
		sameJSONValue[config.Config],
	)
}

func (s *Session) Layers() config.Layers {
	return settle(s,
		func(srv *service.Session) config.Layers { return srv.Layers() },
		s.Session.Layers,
		sameJSONValue[config.Layers],
	)
}

// toolsPair keeps Tools' names and its flag together.
type toolsPair struct {
	names []string
	ok    bool
}

func (s *Session) Tools() ([]string, bool) {
	got := settle(s,
		func(srv *service.Session) toolsPair {
			names, ok := srv.Tools()
			return toolsPair{names: names, ok: ok}
		},
		func() toolsPair {
			names, ok := s.Session.Tools()
			return toolsPair{names: names, ok: ok}
		},
		sameToolsPair,
	)
	return got.names, got.ok
}

func (s *Session) ActiveWork() acpwire.EndWork {
	return settle(s,
		func(srv *service.Session) acpwire.EndWork { return srv.ActiveWork() },
		s.Session.ActiveWork,
		sameJSONValue[acpwire.EndWork],
	)
}

func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet {
	return settle(s,
		func(srv *service.Session) acpwire.SessionSheet { return srv.Sheet(subagentID) },
		func() acpwire.SessionSheet { return s.Session.Sheet(subagentID) },
		sameJSONValue[acpwire.SessionSheet],
	)
}

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)
