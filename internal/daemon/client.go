package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
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
	attachPath       = "/api/projects/attach"
	attachProtocol   = "marshal-acp"
	sessionHeader    = "Marshal-Session"
	agentHeader      = "Marshal-Agent"
	envWarningHeader = "Marshal-Env-Warning"
	pinPath          = "/api/projects/pin"
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

// SpawnRequest is the contract's SpawnRequest; see that declaration.
type SpawnRequest = contract.SpawnRequest

// SpawnResult is the contract's SpawnResult; see that declaration.
type SpawnResult = contract.SpawnResult

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
	// remote marks a client of a server profile rather than of the local
	// socket. A remote enrolls over the authenticated path or not at all: the
	// local socket's credential never leaves this machine.
	remote bool
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
	// The attach speaks TLS or plain TCP; anything else would silently become
	// a plain dial to the scheme's default port rather than being refused.
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("server profile %q is %q: the attach speaks http or https", base, u.Scheme)
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
		remote: true,
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

// StudioCode is one single-use Studio sign-in code (POST /api/studio/code).
type StudioCode struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"` // Unix milliseconds
}

// studioCodePath is the socket route that mints a Studio sign-in code. It is
// served by the socket only (web/bridge/studio_access.go), which is why the
// daemon client can call it with no bearer token.
const studioCodePath = "/api/studio/code"

