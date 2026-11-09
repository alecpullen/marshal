package acptest

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
)

// acptestState is the state every test serves.
func acptestState(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

// fakeWork is a WorkService whose snapshot a test can change.
type fakeWork struct {
	mu   sync.Mutex
	snap viewmodel.WorkSnapshot
	subs []chan pubsub.Event[viewmodel.WorkSnapshot]
}

func (f *fakeWork) WorkSnapshot() viewmodel.WorkSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeWork) SubscribeWork(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	ch := make(chan pubsub.Event[viewmodel.WorkSnapshot], 8)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, sub := range f.subs {
			if sub == ch {
				f.subs = append(f.subs[:i], f.subs[i+1:]...)
				return
			}
		}
	}()
	return ch
}

func (f *fakeWork) ListWork(context.Context, viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	return viewmodel.WorkPage{}, nil
}

func (f *fakeWork) InspectWork(context.Context, viewmodel.WorkTarget, bool) (viewmodel.WorkDetail, error) {
	return viewmodel.WorkDetail{}, nil
}

func (f *fakeWork) StopWork(context.Context, viewmodel.WorkTarget, string) (viewmodel.WorkStopResult, error) {
	return viewmodel.WorkStopResult{}, nil
}

func (f *fakeWork) AcknowledgeWork(context.Context, viewmodel.WorkAck) error { return nil }

// publish changes the snapshot and tells every subscriber.
func (f *fakeWork) publish(snap viewmodel.WorkSnapshot) {
	f.mu.Lock()
	f.snap = snap
	subs := append([]chan pubsub.Event[viewmodel.WorkSnapshot](nil), f.subs...)
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- pubsub.Event[viewmodel.WorkSnapshot]{Type: "work", Payload: snap}:
		default:
		}
	}
}

// fakeEnd is an EndService whose active work a test can change.
type fakeEnd struct {
	mu   sync.Mutex
	work acpwire.EndWork
}

func (f *fakeEnd) ActiveWork() acpwire.EndWork {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.work
}

func (f *fakeEnd) set(work acpwire.EndWork) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.work = work
}

func (f *fakeEnd) AcceptEnd(acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	return acpwire.EndDecision{}, nil
}

func (f *fakeEnd) WaitForEnd(context.Context) error { return nil }

func (f *fakeEnd) RunExitAnalysis(context.Context, string) error { return nil }

