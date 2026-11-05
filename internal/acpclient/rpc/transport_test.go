package rpc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestPipeCarriesFramesBothWays sends a frame from each end and checks that
// the other end receives it whole, including a 3 MiB frame from the agent's
// end that exercises the pipe's client-side cap.
func TestPipeCarriesFramesBothWays(t *testing.T) {
	client, agent := Pipe()
	defer client.Close()
	defer agent.Close()

	agentIn := bufio.NewReader(agent)
	writeErr := make(chan error, 1)

	toAgent := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	go func() { writeErr <- client.Send(toAgent) }()
	got, err := readLine(agentIn)
	if err != nil {
		t.Fatalf("read frame sent by the client: %v", err)
	}
	if !bytes.Equal(got, toAgent) {
		t.Fatalf("agent read %q, want %q", got, toAgent)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("client.Send: %v", err)
	}

	toClient := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	go func() { writeErr <- writeLine(agent, toClient) }()
	got, err = client.Recv()
	if err != nil {
		t.Fatalf("recv frame sent by the agent: %v", err)
	}
	if !bytes.Equal(got, toClient) {
		t.Fatalf("client received %q, want %q", got, toClient)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("agent write: %v", err)
	}

	big := bytes.Repeat([]byte("a"), 3<<20)
	go func() { writeErr <- writeLine(agent, big) }()
	got, err = client.Recv()
	if err != nil {
		t.Fatalf("recv 3 MiB frame: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("3 MiB frame arrived as %d bytes, want %d", len(got), len(big))
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("agent write of 3 MiB frame: %v", err)
	}
}

// TestStreamRefusesAnOversizedFrame checks that a stream whose cap is 1 KiB
// rejects a 2 KiB line with an error that wraps bufio.ErrTooLong.
func TestStreamRefusesAnOversizedFrame(t *testing.T) {
	line := append(bytes.Repeat([]byte("a"), 2<<10), '\n')
	tr := NewStream(bytes.NewReader(line), io.Discard, 1<<10)

	_, err := tr.Recv()
	if err == nil {
		t.Fatal("Recv returned no error for a frame over the cap")
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("Recv error = %v, want an error wrapping bufio.ErrTooLong", err)
	}
}

// TestPipeCloseEndsBothDirections checks that ending either end of a pipe
// ends both directions.
func TestPipeCloseEndsBothDirections(t *testing.T) {
	t.Run("agent closes", func(t *testing.T) {
		client, agent := Pipe()
		defer client.Close()

		if err := agent.Close(); err != nil {
			t.Fatalf("agent.Close: %v", err)
		}
		if _, err := client.Recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("client.Recv after the agent closed = %v, want io.EOF", err)
		}
	})

	t.Run("client closes", func(t *testing.T) {
		client, agent := Pipe()
		defer agent.Close()

		if err := client.Close(); err != nil {
			t.Fatalf("client.Close: %v", err)
		}
		if _, err := agent.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("agent.Read after the client closed = %v, want io.EOF", err)
		}
	})
}

// readLine reads one newline-terminated frame from the agent's byte stream
// and returns it without the newline.
func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return line[:len(line)-1], nil
}

// writeLine writes one frame and its newline to the agent's byte stream.
func writeLine(w io.Writer, frame []byte) error {
	buf := make([]byte, 0, len(frame)+1)
	buf = append(buf, frame...)
	buf = append(buf, '\n')
	_, err := w.Write(buf)
	return err
}