// MintStudioCode asks the daemon for a single-use code, bounded by ctx.
//
// It goes over the socket, which is peer-checked by user ID and needs no
// bearer token, so the token itself never crosses a TCP port on this path: a
// Studio page exchanges the code for it on the loopback listener
// (web/bridge/studio_access.go).
func (c *Client) MintStudioCode(ctx context.Context) (StudioCode, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon"+studioCodePath, nil)
	if err != nil {
		return StudioCode{}, fmt.Errorf("daemon request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return StudioCode{}, fmt.Errorf("daemon at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return StudioCode{}, fmt.Errorf("read daemon answer: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var refused struct {
			Error string `json:"error"`
		}
		detail := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &refused) == nil && refused.Error != "" {
			detail = refused.Error
		}
		return StudioCode{}, fmt.Errorf("daemon answered %s: %s", resp.Status, detail)
	}

	var code StudioCode
	if err := json.Unmarshal(body, &code); err != nil {
		return StudioCode{}, fmt.Errorf("decode daemon answer: %w", err)
	}
	return code, nil
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

// Spawn starts an agent on the daemon (POST /api/agents), bounded by ctx. The
// daemon answers 201 with the new agent's ID, and a warning when the agent
// was minted but something about its start was not fatal.
func (c *Client) Spawn(ctx context.Context, spawn SpawnRequest) (SpawnResult, error) {
	body, err := json.Marshal(spawn)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("encode spawn request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon/api/agents", bytes.NewReader(body))
	if err != nil {
		return SpawnResult{}, fmt.Errorf("daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("daemon at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SpawnResult{}, fmt.Errorf("read daemon answer: %w", err)
	}

	if resp.StatusCode/100 != 2 {
		// The handler answers {"error": "..."} on a refusal; keep
		// that text, since it says why better than the status does.
		var refused struct {
			Error string `json:"error"`
		}
		detail := strings.TrimSpace(string(answer))
		if json.Unmarshal(answer, &refused) == nil && refused.Error != "" {
			detail = refused.Error
		}
		return SpawnResult{}, fmt.Errorf("daemon answered %s: %s", resp.Status, detail)
	}

	var res SpawnResult
	if err := json.Unmarshal(answer, &res); err != nil {
		return SpawnResult{}, fmt.Errorf("decode daemon answer: %w", err)
	}
	return res, nil
}

// Attachment is an attached session: its ID and the upgraded stream.
type Attachment struct {
	SessionID  string
	AgentID    string // the agent whose session this is; "" for a project attach
	EnvWarning string // the project child's environment warning; "" when none
	Conn       io.ReadWriteCloser
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
	// The project child's environment warning rides the same answer, so the
	// client can say why the child fell back without asking the daemon about
	// it (P6 T3). Absent when the environment resolved.
	envWarning := strings.TrimSpace(resp.Header.Get(envWarningHeader))

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
		SessionID:  attached,
		AgentID:    agent,
		EnvWarning: envWarning,
		Conn:       &attachConn{reader: reader, conn: conn},
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

// Enrollment. A session asks the daemon it runs on to bind it to the project
// the daemon already tracks for it, and holds the short-lived credential it
// answers with. The bridge speaks this as an external client — the JSON names
// are the binding, exactly as the attach's are — so nothing under web/ is
// imported here and no internal type leaks into it.

// marshalEnrollPath is the bridge's enrollment route. It is served on the
// daemon's owner-only socket; a server profile that does not serve it answers
// 404, which this client reports as unsupported rather than retrying elsewhere.
const marshalEnrollPath = "/api/marshal/enroll"

// The enrollment states the bridge reports.
const (
	// MarshalStateConnected is a verified binding with a credential.
	MarshalStateConnected = "connected"
	// MarshalStateAmbiguous is more than one candidate instance or project:
	// nothing was granted, and the user has to choose.
	MarshalStateAmbiguous = "ambiguous"
	// MarshalStateUnsupported is an instance that does not serve enrollment.
	MarshalStateUnsupported = "unsupported"
	// MarshalStateUnavailable is an instance that is absent, offline, or does
	// not register the project. A session works without it.
	MarshalStateUnavailable = "unavailable"
)

var (
	// ErrMarshalUnsupported is an instance that does not serve enrollment — no
	// token source for a remote, a 404 from a bridge without the route, or a
	// refusal. Never a reason to fall back to a broader access path.
	ErrMarshalUnsupported = errors.New("marshal enrollment unsupported")
	// ErrMarshalAmbiguous is more than one instance or project to choose from.
	ErrMarshalAmbiguous = errors.New("marshal enrollment ambiguous")
	// ErrMarshalUnavailable is an instance that could not be reached or could
	// not bind this session. It never fails the session that asked.
	ErrMarshalUnavailable = errors.New("marshal enrollment unavailable")
)

// MarshalInstance is one daemon instance's identity: the name that is stable
// for its life — the canonical socket path, or a server profile's base URL —
// and the generation that changes when it restarts or changes owner.
type MarshalInstance struct {
	ID         string
	Generation string
	Remote     bool
}

// MarshalStatus is what discovery and enrollment verified about one session's
// connection: the state it reached, the identities it agrees on, and the
// candidates an ambiguity offers. It carries no credential.
type MarshalStatus struct {
	State      string
	InstanceID string
	Generation string
	ProjectID  string
	SessionID  string
	WorkerID   string
	ItemID     string
	Role       string
	Candidates []string
}

// MarshalEnrollment is one session's enrollment: the status it reached and,
// when that status is connected, the scoped credential to present and when it
// lapses. The credential belongs to the trusted transport and nowhere else —
// not session content, not a prompt, not a tool result, not a log, not a
// project file, and not the child's environment.
type MarshalEnrollment struct {
	Status    MarshalStatus
	Token     string
	ExpiresAt time.Time
}

// marshalEnrollRequest is the enrollment request body.
type marshalEnrollRequest struct {
	ProjectRoot string `json:"projectRoot,omitempty"`
	SessionID   string `json:"sessionId"`
}

// marshalEnrollAnswer is the bridge's answer, field for field.
type marshalEnrollAnswer struct {
	State      string   `json:"state"`
	InstanceID string   `json:"instanceId,omitempty"`
	Generation string   `json:"generation,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	SessionID  string   `json:"sessionId,omitempty"`
	WorkerID   string   `json:"workerId,omitempty"`
	ItemID     string   `json:"itemId,omitempty"`
	Role       string   `json:"role,omitempty"`
	Token      string   `json:"token,omitempty"`
	ExpiresAt  string   `json:"expiresAt,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

// DiscoverMarshal reads the instance this client talks to: its identity, the
// generation a credential is scoped to, and the projects it registers, all from
// the daemon's own answer. A remote profile is named by its configured base URL
// and the local daemon by its canonical socket path, so the two are never
// mistaken for one another.
func (c *Client) DiscoverMarshal(ctx context.Context) (MarshalInstance, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return MarshalInstance{}, fmt.Errorf("%w: %w", ErrMarshalUnavailable, err)
	}
	return c.marshalInstanceOf(info)
}

// marshalInstanceOf derives an instance identity from one daemon answer.
func (c *Client) marshalInstanceOf(info Info) (MarshalInstance, error) {
	id := strings.TrimSpace(c.socket)
	if !c.remote {
		if socket := strings.TrimSpace(info.Socket); socket != "" {
			id = socket
		}
	}
	if id == "" {
		return MarshalInstance{}, fmt.Errorf("%w: instance names no identity", ErrMarshalUnsupported)
	}
	return MarshalInstance{
		ID:         id,
		Generation: fmt.Sprintf("%d:%d", info.PID, info.StartedAt),
		Remote:     c.remote,
	}, nil
}

// EnrollMarshal asks this instance to bind one session. root is the project root
// the caller resolved (Marshal's project identity anchors at the git root), not
// a path a model supplied; the instance resolves the rest of the association
// itself.
//
// A remote enrolls over its configured, authenticated path or not at all: with
// no token source there is nothing to authenticate with, and the answer is
// unsupported rather than an unauthenticated request to a different instance.
// Every failure this returns leaves the session that asked working.
func (c *Client) EnrollMarshal(ctx context.Context, root, sessionID string) (MarshalEnrollment, error) {
	if strings.TrimSpace(sessionID) == "" {
		return MarshalEnrollment{}, fmt.Errorf("%w: no session to enroll", ErrMarshalUnsupported)
	}
	body, err := json.Marshal(marshalEnrollRequest{ProjectRoot: root, SessionID: sessionID})
	if err != nil {
		return MarshalEnrollment{}, fmt.Errorf("encode enrollment request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.host+marshalEnrollPath, bytes.NewReader(body))
	if err != nil {
		return MarshalEnrollment{}, fmt.Errorf("daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.remote {
		if c.token == nil {
			return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnsupported}}, fmt.Errorf("%w: %s has no credential", ErrMarshalUnsupported, c.socket)
		}
		token, err := c.token(ctx)
		if err != nil {
			return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnsupported}}, fmt.Errorf("%w: enrollment token: %w", ErrMarshalUnsupported, err)
		}
		if token == "" {
			return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnsupported}}, fmt.Errorf("%w: %s has no credential", ErrMarshalUnsupported, c.socket)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnavailable, SessionID: sessionID}},
			fmt.Errorf("%w: daemon at %s: %w", ErrMarshalUnavailable, c.socket, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnavailable, SessionID: sessionID}},
			fmt.Errorf("%w: read enrollment answer: %w", ErrMarshalUnavailable, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		// The instance does not serve enrollment. That is the end of it: no
		// other instance is tried, and no broader credential is sent.
		return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnsupported}},
			fmt.Errorf("%w: %s does not serve enrollment", ErrMarshalUnsupported, c.socket)
	case http.StatusUnauthorized, http.StatusForbidden:
		detail := strings.TrimSpace(string(answer))
		var refused struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(answer, &refused) == nil && refused.Error != "" {
			detail = refused.Error
		}
		return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnsupported}},
			fmt.Errorf("%w: %s answered %s: %s", ErrMarshalUnsupported, c.socket, resp.Status, detail)
	default:
		detail := strings.TrimSpace(string(answer))
		var refused struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(answer, &refused) == nil && refused.Error != "" {
			detail = refused.Error
		}
		return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnavailable}},
			fmt.Errorf("%w: %s answered %s: %s", ErrMarshalUnavailable, c.socket, resp.Status, detail)
	}

	var out marshalEnrollAnswer
	if err := json.Unmarshal(answer, &out); err != nil {
		return MarshalEnrollment{Status: MarshalStatus{State: MarshalStateUnavailable, SessionID: sessionID}},
			fmt.Errorf("%w: decode enrollment answer: %w", ErrMarshalUnavailable, err)
	}
	enrollment := MarshalEnrollment{
		Status: MarshalStatus{
			State:      out.State,
			InstanceID: out.InstanceID,
			Generation: out.Generation,
			ProjectID:  out.ProjectID,
			SessionID:  out.SessionID,
			WorkerID:   out.WorkerID,
			ItemID:     out.ItemID,
			Role:       out.Role,
			Candidates: out.Candidates,
		},
		Token: out.Token,
	}
	if out.ExpiresAt != "" {
		if expiresAt, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
			enrollment.ExpiresAt = expiresAt
		}
	}
	switch out.State {
	case MarshalStateConnected:
		if enrollment.Token == "" {
			return enrollment, fmt.Errorf("%w: %s enrolled without a credential", ErrMarshalUnavailable, c.socket)
		}
		return enrollment, nil
	case MarshalStateAmbiguous:
		return enrollment, fmt.Errorf("%w: %s offers %s", ErrMarshalAmbiguous, c.socket, strings.Join(out.Candidates, ", "))
	case MarshalStateUnsupported:
		return enrollment, fmt.Errorf("%w: %s refused this session", ErrMarshalUnsupported, c.socket)
	default:
		return enrollment, fmt.Errorf("%w: %s could not bind this session", ErrMarshalUnavailable, c.socket)
	}
}

