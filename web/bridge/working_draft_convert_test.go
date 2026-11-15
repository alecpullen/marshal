package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSettingsVersion is the project-settings version seam with one token a
// test can move, so a conversion can be raced against a settings write.
type fakeSettingsVersion struct {
	mu    sync.Mutex
	token string
	err   error
}

func (f *fakeSettingsVersion) ProjectSettingsETag(root string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, f.err
}

func (f *fakeSettingsVersion) set(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = token
}

// fakeFrozenFactory is the factory seam with a journal of its own, so every
// persistence boundary can fail on purpose: before the request is journaled (a
// decided refusal), after it is journaled (a lost response), and before the
// response reaches the converter. It resolves defaults exactly once, in
// PrepareFrozen, and never in CreateFrozen, which is the property the frozen
// entry point exists for.
type fakeFrozenFactory struct {
	settings *fakeSettingsVersion

	mu       sync.Mutex
	defaults *FactoryDefaults
	journal  map[string]FactoryItem
	items    []FactoryItem
	prepares int
	creates  int
	requests []FactoryCreate

	prepareErr error
	createErr  error
	// journalBeforeError journals the item before reporting createErr: the
	// factory accepted the request and the caller never learned the answer.
	journalBeforeError bool
	// prepareHook runs after a successful preflight, before the converter
	// reserves its intent.
	prepareHook func()
}

func newFakeFrozenFactory(settings *fakeSettingsVersion) *fakeFrozenFactory {
	return &fakeFrozenFactory{
		settings: settings,
		defaults: &FactoryDefaults{TargetBranch: "release", MaxRounds: 9, CIPollSeconds: 15, CIWaitSeconds: 1800},
		journal:  map[string]FactoryItem{},
	}
}

func (f *fakeFrozenFactory) PrepareFrozen(req FactoryCreate) (FactoryCreate, string, error) {
	f.mu.Lock()
	f.prepares++
	hook := f.prepareHook
	resolved, token, err := f.prepareLocked(req)
	f.mu.Unlock()
	if err == nil && hook != nil {
		hook()
	}
	return resolved, token, err
}

func (f *fakeFrozenFactory) prepareLocked(req FactoryCreate) (FactoryCreate, string, error) {
	if f.prepareErr != nil {
		return FactoryCreate{}, "", f.prepareErr
	}
	resolved := req
	resolved.Config = factoryMergeDefaults(resolved.Config, f.defaults)
	if err := factoryNormalize(&resolved); err != nil {
		return FactoryCreate{}, "", fmt.Errorf("%w: %v", errFactoryInput, err)
	}
	token, err := f.settings.ProjectSettingsETag(resolved.Config.ProjectRoot)
	if err != nil {
		return FactoryCreate{}, "", err
	}
	return resolved, token, nil
}

func (f *fakeFrozenFactory) CreateFrozen(req, resolved FactoryCreate) (FactoryItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.requests = append(f.requests, resolved)
	// The journal is consulted first, exactly as the real factory does: a retry
	// after a crash must return the item the frozen request already produced.
	if item, ok := f.journal[req.RequestID]; ok {
		return item, nil
	}
	if f.createErr != nil && !f.journalBeforeError {
		return FactoryItem{}, f.createErr
	}
	// Nothing is inherited here: an empty field stays empty, which is what
	// makes a frozen request replayable after the project's defaults moved on.
	if err := factoryNormalize(&resolved); err != nil {
		return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
	}
	item := FactoryItem{
		ID: fmt.Sprintf("item-%d", len(f.items)+1), Title: resolved.Title, State: "draft", Version: 1,
		Draft: resolved, RepoID: resolved.Config.RepoID, ProjectRoot: resolved.Config.ProjectRoot,
		HostID: resolved.Config.HostID, CreatedAt: workingDraftTestTime, UpdatedAt: workingDraftTestTime,
	}
	f.items = append(f.items, item)
	f.journal[req.RequestID] = item
	if f.createErr != nil {
		return FactoryItem{}, f.createErr
	}
	return item, nil
}

func (f *fakeFrozenFactory) failCreate(err error, journalFirst bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
	f.journalBeforeError = journalFirst
}

func (f *fakeFrozenFactory) failPrepare(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepareErr = err
}

