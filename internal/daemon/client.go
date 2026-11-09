package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Info mirrors the bridge's DaemonInfo on the wire.
type Info struct {
	Version   string   `json:"version"`
	Profile   string   `json:"profile"`
	PID       int      `json:"pid"`
	StartedAt int64    `json:"startedAt"`
	Socket    string   `json:"socket"`
	Projects  []string `json:"projects"`
}

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