// ResolveMarshalInstance picks the one instance to enroll against: the selected
// one when the user has chosen, the only candidate when there is exactly one,
// and nothing at all when the choice is ambiguous. It never guesses between two
// instances and never invents one.
func ResolveMarshalInstance(candidates []MarshalInstance, selected string) (MarshalInstance, error) {
	selected = strings.TrimSpace(selected)
	if selected != "" {
		for _, candidate := range candidates {
			if candidate.ID == selected {
				return candidate, nil
			}
		}
		return MarshalInstance{}, fmt.Errorf("%w: no instance named %q", ErrMarshalUnsupported, selected)
	}
	if len(candidates) == 0 {
		return MarshalInstance{}, fmt.Errorf("%w: no instance to enroll with", ErrMarshalUnavailable)
	}
	if len(candidates) > 1 {
		return MarshalInstance{}, fmt.Errorf("%w: %d instances configured", ErrMarshalAmbiguous, len(candidates))
	}
	return candidates[0], nil
}

// Access. These are a person's settings for the built-in Studio integration —
// a project's access defaults, one session's override and, when enrollment was
// ambiguous, the project that session acts on. They are user-side routes on the
// authenticated socket; no MCP route and nothing on the model-facing surface
// names them.

const (
	marshalAccessPath          = "/api/marshal/access"
	marshalAccessDefaultPath   = marshalAccessPath + "/default"
	marshalAccessOverridePath  = marshalAccessPath + "/override"
	marshalAccessSelectionPath = marshalAccessPath + "/selection"
)