func (f *fakeFrozenFactory) setDefaults(d FactoryDefaults) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaults = &d
}

func (f *fakeFrozenFactory) itemCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

func (f *fakeFrozenFactory) item(id string) FactoryItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, i := range f.items {
		if i.ID == id {
			return i
		}
	}
	return FactoryItem{}
}

func (f *fakeFrozenFactory) calls() (prepares, creates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prepares, f.creates
}

// convertFixture is one draft store, one fake factory and one converter, with a
// restart that reopens the store from its journal the way a new bridge would.
type convertFixture struct {
	t         *testing.T
	dir       string
	root      string
	settings  *fakeSettingsVersion
	factory   *fakeFrozenFactory
	store     *WorkingDraftStore
	converter *workingDraftConverter
}

func newConvertFixture(t *testing.T) *convertFixture {
	t.Helper()
	settings := &fakeSettingsVersion{token: `"settings-1"`}
	f := &convertFixture{
		t:        t,
		dir:      filepath.Join(t.TempDir(), "working-drafts"),
		root:     "/projects/alpha",
		settings: settings,
		factory:  newFakeFrozenFactory(settings),
	}
	f.open()
	return f
}

func (f *convertFixture) open() {
	f.t.Helper()
	s, err := OpenWorkingDraftStore(f.dir, workingDraftTestClock())
	if err != nil {
		f.t.Fatalf("open working draft store: %v", err)
	}
	f.store = s
	f.converter = newWorkingDraftConverter(s, f.factory, f.settings)
	f.t.Cleanup(func() { _ = f.store.Close() })
}

// restart simulates a bridge crash: the draft store is closed and reopened from
// its journal, while the factory keeps the journal it already has.
func (f *convertFixture) restart() {
	f.t.Helper()
	if err := f.store.Close(); err != nil {
		f.t.Fatalf("close working draft store: %v", err)
	}
	f.open()
}

func (f *convertFixture) createDraft(content WorkingDraftContent) WorkingDraft {
	f.t.Helper()
	d, err := f.store.Create(f.root, "create-draft", content)
	if err != nil {
		f.t.Fatalf("create draft: %v", err)
	}
	return d
}

// convertTestContent is a fully reviewed draft: what the editor holds when the
// operator confirms the handoff.
func convertTestContent() WorkingDraftContent {
	return WorkingDraftContent{
		Title: "Task", Plan: "do the work", Spec: "notes", RepoID: "repo-1",
		TargetBranchOverride: "main", ImplementerOverride: "chosen", ReviewerOverride: "chosen",
		Native: true, ChecksMode: workingDraftChecksNone, NoRequiredCIAcknowledged: true,
	}
}

func TestWorkingDraftConvertCreatesUnapprovedFactoryDraft(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())

	converted, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, "")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if item == nil {
		t.Fatal("convert created no factory item")
	}
	if converted.State != workingDraftConverted || converted.FactoryItemID != item.ID || converted.Version != draft.Version+2 {
		t.Fatalf("draft after conversion: %+v", converted)
	}

	// An unapproved factory draft: nothing is approved, started or run.
	if item.State != "draft" {
		t.Fatalf("factory item state %q", item.State)
	}
	if len(item.Approvals) != 0 || len(item.Rounds) != 0 || len(item.Workers) != 0 || len(item.Effects) != 0 || len(item.PRs) != 0 || item.Decision != nil {
		t.Fatalf("conversion started work: %+v", item)
	}
	if !item.ApprovedAt.IsZero() {
		t.Fatalf("conversion approved the item at %v", item.ApprovedAt)
	}

	// The factory received the reviewed content under the derived request ID.
	wantRequestID := derivedFactoryRequestID(draft.ID, "convert-1")
	if item.Draft.RequestID != wantRequestID {
		t.Fatalf("factory request id %q, want %q", item.Draft.RequestID, wantRequestID)
	}
	if item.Title != draft.Content.Title || item.Draft.Plan != draft.Content.Plan || item.Draft.Spec != draft.Content.Spec {
		t.Fatalf("factory item lost the reviewed content: %+v", item.Draft)
	}
	if item.ProjectRoot != f.root || item.RepoID != draft.Content.RepoID {
		t.Fatalf("factory item landed in another project: %+v", item)
	}
	if item.Draft.Config.TargetBranch != "main" || item.Draft.Config.MaxRounds != 9 || !item.Draft.Config.Native {
		t.Fatalf("factory item was not resolved: %+v", item.Draft.Config)
	}
	if item.Draft.Config.ExpectedChecks == nil || len(item.Draft.Config.ExpectedChecks) != 0 {
		t.Fatalf("the explicit no-required-checks choice did not survive: %+v", item.Draft.Config.ExpectedChecks)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items", n)
	}
	if prepares, creates := f.factory.calls(); prepares != 1 || creates != 1 {
		t.Fatalf("factory was called %d/%d times", prepares, creates)
	}

	// The frozen request stays in the journals: it is not exposed on the draft.
	raw, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	if strings.Contains(string(raw), wantRequestID) {
		t.Fatalf("the frozen factory request is exposed on the draft: %s", raw)
	}
}

