package acp

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// acpIdleTimeout is how long an accepted listen connection may stay
// silent — no bytes in either direction — before the agent closes it.
// A client that dials and then goes silent would otherwise hold the
// single-connection accept loop hostage indefinitely (follow-ups doc
// item #2). Active turns keep the connection alive: every inbound
// frame and every outbound write refreshes the deadline, and a running
// turn streams session/update notifications. A client that wants a
// quiet connection to survive sends a periodic ping; the webbridge
// does exactly that (Child.keepalive in web/bridge/child.go).
const acpIdleTimeout = 10 * time.Minute

// idleDeadlineConn wraps an accepted connection with a read deadline
// that is refreshed by traffic in either direction: each Read arms it
// before blocking, and each successful Write extends it so outbound
// notifications (which arrive without any inbound traffic) keep a
// mid-turn connection open. When the deadline fires the next Read
// returns os.ErrDeadlineExceeded, which the server surfaces as a
// connection error; the accept loop then closes the conn and serves
// the next dialer.
type idleDeadlineConn struct {
	net.Conn
	idle time.Duration
}

// extend pushes the read deadline one idle window into the future.
// net.Conn documents that setting a deadline concurrently with Read
// is safe; the blocked Read is woken when the deadline passes.
func (c *idleDeadlineConn) extend() {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
}

func (c *idleDeadlineConn) Read(p []byte) (int, error) {
	c.extend()
	n, err := c.Conn.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, fmt.Errorf("acp: connection idle for %v without a frame; closing", c.idle)
	}
	return n, err
}

func (c *idleDeadlineConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.extend()
	}
	return n, err
}
