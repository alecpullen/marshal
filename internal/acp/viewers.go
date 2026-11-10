package acp

import (
	"context"
	"net"
	"sync/atomic"
)

// viewerMaxFrameBytes is the largest frame a viewer connection reads: P3's
// frame cap, rpc.MaxFrameBytes.
const viewerMaxFrameBytes = 64 << 20

// viewerLifecycleRefusal is what a viewer connection answers a call that would
// start or load a runtime (P4 D2): the bridge owns sessions' lifecycles.
func viewerLifecycleRefusal() error {
	return &jsonRPCError{
		Code:    invalidRequest,
		Message: "a viewer connection cannot start or load a session",
	}
}

// viewerCapabilityRefusal is what a viewer connection answers a client that did
// not declare the marshal capability (P4 D2).
func viewerCapabilityRefusal() error {
	return &jsonRPCError{
		Code:    invalidRequest,
		Message: "a viewer connection serves marshal clients only",
	}
}

// serveViewer serves one viewer connection until it ends or ctx does. It never
// sets the host's sink: every manager it builds notifies through the viewer's
// own server, so a viewer's updates reach that connection only (P4 D2).
func (h *agentHost) serveViewer(ctx context.Context, conn net.Conn) error {
	srv := NewServer(conn, conn, WithLogger(h.log))
	srv.MaxLineBytes = viewerMaxFrameBytes
	srv.MarshalOnly = true
	alive := &atomic.Bool{}
	alive.Store(true)
	cleanup := h.registerHandlers(srv, alive, srv.Notify, true)
	defer cleanup()
	defer alive.Store(false)
	return srv.Serve(ctx)
}
