package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	agentHeader    = "Marshal-Agent"
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

// Client talks HTTP to the daemon over its socket, or to a server-profile
// bridge over TCP.
type Client struct {
	socket string
	http   *http.Client

	// dial opens the connection an attach handshake is written to: the unix
	// socket for the local daemon, a TCP (and TLS) connection for a server
	// profile.
	dial func(ctx context.Context) (net.Conn, error)
	// host is the request's Host on the wire, and the host the request URL
	// names.
	host string
	// token is the bearer token an attach carries; nil on the socket.
	token func(ctx context.Context) (string, error)
}

// NewClient is a client for the daemon listening on socket.
func NewClient(socket string) *Client {
	// Every request goes to the socket regardless of the host in the URL: the
	// address is a name, not a lookup.
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Client{
		socket: socket,
		dial:   dial,
		host:   "daemon",
		http: &http.Client{
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dial(ctx)
			}},
		},
	}
}

// Remote is the attach client of a server-profile bridge at base (decision 6).
// Only AttachAgent is served there.
func Remote(base string, tlsConfig *tls.Config, token func(ctx context.Context) (string, error)) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("server profile %q: %w", base, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("server profile %q names no host", base)
	}

	// The port is the scheme's default when the URL carries none, so a
	// profile can name a host alone.
	address := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		address = net.JoinHostPort(u.Hostname(), port)
	}
	config := remoteTLSConfig(u, tlsConfig)
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		if config == nil {
			return conn, nil
		}
		// The handshake is the caller's to bound, so a listener that accepts
		// and then says nothing ends with the attach's context, not later.
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}

	c := &Client{
		socket: base,
		dial:   dial,
		host:   u.Host,
		token:  token,
	}
	c.http = &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx)
		}},
	}
	return c, nil
}

// remoteTLSConfig is the configuration a server profile's dial handshakes
// with: the caller's, completed with what the handshake needs and narrowed to
// the one protocol the attach speaks. nil when there is none: a loopback http
// base.
func remoteTLSConfig(u *url.URL, tlsConfig *tls.Config) *tls.Config {
	if tlsConfig == nil {
		return nil
	}
	// The connection is built by hand rather than by an *http.Client, so
	// nothing fills in the name the certificate is checked against.
	config := tlsConfig.Clone()
	if config.ServerName == "" && !config.InsecureSkipVerify {
		config.ServerName = u.Hostname()
	}
	// The handshake over this connection is HTTP/1.1 by construction: an
	// h2 connection would take a framing the attach does not speak.
	config.NextProtos = []string{"http/1.1"}
	return config
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
	AgentID   string // the agent whose session this is; "" for a project attach
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
	return c.upgrade(ctx, attachPath, body)
}

// AttachAgent opens agentID's session as a viewer (P5 F5). ctx bounds the
// handshake only. A refusal is an *AttachError.
func (c *Client) AttachAgent(ctx context.Context, agentID string) (*Attachment, error) {
	// The path is the ID's, so an ID carrying a slash or a space stays one
	// segment of the route.
	att, err := c.upgrade(ctx, "/api/agents/"+url.PathEscape(agentID)+"/attach", nil)
	if err != nil {
		return nil, err
	}
	if att.AgentID == "" {
		_ = att.Conn.Close()
		return nil, fmt.Errorf("daemon upgraded without a %s header", agentHeader)
	}
	if att.AgentID != agentID {
		_ = att.Conn.Close()
		return nil, fmt.Errorf("daemon opened agent %q, want %q", att.AgentID, agentID)
	}
	return att, nil
}

// upgrade is the handshake both attaches share: path, and body when it is
// not nil.
func (c *Client) upgrade(ctx context.Context, path string, body []byte) (*Attachment, error) {
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.host+path, payload)
	if err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	// The response is hijacked, so this request goes on a connection of
	// its own: a *http.Client would take the connection back.
	req.Host = c.host
	req.ContentLength = int64(len(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", attachProtocol)
	if c.token != nil {
		// A token that cannot be fetched ends the attach here: nothing is
		// dialled, so a bridge never sees a request it cannot authorize.
		token, err := c.token(ctx)
		if err != nil {
			return nil, fmt.Errorf("attach token: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	conn, err := c.dial(ctx)
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
	// A project attach leaves this empty; an agent attach names the agent
	// whose session the stream is.
	agent := strings.TrimSpace(resp.Header.Get(agentHeader))

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
		AgentID:   agent,
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
