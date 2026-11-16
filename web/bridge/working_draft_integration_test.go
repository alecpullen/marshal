package bridge

// These two tests are the end-to-end proof the working-draft plan asks for: one
// reviewed draft, edited by two devices, becomes exactly one unapproved factory
// draft across a server restart, and a crash at any conversion boundary is
// finished by reconciliation without ever creating a second factory item.
//
// Both drive the committed surface only. The HTTP half runs a real server over
// the real store and the real converter behind the API's own bearer auth; the
// factory preflight is this package's fake, which journals request ID -> item
// the way the factory store does and can refuse a request before it journals.
// That is the "fake factory preflight" the plan names: the factory's own
// persistence boundary is real in the store and simulated, one request at a
// time, in the factory.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"testing"
)

// e2eService is one running bridge over one working-draft state directory: a
// real store, the real converter and a real HTTP server behind the API's bearer
// check. A restart is close followed by open on the same directory, which is
// what a bridge process restart looks like from the store's side: the draft
// store rebuilds from its journal and the fake factory keeps the journal it
// already has.
type e2eService struct {
	t        *testing.T
	fleet    *Fleet
	root     string
	dir      string
	settings *fakeSettingsVersion
	factory  *fakeFrozenFactory

	store      *WorkingDraftStore
	conversion *workingDraftConverter
	server     *httptest.Server
}

func newE2EService(t *testing.T) *e2eService {
	t.Helper()
	fleet := NewFleet(NewWorkspace(filepath.Join(t.TempDir(), "fleet.json")), "", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(fleet.Close)
	// The workspace writes through a rename, so its path is a file under a
	// temporary directory, and the project root is a directory of its own.
	root := t.TempDir()
	if err := fleet.ws.AddProject(root); err != nil {
		t.Fatalf("register project: %v", err)
	}
	settings := &fakeSettingsVersion{token: `"settings-1"`}
	s := &e2eService{
		t:        t,
		fleet:    fleet,
		root:     root,
		dir:      filepath.Join(t.TempDir(), "working-drafts"),
		settings: settings,
		factory:  newFakeFrozenFactory(settings),
	}
	s.open()
	t.Cleanup(s.close)
	return s
}

// open starts the bridge over the state directory: the store is opened from its
// journal, the converter is built over it and the fake preflight, and the
// working-draft routes go on a mux the test's server wraps in bearer auth.
func (s *e2eService) open() {
	s.t.Helper()
	store, err := OpenWorkingDraftStore(s.dir, workingDraftTestClock())
	if err != nil {
		s.t.Fatalf("open working draft store: %v", err)
	}
	conversion := newWorkingDraftConverter(store, s.factory, s.settings)
	mux := http.NewServeMux()
	srv := &Server{fleet: s.fleet, mux: mux}
	srv.workingDraftRoutes(store, conversion, s.dir)
	s.store, s.conversion = store, conversion
	s.server = httptest.NewServer(bearerAuth(workingDraftHTTPToken, mux))
}

// close stops the server and releases the store's ownership lock, so a restart
// can reopen the same directory.
func (s *e2eService) close() {
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			s.t.Errorf("close working draft store: %v", err)
		}
		s.store, s.conversion = nil, nil
	}
}

// reconcile is the startup pass a real bridge runs: it finishes every
// conversion a previous process left reserved.
func (s *e2eService) reconcile() {
	s.t.Helper()
	if err := s.conversion.Reconcile(context.Background()); err != nil {
		s.t.Fatalf("reconcile: %v", err)
	}
}