func TestWorkingDraftConvertConcurrentOneItem(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())

	const callers = 8
	type outcome struct {
		draft WorkingDraft
		item  *FactoryItem
		err   error
	}
	results := make([]outcome, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n := 0; n < callers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			d, item, err := f.converter.Convert(context.Background(), draft.ID, fmt.Sprintf("convert-%d", n), draft.Version, FactoryCreate{}, "")
			results[n] = outcome{d, item, err}
		}(n)
	}
	close(start)
	wg.Wait()

	converted := 0
	for n, r := range results {
		switch {
		case r.err == nil:
			converted++
			if r.item == nil || r.draft.State != workingDraftConverted || r.draft.FactoryItemID != r.item.ID {
				t.Fatalf("caller %d converted to %+v", n, r.draft)
			}
		case errors.Is(r.err, ErrWorkingDraftConflict):
			if r.item != nil {
				t.Fatalf("caller %d lost with an item: %+v", n, r.item)
			}
		default:
			t.Fatalf("caller %d: %v", n, r.err)
		}
	}
	if converted != 1 {
		t.Fatalf("%d callers converted one draft", converted)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items", n)
	}
	stored, err := f.store.Get(draft.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.State != workingDraftConverted || stored.Version != draft.Version+2 || stored.FactoryItemID == "" {
		t.Fatalf("draft after concurrent conversions: %+v", stored)
	}
	if pending, err := f.store.convertingDrafts(); err != nil || len(pending) != 0 {
		t.Fatalf("conversions left reserved: %v %+v", err, pending)
	}
}

func TestWorkingDraftConvertLostResponse(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())
	// The factory journaled the item and the response never arrived.
	f.factory.failCreate(errors.New("connection reset after the request was accepted"), true)

	reserved, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, "")
	if !errors.Is(err, ErrWorkingDraftUncertain) {
		t.Fatalf("lost response: %v", err)
	}
	if item != nil {
		t.Fatalf("an uncertain outcome claimed an item: %+v", item)
	}
	if reserved.State != workingDraftConverting || reserved.Version != draft.Version+1 {
		t.Fatalf("draft after a lost response: %+v", reserved)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items", n)
	}

	// The project's settings move on while the outcome is unknown; the frozen
	// request is still what completes the conversion.
	f.factory.setDefaults(FactoryDefaults{TargetBranch: "changed", MaxRounds: 3})
	f.factory.failCreate(nil, false)
	f.settings.set(`"settings-2"`)

	if err := f.converter.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	done, err := f.store.Get(draft.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if done.State != workingDraftConverted || done.FactoryItemID != "item-1" {
		t.Fatalf("reconciled draft: %+v", done)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("reconciliation created a second item: %d", n)
	}
	if _, creates := f.factory.calls(); creates != 2 {
		t.Fatalf("reconciliation did not replay the frozen request: %d creates", creates)
	}
	found := f.factory.item("item-1")
	if found.Draft.RequestID != derivedFactoryRequestID(draft.ID, "convert-1") {
		t.Fatalf("the item carries %q", found.Draft.RequestID)
	}
	if found.Draft.Config.MaxRounds != 9 {
		t.Fatalf("the frozen request was re-resolved: %+v", found.Draft.Config)
	}
}

