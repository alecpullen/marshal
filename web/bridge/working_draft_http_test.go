package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// workingDraftHTTPToken is the bearer token every test server is built with, so
// each test can assert the existing single-user API authentication still
// applies to the working-draft routes.
const workingDraftHTTPToken = "working-draft-secret"

// fakeWorkingDraftConversion is the conversion seam the HTTP tests use. It
// records each call and, in its default mode, performs the same two store
// transitions a real conversion does, so a stale or deleted draft is rejected
// by the store rather than by the fake.
type fakeWorkingDraftConversion struct {
	store *WorkingDraftStore

	mu    sync.Mutex
	mode  string
	calls []workingDraftConversionCall
}

type workingDraftConversionCall struct {
	id              string
	requestID       string
	expectedVersion uint64
	request         FactoryCreate
	settingsVersion string
}

func (f *fakeWorkingDraftConversion) setMode(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
}

func (f *fakeWorkingDraftConversion) callsSeen() []workingDraftConversionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]workingDraftConversionCall(nil), f.calls...)
}

func (f *fakeWorkingDraftConversion) Convert(_ context.Context, id, requestID string, expectedVersion uint64, request FactoryCreate, settingsVersion string) (WorkingDraft, *FactoryItem, error) {
	f.mu.Lock()
	f.calls = append(f.calls, workingDraftConversionCall{id, requestID, expectedVersion, request, settingsVersion})
	mode := f.mode
	f.mu.Unlock()

	switch mode {
	case "uncertain":
		draft, err := f.store.Get(id)
		if err != nil {
			return WorkingDraft{}, nil, err
		}
		return draft, nil, fmt.Errorf("%w: factory journal unavailable", ErrWorkingDraftUncertain)
	}
	if _, err := f.store.BeginConversion(id, requestID, expectedVersion, request, settingsVersion); err != nil {
		return WorkingDraft{}, nil, err
	}
	draft, err := f.store.Get(id)
	if err != nil {
		return WorkingDraft{}, nil, err
	}
	item := FactoryItem{ID: "item-" + draft.ID[:8], Title: request.Title, ProjectRoot: draft.ProjectRoot, State: "draft", Version: 1}
	done, err := f.store.CompleteConversion(id, "done-"+requestID, item.ID)
	if err != nil {
		return draft, nil, err
	}
	return done, &item, nil
}

func (f *fakeWorkingDraftConversion) Reconcile(context.Context) error { return nil }

// workingDraftHTTPEnv is one server with a real store, a registered project and
// a fake conversion, ready to take authenticated requests.
type workingDraftHTTPEnv struct {
	t     *testing.T
	fleet *Fleet
	srv   *Server
	store *WorkingDraftStore
	dir   string
	conv  *fakeWorkingDraftConversion
	root  string
	other string
}