// MarshalAccessAnswer is one answer from the access surface: the status the
// bridge returned and its body, which is the seam's access view as JSON.
//
// A 409 is an answer and not an error: it carries the state that won, so the
// editor can show the conflict and keep the edit it was making. Every other
// non-2xx is an error, described without the workspace path.
type MarshalAccessAnswer struct {
	Status int
	Body   []byte
}

// MarshalAccess reads the access surface for one session. An empty projectID
// lets the bridge resolve the project the session acts on, and an empty
// workerID resolves the session's own worker identity.
func (c *Client) MarshalAccess(ctx context.Context, projectID, sessionID, workerID string) (MarshalAccessAnswer, error) {
	query := url.Values{}
	if projectID != "" {
		query.Set("projectId", projectID)
	}
	if sessionID != "" {
		query.Set("sessionId", sessionID)
	}
	if workerID != "" {
		query.Set("workerId", workerID)
	}
	path := marshalAccessPath
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return c.marshalAccessDo(ctx, http.MethodGet, path, nil)
}

// SaveMarshalAccessDefault writes one project's access defaults against the
// version the caller read.
func (c *Client) SaveMarshalAccessDefault(ctx context.Context, body []byte) (MarshalAccessAnswer, error) {
	return c.marshalAccessDo(ctx, http.MethodPut, marshalAccessDefaultPath, body)
}