func TestWorkingDraftConvertRestartAfterIntent(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())
	// The intent is reserved and the factory was never reached.
	f.factory.failCreate(ErrFactoryUnavailable, false)
	if _, _, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, ""); !errors.Is(err, ErrWorkingDraftUncertain) {
		t.Fatalf("convert with the factory down: %v", err)
	}
	if n := f.factory.itemCount(); n != 0 {
		t.Fatalf("the unavailable factory created %d items", n)
	}
	if pending, err := f.store.convertingDrafts(); err != nil || len(pending) != 1 || pending[0].RequestID != "convert-1" {
		t.Fatalf("the reservation was not durable: %v %+v", err, pending)
	}

	// A crash, and the project's settings changed while the bridge was down.
	f.factory.failCreate(nil, false)
	f.factory.setDefaults(FactoryDefaults{TargetBranch: "changed", MaxRounds: 3})
	f.settings.set(`"settings-2"`)
	f.restart()

	if err := f.converter.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	done, err := f.store.Get(draft.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if done.State != workingDraftConverted || done.FactoryItemID != "item-1" || done.Version != draft.Version+2 {
		t.Fatalf("reconciled draft: %+v", done)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("reconciliation created %d items", n)
	}
	item := f.factory.item("item-1")
	if item.Draft.RequestID != derivedFactoryRequestID(draft.ID, "convert-1") {
		t.Fatalf("the frozen factory request id was lost: %q", item.Draft.RequestID)
	}
	// The persisted values, not the new defaults, produced the item.
	if item.Draft.Config.MaxRounds != 9 || item.Draft.Config.TargetBranch != "main" {
		t.Fatalf("reconciliation re-resolved the request: %+v", item.Draft.Config)
	}
	if pending, err := f.store.convertingDrafts(); err != nil || len(pending) != 0 {
		t.Fatalf("the conversion stayed reserved: %v %+v", err, pending)
	}
}

func TestWorkingDraftConvertRestartAfterFactoryAppend(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())
	// The factory accepted and journaled the request; the bridge never heard.
	f.factory.failCreate(errors.New("timeout waiting for the factory"), true)
	if _, _, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, ""); !errors.Is(err, ErrWorkingDraftUncertain) {
		t.Fatalf("convert with a lost response: %v", err)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items", n)
	}

	f.factory.failCreate(nil, false)
	f.restart()

	if err := f.converter.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	done, err := f.store.Get(draft.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if done.State != workingDraftConverted || done.FactoryItemID != "item-1" {
		t.Fatalf("reconciled draft: %+v", done)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("reconciliation duplicated the factory item: %d", n)
	}
	if _, creates := f.factory.calls(); creates != 2 {
		t.Fatalf("reconciliation did not replay through the journal: %d creates", creates)
	}
}

func TestWorkingDraftConvertRestartAfterAssociation(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())
	converted, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, "")
	if err != nil || item == nil {
		t.Fatalf("convert: %v %+v", err, item)
	}
	_, createsBefore := f.factory.calls()

	f.restart()

	if err := f.converter.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, err := f.store.Get(draft.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != workingDraftConverted || got.FactoryItemID != item.ID || got.Version != converted.Version {
		t.Fatalf("reconciliation disturbed the associated draft: %+v", got)
	}
	if _, creates := f.factory.calls(); creates != createsBefore {
		t.Fatalf("reconciliation created another item: %d creates", creates)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items", n)
	}
}

