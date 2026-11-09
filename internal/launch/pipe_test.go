package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/tui"
	"marshal/internal/db"
	"marshal/internal/trust"
)

// pipeNow is the clock every test in this file pins: the session ID Run
// creates derives from it, so a test can name the row it later reads back.
var pipeNow = time.Unix(100, 0)

// pipeAppOptions is the runtime wiring these tests share: a fixed clock, a
// provider-less config, an isolated home and working directory, and a trust
// decision that needs no prompt.
func pipeAppOptions(dir, home string) []app.Option {
	return []app.Option{
		app.WithNow(func() time.Time { return pipeNow }),
		app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
		app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		app.WithHomeDir(home),
		app.WithWorkingDir(dir),
	}
}

// TestRunServesTheTUIOverThePipe pins the seam: the model launch hands the
// program is the pipe's client, not an in-process adapter. Its host reports a
// shared filesystem, and its session mirrors the served session's identity and
// status.
func TestRunServesTheTUIOverThePipe(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()

	var (
		mu         sync.Mutex
		servedSess *service.Session
	)
	capture := withServed(func(_ *service.Host, sess *service.Session) {
		mu.Lock()
		servedSess = sess
		mu.Unlock()
	})

	var (
		hostIsPipe       bool
		sharedFilesystem bool
		sessIsPipe       bool
		idMatches        bool
		clientStatus     string
		servedStatus     string
		statusErr        error
	)
	err := Run(context.Background(), bytes.NewBuffer(nil),
		WithApp(pipeAppOptions(dir, home)...),
		capture,
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			m := model.(tui.Model)
			host, ok := m.Host().(*rpc.Host)
			hostIsPipe = ok
			if ok {
				sharedFilesystem = host.SharedFilesystem()
			}
			sess, ok := m.Session().(*rpc.Session)
			sessIsPipe = ok
			if !ok {
				return ProgramResult{}
			}

			mu.Lock()
			agentSide := servedSess
			mu.Unlock()
			if agentSide == nil {
				statusErr = errors.New("the pass served no session")
				return ProgramResult{}
			}
			idMatches = sess.ID() == agentSide.ID()

			clientBytes, cerr := json.Marshal(sess.Status())
			servedBytes, serr := json.Marshal(agentSide.Status())
			statusErr = errors.Join(cerr, serr)
			clientStatus, servedStatus = string(clientBytes), string(servedBytes)
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !hostIsPipe {
		t.Fatal("the TUI model does not hold the pipe's client host")
	}
	if !sharedFilesystem {
		t.Error("the pipe's host does not report a shared filesystem")
	}
	if !sessIsPipe {
		t.Fatal("the TUI model does not hold the pipe's client session")
	}
	if !idMatches {
		t.Error("the pipe's session ID does not match the served session's ID")
	}
	if statusErr != nil {
		t.Fatalf("marshal the statuses: %v", statusErr)
	}
	if clientStatus != servedStatus {
		t.Errorf("the pipe's status = %s, want the served session's %s", clientStatus, servedStatus)
	}
}

// closableAgentEnd is the test's wrapper around the agent's end of a pass's
// pipe. The test closes it to lose the connection; a second close, from the
// server's own teardown, does nothing.
type closableAgentEnd struct {
	io.ReadWriteCloser
	once sync.Once
	err  error
}

func (c *closableAgentEnd) Close() error {
	c.once.Do(func() { c.err = c.ReadWriteCloser.Close() })
	return c.err
}

// TestRunEndsWhenTheAgentsConnectionCloses pins R11: a connection that ends on
// its own ends the program (its context cancels) and Run reports the loss
// instead of exiting as if the user had quit.
func TestRunEndsWhenTheAgentsConnectionCloses(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()

	var (
		mu  sync.Mutex
		end *closableAgentEnd
	)
	wrap := func(conn io.ReadWriteCloser) io.ReadWriteCloser {
		c := &closableAgentEnd{ReadWriteCloser: conn}
		mu.Lock()
		end = c
		mu.Unlock()
		return c
	}

	ctxEnded := make(chan struct{})
	runErr := Run(context.Background(), bytes.NewBuffer(nil),
		WithApp(pipeAppOptions(dir, home)...),
		withAgentConn(wrap),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			mu.Lock()
			c := end
			mu.Unlock()
			if c == nil {
				return ProgramResult{Err: errors.New("the pass dialled no agent connection")}
			}
			if err := c.Close(); err != nil {
				return ProgramResult{Err: fmt.Errorf("close the agent's end: %w", err)}
			}
			select {
			case <-ctx.Done():
				close(ctxEnded)
			case <-time.After(2 * time.Second):
			}
			return ProgramResult{}
		}),
	)

	select {
	case <-ctxEnded:
	case <-time.After(time.Second):
		t.Fatal("the program's context did not end after the agent's connection closed")
	}
	if runErr == nil {
		t.Fatal("Run returned no error after the agent's connection closed")
	}
	if !strings.Contains(runErr.Error(), "the agent's connection closed") {
		t.Fatalf("Run error = %v, want it to report the agent's connection closed", runErr)
	}
}

