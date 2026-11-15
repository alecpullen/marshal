package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The working-draft API is the Studio client's durable copy of an incomplete
// manual factory handoff: create, save, list, read, delete and convert, all
// scoped to a registered project. It sits on the same /api mux as the factory
// routes it converts into, so the existing bearer authentication covers it and
// there is no second auth path to get wrong.
//
// Nothing the browser sends is trusted as an identity. A by-ID route reads the
// draft first and takes the project from it, never from a query or body, and a
// conversion derives its factory request from the reviewed content. That is
// what keeps an ID from reaching another project's draft.

const (
	// workingDraftInstallationName is the file holding the bridge's installation
	// identity, under the working-draft state directory.
	workingDraftInstallationName = "installation.json"
	// workingDraftEnvelopeLimit bounds one working-draft request body: the
	// factory's 2 MiB request limit plus 64 KiB of JSON envelope around it. A
	// larger body is refused with 413 before it is parsed.
	workingDraftEnvelopeLimit = factoryRequestLimit + 64<<10
	// workingDraftInstallationIDLen is the length of the installation ID: 16
	// random bytes in lowercase hex.
	workingDraftInstallationIDLen = 32
	// workingDraftStateSubpath is where the store lives, beside the factory
	// journal's own state.
	workingDraftStateSubpath = "factory/working-drafts/v1"
)

// errWorkingDraftInput is a request this API refuses before it reaches the
// store: malformed JSON, an unknown field, trailing bytes or a missing value.
var errWorkingDraftInput = errors.New("invalid working draft request")

// errWorkingDraftTooLarge is a body past workingDraftEnvelopeLimit. It is
// separate from errWorkingDraftInput because the status differs (413).
var errWorkingDraftTooLarge = errors.New("working draft request body too large")

// workingDraftConversion is the conversion seam the routes use, so the HTTP
// surface is exercisable without a factory. *workingDraftConverter is the
// production implementation.
type workingDraftConversion interface {
	Convert(ctx context.Context, id, requestID string, expectedVersion uint64, request FactoryCreate, settingsVersion string) (WorkingDraft, *FactoryItem, error)
	Reconcile(ctx context.Context) error
}

// workingDraftSetup opens the working-draft store beside the factory state and
// registers its routes. It is called from NewServer for a real factory API;
// anything else keeps its routes exactly as they were.
//
// A store that cannot be opened is not omitted: the routes stay registered and
// answer 503, so a client sees an unavailable store rather than a missing
// feature. Startup reconciliation finishes any conversion a previous bridge
// left reserved; a failure is reported by the drafts' own state, so it is not
// propagated (and nothing here logs a path or draft content).
func (s *Server) workingDraftSetup(api FactoryAPI) {
	concrete, ok := api.(*factoryAPI)
	if !ok || concrete.fleet == nil || concrete.fleet.ws == nil || concrete.fleet.stateDir == "" {
		return
	}
	dir := filepath.Join(concrete.fleet.stateDir, workingDraftStateSubpath)
	store, err := OpenWorkingDraftStore(dir, nil)
	if err != nil {
		s.workingDraftRoutes(nil, nil, dir)
		return
	}
	conversion := newWorkingDraftConverter(store, concrete, concrete.fleet.ws)
	_ = conversion.Reconcile(context.Background())
	if s.fleet != nil {
		s.fleet.workingDrafts = store
	} else {
		concrete.fleet.workingDrafts = store
	}
	s.workingDraftRoutes(store, conversion, dir)
}