func TestWorkingDraftConvertPreflightFailure(t *testing.T) {
	t.Run("draft-content-refusal", func(t *testing.T) {
		f := newConvertFixture(t)
		content := convertTestContent()
		content.ChecksMode = workingDraftChecksCustom
		content.ExpectedChecksText = "linux-build: pending"
		draft := f.createDraft(content)

		if _, _, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, ""); !errors.Is(err, errFactoryInput) {
			t.Fatalf("unparseable checks: %v", err)
		}
		if prepares, creates := f.factory.calls(); prepares != 0 || creates != 0 {
			t.Fatalf("a refused draft reached the factory: %d/%d", prepares, creates)
		}
		got, err := f.store.Get(draft.ID)
		if err != nil || got.State != workingDraftEditing || got.Version != draft.Version {
			t.Fatalf("a refused draft changed: %v %+v", err, got)
		}
	})

	t.Run("preflight-refusal", func(t *testing.T) {
		f := newConvertFixture(t)
		draft := f.createDraft(convertTestContent())
		f.factory.failPrepare(fmt.Errorf("%w: trusted registered project required", errFactoryConfig))

		if _, _, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, ""); !errors.Is(err, errFactoryConfig) {
			t.Fatalf("preflight refusal: %v", err)
		}
		if _, creates := f.factory.calls(); creates != 0 {
			t.Fatalf("a refused preflight created an item")
		}
		got, err := f.store.Get(draft.ID)
		if err != nil || got.State != workingDraftEditing || got.Version != draft.Version || got.Error != "" {
			t.Fatalf("a refused preflight changed the draft: %v %+v", err, got)
		}
	})

	t.Run("confirmed-create-refusal", func(t *testing.T) {
		f := newConvertFixture(t)
		draft := f.createDraft(convertTestContent())
		f.factory.failCreate(fmt.Errorf("%w: invalid check identity", errFactoryInput), false)

		failed, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, "")
		if !errors.Is(err, errFactoryInput) || errors.Is(err, ErrWorkingDraftUncertain) {
			t.Fatalf("confirmed refusal: %v", err)
		}
		if item != nil {
			t.Fatalf("a refused conversion claimed an item: %+v", item)
		}
		// The draft is editable again, carries the reason and has a new version,
		// and no factory item exists.
		if failed.State != workingDraftEditing || failed.Error == "" || failed.Version != draft.Version+2 || failed.FactoryItemID != "" {
			t.Fatalf("draft after a confirmed refusal: %+v", failed)
		}
		if n := f.factory.itemCount(); n != 0 {
			t.Fatalf("a confirmed refusal created %d items", n)
		}
		if pending, err := f.store.convertingDrafts(); err != nil || len(pending) != 0 {
			t.Fatalf("a confirmed refusal left a reservation: %v %+v", err, pending)
		}

		// A subsequent conversion is a new one: a new request ID and a new
		// derived factory request.
		f.factory.failCreate(nil, false)
		converted, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-2", failed.Version, FactoryCreate{}, "")
		if err != nil || item == nil || converted.State != workingDraftConverted {
			t.Fatalf("conversion after a refusal: %v %+v", err, converted)
		}
		second := derivedFactoryRequestID(draft.ID, "convert-2")
		if item.Draft.RequestID != second || second == derivedFactoryRequestID(draft.ID, "convert-1") {
			t.Fatalf("the retried conversion reused a factory request id: %q", item.Draft.RequestID)
		}
		if pending, err := f.store.convertingDrafts(); err != nil || len(pending) != 0 {
			t.Fatalf("conversions left reserved: %v %+v", err, pending)
		}
	})
}

func TestWorkingDraftConvertFactoryUnavailable(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())
	f.factory.failCreate(ErrFactoryUnavailable, false)

	reserved, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, "")
	if !errors.Is(err, ErrWorkingDraftUncertain) || errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("unavailable factory: %v", err)
	}
	if item != nil {
		t.Fatalf("an unavailable factory produced an item: %+v", item)
	}
	if reserved.State != workingDraftConverting || reserved.Version != draft.Version+1 {
		t.Fatalf("the draft did not stay reserved: %+v", reserved)
	}
	if n := f.factory.itemCount(); n != 0 {
		t.Fatalf("an unavailable factory journaled %d items", n)
	}

	f.factory.failCreate(nil, false)
	if err := f.converter.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	done, err := f.store.Get(draft.ID)
	if err != nil || done.State != workingDraftConverted || done.FactoryItemID != "item-1" {
		t.Fatalf("reconciled draft: %v %+v", err, done)
	}
	if n := f.factory.itemCount(); n != 1 {
		t.Fatalf("factory journal holds %d items", n)
	}
}