// encodeJSON is what two reads are compared by: the wire's own encoding.
func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// TestADirectChangeIsReadBack is decision 4's proof: each of the ten reads,
// after a change made directly on the state or on the runtime's dependency,
// returns the change — whether a sync was needed or a forwarder had already
// pushed it.
func TestADirectChangeIsReadBack(t *testing.T) {
	// current is the session under test, for a row whose change must wait for
	// the runtime's own view to move before the read can compare answers.
	var current *Session
	rows := []struct {
		name   string
		read   func(s *Session, child *session.State) any
		change func(t *testing.T, st *session.State, work *fakeWork, end *fakeEnd, tools *[]string, child *session.State)
	}{
		{
			name: "status",
			read: func(s *Session, _ *session.State) any { return s.Status() },
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				st.SetTurnUsage(7)
			},
		},
		{
			name: "stack",
			read: func(s *Session, _ *session.State) any { return s.Stack(0, false) },
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				st.AddMessage(session.RoleUser, "a new turn", session.ContentTypePlain)
			},
		},
		{
			name: "pending",
			read: func(s *Session, _ *session.State) any { return s.Pending() },
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				st.SetPendingApproval(&session.PendingToolCall{ID: "p1", Name: "shell.run", Command: "ls"})
			},
		},
		{
			name: "layers",
			read: func(s *Session, _ *session.State) any { return s.Layers() },
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				st.SetLayers(config.Layers{Default: config.Default(), Merged: config.Default(), Migrated: true})
			},
		},
		{
			name: "config",
			read: func(s *Session, _ *session.State) any { return s.Config() },
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				// A reloaded config is the runtime's own path to a changed
				// Config, as a settings save reaches it.
				cfg := config.Default()
				cfg.Providers = map[string]config.ProviderConfig{"acptest": {}}
				if _, err := st.RebaseInteractionConfig(cfg); err != nil {
					t.Fatalf("RebaseInteractionConfig: %v", err)
				}
			},
		},
		{
			name: "sheet",
			read: func(s *Session, _ *session.State) any { return s.Sheet(0) },
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				// The session's own sheet reads the state's context pack, so a
				// new pack moves it.
				st.SetContextPack(contextpack.Pack{TokenUsage: contextpack.TokenUsage{MaxTokens: 4096, EstimatedTokens: 128}})
			},
		},
		{
			name: "work",
			read: func(s *Session, _ *session.State) any {
				snapshot, ok := s.Work()
				return []any{snapshot, ok}
			},
			change: func(t *testing.T, st *session.State, work *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				work.publish(viewmodel.WorkSnapshot{ConversationID: st.SessionID(), Revision: 2})
				// The service's Work is its own subscription's mirror, so wait
				// for that to move: only then do the two answers differ, and the
				// settle has something to carry to the client.
				deadline := time.Now().Add(2 * time.Second)
				for {
					if srv, ok := current.service(); ok {
						if snapshot, _ := srv.Work(); snapshot.Revision == 2 {
							return
						}
					}
					if time.Now().After(deadline) {
						t.Fatal("the service never saw the new work snapshot")
					}
					time.Sleep(5 * time.Millisecond)
				}
			},
		},
		{
			name: "active work",
			read: func(s *Session, _ *session.State) any { return s.ActiveWork() },
			change: func(t *testing.T, st *session.State, _ *fakeWork, end *fakeEnd, _ *[]string, _ *session.State) {
				end.set(acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "w1", Label: "running run"}}})
			},
		},
		{
			name: "tools",
			read: func(s *Session, _ *session.State) any {
				names, ok := s.Tools()
				return []any{names, ok}
			},
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, tools *[]string, _ *session.State) {
				*tools = []string{"file.read", "file.write"}
			},
		},
		{
			name: "access",
			read: func(s *Session, _ *session.State) any {
				request, ok := s.ReconcileAccess(context.Background())
				return []any{request, ok}
			},
			change: func(t *testing.T, st *session.State, _ *fakeWork, _ *fakeEnd, _ *[]string, _ *session.State) {
				if _, _, err := st.RegisterAccess(context.Background(), &session.PendingToolCall{
					ID: "a1", Name: "file.read", Args: "{}",
					ResponseChan: make(chan session.UserApprovalDecision, 1),
				}, session.AccessIntent{System: true, Reason: "read outside"}); err != nil {
					t.Fatalf("RegisterAccess: %v", err)
				}
			},
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			st, d, work, end, tools, child := acptestDeps(t)
			_, sess := Open(t, d)
			current = sess
			before := encodeJSON(row.read(sess, child))
			row.change(t, st, work, end, tools, child)
			after := encodeJSON(row.read(sess, child))
			if before == after {
				t.Fatalf("the read did not carry the change:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// acptestDeps builds a state, a registered running subagent, and the deps whose
// reads the rows change.
func acptestDeps(t *testing.T) (*session.State, service.Deps, *fakeWork, *fakeEnd, *[]string, *session.State) {
	t.Helper()
	st := acptestState(t, "s1")
	child := acptestState(t, "child")
	st.RegisterSubagent("scout", child)
	work := &fakeWork{snap: viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 1}}
	end := &fakeEnd{}
	tools := &[]string{"file.read"}
	d := service.Deps{
		State: st,
		Work:  work,
		End:   end,
		Tools: func() ([]string, bool) { return *tools, true },
		AccessRequest: func() (session.AccessRequest, bool) {
			return st.AccessRequest()
		},
		ResolveAccess: func(choice session.AccessChoice) (session.AccessResult, error) {
			return st.ResolveAccess(choice, nil)
		},
	}
	return st, d, work, end, tools, child
}

// TestAReadThatMatchesDoesNotSync pins the comparison: an unchanged state's
// read costs no sync, which is what catches a read whose two encodings never
// agree.
func TestAReadThatMatchesDoesNotSync(t *testing.T) {
	_, sess := Open(t, service.Deps{State: acptestState(t, "s1")})
	readAll := func() {
		_ = sess.Status()
		_ = sess.Stack(0, false)
		_ = sess.Pending()
		_, _ = sess.ReconcileAccess(context.Background())
		_, _ = sess.Work()
		_ = sess.Config()
		_ = sess.Layers()
		_, _ = sess.Tools()
		_ = sess.ActiveWork()
		_ = sess.Sheet(0)
	}
	readAll()
	if got := sess.Syncs(); got != 0 {
		t.Fatalf("the first round synced %d times, want none", got)
	}
	readAll()
	if got := sess.Syncs(); got != 0 {
		t.Fatalf("the second round synced %d times, want none: a read whose encodings never agree", got)
	}
}

// TestTheAgentClosesWithTheTest pins the cleanup: the connection ends and the
// service is released when the test that opened it ends.
func TestTheAgentClosesWithTheTest(t *testing.T) {
	var (
		host *Host
		emb  func(string) (*service.Session, bool)
	)
	t.Run("open", func(t *testing.T) {
		host, _ = Open(t, service.Deps{State: acptestState(t, "s1")})
		emb = host.emb.Service
	})
	select {
	case <-host.Done():
	default:
		t.Fatal("the host's connection is still open after its test ended")
	}
	if _, ok := emb("s1"); ok {
		t.Fatal("the embedding still serves the session after its test ended")
	}
}

// TestAReplacementIsFollowed pins Host.NewSession: the wrapper answers the
// replacement's state and settles its reads.
func TestAReplacementIsFollowed(t *testing.T) {
	var replacement *session.State
	d := service.Deps{
		State: acptestState(t, "s1"),
		NewSession: func(name string) (*session.State, error) {
			replacement = acptestState(t, "s2")
			return replacement, nil
		},
	}
	host, _ := Open(t, d)

	next, err := host.NewSession(context.Background(), "second")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if got := next.ID(); got != "s2" {
		t.Fatalf("the replacement's ID = %q, want %q", got, "s2")
	}
	ts, ok := next.(*Session)
	if !ok {
		t.Fatalf("the replacement is %T, want *acptest.Session", next)
	}
	if got := ts.State(); got != replacement {
		t.Fatal("State() is not the replacement's state")
	}
	replacement.SetTurnUsage(9)
	if got := ts.Status().TurnUsed; got != 9 {
		t.Fatalf("the replacement's status turn usage = %d, want 9", got)
	}
}

// TestAStateWithoutAnIDIsServed pins decision 9.
func TestAStateWithoutAnIDIsServed(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	host, sess := Open(t, service.Deps{State: st})
	if got := sess.ID(); got != UnnamedSessionID {
		t.Fatalf("the served ID = %q, want %q", got, UnnamedSessionID)
	}
	if _, ok := host.emb.Service(UnnamedSessionID); !ok {
		t.Fatal("the embedding does not serve the unnamed session")
	}
	_ = sess.Status()
}

// TestUnsettledReadsDoNotSync pins Unsettled: the reads are the client's own.
func TestUnsettledReadsDoNotSync(t *testing.T) {
	st := acptestState(t, "s1")
	_, sess := Open(t, service.Deps{State: st}, Unsettled())
	_ = sess.Status()
	st.SetTurnUsage(3)
	_ = sess.Status()
	if got := sess.Syncs(); got != 0 {
		t.Fatalf("Unsettled synced %d times, want none", got)
	}
}
