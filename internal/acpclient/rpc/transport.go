// Package rpc is the marshal client's JSON-RPC transport: frames over stdio or
// over the in-process pipe to the agent.
package rpc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxFrameBytes is the largest frame the pipe's client end reads (P3 R2).
// The agent's end sets the same cap on its server.
const MaxFrameBytes = 64 << 20

// ErrClosed is wrapped by every error a call returns because the connection
// ended.
var ErrClosed = errors.New("rpc: connection closed")

// Transport is a framed, two-way message stream (P3 R1).
type Transport interface {
	// Send writes one frame: one complete JSON value, without a newline.
	Send(frame []byte) error
	// Recv returns the next frame. It returns io.EOF once the other end has
	// closed cleanly, and another error when the stream breaks or a frame is
	// larger than the transport's cap.
	Recv() ([]byte, error)
	// Close ends both directions. A Recv blocked on the other end returns.
	Close() error
}

// NewStream frames a byte stream with newlines, as `marshal acp` speaks on
// stdio. maxFrame bounds one received frame. Close closes r and w when they
// are io.Closers.
func NewStream(r io.Reader, w io.Writer, maxFrame int) Transport {
	sc := bufio.NewScanner(r)
	// bufio.Scanner's Buffer takes the buffer's length from its capacity, so
	// the initial buffer must not be larger than maxFrame: otherwise the
	// scanner quietly holds frames over the cap instead of reporting
	// bufio.ErrTooLong.
	initial := 64 * 1024
	if maxFrame < initial {
		initial = maxFrame
	}
	sc.Buffer(make([]byte, 0, initial), maxFrame)
	return &stream{r: r, w: w, sc: sc, maxFrame: maxFrame}
}

// stream is the newline-framed Transport NewStream returns.
type stream struct {
	r        io.Reader
	w        io.Writer
	sc       *bufio.Scanner
	maxFrame int

	closeOnce sync.Once
	closeErr  error
}

// Send writes frame and its newline in one Write, so two senders can never
// interleave halves of a frame.
func (s *stream) Send(frame []byte) error {
	buf := make([]byte, 0, len(frame)+1)
	buf = append(buf, frame...)
	buf = append(buf, '\n')
	if _, err := s.w.Write(buf); err != nil {
		return err
	}
	return nil
}

// Recv returns the next non-empty line, copied out of the scanner's reusable
// buffer. An oversized line is the pinned error wrapping bufio.ErrTooLong.
func (s *stream) Recv() ([]byte, error) {
	for s.sc.Scan() {
		line := s.sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			// The server's scanLines skips blank lines; so do we.
			continue
		}
		frame := make([]byte, len(line))
		copy(frame, line)
		return frame, nil
	}
	err := s.sc.Err()
	if err == nil {
		return nil, io.EOF
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return nil, fmt.Errorf("rpc: frame larger than %d bytes: %w", s.maxFrame, err)
	}
	return nil, err
}

// Close closes the reader and the writer when each is an io.Closer. It is
// safe to call more than once; every call reports the first close's error.
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		var err error
		if c, ok := s.r.(io.Closer); ok {
			err = c.Close()
		}
		if c, ok := s.w.(io.Closer); ok {
			if werr := c.Close(); err == nil {
				err = werr
			}
		}
		s.closeErr = err
	})
	return s.closeErr
}

// Pipe returns the two ends of an in-memory connection: the client's
// transport, capped at MaxFrameBytes, and the agent's byte stream, which
// acp.NewServer reads and writes. Closing either end ends both directions.
func Pipe() (Transport, io.ReadWriteCloser) {
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()

	client := NewStream(agentToClientR, clientToAgentW, MaxFrameBytes)
	agent := &agentEnd{r: clientToAgentR, w: agentToClientW}
	return client, agent
}

// agentEnd is the agent's side of Pipe. Closing it closes both pipes, so the
// client's Recv returns io.EOF.
type agentEnd struct {
	r *io.PipeReader
	w *io.PipeWriter

	closeOnce sync.Once
	closeErr  error
}

func (a *agentEnd) Read(p []byte) (int, error) { return a.r.Read(p) }

func (a *agentEnd) Write(p []byte) (int, error) { return a.w.Write(p) }

// Close closes both directions. It is safe to call more than once.
func (a *agentEnd) Close() error {
	a.closeOnce.Do(func() {
		var err error
		if rerr := a.r.Close(); rerr != nil {
			err = rerr
		}
		if werr := a.w.Close(); err == nil {
			err = werr
		}
		a.closeErr = err
	})
	return a.closeErr
}