// workingDraftRoutes registers the working-draft routes. store and conversion
// are nil when the store could not be opened, which the handlers report as
// unavailable; installationPath is where the installation identity is kept.
func (s *Server) workingDraftRoutes(store *WorkingDraftStore, conversion workingDraftConversion, installationPath string) {
	s.mux.HandleFunc("POST /api/factory/working-drafts", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftCreate(w, r, store)
	})
	s.mux.HandleFunc("GET /api/factory/working-drafts", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftList(w, r, store)
	})
	// The literal path wins over {id}, so an installation read never resolves a
	// draft id.
	s.mux.HandleFunc("GET /api/factory/working-drafts/installation", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftInstallation(w, r, installationPath)
	})
	s.mux.HandleFunc("GET /api/factory/working-drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftGet(w, r, store)
	})
	s.mux.HandleFunc("PUT /api/factory/working-drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftSave(w, r, store)
	})
	s.mux.HandleFunc("DELETE /api/factory/working-drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftDelete(w, r, store)
	})
	s.mux.HandleFunc("POST /api/factory/working-drafts/{id}/convert", func(w http.ResponseWriter, r *http.Request) {
		s.workingDraftConvert(w, r, store, conversion)
	})
}

// workingDraftWriteError maps a working-draft error onto the statuses the
// client distinguishes: not found, validation, conflict, unavailable and
// uncertain. A conflict carries the current accessible draft when the caller
// gives one — never a draft from a project it cannot see.
func workingDraftWriteError(w http.ResponseWriter, err error, draft *WorkingDraft) {
	status, code := http.StatusBadRequest, "invalid_input"
	switch {
	case errors.Is(err, ErrWorkingDraftNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrWorkingDraftUncertain):
		status, code = http.StatusConflict, "uncertain"
	case errors.Is(err, ErrWorkingDraftConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, ErrWorkingDraftUnavailable),
		errors.Is(err, ErrFactoryUnavailable),
		errors.Is(err, ErrFactoryLocked),
		errors.Is(err, ErrFactoryCorrupt):
		status, code = http.StatusServiceUnavailable, "unavailable"
	case errors.Is(err, errWorkingDraftTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "too_large"
	case errors.Is(err, errFactoryConfig), errors.Is(err, ErrFactoryForgeUnsupported):
		code = "unsupported"
	}
	body := map[string]any{"error": err.Error(), "code": code}
	if draft != nil {
		body["draft"] = *draft
	}
	if code == "uncertain" {
		body["uncertain"] = true
	}
	writeJSON(w, status, body)
}

// workingDraftDecode reads one request body strictly: unknown fields and
// trailing JSON are refused, and an oversized body is a 413 rather than a
// validation error. It mirrors the factory decoder's shape.
func workingDraftDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, workingDraftEnvelopeLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			workingDraftWriteError(w, fmt.Errorf("%w: body exceeds 2 MiB", errWorkingDraftTooLarge), nil)
			return false
		}
		workingDraftWriteError(w, fmt.Errorf("%w: malformed JSON", errWorkingDraftInput), nil)
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		workingDraftWriteError(w, fmt.Errorf("%w: trailing JSON", errWorkingDraftInput), nil)
		return false
	}
	return true
}

// workingDraftScope refuses a project root the fleet does not have registered,
// reported as an unknown draft: no unregistered root names a scope, and a
// response never echoes a root or draft the caller cannot see.
func (s *Server) workingDraftScope(root string) error {
	if s.fleet == nil || s.fleet.ws == nil {
		return fmt.Errorf("%w: no project scope", ErrWorkingDraftUnavailable)
	}
	if err := s.fleet.registeredProject(root); err != nil {
		return fmt.Errorf("%w: project is not registered", ErrWorkingDraftNotFound)
	}
	return nil
}

// workingDraftLoad reads one draft and requires its stored project to be
// registered. The draft's own root is the only scope a by-ID route uses.
func (s *Server) workingDraftLoad(w http.ResponseWriter, store *WorkingDraftStore, id string) (WorkingDraft, bool) {
	draft, err := store.Get(id)
	if err != nil {
		workingDraftWriteError(w, err, nil)
		return WorkingDraft{}, false
	}
	if err := s.workingDraftScope(draft.ProjectRoot); err != nil {
		workingDraftWriteError(w, err, nil)
		return WorkingDraft{}, false
	}
	return draft, true
}