func TestWorkingDraftConvertChangedSettingsRequiresReview(t *testing.T) {
	t.Run("reviewed-against-an-older-version", func(t *testing.T) {
		f := newConvertFixture(t)
		draft := f.createDraft(convertTestContent())

		_, _, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, `"settings-0"`)
		if !errors.Is(err, ErrWorkingDraftConflict) {
			t.Fatalf("stale settings version: %v", err)
		}
		if _, creates := f.factory.calls(); creates != 0 {
			t.Fatal("a stale review created a factory item")
		}
		got, err := f.store.Get(draft.ID)
		if err != nil || got.State != workingDraftEditing || got.Version != draft.Version {
			t.Fatalf("a stale review changed the draft: %v %+v", err, got)
		}
	})

	t.Run("settings-written-while-reserving", func(t *testing.T) {
		f := newConvertFixture(t)
		draft := f.createDraft(convertTestContent())
		// The settings write lands after preflight read the version and before
		// the intent is reserved.
		f.factory.prepareHook = func() { f.settings.set(`"settings-2"`) }

		reserved, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, FactoryCreate{}, "")
		if !errors.Is(err, ErrWorkingDraftConflict) {
			t.Fatalf("settings changed during the reserve: %v", err)
		}
		if item != nil {
			t.Fatalf("a conflicted conversion produced an item: %+v", item)
		}
		if reserved.State != workingDraftConverting || reserved.Version != draft.Version+1 {
			t.Fatalf("the reservation is not durable: %+v", reserved)
		}
		if _, creates := f.factory.calls(); creates != 0 {
			t.Fatal("a conflicted conversion reached the factory")
		}

		// The reserved conversion finishes with the values it was reviewed
		// with, so nothing is lost by the refusal.
		f.factory.prepareHook = nil
		if err := f.converter.Reconcile(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		done, err := f.store.Get(draft.ID)
		if err != nil || done.State != workingDraftConverted || done.FactoryItemID != "item-1" {
			t.Fatalf("reconciled draft: %v %+v", err, done)
		}
		if n := f.factory.itemCount(); n != 1 {
			t.Fatalf("factory journal holds %d items", n)
		}
	})
}

// newRealFactoryForConvert builds the production factory API over a real fleet,
// factory store and workspace, so a test can exercise what the frozen entry
// point actually does with the project's live defaults.
func newRealFactoryForConvert(t *testing.T) (*factoryAPI, *Fleet, string) {
	t.Helper()
	agent := newFakeAgent()
	fleet := ctlFleet(t, agent)
	root := t.TempDir()
	if err := fleet.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	if err := fleet.ws.PutRepo(Repo{ID: "repo", URL: "https://example.invalid/repo.git", Branch: "main", OwnerID: DefaultOwnerID, Forge: "github", CredRef: "test-pat"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_TEST_PAT", "fixture-only")
	fleet.creds = NewCredentialStore([]Credential{{ID: "test-pat", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "FACTORY_TEST_PAT"}})
	agent.results["config/get"] = map[string]any{
		"providers": map[string]any{"test": map[string]any{"type": "openai_compatible", "hasKey": true}},
		"presets":   map[string]any{"test/model": map[string]any{"provider": "test", "model": "model"}},
		"profiles":  map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}},
	}
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	api, ok := NewFactoryAPI(store, nil, fleet).(*factoryAPI)
	if !ok {
		t.Fatal("unexpected factory API implementation")
	}
	return api, fleet, root
}

