package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// The attach wire contract. The bridge speaks it as an external ACP
// client, so the strings are the binding rather than a shared type.
const (
	attachPath     = "/api/projects/attach"
	attachProtocol = "marshal-acp"
	sessionHeader  = "Marshal-Session"
	pinPath        = "/api/projects/pin"
)

// ChildInfo is the contract's DaemonChild; see that declaration.
type ChildInfo = contract.DaemonChild

// SessionInfo is the contract's DaemonSession; see that declaration.
type SessionInfo = contract.DaemonSession

// RefreshInfo is the contract's DaemonRefresh; see that declaration.
type RefreshInfo = contract.DaemonRefresh

// Info is the contract's DaemonInfo; see that declaration.
type Info = contract.DaemonInfo

// PinState is the contract's PinState; see that declaration.
type PinState = contract.PinState

// Client talks HTTP to the daemon over its socket.
type Client struct {
	socket string
	http   *http.Client
}

// NewClient is a client for the daemon listening on socket.
func NewClient(socket string) *Client {
	return &Client{
		socket: socket,
		http: &http.Client{
			// Every request goes to the socket regardless of the
			// host in the URL: the address is a name, not a lookup.
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// Info reads GET /api/daemon, bounded by ctx.
func (c *Client) Info(ctx context.Context) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon/api/daemon", nil)
	if err != nil {
		return Info{}, fmt.Errorf("daemon request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("daemon at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Info{}, fmt.Errorf("read daemon answer: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// The handler answers {"error": "..."} on a refusal; keep
		// that text, since it says why better than the status does.
		var refused struct {
			Error string `json:"error"`
		}
		detail := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &refused) == nil && refused.Error != "" {
			detail = refused.Error
		}
		return Info{}, fmt.Errorf("daemon answered %s: %s", resp.Status, detail)
	}

	var info Info
	if err := json.Unmarshal(body, &info); err != nil {
		return Info{}, fmt.Errorf("decode daemon answer: %w", err)
	}
	return info, nil
}

// Pin pins or unpins root on the daemon, bounded by ctx.
func (c *Client) Pin(ctx context.Context, root string, pinned bool) (PinState, error) {
	body, err := json.Marshal(struct {
		Root   string `json:"root"`
		Pinned bool   `json:"pinned"`
	}{Root: root, Pinned: pinned})
	if err != nil {
		return PinState{}, fmt.Errorf("encode pin request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon"+pinPath, bytes.NewReader(body))
	if err != nil {
		return PinState{}, fmt.Errorf("daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return PinState{}, fmt.Errorf("daemon at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PinState{}, fmt.Errorf("read daemon answer: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// The handler answers {"error": "..."} on a refusal; keep
		// that text, since it says why better than the status does.
		var refused struct {
			Error string `json:"error"`
		}
		detail := strings.TrimSpace(string(answer))
		if json.Unmarshal(answer, &refused) == nil && refused.Error != "" {
			detail = refused.Error
		}
		return PinState{}, fmt.Errorf("daemon answered %s: %s", resp.Status, detail)
	}

	var state PinState
	if err := json.Unmarshal(answer, &state); err != nil {
		return PinState{}, fmt.Errorf("decode daemon answer: %w", err)
	}
	return state, nil
}

// Attachment is an attached session: its ID and the upgraded stream.
type Attachment struct {
	SessionID string
	Conn      io.ReadWriteCloser
}

// AttachError is a refused attach: the daemon's status and its sentence.
type AttachError struct {
	Status  int
	Message string
}

func (e *AttachError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.Status)
	}
	return fmt.Sprintf("daemon refused attach (%d): %s", e.Status, message)
}

// Attach asks the daemon for root's session, a new one when sessionID is
// empty, and returns the upgraded stream (P4 D3). ctx bounds the handshake
// only.
func (c *Client) Attach(ctx context.Context, root, sessionID string) (*Attachment, error) {
	body, err := json.Marshal(struct {
		Root      string `json:"root"`
		SessionID string `json:"sessionId,omitempty"`
	}{Root: root, SessionID: sessionID})
	if err != nil {
		return nil, fmt.Errorf("encode attach request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon"+attachPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	// The response is hijacked, so this request goes on a connection of
	// its own: a *http.Client would take the connection back.
	req.Host = "daemon"
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", attachProtocol)

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("daemon at %s: %w", c.socket, err)
	}
	// The caller's context bounds the handshake, not the stream: its deadline
	// becomes the connection's for the handshake and is cleared again before
	// the stream is handed over. A cancellation with no deadline ends the
	// handshake too, by closing the connection: a caller that has stopped
	// waiting must not be left holding one the daemon may answer on later.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("bound attach handshake: %w", err)
		}
	}
	handshakeDone := make(chan struct{})
	defer close(handshakeDone)
	var (
		hsMu       sync.Mutex
		handedOver bool
	)
	go func() {
		select {
		case <-ctx.Done():
		case <-handshakeDone:
			return
		}
		hsMu.Lock()
		if !handedOver {
			_ = conn.Close()
		}
		hsMu.Unlock()
	}()

	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("attach handshake: %w", cerr)
		}
		return nil, fmt.Errorf("write attach request: %w", err)
	}

	// The reader may already hold stream bytes the daemon sent right
	// behind the 101, so the stream reads it before the connection.
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		// A cancelled handshake closes the connection under this read. The
		// caller's own cancellation says why better than the socket error does.
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("attach handshake: %w", cerr)
		}
		return nil, fmt.Errorf("read attach answer: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		// The handler answers {"error": "..."} on a refusal; keep
		// that text, since it says why better than the status does.
		var refused struct {
			Error string `json:"error"`
		}
		detail := ""
		if raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err == nil {
			detail = strings.TrimSpace(string(raw))
			if json.Unmarshal(raw, &refused) == nil && strings.TrimSpace(refused.Error) != "" {
				detail = strings.TrimSpace(refused.Error)
			}
		}
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, &AttachError{Status: resp.StatusCode, Message: detail}
	}

	if got := strings.TrimSpace(resp.Header.Get("Upgrade")); !strings.EqualFold(got, attachProtocol) {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("daemon upgraded with protocol %q, want %q", got, attachProtocol)
	}
	attached := strings.TrimSpace(resp.Header.Get(sessionHeader))
	if attached == "" {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("daemon upgraded without a %s header", sessionHeader)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("release attach handshake: %w", err)
	}
	hsMu.Lock()
	handedOver = true
	hsMu.Unlock()
	if err := ctx.Err(); err != nil {
		// The caller gave up as the handshake completed: the stream is not
		// theirs to keep.
		_ = conn.Close()
		return nil, fmt.Errorf("attach handshake: %w", err)
	}
	return &Attachment{
		SessionID: attached,
		Conn:      &attachConn{reader: reader, conn: conn},
	}, nil
}

// attachConn is the upgraded stream: the handshake's reader first, so the
// bytes buffered behind the 101 are not lost, then the connection.
type attachConn struct {
	reader *bufio.Reader
	conn   net.Conn
}

func (a *attachConn) Read(p []byte) (int, error)  { return a.reader.Read(p) }
func (a *attachConn) Write(p []byte) (int, error) { return a.conn.Write(p) }
func (a *attachConn) Close() error                { return a.conn.Close() }