// workingDraftCurrent is the draft a 409 body may carry: the stored state, or
// nil when it cannot be read or its project is no longer registered.
func (s *Server) workingDraftCurrent(store *WorkingDraftStore, id string) *WorkingDraft {
	if store == nil {
		return nil
	}
	draft, err := store.Get(id)
	if err != nil || s.workingDraftScope(draft.ProjectRoot) != nil {
		return nil
	}
	return &draft
}

// workingDraftCreate stores a new draft for a registered project.
func (s *Server) workingDraftCreate(w http.ResponseWriter, r *http.Request, store *WorkingDraftStore) {
	if store == nil {
		workingDraftWriteError(w, ErrWorkingDraftUnavailable, nil)
		return
	}
	var body workingDraftCreateBody
	if !workingDraftDecode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ProjectRoot) == "" {
		workingDraftWriteError(w, fmt.Errorf("%w: projectRoot required", errWorkingDraftInput), nil)
		return
	}
	if err := s.workingDraftScope(body.ProjectRoot); err != nil {
		workingDraftWriteError(w, err, nil)
		return
	}
	draft, replay, err := store.create(body.ProjectRoot, body.RequestID, body.Content)
	if err != nil {
		// A create has no draft to show: the stored one may belong to another
		// project, and a reused request ID names no draft the caller may see.
		workingDraftWriteError(w, err, nil)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"draft": draft})
}

// workingDraftList returns the non-deleted drafts of one registered project.
func (s *Server) workingDraftList(w http.ResponseWriter, r *http.Request, store *WorkingDraftStore) {
	if store == nil {
		workingDraftWriteError(w, ErrWorkingDraftUnavailable, nil)
		return
	}
	root := strings.TrimSpace(r.URL.Query().Get("projectRoot"))
	if root == "" {
		workingDraftWriteError(w, fmt.Errorf("%w: projectRoot required", errWorkingDraftInput), nil)
		return
	}
	if err := s.workingDraftScope(root); err != nil {
		workingDraftWriteError(w, err, nil)
		return
	}
	drafts, err := store.List(root)
	if err != nil {
		workingDraftWriteError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": drafts})
}

// workingDraftGet returns one draft's full current state, including a tombstone
// so a device can compare its own copy with the recorded deletion.
func (s *Server) workingDraftGet(w http.ResponseWriter, r *http.Request, store *WorkingDraftStore) {
	if store == nil {
		workingDraftWriteError(w, ErrWorkingDraftUnavailable, nil)
		return
	}
	draft, ok := s.workingDraftLoad(w, store, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft})
}