func newWorkingDraftHTTPEnv(t *testing.T) *workingDraftHTTPEnv {
	t.Helper()
	fleet := NewFleet(NewWorkspace(filepath.Join(t.TempDir(), "fleet.json")), "", nil, t.TempDir(), Limits{}, "")
	t.Cleanup(fleet.Close)
	root := t.TempDir()
	other := t.TempDir()
	for _, p := range []string{root, other} {
		if err := fleet.ws.AddProject(p); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "working-drafts")
	store, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
	if err != nil {
		t.Fatalf("open working draft store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	conv := &fakeWorkingDraftConversion{store: store}
	srv := NewServer(fleet)
	srv.workingDraftRoutes(store, conv, dir)
	return &workingDraftHTTPEnv{t: t, fleet: fleet, srv: srv, store: store, dir: dir, conv: conv, root: root, other: other}
}

// do issues one request with a raw body, so malformed and oversized payloads
// can be sent exactly as they are.
func (e *workingDraftHTTPEnv) do(method, path, body, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := testRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	return w
}

// authed JSON-encodes body and sends it with the test token.
func (e *workingDraftHTTPEnv) authed(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		raw = string(b)
	}
	return e.do(method, path, raw, workingDraftHTTPToken)
}

func workingDraftResponse(t *testing.T, rec *httptest.ResponseRecorder) WorkingDraft {
	t.Helper()
	var body struct {
		Draft WorkingDraft `json:"draft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode draft from %q: %v", rec.Body.String(), err)
	}
	return body.Draft
}

func (e *workingDraftHTTPEnv) create(requestID, title string) WorkingDraft {
	e.t.Helper()
	rec := e.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: e.root, RequestID: requestID, Content: workingDraftTestContent(title),
	})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	return workingDraftResponse(e.t, rec)
}

func (e *workingDraftHTTPEnv) get(id string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.authed(http.MethodGet, "/api/factory/working-drafts/"+id, nil)
}

func TestWorkingDraftHTTPProjectScope(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)

	// An unregistered project root cannot name a scope.
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: "/not/registered", RequestID: "scope-1", Content: workingDraftTestContent("scope"),
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("create for unregistered root = %d, want 404", rec.Code)
	}
	if rec := env.authed(http.MethodGet, "/api/factory/working-drafts?projectRoot=/not/registered", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("list for unregistered root = %d, want 404", rec.Code)
	}
	if rec := env.authed(http.MethodGet, "/api/factory/working-drafts", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("list without projectRoot = %d, want 400", rec.Code)
	}

	mine := env.create("scope-2", "mine")
	if mine.ProjectRoot != env.root || mine.State != workingDraftEditing || mine.Version != 1 {
		t.Fatalf("created draft = %+v", mine)
	}
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: env.other, RequestID: "scope-3", Content: workingDraftTestContent("theirs"),
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create for second project = %d: %s", rec.Code, rec.Body.String())
	}

	list := func(root string) []WorkingDraft {
		t.Helper()
		rec := env.authed(http.MethodGet, "/api/factory/working-drafts?projectRoot="+root, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Drafts []WorkingDraft `json:"drafts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return body.Drafts
	}
	got := list(env.root)
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("project list = %+v, want only %s", got, mine.ID)
	}
	if other := list(env.other); len(other) != 1 || other[0].Content.Title != "theirs" {
		t.Fatalf("other project list = %+v", other)
	}

	// A draft whose project is no longer registered is unreachable by ID too.
	theirs := list(env.other)[0]
	if err := env.fleet.ws.RemoveProject(env.other); err != nil {
		t.Fatal(err)
	}
	if rec := env.get(theirs.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("get draft of unregistered project = %d, want 404", rec.Code)
	}
	if rec := env.authed(http.MethodPut, "/api/factory/working-drafts/"+theirs.ID, workingDraftSaveBody{
		RequestID: "scope-4", ExpectedVersion: 1, Content: workingDraftTestContent("edited"),
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("save draft of unregistered project = %d, want 404", rec.Code)
	}
	if rec := env.authed(http.MethodGet, "/api/factory/working-drafts?projectRoot="+env.other, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("list of unregistered project = %d, want 404", rec.Code)
	}
}

func TestWorkingDraftHTTPInstallationIdentity(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	response := func(srv *Server) string {
		t.Helper()
		r := testRequest(http.MethodGet, "/api/factory/working-drafts/installation", nil)
		r.Header.Set("Authorization", "Bearer "+workingDraftHTTPToken)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("installation = %d: %s", w.Code, w.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode installation: %v", err)
		}
		if len(body) != 1 {
			t.Fatalf("installation body = %v, want only installationId", body)
		}
		id := body["installationId"]
		if len(id) != workingDraftInstallationIDLen {
			t.Fatalf("installation id = %q, want %d chars", id, workingDraftInstallationIDLen)
		}
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Fatalf("installation id %q is not lowercase hex", id)
			}
		}
		// The identity is never a path or the token.
		if strings.Contains(w.Body.String(), env.root) || strings.Contains(w.Body.String(), workingDraftHTTPToken) {
			t.Fatalf("installation leaked path or token: %s", w.Body.String())
		}
		return id
	}

	id := response(env.srv)
	if again := response(env.srv); again != id {
		t.Fatalf("installation id changed between reads: %q then %q", id, again)
	}
	// A restart over the same state directory keeps the identity.
	restarted := NewServer(env.fleet)
	restarted.workingDraftRoutes(env.store, env.conv, env.dir)
	if reopened := response(restarted); reopened != id {
		t.Fatalf("installation id changed across restart: %q then %q", id, reopened)
	}
	if direct, err := installationID(env.dir); err != nil || direct != id {
		t.Fatalf("installationID = %q, %v; want %q", direct, err, id)
	}

	stored, err := os.Stat(filepath.Join(env.dir, workingDraftInstallationName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := stored.Mode().Perm(); perm != 0o600 {
		t.Fatalf("installation file mode = %o, want 600", perm)
	}
	raw, err := os.ReadFile(filepath.Join(env.dir, workingDraftInstallationName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), env.dir) || strings.Contains(string(raw), workingDraftHTTPToken) {
		t.Fatalf("installation file carries a path or token: %s", raw)
	}

	// An unreadable identity is unavailable, never silently replaced.
	if err := os.WriteFile(filepath.Join(env.dir, workingDraftInstallationName), []byte("not an id"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := env.authed(http.MethodGet, "/api/factory/working-drafts/installation", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable installation identity = %d, want 503", rec.Code)
	}
}

func TestWorkingDraftHTTPIncompleteRoundTrip(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	// An incomplete draft is accepted: empty and temporarily invalid values are
	// editor state, not factory configuration.
	incomplete := WorkingDraftContent{ChecksMode: workingDraftChecksCustom, MaxRoundsOverride: "two"}
	rec := env.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: env.root, RequestID: "round-1", Content: incomplete,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	draft := workingDraftResponse(t, rec)
	if draft.Content != incomplete || draft.Version != 1 || draft.State != workingDraftEditing {
		t.Fatalf("incomplete draft = %+v", draft)
	}
	if got := workingDraftResponse(t, env.get(draft.ID)); got.Content != incomplete || got.Version != 1 {
		t.Fatalf("get = %+v", got)
	}

	edited := workingDraftTestContent("edited")
	saved := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "round-2", ExpectedVersion: 1, Content: edited,
	})
	if saved.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", saved.Code, saved.Body.String())
	}
	afterSave := workingDraftResponse(t, saved)
	if afterSave.Version != 2 || afterSave.State != workingDraftEditing || afterSave.Content != edited {
		t.Fatalf("saved draft = %+v", afterSave)
	}
	if got := workingDraftResponse(t, env.get(draft.ID)); got.Version != 2 || got.Content != edited {
		t.Fatalf("get after save = %+v", got)
	}

	rec = env.authed(http.MethodGet, "/api/factory/working-drafts?projectRoot="+env.root, nil)
	var list struct {
		Drafts []WorkingDraft `json:"drafts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Drafts) != 1 || list.Drafts[0].Version != 2 || list.Drafts[0].Content != edited {
		t.Fatalf("list = %+v", list.Drafts)
	}
}

func TestWorkingDraftHTTPConflictAndReplay(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	draft := env.create("conflict-1", "first")
	first := workingDraftTestContent("first")
	second := workingDraftTestContent("second")

	if rec := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "conflict-2", ExpectedVersion: 1, Content: first,
	}); rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}

	// A stale version is a conflict, carries the current state and mutates
	// nothing.
	stale := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "conflict-3", ExpectedVersion: 1, Content: second,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409: %s", stale.Code, stale.Body.String())
	}
	var conflict struct {
		Code  string       `json:"code"`
		Draft WorkingDraft `json:"draft"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Code != "conflict" || conflict.Draft.ID != draft.ID || conflict.Draft.Version != 2 || conflict.Draft.Content != first {
		t.Fatalf("conflict body = %+v", conflict)
	}
	if got := workingDraftResponse(t, env.get(draft.ID)); got.Version != 2 || got.Content != first {
		t.Fatalf("conflict mutated the draft: %+v", got)
	}

	// An identical replay of an accepted request returns its original result,
	// even though later versions exist.
	replayed := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "conflict-2", ExpectedVersion: 1, Content: first,
	})
	if replayed.Code != http.StatusOK || workingDraftResponse(t, replayed).Version != 2 {
		t.Fatalf("replay = %d: %s", replayed.Code, replayed.Body.String())
	}
	// Reusing the request ID with different bytes is a conflict.
	reused := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "conflict-2", ExpectedVersion: 2, Content: second,
	})
	if reused.Code != http.StatusConflict {
		t.Fatalf("reused request id = %d, want 409: %s", reused.Code, reused.Body.String())
	}

	// A 409 never carries another project's draft.
	theirs := env.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: env.other, RequestID: "conflict-4", Content: workingDraftTestContent("theirs"),
	})
	if theirs.Code != http.StatusCreated {
		t.Fatalf("other project create = %d: %s", theirs.Code, theirs.Body.String())
	}
	otherDraft := workingDraftResponse(t, theirs)
	scoped := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "conflict-5", ExpectedVersion: 1, Content: second,
	})
	if scoped.Code != http.StatusConflict {
		t.Fatalf("scoped conflict = %d, want 409", scoped.Code)
	}
	if strings.Contains(scoped.Body.String(), otherDraft.ID) || strings.Contains(scoped.Body.String(), env.other) {
		t.Fatalf("conflict leaked another project's draft: %s", scoped.Body.String())
	}

	// A create replay returns the original draft; changed content conflicts.
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: env.root, RequestID: "conflict-1", Content: workingDraftTestContent("first"),
	}); rec.Code != http.StatusOK || workingDraftResponse(t, rec).ID != draft.ID {
		t.Fatalf("create replay = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: env.root, RequestID: "conflict-1", Content: workingDraftTestContent("changed"),
	}); rec.Code != http.StatusConflict {
		t.Fatalf("create reuse = %d, want 409", rec.Code)
	}
}

