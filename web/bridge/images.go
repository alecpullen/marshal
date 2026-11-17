package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"marshal/web/bridge/contract"
)

// imagesPath is the upload route, beside GET /api/projects: the project root
// and the session the image belongs to are the addressing, so they travel in
// the query while the body stays exactly contract.ImageUpload.
const imagesPath = "/api/projects/images"

// maxImageBodyBytes bounds an upload body, counted in base64 characters and
// rejected before anything is decoded.
//
// web/ is standard-library only and may not read [images].max_input_bytes, so
// this is twice the documented 20 MiB default input cap: base64 inflates by
// 4/3 and the JSON framing adds a little more. A deployment that raises the
// config cap above the default is bounded here, not there — the coupling is
// deliberate and one-directional (this limit can only refuse an upload the
// agent would have taken, never forward one it would refuse).
const maxImageBodyBytes = 41943040

// uploadImage serves POST /api/projects/images (spec §5.6): the client's image
// goes to the session's child once and the ref it answers is what a later
// prompt attaches. Bearer auth is the /api/ middleware's, not this handler's.
func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(r.URL.Query().Get("root"))
	if root == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root query parameter is required"})
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session query parameter is required"})
		return
	}
	if !filepath.IsAbs(root) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root must be an absolute path"})
		return
	}
	root = filepath.Clean(root)

	// A known-oversize body is refused without reading it; the streaming
	// reader below catches a chunked one that declared no length.
	if r.ContentLength > maxImageBodyBytes {
		writeImageTooLarge(w)
		return
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBodyBytes)
	var body contract.ImageUpload
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeImageTooLarge(w)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must contain a single JSON value"})
		return
	}

	reg, sid, err := s.imageTarget(r.Context(), root, sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Registry.call injects the sessionId itself, so the params carry only
	// what the body does.
	res, err := reg.call(r.Context(), sid, imageMethod, "images_put", map[string]any{
		"data":  body.Data,
		"label": body.Label,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	var out contract.ImageUploadResult
	if err := json.Unmarshal(res, &out); err != nil {
		writeErr(w, fmt.Errorf("bridge: decode %s result: %w", imageMethod, err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// imageMethod is the child method an upload relays. It is a literal because
// everything under web/ is standard-library only and so may not import the
// agent's acpwire package that declares it.
const imageMethod = "session/images_put"

func writeImageTooLarge(w http.ResponseWriter) {
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
		"error": fmt.Sprintf("image upload exceeds %d bytes", maxImageBodyBytes),
	})
}

// imageTarget resolves where an upload belongs: the session's own registry
// when the bridge already knows that session, else the project child rooted at
// root — which must know the session id too, since the image is stored on the
// session's runtime. A child that does not know it is an unknown session, not
// a reason to open a dialer of our own.
func (s *Server) imageTarget(ctx context.Context, root, sessionID string) (*Registry, string, error) {
	reg, _, sid, err := s.registryForSession(sessionID)
	if err == nil {
		if reg == nil {
			return nil, "", ErrUnknownSession
		}
		return reg, sid, nil
	}
	if s.fleet == nil {
		return nil, "", ErrUnknownSession
	}
	pc, err := s.fleet.projectChildFor(ctx, root)
	if err != nil {
		return nil, "", err
	}
	if _, tracked := pc.reg.lookup(sessionID); !tracked {
		return nil, "", ErrUnknownSession
	}
	return pc.reg, sessionID, nil
}
