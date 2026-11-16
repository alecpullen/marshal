package bridge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"marshal/web/bridge/contract"
)

// uploadToken is the bearer token the upload tests' bridges require: the
// route is under /api/, so the middleware's check is the only one there is.
const uploadToken = "upload-secret"

// imageURL addresses one session's upload on root.
func imageURL(root, sessionID string) string {
	q := url.Values{}
	q.Set("root", root)
	q.Set("session", sessionID)
	return imagesPath + "?" + q.Encode()
}

// uploadSession opens root's project child and one session on it, returning
// the ACP session id the route addresses.
func uploadSession(t *testing.T, f *Fleet, root string) string {
	t.Helper()
	ctx := ctlContext(t)
	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	sid, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	return sid
}

// storeImages makes a fake project child store what it is given: the ref it
// answers hashes the decoded bytes, which is what the real store's
// content-addressed ref does.
func storeImages(fake *fakeProjectChild) {
	fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		if method != imageMethod {
			return nil, nil, false
		}
		var p struct {
			SessionID string `json:"sessionId"`
			Data      string `json:"data"`
			Label     string `json:"label"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: err.Error()}, true
		}
		if p.SessionID == "" {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "sessionId is required"}, true
		}
		raw, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "data is not base64"}, true
		}
		sum := sha256.Sum256(raw)
		return map[string]any{"image": map[string]any{
			"hash":   hex.EncodeToString(sum[:]),
			"mime":   "image/png",
			"width":  3,
			"height": 2,
			"bytes":  len(raw),
			"label":  p.Label,
		}}, nil, true
	}
}

// TestUploadImageReturnsRef: the route relays the upload to the session's
// child once and answers the ref the child made, whose hash is the stored
// bytes'.
func TestUploadImageReturnsRef(t *testing.T) {
	f, fakeFor := projectFleetWith(t, storeImages)
	root := t.TempDir()
	sessionID := uploadSession(t, f, root)
	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}

	srv := NewServer(f, uploadToken)
	raw := []byte("png bytes, not a real png")
	rec := doReq(t, srv, http.MethodPost, imageURL(root, sessionID),
		contract.ImageUpload{Data: base64.StdEncoding.EncodeToString(raw), Label: "shot of the ui"},
		map[string]string{"Authorization": "Bearer " + uploadToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got contract.ImageUploadResult
	decodeBody(t, rec, &got)

	want := sha256.Sum256(raw)
	if got.Image.Hash != hex.EncodeToString(want[:]) {
		t.Fatalf("hash = %q, want the hash of the stored bytes %q",
			got.Image.Hash, hex.EncodeToString(want[:]))
	}
	if got.Image.MIME != "image/png" || got.Image.Width != 3 || got.Image.Height != 2 {
		t.Fatalf("ref = %+v, want the child's mime and dimensions", got.Image)
	}
	if got.Image.Bytes != len(raw) || got.Image.Label != "shot of the ui" {
		t.Fatalf("ref = %+v, want %d bytes labelled from the body", got.Image, len(raw))
	}

	// The params carried the addressed session once, injected by the
	// registry, and never a second copy from the body.
	calls := fake.calls(imageMethod)
	if len(calls) != 1 {
		t.Fatalf("%s was called %d times, want once", imageMethod, len(calls))
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(calls[0]), &params); err != nil {
		t.Fatalf("decode params %q: %v", calls[0], err)
	}
	if params["sessionId"] != sessionID {
		t.Fatalf("sessionId = %v, want %q", params["sessionId"], sessionID)
	}
	if _, dup := params["SessionID"]; dup {
		t.Fatalf("params carry a second session id: %v", params)
	}
}

// TestUploadImageRequiresToken: the route is under /api/, so the bearer
// middleware refuses a request without the token before the handler runs.
func TestUploadImageRequiresToken(t *testing.T) {
	f, fakeFor := projectFleetWith(t, storeImages)
	root := t.TempDir()
	sessionID := uploadSession(t, f, root)

	srv := NewServer(f, uploadToken)
	body := contract.ImageUpload{Data: base64.StdEncoding.EncodeToString([]byte("raw"))}
	rec := doReq(t, srv, http.MethodPost, imageURL(root, sessionID), body, nil)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 401 or 403; body = %s", rec.Code, rec.Body.String())
	}
	if calls := fakeFor(root).calls(imageMethod); len(calls) != 0 {
		t.Fatalf("an unauthenticated upload reached the child: %v", calls)
	}
}

// TestUploadImageRejectsOversizeBody: a body over the cap is refused, before
// anything is decoded, and the child is never opened.
func TestUploadImageRejectsOversizeBody(t *testing.T) {
	f, fakeFor := projectFleetWith(t, storeImages)
	root := t.TempDir()

	srv := NewServer(f, uploadToken)
	req := httptest.NewRequest(http.MethodPost, imageURL(root, "s-1"), nil)
	req.Header.Set("Authorization", "Bearer "+uploadToken)
	req.ContentLength = maxImageBodyBytes + 1
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", rec.Code, rec.Body.String())
	}
	if fake := fakeFor(root); fake != nil {
		if calls := fake.calls(imageMethod); len(calls) != 0 {
			t.Fatalf("an oversize upload reached the child: %v", calls)
		}
	}
}

// TestUploadImageChildErrorSurfaces: the child's refusal is answered as a JSON
// error carrying its message, not as a 500 with a Go stack.
func TestUploadImageChildErrorSurfaces(t *testing.T) {
	const message = "the image store is disabled for this session"
	f, _ := projectFleetWith(t, func(fake *fakeProjectChild) {
		fake.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
			if method != imageMethod {
				return nil, nil, false
			}
			return nil, &rpcError{Code: -32603, Message: message}, true
		}
	})
	root := t.TempDir()
	sessionID := uploadSession(t, f, root)

	srv := NewServer(f, uploadToken)
	rec := doReq(t, srv, http.MethodPost, imageURL(root, sessionID),
		contract.ImageUpload{Data: base64.StdEncoding.EncodeToString([]byte("raw"))},
		map[string]string{"Authorization": "Bearer " + uploadToken})
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("a child error became a 500: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", ct)
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if !strings.Contains(body["error"], message) {
		t.Fatalf("error = %q, want the child's message %q", body["error"], message)
	}
	if strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("the response carries a Go stack: %s", rec.Body.String())
	}
}

// TestUploadImageRequiresQueryParams: root and session are the addressing, so
// a missing one is a 400 that names it.
func TestUploadImageRequiresQueryParams(t *testing.T) {
	f, _ := projectFleetWith(t, storeImages)
	root := t.TempDir()
	sessionID := uploadSession(t, f, root)
	srv := NewServer(f, uploadToken)
	auth := map[string]string{"Authorization": "Bearer " + uploadToken}
	body := contract.ImageUpload{Data: base64.StdEncoding.EncodeToString([]byte("raw"))}

	for _, tc := range []struct {
		name, target, missing string
	}{
		{name: "no root", target: imagesPath + "?session=" + sessionID, missing: "root"},
		{name: "empty root", target: imagesPath + "?root=&session=" + sessionID, missing: "root"},
		{name: "no session", target: imagesPath + "?root=" + url.QueryEscape(root), missing: "session"},
		{name: "empty session", target: imagesPath + "?root=" + url.QueryEscape(root) + "&session=", missing: "session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doReq(t, srv, http.MethodPost, tc.target, body, auth)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			var got map[string]string
			decodeBody(t, rec, &got)
			if !strings.Contains(got["error"], tc.missing) {
				t.Fatalf("error = %q, want it to name %q", got["error"], tc.missing)
			}
		})
	}
}