func TestWorkingDraftHTTPDeleteTombstone(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	draft := env.create("delete-1", "doomed")

	deleted := env.authed(http.MethodDelete, "/api/factory/working-drafts/"+draft.ID, workingDraftDeleteBody{
		RequestID: "delete-2", ExpectedVersion: 1,
	})
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", deleted.Code, deleted.Body.String())
	}
	tombstone := workingDraftResponse(t, deleted)
	if tombstone.State != workingDraftDeleted || tombstone.Version != 2 {
		t.Fatalf("tombstone = %+v", tombstone)
	}
	// A tombstone is readable so a device can compare its own copy, and is
	// excluded from lists.
	if got := workingDraftResponse(t, env.get(draft.ID)); got.State != workingDraftDeleted {
		t.Fatalf("get tombstone = %+v", got)
	}
	rec := env.authed(http.MethodGet, "/api/factory/working-drafts?projectRoot="+env.root, nil)
	var list struct {
		Drafts []WorkingDraft `json:"drafts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Drafts) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list.Drafts)
	}

	// A stale client must not resurrect the tombstone.
	if rec := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, workingDraftSaveBody{
		RequestID: "delete-3", ExpectedVersion: 2, Content: workingDraftTestContent("resurrected"),
	}); rec.Code != http.StatusConflict {
		t.Fatalf("save after delete = %d, want 409", rec.Code)
	}
	if got := workingDraftResponse(t, env.get(draft.ID)); got.State != workingDraftDeleted || got.Content.Title == "resurrected" {
		t.Fatalf("tombstone was resurrected: %+v", got)
	}
	// A stale delete is refused with the current state.
	stale := env.authed(http.MethodDelete, "/api/factory/working-drafts/"+draft.ID, workingDraftDeleteBody{
		RequestID: "delete-4", ExpectedVersion: 1,
	})
	if stale.Code != http.StatusConflict || workingDraftResponse(t, stale).Version != 2 {
		t.Fatalf("stale delete = %d: %s", stale.Code, stale.Body.String())
	}
	// The accepted delete replays its original result.
	if rec := env.authed(http.MethodDelete, "/api/factory/working-drafts/"+draft.ID, workingDraftDeleteBody{
		RequestID: "delete-2", ExpectedVersion: 1,
	}); rec.Code != http.StatusOK || workingDraftResponse(t, rec).State != workingDraftDeleted {
		t.Fatalf("delete replay = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkingDraftHTTPConvertAndUncertain(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	draft := env.create("convert-1", "convertible")
	request := FactoryCreate{
		Title:  "convertible",
		Plan:   "plan",
		Config: FactoryConfig{RepoID: "repo-1", ProjectRoot: env.root, Native: true, NoRequiredCIAcknowledged: true},
	}

	converted := env.authed(http.MethodPost, "/api/factory/working-drafts/"+draft.ID+"/convert", workingDraftConvertBody{
		RequestID: "convert-2", ExpectedVersion: 1, FactoryRequest: &request, SettingsVersion: "settings-v1",
	})
	if converted.Code != http.StatusOK {
		t.Fatalf("convert = %d: %s", converted.Code, converted.Body.String())
	}
	var body struct {
		Draft WorkingDraft `json:"draft"`
		Item  *FactoryItem `json:"item"`
	}
	if err := json.Unmarshal(converted.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode convert: %v", err)
	}
	if body.Item == nil || body.Item.ID == "" {
		t.Fatalf("convert returned no item: %s", converted.Body.String())
	}
	if body.Draft.State != workingDraftConverted || body.Draft.FactoryItemID != body.Item.ID {
		t.Fatalf("converted draft = %+v", body.Draft)
	}
	calls := env.conv.callsSeen()
	if len(calls) != 1 || calls[0].settingsVersion != "settings-v1" || calls[0].request.Title != "convertible" || calls[0].expectedVersion != 1 {
		t.Fatalf("conversion calls = %+v", calls)
	}

	// A nil factory request is the caller accepting the server's own reading.
	second := env.create("convert-3", "server derived")
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts/"+second.ID+"/convert", workingDraftConvertBody{
		RequestID: "convert-4", ExpectedVersion: 1,
	}); rec.Code != http.StatusOK {
		t.Fatalf("convert without request = %d: %s", rec.Code, rec.Body.String())
	}
	calls = env.conv.callsSeen()
	if !reflectDeepEqual(calls[1].request, FactoryCreate{}) {
		t.Fatalf("nil factory request was not passed as the zero request: %+v", calls[1].request)
	}

	// An establishable-outcome failure is a durable uncertain state: 409, the
	// draft, and the flag that says the outcome is unknown.
	third := env.create("convert-5", "uncertain")
	env.conv.setMode("uncertain")
	uncertain := env.authed(http.MethodPost, "/api/factory/working-drafts/"+third.ID+"/convert", workingDraftConvertBody{
		RequestID: "convert-6", ExpectedVersion: 1,
	})
	if uncertain.Code != http.StatusConflict {
		t.Fatalf("uncertain convert = %d, want 409: %s", uncertain.Code, uncertain.Body.String())
	}
	var uncertainBody struct {
		Uncertain bool         `json:"uncertain"`
		Draft     WorkingDraft `json:"draft"`
	}
	if err := json.Unmarshal(uncertain.Body.Bytes(), &uncertainBody); err != nil {
		t.Fatalf("decode uncertain: %v", err)
	}
	if !uncertainBody.Uncertain || uncertainBody.Draft.ID != third.ID || uncertainBody.Draft.State != workingDraftEditing {
		t.Fatalf("uncertain body = %+v", uncertainBody)
	}

	// A stale version is rejected without a factory run and without mutating the
	// draft; the conversion reports the conflict, the store refuses the reserve.
	env.conv.setMode("")
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts/"+third.ID+"/convert", workingDraftConvertBody{
		RequestID: "convert-7", ExpectedVersion: 9,
	}); rec.Code != http.StatusConflict {
		t.Fatalf("stale convert = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := workingDraftResponse(t, env.get(third.ID)); got.State != workingDraftEditing || got.Version != 1 {
		t.Fatalf("stale convert changed the draft: %+v", got)
	}
	// An unknown draft is not found, and no conversion runs.
	if rec := env.authed(http.MethodPost, "/api/factory/working-drafts/unknown/convert", workingDraftConvertBody{
		RequestID: "convert-8", ExpectedVersion: 1,
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown convert = %d, want 404", rec.Code)
	}
}

// reflectDeepEqual compares two factory requests field by field through their
// JSON encoding, which is what the converter compares.
func reflectDeepEqual(a, b FactoryCreate) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

func TestWorkingDraftHTTPMalformedAndLimits(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	cases := map[string]struct {
		method, path, body string
	}{
		"malformed":          {http.MethodPost, "/api/factory/working-drafts", `{"projectRoot":`},
		"unknown field":      {http.MethodPost, "/api/factory/working-drafts", fmt.Sprintf(`{"projectRoot":%q,"requestId":"m1","content":{},"bogus":1}`, env.root)},
		"trailing json":      {http.MethodPost, "/api/factory/working-drafts", fmt.Sprintf(`{"projectRoot":%q,"requestId":"m2","content":{}}{}`, env.root)},
		"missing root":       {http.MethodPost, "/api/factory/working-drafts", `{"requestId":"m3","content":{}}`},
		"missing request id": {http.MethodPost, "/api/factory/working-drafts", fmt.Sprintf(`{"projectRoot":%q,"content":{}}`, env.root)},
	}
	for name, c := range cases {
		if rec := env.do(c.method, c.path, c.body, workingDraftHTTPToken); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}

	// Over the factory import limit, but inside the envelope: the store's own
	// limit answers 400 rather than creating a draft nothing could convert.
	draft := env.create("limit-1", "small")
	oversized := workingDraftSaveBody{
		RequestID:       "limit-2",
		ExpectedVersion: 1,
		Content:         WorkingDraftContent{Title: "big", Plan: strings.Repeat("p", factoryImportLimit+1)},
	}
	if rec := env.authed(http.MethodPut, "/api/factory/working-drafts/"+draft.ID, oversized); rec.Code != http.StatusBadRequest {
		t.Fatalf("over-limit content = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	// Past the whole envelope the body is refused before it is parsed. The
	// payload is a JSON string so the limit, not a syntax error, is what stops
	// it.
	huge := fmt.Sprintf(`{"projectRoot":%q,"requestId":"big","content":{"title":"%s"}}`, env.root, strings.Repeat("x", workingDraftEnvelopeLimit))
	if rec := env.do(http.MethodPost, "/api/factory/working-drafts", huge, workingDraftHTTPToken); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	// A save body is strict about its own shape too.
	if rec := env.do(http.MethodPut, "/api/factory/working-drafts/"+draft.ID,
		fmt.Sprintf(`{"requestId":"limit-3","expectedVersion":1,"content":{},"projectRoot":%q}`, env.other), workingDraftHTTPToken); rec.Code != http.StatusBadRequest {
		t.Fatalf("save with unknown field = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkingDraftHTTPUnavailableStore(t *testing.T) {
	env := newWorkingDraftHTTPEnv(t)
	draft := env.create("unavailable-1", "secret-content")
	if err := env.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/factory/working-drafts?projectRoot=" + env.root, nil},
		{http.MethodGet, "/api/factory/working-drafts/" + draft.ID, nil},
		{http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{ProjectRoot: env.root, RequestID: "unavailable-2"}},
		{http.MethodPut, "/api/factory/working-drafts/" + draft.ID, workingDraftSaveBody{RequestID: "unavailable-3", ExpectedVersion: 1}},
		{http.MethodDelete, "/api/factory/working-drafts/" + draft.ID, workingDraftDeleteBody{RequestID: "unavailable-4", ExpectedVersion: 1}},
		{http.MethodPost, "/api/factory/working-drafts/" + draft.ID + "/convert", workingDraftConvertBody{RequestID: "unavailable-5", ExpectedVersion: 1}},
	}
	for _, route := range routes {
		rec := env.authed(route.method, route.path, route.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s with a closed store = %d, want 503: %s", route.method, route.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret-content") || strings.Contains(rec.Body.String(), env.dir) {
			t.Fatalf("%s %s leaked draft content or a path: %s", route.method, route.path, rec.Body.String())
		}
	}

	// A store that could not be opened at startup registers the same routes,
	// answering unavailable rather than 404.
	bare := NewServer(env.fleet)
	bare.workingDraftRoutes(nil, nil, env.dir)
	r := testRequest(http.MethodGet, "/api/factory/working-drafts?projectRoot="+env.root, nil)
	r.Header.Set("Authorization", "Bearer "+workingDraftHTTPToken)
	w := httptest.NewRecorder()
	bare.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("routes without a store = %d, want 503: %s", w.Code, w.Body.String())
	}
}