// request issues one authenticated request. Bodies are the wire envelopes the
// Studio client sends, so they stay inside the server's own size limit.
func (s *e2eService) request(method, path string, body any) (int, []byte) {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("encode %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.server.URL+path, reader)
	if err != nil {
		s.t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+workingDraftHTTPToken)
	resp, err := s.server.Client().Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(workingDraftEnvelopeLimit)))
	if err != nil {
		s.t.Fatalf("read %s %s response: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

// installationID reads the bridge's installation identity over HTTP and checks
// its shape, so a restart can be shown to keep the same one.
func (s *e2eService) installationID() string {
	s.t.Helper()
	status, raw := s.request(http.MethodGet, "/api/factory/working-drafts/installation", nil)
	if status != http.StatusOK {
		s.t.Fatalf("installation = %d: %s", status, raw)
	}
	var body struct {
		InstallationID string `json:"installationId"`
	}
	e2eDecode(s.t, raw, &body)
	if !validInstallationID(body.InstallationID) {
		s.t.Fatalf("installation id = %q, want %d lowercase hex characters", body.InstallationID, workingDraftInstallationIDLen)
	}
	return body.InstallationID
}

// reserveIntent performs exactly the durable half of a conversion — the
// server's own reading of the draft, the factory's frozen preflight and the
// intent — and returns the frozen request. It is the state a crash between the
// reserve and the factory call leaves on disk.
func (s *e2eService) reserveIntent(draft WorkingDraft, requestID string) FactoryCreate {
	s.t.Helper()
	derived, err := workingDraftFactoryRequest(draft, derivedFactoryRequestID(draft.ID, requestID))
	if err != nil {
		s.t.Fatalf("derive factory request: %v", err)
	}
	resolved, settingsVersion, err := s.factory.PrepareFrozen(derived)
	if err != nil {
		s.t.Fatalf("factory preflight: %v", err)
	}
	if _, err := s.store.BeginConversion(draft.ID, requestID, draft.Version, resolved, settingsVersion); err != nil {
		s.t.Fatalf("reserve conversion: %v", err)
	}
	return resolved
}

// oneItem requires the factory journal to hold exactly one item and returns it,
// having checked that no approval, round, worker, effect, pull request or
// decision ever touched it.
func (s *e2eService) oneItem() FactoryItem {
	s.t.Helper()
	s.factory.mu.Lock()
	items := append([]FactoryItem(nil), s.factory.items...)
	s.factory.mu.Unlock()
	if len(items) != 1 {
		s.t.Fatalf("factory journal holds %d items, want 1", len(items))
	}
	assertUnapprovedFactoryDraft(s.t, items[0])
	return items[0]
}

// e2eContext is one editing device: its own editor identity, and therefore its
// own request-ID sequence. Two contexts drive one draft over HTTP only.
type e2eContext struct {
	svc    *e2eService
	editor string
	seq    int
}

func (s *e2eService) editor(editor string) *e2eContext {
	return &e2eContext{svc: s, editor: editor}
}

// requestID names one operation of this editor. Every request carries one, so a
// retry replays instead of repeating and two devices cannot collide: the editor
// prefix is what keeps the sequences apart.
func (c *e2eContext) requestID(kind string) string {
	c.seq++
	return fmt.Sprintf("%s-%s-%d", c.editor, kind, c.seq)
}

func (c *e2eContext) create(content WorkingDraftContent) WorkingDraft {
	c.svc.t.Helper()
	status, raw := c.svc.request(http.MethodPost, "/api/factory/working-drafts", workingDraftCreateBody{
		ProjectRoot: c.svc.root, RequestID: c.requestID("create"), Content: content,
	})
	if status != http.StatusCreated {
		c.svc.t.Fatalf("create = %d: %s", status, raw)
	}
	var body struct {
		Draft WorkingDraft `json:"draft"`
	}
	e2eDecode(c.svc.t, raw, &body)
	return body.Draft
}

// e2eSaveResult is one conditional save: its status, the error code the server
// chose, and the draft the response carried (the new one, or the current one on
// a conflict).
type e2eSaveResult struct {
	status int
	code   string
	draft  WorkingDraft
	raw    []byte
}

func (c *e2eContext) save(id string, expectedVersion uint64, content WorkingDraftContent) e2eSaveResult {
	c.svc.t.Helper()
	status, raw := c.svc.request(http.MethodPut, "/api/factory/working-drafts/"+id, workingDraftSaveBody{
		RequestID: c.requestID("save"), ExpectedVersion: expectedVersion, Content: content,
	})
	var body struct {
		Code  string       `json:"code"`
		Draft WorkingDraft `json:"draft"`
	}
	e2eDecode(c.svc.t, raw, &body)
	return e2eSaveResult{status: status, code: body.Code, draft: body.Draft, raw: raw}
}

// e2eConversion is one conversion request: its status, the uncertain flag, the
// draft it reported and the item it created, if any.
type e2eConversion struct {
	status    int
	uncertain bool
	draft     WorkingDraft
	item      *FactoryItem
	requestID string
	raw       []byte
}

func (c *e2eContext) convert(id string, expectedVersion uint64, settingsVersion string) e2eConversion {
	c.svc.t.Helper()
	requestID := c.requestID("convert")
	status, raw := c.svc.request(http.MethodPost, "/api/factory/working-drafts/"+id+"/convert", workingDraftConvertBody{
		// An omitted factory request is the caller accepting the server's own
		// reading of the reviewed content.
		RequestID: requestID, ExpectedVersion: expectedVersion, SettingsVersion: settingsVersion,
	})
	var body struct {
		Uncertain bool         `json:"uncertain"`
		Draft     WorkingDraft `json:"draft"`
		Item      *FactoryItem `json:"item"`
	}
	e2eDecode(c.svc.t, raw, &body)
	return e2eConversion{status: status, uncertain: body.Uncertain, draft: body.Draft, item: body.Item, requestID: requestID, raw: raw}
}

func (c *e2eContext) reload(id string) WorkingDraft {
	c.svc.t.Helper()
	status, raw := c.svc.request(http.MethodGet, "/api/factory/working-drafts/"+id, nil)
	if status != http.StatusOK {
		c.svc.t.Fatalf("reload %s = %d: %s", id, status, raw)
	}
	var body struct {
		Draft WorkingDraft `json:"draft"`
	}
	e2eDecode(c.svc.t, raw, &body)
	return body.Draft
}

func (c *e2eContext) list() []WorkingDraft {
	c.svc.t.Helper()
	status, raw := c.svc.request(http.MethodGet, "/api/factory/working-drafts?projectRoot="+url.QueryEscape(c.svc.root), nil)
	if status != http.StatusOK {
		c.svc.t.Fatalf("list = %d: %s", status, raw)
	}
	var body struct {
		Drafts []WorkingDraft `json:"drafts"`
	}
	e2eDecode(c.svc.t, raw, &body)
	// Sorted by draft id, so the assertion never depends on map iteration or on
	// the shared test clock making every timestamp equal.
	sort.Slice(body.Drafts, func(i, j int) bool { return body.Drafts[i].ID < body.Drafts[j].ID })
	return body.Drafts
}

func e2eDecode(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

// assertUnapprovedFactoryDraft requires a factory draft that nothing has
// approved, started or run: a conversion creates a reviewable draft and stops.
func assertUnapprovedFactoryDraft(t *testing.T, item FactoryItem) {
	t.Helper()
	if item.State != "draft" {
		t.Fatalf("factory item state %q, want draft", item.State)
	}
	if len(item.Approvals) != 0 || len(item.Rounds) != 0 || len(item.Workers) != 0 ||
		len(item.Effects) != 0 || len(item.PRs) != 0 || len(item.Grants) != 0 || item.Decision != nil {
		t.Fatalf("the conversion started work: %+v", item)
	}
	if !item.ApprovedAt.IsZero() {
		t.Fatalf("the conversion approved the item at %v", item.ApprovedAt)
	}
}

// e2eReviewedContent is a complete manual handoff as the editor holds it: the
// project's repo, a native run, an explicit no-required-checks choice and both
// role profiles chosen.
func e2eReviewedContent(title string) WorkingDraftContent {
	c := workingDraftTestContent(title)
	c.Native = true
	c.NativeChosen = true
	c.ChecksMode = workingDraftChecksNone
	c.NoRequiredCIAcknowledged = true
	c.ImplementerOverride = "chosen"
	c.ReviewerOverride = "chosen"
	return c
}

// TestWorkingDraftEndToEnd is the cross-device flow: two editors, one draft,
// a conflict resolved by a field-by-field merge, a conversion into exactly one
// unapproved factory draft, and a reload after a server restart.
func TestWorkingDraftEndToEnd(t *testing.T) {
	svc := newE2EService(t)
	a := svc.editor("context-a")
	b := svc.editor("context-b")
	if a.editor == b.editor {
		t.Fatal("the two contexts must have distinct editor identities")
	}

	// Context A creates the draft.
	created := a.create(e2eReviewedContent("Ship the reviewed draft"))
	if created.State != workingDraftEditing || created.Version != 1 || created.ProjectRoot != svc.root {
		t.Fatalf("created draft = %+v", created)
	}

	// A saves twice: the plan first, then the spec.
	first := created.Content
	first.Plan = "context A: implement the reviewed plan"
	saved := a.save(created.ID, 1, first)
	if saved.status != http.StatusOK || saved.draft.Version != 2 || saved.draft.Content != first {
		t.Fatalf("first save = %d %+v: %s", saved.status, saved.draft, saved.raw)
	}
	second := first
	second.Spec = "context A: notes for the reviewer"
	saved = a.save(created.ID, 2, second)
	if saved.status != http.StatusOK || saved.draft.Version != 3 || saved.draft.Content != second {
		t.Fatalf("second save = %d %+v: %s", saved.status, saved.draft, saved.raw)
	}

	// Context B lists the project and finds the draft at version 3.
	drafts := b.list()
	if len(drafts) != 1 || drafts[0].ID != created.ID || drafts[0].Version != 3 || drafts[0].Content != second {
		t.Fatalf("project list = %+v, want only %s at version 3", drafts, created.ID)
	}

	// B's own copy was version 1. Saving against it is a conflict that carries
	// the current draft and mutates nothing.
	stale := b.save(created.ID, 1, e2eReviewedContent("context B: a different rewrite"))
	if stale.status != http.StatusConflict || stale.code != "conflict" {
		t.Fatalf("stale save = %d %q: %s", stale.status, stale.code, stale.raw)
	}
	if stale.draft.ID != created.ID || stale.draft.Version != 3 || stale.draft.Content != second {
		t.Fatalf("conflict carried %+v, want the current draft at version 3", stale.draft)
	}
	if got := b.reload(created.ID); got.Version != 3 || got.Content != second {
		t.Fatalf("a conflicted save mutated the draft: %+v", got)
	}

	// B merges field by field: A's title, plan and checks stand, and B adds the
	// spec, its reviewer instructions and a CI poll override.
	merged := stale.draft.Content
	merged.Spec = "context B: spec merged from both devices"
	merged.ReviewerInstructions = "context B: read the plan before the diff"
	merged.CIPollOverride = "30"
	saved = b.save(created.ID, stale.draft.Version, merged)
	if saved.status != http.StatusOK || saved.draft.Version != 4 {
		t.Fatalf("merged save = %d %+v: %s", saved.status, saved.draft, saved.raw)
	}
	if saved.draft.Content != merged {
		t.Fatalf("merged draft = %+v, want %+v", saved.draft.Content, merged)
	}

	// B converts the exact reviewed version. The factory request is the
	// server's own reading: the body names no request.
	conversion := b.convert(created.ID, saved.draft.Version, svc.settings.token)
	if conversion.status != http.StatusOK {
		t.Fatalf("convert = %d: %s", conversion.status, conversion.raw)
	}
	if conversion.item == nil {
		t.Fatalf("convert returned no item: %s", conversion.raw)
	}
	item := *conversion.item
	assertUnapprovedFactoryDraft(t, item)
	if conversion.draft.State != workingDraftConverted || conversion.draft.FactoryItemID != item.ID {
		t.Fatalf("converted draft = %+v", conversion.draft)
	}
	if conversion.draft.Version != saved.draft.Version+2 {
		t.Fatalf("converted draft version = %d, want %d", conversion.draft.Version, saved.draft.Version+2)
	}

	// The item carries the merged, reviewed values, resolved once by the
	// preflight: the overrides that were stated and the defaults that were not.
	if item.ProjectRoot != svc.root || item.RepoID != merged.RepoID {
		t.Fatalf("item landed in another project: %+v", item)
	}
	if item.Title != merged.Title || item.Draft.Plan != merged.Plan || item.Draft.Spec != merged.Spec {
		t.Fatalf("item lost the merged content: %+v", item.Draft)
	}
	config := item.Draft.Config
	if config.ReviewerInstructions != merged.ReviewerInstructions {
		t.Fatalf("reviewer instructions = %q, want %q", config.ReviewerInstructions, merged.ReviewerInstructions)
	}
	if config.CIPollSeconds != 30 {
		t.Fatalf("CI poll = %d, want the merged override 30", config.CIPollSeconds)
	}
	if config.TargetBranch != "main" || config.MaxRounds != 9 {
		t.Fatalf("preflight did not resolve the reviewed request: %+v", config)
	}
	// The factory request identity is the server's own derivation, not anything
	// the client sent.
	if want := derivedFactoryRequestID(created.ID, conversion.requestID); item.Draft.RequestID != want {
		t.Fatalf("factory request id %q, want %q", item.Draft.RequestID, want)
	}
	if n := svc.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items, want 1", n)
	}

	// A fresh read returns the converted draft with its factory item.
	reloaded := b.reload(created.ID)
	if reloaded.State != workingDraftConverted || reloaded.FactoryItemID != item.ID {
		t.Fatalf("reloaded draft = %+v", reloaded)
	}
	if reloaded.Version != conversion.draft.Version || reloaded.Content != merged {
		t.Fatalf("reloaded draft = %+v, want version %d with the merged content", reloaded, conversion.draft.Version)
	}

	// The installation identity is stable across two reads and across a server
	// restart over the same state directory.
	identity := svc.installationID()
	if again := svc.installationID(); again != identity {
		t.Fatalf("installation id changed between reads: %q then %q", identity, again)
	}

	svc.close()
	svc.open()
	svc.reconcile()

	if after := svc.installationID(); after != identity {
		t.Fatalf("installation id changed across a restart: %q then %q", identity, after)
	}
	// Reconciliation after a completed conversion is a no-op: the same draft,
	// the same item, nothing new created.
	restarted := b.reload(created.ID)
	if restarted.State != workingDraftConverted || restarted.FactoryItemID != item.ID || restarted.Version != reloaded.Version {
		t.Fatalf("draft after a restart = %+v", restarted)
	}
	if restarted.Content != merged {
		t.Fatalf("draft lost its merged content across a restart: %+v", restarted.Content)
	}
	if n := svc.factory.itemCount(); n != 1 {
		t.Fatalf("the restart created %d items", n)
	}
}

// TestWorkingDraftEndToEndRecovery restarts the bridge at each persistence
// boundary of one conversion. The real draft journal is the evidence: whatever
// a crash left behind, reconciliation finishes exactly one conversion into
// exactly one item and never a second one.
func TestWorkingDraftEndToEndRecovery(t *testing.T) {
	const conversionRequestID = "convert-1"

	// newRecovery is the state every boundary starts from: one running bridge
	// and one editing draft created over HTTP.
	newRecovery := func(t *testing.T) (*e2eService, *e2eContext, WorkingDraft) {
		t.Helper()
		svc := newE2EService(t)
		editor := svc.editor("recovery")
		return svc, editor, editor.create(e2eReviewedContent("Recover the reviewed draft"))
	}

	t.Run("before-intent", func(t *testing.T) {
		svc, editor, draft := newRecovery(t)

		// Nothing was reserved, so the restart has nothing to finish and the
		// draft is untouched.
		svc.close()
		svc.open()
		svc.reconcile()

		if got := editor.reload(draft.ID); got.State != workingDraftEditing || got.Version != draft.Version || got.FactoryItemID != "" {
			t.Fatalf("draft after a pre-intent restart = %+v", got)
		}
		if n := svc.factory.itemCount(); n != 0 {
			t.Fatalf("a pre-intent restart created %d items", n)
		}
		// Nothing was lost either: the same draft still converts into exactly
		// one item.
		conversion := editor.convert(draft.ID, draft.Version, svc.settings.token)
		if conversion.status != http.StatusOK || conversion.item == nil {
			t.Fatalf("convert after a pre-intent restart = %d: %s", conversion.status, conversion.raw)
		}
		item := svc.oneItem()
		if item.ID != conversion.item.ID {
			t.Fatalf("converted item %s, journal holds %s", conversion.item.ID, item.ID)
		}
	})

	t.Run("after-intent", func(t *testing.T) {
		svc, editor, draft := newRecovery(t)
		svc.reserveIntent(draft, conversionRequestID)

		// The intent is durable and the factory was never reached.
		if got := editor.reload(draft.ID); got.State != workingDraftConverting || got.Version != draft.Version+1 {
			t.Fatalf("draft after the reserve = %+v", got)
		}
		if n := svc.factory.itemCount(); n != 0 {
			t.Fatalf("the reserve created %d items", n)
		}

		svc.close()
		svc.open()
		svc.reconcile()

		item := svc.oneItem()
		if want := derivedFactoryRequestID(draft.ID, conversionRequestID); item.Draft.RequestID != want {
			t.Fatalf("factory request id %q, want the reserved %q", item.Draft.RequestID, want)
		}
		got := editor.reload(draft.ID)
		if got.State != workingDraftConverted || got.FactoryItemID != item.ID || got.Version != draft.Version+2 {
			t.Fatalf("draft after reconciling the intent = %+v", got)
		}
	})

	t.Run("after-factory-append", func(t *testing.T) {
		svc, editor, draft := newRecovery(t)
		frozen := svc.reserveIntent(draft, conversionRequestID)

		// The factory journaled the request; the association never ran, so the
		// draft is still converting and knows no item.
		journaled, err := svc.factory.CreateFrozen(frozen, frozen)
		if err != nil {
			t.Fatalf("journal the factory request: %v", err)
		}
		if got := editor.reload(draft.ID); got.State != workingDraftConverting || got.FactoryItemID != "" {
			t.Fatalf("draft before the association = %+v", got)
		}

		svc.close()
		svc.open()
		svc.reconcile()

		// The same item, never a second one: the frozen request is what
		// reconciliation replays.
		item := svc.oneItem()
		if item.ID != journaled.ID {
			t.Fatalf("reconciliation created a second item: %s then %s", journaled.ID, item.ID)
		}
		got := editor.reload(draft.ID)
		if got.State != workingDraftConverted || got.FactoryItemID != journaled.ID {
			t.Fatalf("draft after reconciling the append = %+v", got)
		}
	})

	t.Run("after-association", func(t *testing.T) {
		svc, editor, draft := newRecovery(t)
		conversion := editor.convert(draft.ID, draft.Version, svc.settings.token)
		if conversion.status != http.StatusOK || conversion.item == nil {
			t.Fatalf("convert = %d: %s", conversion.status, conversion.raw)
		}

		svc.close()
		svc.open()
		// Reconciliation of a finished conversion is a no-op, not a second
		// conversion of a converted draft.
		svc.reconcile()

		item := svc.oneItem()
		if item.ID != conversion.item.ID {
			t.Fatalf("reconciliation created a second item: %s then %s", conversion.item.ID, item.ID)
		}
		got := editor.reload(draft.ID)
		if got.State != workingDraftConverted || got.FactoryItemID != conversion.item.ID || got.Version != conversion.draft.Version {
			t.Fatalf("draft after the restart = %+v", got)
		}
	})

	t.Run("indeterminate-outcome", func(t *testing.T) {
		svc, editor, draft := newRecovery(t)
		svc.reserveIntent(draft, conversionRequestID)

		// The factory may have accepted the request and lost the answer, so a
		// reconciliation cannot establish the outcome. It must say so and leave
		// the intent reserved rather than guess or repeat the request.
		svc.factory.failCreate(ErrFactoryUnavailable, false)
		svc.close()
		svc.open()
		if err := svc.conversion.Reconcile(context.Background()); !errors.Is(err, ErrWorkingDraftUncertain) {
			t.Fatalf("reconcile with an unreadable factory = %v, want an uncertain outcome", err)
		}

		if got := editor.reload(draft.ID); got.State != workingDraftConverting || got.FactoryItemID != "" {
			t.Fatalf("an uncertain reconciliation changed the draft: %+v", got)
		}
		if n := svc.factory.itemCount(); n != 0 {
			t.Fatalf("an uncertain reconciliation created %d items", n)
		}
		pending, err := svc.store.convertingDrafts()
		if err != nil || len(pending) != 1 || pending[0].RequestID != conversionRequestID {
			t.Fatalf("the reservation was not kept for a later pass: %v %+v", err, pending)
		}

		// The factory recovers, and the same reservation finishes with exactly
		// one item — no duplicate, no second approval.
		svc.factory.failCreate(nil, false)
		svc.reconcile()

		item := svc.oneItem()
		got := editor.reload(draft.ID)
		if got.State != workingDraftConverted || got.FactoryItemID != item.ID {
			t.Fatalf("draft after the recovered reconciliation = %+v", got)
		}
	})
}