// SaveMarshalAccessOverride writes one session's or worker's override against
// the version the caller read.
func (c *Client) SaveMarshalAccessOverride(ctx context.Context, body []byte) (MarshalAccessAnswer, error) {
	return c.marshalAccessDo(ctx, http.MethodPut, marshalAccessOverridePath, body)
}

// SelectMarshalInstance records the project an ambiguous session acts on, or
// clears the choice when the body names none.
func (c *Client) SelectMarshalInstance(ctx context.Context, body []byte) (MarshalAccessAnswer, error) {
	return c.marshalAccessDo(ctx, http.MethodPut, marshalAccessSelectionPath, body)
}

// marshalAccessDo issues one access request. A remote carries its configured
// credential, and the local socket needs none: its routes are served on the
// bare mux, where only the owner can reach them.
func (c *Client) marshalAccessDo(ctx context.Context, method, path string, body []byte) (MarshalAccessAnswer, error) {
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.host+path, payload)
	if err != nil {
		return MarshalAccessAnswer{}, fmt.Errorf("daemon request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.remote {
		if c.token == nil {
			return MarshalAccessAnswer{}, fmt.Errorf("%w: %s has no credential", ErrMarshalUnsupported, c.socket)
		}
		token, err := c.token(ctx)
		if err != nil {
			return MarshalAccessAnswer{}, fmt.Errorf("%w: access token: %w", ErrMarshalUnsupported, err)
		}
		if token == "" {
			return MarshalAccessAnswer{}, fmt.Errorf("%w: %s has no credential", ErrMarshalUnsupported, c.socket)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return MarshalAccessAnswer{}, fmt.Errorf("%w: daemon at %s: %w", ErrMarshalUnavailable, c.socket, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return MarshalAccessAnswer{}, fmt.Errorf("%w: read access answer: %w", ErrMarshalUnavailable, err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusConflict:
		// A conflict is a conditional write that lost: the body names the
		// value that won, so the caller shows it instead of guessing.
		return MarshalAccessAnswer{Status: resp.StatusCode, Body: answer}, nil
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return MarshalAccessAnswer{}, fmt.Errorf("%w: %s does not serve access settings", ErrMarshalUnsupported, c.socket)
	case http.StatusUnauthorized, http.StatusForbidden:
		return MarshalAccessAnswer{}, fmt.Errorf("%w: %s refused access settings: %s", ErrMarshalUnsupported, c.socket, marshalAccessDetail(answer, resp.Status))
	default:
		return MarshalAccessAnswer{}, fmt.Errorf("%w: %s answered %s: %s", ErrMarshalUnavailable, c.socket, resp.Status, marshalAccessDetail(answer, resp.Status))
	}
}

// marshalAccessDetail is the bridge's own error text when it sent one, and the
// status otherwise. It never includes a body that was not an error object.
func marshalAccessDetail(body []byte, status string) string {
	var refused struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &refused) == nil && strings.TrimSpace(refused.Error) != "" {
		return refused.Error
	}
	return status
}