func TestWorkingDraftConvertFrozenEmptyFieldsDoNotRinherit(t *testing.T) {
	api, fleet, root := newRealFactoryForConvert(t)
	store, _ := openWorkingDraftStore(t)

	// Every inheritable field is blank: the reviewed values come from the
	// project's defaults at the time of the conversion.
	content := WorkingDraftContent{
		Title: "Task", Plan: "do the work", RepoID: "repo", Native: true,
		ImplementerOverride: "chosen", ReviewerOverride: "chosen", ChecksMode: workingDraftChecksNone,
	}
	draft, err := store.Create(root, "create", content)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if err := fleet.ws.PutProjectSettings(root, ProjectSettings{FactoryDefaults: &FactoryDefaults{TargetBranch: "release", MaxRounds: 9}}); err != nil {
		t.Fatalf("put settings: %v", err)
	}

	serverDerived, err := workingDraftFactoryRequest(draft, derivedFactoryRequestID(draft.ID, "convert-1"))
	if err != nil {
		t.Fatalf("derive request: %v", err)
	}
	resolved, settingsVersion, err := api.PrepareFrozen(serverDerived)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if resolved.Config.TargetBranch != "release" || resolved.Config.MaxRounds != 9 {
		t.Fatalf("defaults were not resolved: %+v", resolved.Config)
	}
	if _, err := store.BeginConversion(draft.ID, "convert-1", draft.Version, resolved, settingsVersion); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// The project's defaults move on while the intent is reserved.
	if err := fleet.ws.PutProjectSettings(root, ProjectSettings{FactoryDefaults: &FactoryDefaults{TargetBranch: "changed", MaxRounds: 3}}); err != nil {
		t.Fatalf("put changed settings: %v", err)
	}

	record, ok := store.conversionRecord(draft.ID)
	if !ok || len(record.Request) == 0 {
		t.Fatal("the reservation kept no frozen request")
	}
	var frozen FactoryCreate
	if err := json.Unmarshal(record.Request, &frozen); err != nil {
		t.Fatalf("decode frozen request: %v", err)
	}
	item, err := api.CreateFrozen(frozen, frozen)
	if err != nil {
		t.Fatalf("create frozen: %v", err)
	}
	if item.Draft.Config.TargetBranch != "release" || item.Draft.Config.MaxRounds != 9 {
		t.Fatalf("the frozen request re-inherited the changed defaults: %+v", item.Draft.Config)
	}

	// The same empty fields through Create do inherit the new defaults, which is
	// exactly why the frozen entry point exists.
	plain := serverDerived
	plain.RequestID = "wd-plain"
	other, err := api.Create(plain)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if other.Draft.Config.TargetBranch != "changed" || other.Draft.Config.MaxRounds != 3 {
		t.Fatalf("Create did not resolve the live defaults: %+v", other.Draft.Config)
	}
}

func TestWorkingDraftConvertRejectsTamperedFactoryRequest(t *testing.T) {
	f := newConvertFixture(t)
	draft := f.createDraft(convertTestContent())

	// A browser may not claim a different plan, and may not name the factory
	// request ID.
	tampered := FactoryCreate{RequestID: "browser-chosen", Title: draft.Content.Title, Plan: "something else entirely", Config: FactoryConfig{RepoID: "repo-1", ProjectRoot: f.root, Native: true}}
	_, _, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, tampered, "")
	if !errors.Is(err, errWorkingDraftTampered) || !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("tampered request: %v", err)
	}
	if _, creates := f.factory.calls(); creates != 0 {
		t.Fatal("a tampered request created a factory item")
	}
	got, err := f.store.Get(draft.ID)
	if err != nil || got.State != workingDraftEditing || got.Version != draft.Version {
		t.Fatalf("a tampered request changed the draft: %v %+v", err, got)
	}

	// The server's own reading is accepted, and the derived ID is what the
	// factory sees, not the caller's.
	serverDerived, err := workingDraftFactoryRequest(draft, derivedFactoryRequestID(draft.ID, "convert-1"))
	if err != nil {
		t.Fatalf("derive request: %v", err)
	}
	converted, item, err := f.converter.Convert(context.Background(), draft.ID, "convert-1", draft.Version, serverDerived, "")
	if err != nil || item == nil || converted.State != workingDraftConverted {
		t.Fatalf("matching request: %v %+v", err, converted)
	}
	wantID := derivedFactoryRequestID(draft.ID, "convert-1")
	if item.Draft.RequestID != wantID {
		t.Fatalf("factory request id %q, want %q", item.Draft.RequestID, wantID)
	}

	// A caller that echoes the resolved configuration the server showed it is
	// accepted too: both references are the server's own reading.
	second := f.createDifferentDraft()
	echo := f.mustPrepare(second, "convert-echo")
	if _, echoed, err := f.converter.Convert(context.Background(), second.ID, "convert-echo", second.Version, echo, ""); err != nil || echoed == nil {
		t.Fatalf("resolved echo: %v %+v", err, echoed)
	}
}

// createDifferentDraft adds a second, independent draft to the same project.
func (f *convertFixture) createDifferentDraft() WorkingDraft {
	f.t.Helper()
	return f.createDraftWithKey("create-draft-2", convertTestContent())
}

