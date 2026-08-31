package bridge

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
)

// fakeTransport records lifecycle calls and hands back in-memory pipes.
type fakeTransport struct {
	mu          sync.Mutex
	opens       int
	signals     []os.Signal
	killed      bool
	detached    int
	exit        chan error
	toChild     *io.PipeWriter
	fromChld    *io.PipeReader
	childStdout *io.PipeWriter
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{exit: make(chan error, 1)}
}

func (f *fakeTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	f.mu.Lock()
	f.opens++
	f.mu.Unlock()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f.toChild = inW
	f.fromChld = outR
	// Answer every request with an empty result so the child's
	// initialize handshake (checkAgentVersion) does not block.
	go f.serve(inR, outW)
	f.childStdout = outW
	errR, _ := io.Pipe()
	return inW, outR, errR, nil
}

// serve reads JSON-RPC requests from the child and answers each with an
// empty result, so a request that blocks on a response (the initialize
// handshake) returns instead of hanging the test.
func (f *fakeTransport) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
	}
}

func (f *fakeTransport) Wait() error { return <-f.exit }

func (f *fakeTransport) Signal(sig os.Signal) error {
	f.mu.Lock()
	f.signals = append(f.signals, sig)
	fromChld := f.fromChld
	f.mu.Unlock()
	// A real process's stdout closes when it exits. Closing the read end
	// lets the Child's readLoop reach EOF so supervision can proceed to
	// Wait; without it Stop would block forever on readDone.
	if fromChld != nil {
		_ = fromChld.Close()
	}
	f.exit <- nil
	return nil
}

func (f *fakeTransport) Kill() error {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeTransport) Detach() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached++
	return nil
}

func TestChildUsesInjectedTransport(t *testing.T) {
	tr := newFakeTransport()
	c := &Child{Transport: tr}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	tr.mu.Lock()
	opens := tr.opens
	tr.mu.Unlock()
	if opens != 1 {
		t.Fatalf("transport opened %d times, want 1", opens)
	}
}

func TestChildStopSignalsTransport(t *testing.T) {
	tr := newFakeTransport()
	c := &Child{Transport: tr}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.Stop()

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.signals) == 0 {
		t.Fatal("Stop did not signal the transport")
	}
}