// TestRunReleasesTheServiceBeforeTheRuntimeCloses pins decision 7: the pass's
// service is released by the time Run returns, so the runtime it reads closes
// after the last reader stopped. A released session answers CodeSessionClosed,
// and Finish has already marked the session ended.
func TestRunReleasesTheServiceBeforeTheRuntimeCloses(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()

	served := &servedPair{}
	err := Run(context.Background(), bytes.NewBuffer(nil),
		WithApp(pipeAppOptions(dir, home)...),
		served.option(),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	served.mu.Lock()
	sess := served.sess
	served.mu.Unlock()
	if sess == nil {
		t.Fatal("the pass served no session")
	}
	if err := sess.Stop(context.Background(), "stop"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("Stop on the served session after Run = %v, want CodeSessionClosed", err)
	}

	database, dberr := db.Open(db.Path(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()
	got, err := database.GetSession(fmt.Sprintf("sess_%d", pipeNow.UnixNano()))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt is nil: the session must be marked ended after its service was released")
	}
}

// TestRunResumesOnAFreshPipe pins that a resume request tears the pass down and
// builds a new pipe: the second pass serves its own pair, and the first one is
// already released when that pass starts.
func TestRunResumesOnAFreshPipe(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()

	var (
		mu    sync.Mutex
		pairs []*servedPair
	)
	collect := withServed(func(host *service.Host, sess *service.Session) {
		p := &servedPair{host: host, sess: sess}
		mu.Lock()
		pairs = append(pairs, p)
		mu.Unlock()
	})

	var (
		runs            int
		firstReleaseErr error
		secondStatusErr error
	)
	runErr := Run(context.Background(), bytes.NewBuffer(nil),
		WithApp(pipeAppOptions(dir, home)...),
		collect,
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			runs++
			mu.Lock()
			passes := append([]*servedPair(nil), pairs...)
			mu.Unlock()
			switch runs {
			case 1:
				// Resume the session this pass opened: it exists in the
				// project database, so the second pass loads it.
				return ProgramResult{ResumeSession: model.(tui.Model).Session().ID()}
			case 2:
				if len(passes) != 2 {
					secondStatusErr = fmt.Errorf("the second pass has %d served pairs, want 2", len(passes))
					return ProgramResult{}
				}
				firstReleaseErr = passes[0].sess.Stop(context.Background(), "second pass")
			}
			return ProgramResult{}
		}),
	)
	if runErr != nil {
		t.Fatalf("Run returned error: %v", runErr)
	}
	if runs != 2 {
		t.Fatalf("program ran %d times, want 2 (initial + resumed pass)", runs)
	}
	mu.Lock()
	passes := append([]*servedPair(nil), pairs...)
	mu.Unlock()
	if len(passes) != 2 {
		t.Fatalf("Run served %d pairs, want one per pass", len(passes))
	}
	if passes[0] == passes[1] || passes[0].sess == passes[1].sess {
		t.Fatal("the resumed pass reused the first pass's pipe")
	}
	if secondStatusErr != nil {
		t.Fatal(secondStatusErr)
	}
	if !acpclient.IsCode(firstReleaseErr, acpwire.CodeSessionClosed) {
		t.Fatalf("Stop on the first pass's session when the second pass started = %v, want CodeSessionClosed", firstReleaseErr)
	}
	if err := passes[1].sess.Stop(context.Background(), "stop"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("Stop on the second pass's session after Run = %v, want CodeSessionClosed", err)
	}
}