// workingDraftSave conditionally replaces one editing draft's content.
func (s *Server) workingDraftSave(w http.ResponseWriter, r *http.Request, store *WorkingDraftStore) {
	if store == nil {
		workingDraftWriteError(w, ErrWorkingDraftUnavailable, nil)
		return
	}
	id := r.PathValue("id")
	var body workingDraftSaveBody
	if !workingDraftDecode(w, r, &body) {
		return
	}
	if _, ok := s.workingDraftLoad(w, store, id); !ok {
		return
	}
	draft, err := store.Save(id, body.RequestID, body.ExpectedVersion, body.Content)
	if err != nil {
		workingDraftWriteError(w, err, s.workingDraftCurrent(store, id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft})
}

// workingDraftDelete records a tombstone for one draft.
func (s *Server) workingDraftDelete(w http.ResponseWriter, r *http.Request, store *WorkingDraftStore) {
	if store == nil {
		workingDraftWriteError(w, ErrWorkingDraftUnavailable, nil)
		return
	}
	id := r.PathValue("id")
	var body workingDraftDeleteBody
	if !workingDraftDecode(w, r, &body) {
		return
	}
	if _, ok := s.workingDraftLoad(w, store, id); !ok {
		return
	}
	draft, err := store.Delete(id, body.RequestID, body.ExpectedVersion)
	if err != nil {
		workingDraftWriteError(w, err, s.workingDraftCurrent(store, id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft})
}

// workingDraftConvert turns one reviewed draft version into one factory item,
// reporting a durable converting/uncertain state when the outcome cannot be
// established.
func (s *Server) workingDraftConvert(w http.ResponseWriter, r *http.Request, store *WorkingDraftStore, conversion workingDraftConversion) {
	if store == nil || conversion == nil {
		workingDraftWriteError(w, ErrWorkingDraftUnavailable, nil)
		return
	}
	id := r.PathValue("id")
	var body workingDraftConvertBody
	if !workingDraftDecode(w, r, &body) {
		return
	}
	if _, ok := s.workingDraftLoad(w, store, id); !ok {
		return
	}
	// A zero request is the caller accepting the server's own reading of the
	// draft, which is what the converter resolves from the stored content.
	request := FactoryCreate{}
	if body.FactoryRequest != nil {
		request = *body.FactoryRequest
	}
	draft, item, err := conversion.Convert(r.Context(), id, body.RequestID, body.ExpectedVersion, request, body.SettingsVersion)
	if err != nil {
		if errors.Is(err, ErrWorkingDraftUncertain) {
			// Report the state the draft is actually in; a reserved conversion
			// is converting and must not be presented as saved or failed. A
			// read that fails here does not replace the uncertain answer with a
			// different error: the outcome is still unknown.
			current := draft
			if current.ID == "" {
				if stored, readErr := store.Get(id); readErr == nil {
					current = stored
				}
			}
			if current.ID == "" {
				workingDraftWriteError(w, err, nil)
				return
			}
			if scopeErr := s.workingDraftScope(current.ProjectRoot); scopeErr != nil {
				workingDraftWriteError(w, err, nil)
				return
			}
			workingDraftWriteError(w, err, &current)
			return
		}
		workingDraftWriteError(w, err, s.workingDraftCurrent(store, id))
		return
	}
	body2 := map[string]any{"draft": draft}
	if item != nil {
		body2["item"] = *item
	}
	writeJSON(w, http.StatusOK, body2)
}

// workingDraftInstallation returns the bridge's stable installation identity.
func (s *Server) workingDraftInstallation(w http.ResponseWriter, r *http.Request, installationPath string) {
	id, err := installationID(installationPath)
	if err != nil {
		workingDraftWriteError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installationId": id})
}

// installationID returns the installation identity stored under dir, creating
// and syncing it once. The value is random and opaque: no filesystem path,
// token or user data is stored beside it, and the same directory keeps the same
// ID across restarts.
func installationID(dir string) (string, error) {
	path := filepath.Join(dir, workingDraftInstallationName)
	raw, err := os.ReadFile(path)
	if err == nil {
		return parseInstallationID(raw)
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("%w: installation identity: %v", ErrWorkingDraftUnavailable, err)
	}
	id, err := newInstallationID()
	if err != nil {
		return "", fmt.Errorf("%w: installation identity: %v", ErrWorkingDraftUnavailable, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: installation identity: %v", ErrWorkingDraftUnavailable, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another process won the race: use the identity it wrote rather
			// than replacing it.
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("%w: installation identity: %v", ErrWorkingDraftUnavailable, readErr)
			}
			return parseInstallationID(raw)
		}
		return "", fmt.Errorf("%w: installation identity: %v", ErrWorkingDraftUnavailable, err)
	}
	body, err := json.Marshal(struct {
		InstallationID string `json:"installationId"`
	}{id})
	if err == nil {
		_, err = f.Write(append(body, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("%w: installation identity: %v", ErrWorkingDraftUnavailable, err)
	}
	// Persist the directory entry, so the identity survives a crash rather than
	// being regenerated as a different one.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return id, nil
}

func parseInstallationID(raw []byte) (string, error) {
	var stored struct {
		InstallationID string `json:"installationId"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil || !validInstallationID(stored.InstallationID) {
		// Never replace an unreadable identity: a device keyed to the old one
		// would silently stop matching its local records.
		return "", fmt.Errorf("%w: installation identity is unreadable", ErrWorkingDraftUnavailable)
	}
	return stored.InstallationID, nil
}

func validInstallationID(id string) bool {
	if len(id) != workingDraftInstallationIDLen {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func newInstallationID() (string, error) {
	b := make([]byte, workingDraftInstallationIDLen/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
