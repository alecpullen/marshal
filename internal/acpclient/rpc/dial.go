package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"marshal/internal/acpwire"
)

// DialOptions configure Dial.
type DialOptions struct {
	Handlers Handlers
	// SharedFilesystem is what Host.SharedFilesystem reports: true for the
	// pipe.
	SharedFilesystem bool
	// AgentVersion, when not empty, is the version the agent must report.
	AgentVersion string
}

// Host is the client's side of the agent host. Each P3 group plan adds its
// methods.
type Host struct {
	conn   *Conn
	agent  acpwire.AgentInfo
	shared bool

	mu       sync.Mutex
	sessions map[string]*Session
}

// Dial starts a connection over t, runs initialize, and returns the host. On
// any failure it closes the connection.
func Dial(ctx context.Context, t Transport, opts DialOptions) (*Host, error) {
	host := &Host{shared: opts.SharedFilesystem, sessions: make(map[string]*Session)}

	// The connection's handlers are the host's own: notifications pass through
	// its router before the caller sees them, and agent requests go to the
	// caller's handler unchanged.
	conn := NewConn(t, Handlers{
		Notify: func(method string, params json.RawMessage) {
			host.routeNotify(method, params)
			if opts.Handlers.Notify != nil {
				opts.Handlers.Notify(method, params)
			}
		},
		Request: opts.Handlers.Request,
	})
	host.conn = conn
	// One goroutine ends every attached session's Changed when the connection
	// does.
	go func() {
		<-conn.Done()
		host.closeSessions()
	}()

	params := acpwire.InitializeParams{
		ProtocolVersion: acpwire.MarshalProtocolVersion,
		ClientCapabilities: &acpwire.ClientCapabilities{
			Marshal: &acpwire.MarshalCapability{ProtocolVersion: acpwire.MarshalProtocolVersion},
		},
	}
	var result acpwire.InitializeResult
	if err := conn.Call(ctx, "initialize", params, &result); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("rpc: initialize: %w", err)
	}

	if result.AgentCapabilities.Marshal == nil ||
		result.AgentCapabilities.Marshal.ProtocolVersion != acpwire.MarshalProtocolVersion {
		_ = conn.Close()
		return nil, fmt.Errorf("rpc: the agent does not speak marshal protocol %d", acpwire.MarshalProtocolVersion)
	}

	if opts.AgentVersion != "" && opts.AgentVersion != result.AgentInfo.Version {
		_ = conn.Close()
		return nil, fmt.Errorf("rpc: agent version %q, want %q", result.AgentInfo.Version, opts.AgentVersion)
	}

	host.agent = result.AgentInfo
	return host, nil
}

// SharedFilesystem reports whether the agent reads the same filesystem as the
// client, as it does over the pipe.
func (h *Host) SharedFilesystem() bool {
	return h.shared
}

// Agent is the agent's identity, from initialize.
func (h *Host) Agent() acpwire.AgentInfo {
	return h.agent
}

// Close ends the connection.
func (h *Host) Close() error {
	return h.conn.Close()
}

// Done is closed when the connection has ended.
func (h *Host) Done() <-chan struct{} {
	return h.conn.Done()
}

// Err is nil while the connection lives, and the connection's end error
// afterwards.
func (h *Host) Err() error {
	return h.conn.Err()
}
