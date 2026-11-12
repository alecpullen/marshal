package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"marshal/web/bridge/contract"
)

// pinPath is the socket's pin route (P4 D9).
const pinPath = "/api/projects/pin"

// pinRequest is the pin route's body.
type pinRequest struct {
	Root   string `json:"root"`
	Pinned bool   `json:"pinned"`
}

// pinProject serves POST /api/projects/pin on the socket (P4 D9). Every
// refusal is an ordinary JSON answer, and a successful pin answers the state
// the fleet holds afterwards.
func (s *Server) pinProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "pin is a POST"})
		return
	}
	var req pinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	root := req.Root
	if !filepath.IsAbs(root) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root must be an absolute path"})
		return
	}
	root = filepath.Clean(root)
	// A pin keeps a child running for the root, so the root has to be one the
	// daemon can start a child in. An unpin is always allowed: a root that has
	// since been removed must still be releasable (decision 4), as Fleet.Pin
	// itself allows.
	if req.Pinned {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root must be an existing directory"})
			return
		}
	}
	if s.fleet == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this bridge has no fleet"})
		return
	}

	// The child is started and held within one handshake budget, as the
	// attach route resolves its own: a stuck child must not hold the request
	// open indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), controlHandshakeTimeout)
	defer cancel()

	state, err := s.fleet.Pin(ctx, root, req.Pinned)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// PinState is contract.PinState; see that declaration.
type PinState = contract.PinState

// pinned reports whether root is pinned in the workspace.
func (f *Fleet) pinned(root string) bool {
	return f.ws.IsPinned(filepath.Clean(root))
}

// hasProjectChild reports whether root already has a child entry, started or
// starting. A caller that finds one did not start it, so it has not held the
// root.
func (f *Fleet) hasProjectChild(root string) bool {
	f.projMu.Lock()
	defer f.projMu.Unlock()
	_, ok := f.projects[filepath.Clean(root)]
	return ok
}

// holdLocked sends project/hold for pc's root on its current generation.
// The caller holds pc.mu.
func (f *Fleet) holdLocked(ctx context.Context, pc *projectChild) error {
	child := pc.child
	if child == nil {
		return fmt.Errorf("bridge: project child for %s is not running", pc.root)
	}
	if _, err := child.Request(ctx, "project/hold", map[string]any{"root": pc.root}); err != nil {
		return fmt.Errorf("bridge: hold project child for %s: %w", pc.root, err)
	}
	return nil
}

// Pin pins or unpins root (decisions 2 and 4).
//
// A pin is recorded before the child is resolved, so a start that fails leaves
// the root pinned: the operator asked for it to be kept, and the next start
// tries again (decision 4). A hold on a root that already holds changes
// nothing on the agent side, which is why a child that was already running
// when the pin arrived is held here as well.
func (f *Fleet) Pin(ctx context.Context, root string, pinned bool) (PinState, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return PinState{}, fmt.Errorf("bridge: pin root %q is not absolute", root)
	}
	if pinned {
		// A pin keeps a child running for a root, so the root has to be one
		// the daemon can start a child in. An unpin is always allowed: a root
		// that has since been removed must still be releasable.
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return PinState{}, fmt.Errorf("bridge: pin root %q is not an existing directory", root)
		}
	}
	if err := f.ws.SetPinned(root, pinned); err != nil {
		return PinState{}, err
	}

	if pinned {
		// Resolving starts the child when it is not running, cancels its
		// linger, and — now that the root is pinned — holds it.
		pc, err := f.projectChildFor(ctx, root)
		if err != nil {
			return PinState{Root: root, Pinned: true}, err
		}
		// A child that was already running when the pin arrived has not held
		// it yet, so hold it here too.
		pc.mu.Lock()
		err = f.holdLocked(ctx, pc)
		pc.mu.Unlock()
		if err != nil {
			return PinState{Root: root, Pinned: true}, err
		}
		return PinState{Root: root, Pinned: true, Running: true}, nil
	}

	f.projMu.Lock()
	pc, ok := f.projects[root]
	f.projMu.Unlock()
	running := false
	if ok {
		pc.mu.Lock()
		if pc.child != nil {
			running = true
			if _, err := pc.child.Request(ctx, "project/release", map[string]any{"root": pc.root}); err != nil {
				// A release that fails leaves the child holding a root that is
				// no longer pinned. It is logged rather than returned: the pin
				// is gone, which is what the operator asked for, and the linger
				// below still reaps the child.
				slog.Default().Warn("webbridge: release a project child",
					"root", pc.root, "err", err)
			}
		}
		pc.mu.Unlock()
	}
	if ok {
		// The child no longer holds the root, so it is an ordinary empty child
		// again and the linger reaps it.
		f.rearmLinger(pc)
	}
	return PinState{Root: root, Pinned: false, Running: running}, nil
}

// StartPinned starts every pinned project's child, one after another, logging
// a failure and going on (decision 9).
func (f *Fleet) StartPinned(ctx context.Context) {
	for _, root := range f.ws.Pinned() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			slog.Default().Warn("webbridge: pinned project root is not a directory", "root", root)
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, controlHandshakeTimeout)
		// A child that already existed was not started here, so it has not
		// held the root: either it was running when the pin arrived, or its
		// hold failed at its start. Hold it here, which the agent treats as
		// idempotent (decision 4). A child this call starts holds itself.
		existed := f.hasProjectChild(root)
		pc, err := f.projectChildFor(hctx, root)
		if err == nil && existed {
			pc.mu.Lock()
			err = f.holdLocked(hctx, pc)
			pc.mu.Unlock()
		}
		cancel()
		if err != nil {
			slog.Default().Warn("webbridge: start a pinned project child",
				"root", root, "err", err)
		}
	}
}