func (f *convertFixture) createDraftWithKey(requestID string, content WorkingDraftContent) WorkingDraft {
	f.t.Helper()
	d, err := f.store.Create(f.root, requestID, content)
	if err != nil {
		f.t.Fatalf("create draft: %v", err)
	}
	return d
}

// mustPrepare is the server's resolved reading of a draft, which is what a
// browser shows the operator and may echo back.
func (f *convertFixture) mustPrepare(draft WorkingDraft, requestID string) FactoryCreate {
	f.t.Helper()
	derived, err := workingDraftFactoryRequest(draft, derivedFactoryRequestID(draft.ID, requestID))
	if err != nil {
		f.t.Fatalf("derive request: %v", err)
	}
	resolved, _, err := f.factory.PrepareFrozen(derived)
	if err != nil {
		f.t.Fatalf("prepare: %v", err)
	}
	return resolved
}

func TestWorkingDraftConvertLegacyFactoryReplay(t *testing.T) {
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatalf("open factory store: %v", err)
	}
	defer store.Close()

	req := FactoryCreate{RequestID: "legacy-1", Title: "Task", Plan: "build", Config: FactoryConfig{
		RepoID: "repo-1", ProjectRoot: "/projects/alpha", TargetBranch: "main", Native: true,
		ImplementerRouting: json.RawMessage(`{"profile":"chosen"}`), ReviewerRouting: json.RawMessage(`{"profile":"chosen"}`),
		ExpectedChecks: []FactoryCheckKey{},
	}}
	original, err := store.Create(req, workingDraftTestTime)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// No fleet: resolution and preflight would both fail, so only a replay that
	// runs first can answer with the original work item.
	api := &factoryAPI{store: store}
	replayed, err := api.Create(req)
	if err != nil {
		t.Fatalf("create replay: %v", err)
	}
	if replayed.ID != original.ID || replayed.Version != original.Version || replayed.Draft.RequestID != req.RequestID {
		t.Fatalf("replay returned another item: %+v", replayed)
	}
	if replayed.Draft.Config.MaxRounds != original.Draft.Config.MaxRounds || replayed.CreatedAt != original.CreatedAt {
		t.Fatalf("replay re-resolved the item: %+v", replayed)
	}
	// The frozen entry point replays the same journal record before it does
	// anything else, which is what makes a retry after a crash safe.
	frozenReplay, err := api.CreateFrozen(req, req)
	if err != nil {
		t.Fatalf("frozen replay: %v", err)
	}
	if frozenReplay.ID != original.ID {
		t.Fatalf("frozen replay returned another item: %+v", frozenReplay)
	}
	if list := store.List(); len(list) != 1 {
		t.Fatalf("replays created %d items", len(list))
	}

	// A changed payload under the same request ID is still a conflict, so the
	// replay path cannot be used to overwrite an item.
	changed := req
	changed.Title = "different"
	if _, err := api.Create(changed); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("changed payload on replay: %v", err)
	}
}

// The converter never trusts a caller's factory request ID, and a conversion of
// one draft cannot be mistaken for a conversion of another.
func TestWorkingDraftConvertDerivedIDsAreStableAndNamespaced(t *testing.T) {
	if got, want := derivedFactoryRequestID("draft", "req"), derivedFactoryRequestID("draft", "req"); got != want {
		t.Fatalf("derivation is not stable: %q %q", got, want)
	}
	if derivedFactoryRequestID("draft", "req") == derivedFactoryRequestID("draft", "other") {
		t.Fatal("two conversions share a factory request id")
	}
	if derivedFactoryRequestID("draft", "req") == derivedFactoryRequestID("other", "req") {
		t.Fatal("two drafts share a factory request id")
	}
	if got := derivedFactoryRequestID("draft", "req"); !strings.HasPrefix(got, "wd-") {
		t.Fatalf("factory request id %q is not namespaced", got)
	}
	if got := derivedDoneID("draft", "req"); !strings.HasPrefix(got, "wd-done-") || got == derivedFactoryRequestID("draft", "req") {
		t.Fatalf("association id %q collides with the factory request", got)
	}
	if got := derivedFailID("draft", "req"); !strings.HasPrefix(got, "wd-failed-") || got == derivedDoneID("draft", "req") {
		t.Fatalf("failure id %q collides with the association", got)
	}
}
