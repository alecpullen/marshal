package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var workingDraftTestTime = time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)

// workingDraftTestClock is a fake clock: every mutation must take its timestamp
// from this function, so a restart can prove that times are loaded, not redated.
func workingDraftTestClock() func() time.Time {
	return func() time.Time { return workingDraftTestTime }
}

func openWorkingDraftStore(t *testing.T) (*WorkingDraftStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "working-drafts")
	s, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
	if err != nil {
		t.Fatalf("open working draft store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func workingDraftTestContent(title string) WorkingDraftContent {
	return WorkingDraftContent{Title: title, Plan: "plan for " + title, Spec: "spec for " + title, RepoID: "repo-1", TargetBranchOverride: "main"}
}

func workingDraftTestFactoryRequest() FactoryCreate {
	return FactoryCreate{RequestID: "factory-create-1", Title: "first", Plan: "plan\n", Config: FactoryConfig{RepoID: "repo-1", ProjectRoot: "/projects/alpha", HostID: "local", Native: true, NoRequiredCIAcknowledged: true}}
}

func workingDraftTestJournalPath(dir string) string {
	return filepath.Join(dir, workingDraftJournalName)
}

func workingDraftTestJournalLines(t *testing.T, dir string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(workingDraftTestJournalPath(dir))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(b) == 0 {
		return nil
	}
	return bytes.Split(b, []byte("\n"))
}

func workingDraftTestWriteJournal(t *testing.T, dir string, lines [][]byte) {
	t.Helper()
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(workingDraftTestJournalPath(dir), b.Bytes(), 0600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
}

// workingDraftTestRewrite re-encodes one record with a mutated payload and a
// matching checksum, so only the field under test is wrong.
func workingDraftTestRewrite(t *testing.T, line []byte, mutate func(*workingDraftPayload)) []byte {
	t.Helper()
	var r workingDraftRecord
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	var p workingDraftPayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	mutate(&p)
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	r.Payload = payload
	r.Checksum = factoryDigest(payload)
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return out
}

// workingDraftTestJournalFixture returns a directory holding two durable
// records: a create and one save.
func workingDraftTestJournalFixture(t *testing.T) string {
	t.Helper()
	s, dir := openWorkingDraftStore(t)
	a, err := s.Create("/projects/alpha", "create", workingDraftTestContent("first"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Save(a.ID, "save-1", 1, workingDraftTestContent("second")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dir
}

func TestWorkingDraftIncompleteAndMultiple(t *testing.T) {
	s, _ := openWorkingDraftStore(t)
	const root = "/projects/alpha"

	// An empty draft is a legitimate starting point: a working draft is not a
	// handoff, so nothing about the plan is required or validated.
	empty, err := s.Create(root, "create-empty", WorkingDraftContent{})
	if err != nil {
		t.Fatalf("create empty draft: %v", err)
	}
	if empty.ID == "" || empty.Version != 1 || empty.State != workingDraftEditing || empty.ProjectRoot != root {
		t.Fatalf("unexpected new draft: %+v", empty)
	}
	if !empty.UpdatedAt.Equal(workingDraftTestTime) {
		t.Fatalf("create used the wall clock: %v", empty.UpdatedAt)
	}
	got, err := s.Get(empty.ID)
	if err != nil {
		t.Fatalf("get empty draft: %v", err)
	}
	if got.Content != (WorkingDraftContent{}) {
		t.Fatalf("empty content changed: %+v", got.Content)
	}

	// Temporarily invalid editor state persists verbatim. A native draft with a
	// workspace override and a negative round limit would fail factory
	// validation; saving must not resolve or reject it.
	partial := WorkingDraftContent{
		Title: "Half a thought", Spec: "outline", RepoID: "repo-1",
		WorkspaceOverride: "/tmp/workspace", Native: true,
		MaxRoundsOverride: "-3", CIPollOverride: "abc",
		ChecksMode: "custom", ExpectedChecksText: "linux-build: pending",
		NoRequiredCIAcknowledged: true,
	}
	p, err := s.Create(root, "create-partial", partial)
	if err != nil {
		t.Fatalf("create partial draft: %v", err)
	}
	if got, err = s.Get(p.ID); err != nil || got.Content != partial {
		t.Fatalf("partial content not preserved: %v %+v", err, got.Content)
	}

	// Replaying the create returns the first draft instead of adding a third.
	replay, err := s.Create(root, "create-partial", partial)
	if err != nil || replay.ID != p.ID || replay.Version != 1 {
		t.Fatalf("create replay: %v %+v", err, replay)
	}

	list, err := s.List(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 drafts, got %d", len(list))
	}
	seen := map[string]bool{}
	for _, d := range list {
		if d.ProjectRoot != root {
			t.Fatalf("list crossed projects: %+v", d)
		}
		if d.State != workingDraftEditing {
			t.Fatalf("unexpected state: %+v", d)
		}
		seen[d.ID] = true
	}
	if !seen[empty.ID] || !seen[p.ID] {
		t.Fatalf("list lost a draft: %+v", list)
	}
	if other, err := s.List("/projects/beta"); err != nil || len(other) != 0 {
		t.Fatalf("other project saw drafts: %v %+v", err, other)
	}
}

func TestWorkingDraftConditionalSave(t *testing.T) {
	s, _ := openWorkingDraftStore(t)
	const root = "/projects/alpha"

	a, err := s.Create(root, "create", workingDraftTestContent("first"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second := workingDraftTestContent("second")
	v2, err := s.Save(a.ID, "save-1", 1, second)
	if err != nil || v2.Version != 2 || v2.Content != second {
		t.Fatalf("save: %v %+v", err, v2)
	}
	if v2.ProjectRoot != root {
		t.Fatalf("save moved the draft: %q", v2.ProjectRoot)
	}
	if !v2.UpdatedAt.Equal(workingDraftTestTime) {
		t.Fatalf("save used the wall clock: %v", v2.UpdatedAt)
	}

	// A stale expected version is a conflict and changes nothing.
	if _, err := s.Save(a.ID, "save-stale", 1, workingDraftTestContent("stale")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("stale save: %v", err)
	}
	got, err := s.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Version != 2 || got.Content != second {
		t.Fatalf("stale save mutated the draft: %+v", got)
	}

	third := workingDraftTestContent("third")
	v3, err := s.Save(a.ID, "save-2", 2, third)
	if err != nil || v3.Version != 3 || v3.Content != third {
		t.Fatalf("second save: %v %+v", err, v3)
	}

	if _, err := s.Save("missing", "save-missing", 1, third); !errors.Is(err, ErrWorkingDraftNotFound) {
		t.Fatalf("save of unknown draft: %v", err)
	}
	if _, err := s.Save(a.ID, "   ", 3, third); err == nil {
		t.Fatal("accepted an empty request id")
	}

	// The store's only content rules are its own limits.
	overPlan := WorkingDraftContent{Plan: strings.Repeat("x", factoryImportLimit+1)}
	if _, err := s.Save(a.ID, "save-plan", 3, overPlan); err == nil {
		t.Fatal("accepted an oversized plan and spec")
	}
	overEnvelope := WorkingDraftContent{Title: "big", ExpectedChecksText: strings.Repeat("y", factoryRequestLimit+1)}
	if _, err := s.Save(a.ID, "save-envelope", 3, overEnvelope); err == nil {
		t.Fatal("accepted an oversized content envelope")
	}
	if _, err := s.Create(root, "create-plan", overPlan); err == nil {
		t.Fatal("created with an oversized plan and spec")
	}
	got, err = s.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Version != 3 || got.Content != third {
		t.Fatalf("rejected save mutated the draft: %+v", got)
	}
}

func TestWorkingDraftIdempotentReplayAndMismatch(t *testing.T) {
	s, _ := openWorkingDraftStore(t)
	const root = "/projects/alpha"

	base := workingDraftTestContent("first")
	a, err := s.Create(root, "create", base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second := workingDraftTestContent("second")
	if _, err := s.Save(a.ID, "save-1", 1, second); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	third := workingDraftTestContent("third")
	if _, err := s.Save(a.ID, "save-2", 2, third); err != nil {
		t.Fatalf("save 2: %v", err)
	}

	// An accepted request replays its original result even though the draft has
	// moved on and its expected version is now stale.
	replay, err := s.Save(a.ID, "save-1", 1, second)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Version != 2 || replay.Content != second {
		t.Fatalf("replay returned the wrong result: %+v", replay)
	}

	// The same request id with a different payload conflicts instead.
	if _, err := s.Save(a.ID, "save-1", 3, third); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("reused request id with another payload: %v", err)
	}
	if _, err := s.Save(a.ID, "save-1", 3, base); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("reused request id with the original payload: %v", err)
	}

	// A replayed create returns the original draft and creates nothing.
	again, err := s.Create(root, "create", base)
	if err != nil || again.ID != a.ID || again.Version != 1 {
		t.Fatalf("create replay: %v %+v", err, again)
	}
	if _, err := s.Create(root, "create", second); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("create replayed with another payload: %v", err)
	}
	if _, err := s.Create("/projects/beta", "create", base); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("create replayed into another project: %v", err)
	}

	// Delete replays too, and returns the tombstone it recorded; the version is
	// not compared on a replay.
	tomb, err := s.Delete(a.ID, "delete", 3)
	if err != nil || tomb.Version != 4 || tomb.State != workingDraftDeleted {
		t.Fatalf("delete: %v %+v", err, tomb)
	}
	replayed, err := s.Delete(a.ID, "delete", 1)
	if err != nil || replayed.Version != tomb.Version || replayed.State != workingDraftDeleted {
		t.Fatalf("delete replay: %v %+v", err, replayed)
	}

	// Earlier saves still replay from their recorded identity after the delete.
	replay, err = s.Save(a.ID, "save-2", 2, third)
	if err != nil || replay.Version != 3 {
		t.Fatalf("save replay after delete: %v %+v", err, replay)
	}
	got, err := s.Get(a.ID)
	if err != nil || got.Version != 4 || got.State != workingDraftDeleted {
		t.Fatalf("replay resurrected the draft: %v %+v", err, got)
	}
}

func TestWorkingDraftDeleteDoesNotResurrect(t *testing.T) {
	s, dir := openWorkingDraftStore(t)
	const root = "/projects/alpha"

	a, err := s.Create(root, "create", workingDraftTestContent("first"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tomb, err := s.Delete(a.ID, "delete", 1)
	if err != nil || tomb.State != workingDraftDeleted || tomb.Version != 2 {
		t.Fatalf("delete: %v %+v", err, tomb)
	}
	if tomb.Content.Title != "first" {
		t.Fatalf("tombstone lost the content a device needs to recover: %+v", tomb.Content)
	}
	if got, err := s.Get(a.ID); err != nil || got.State != workingDraftDeleted {
		t.Fatalf("get tombstone: %v %+v", err, got)
	}
	if list, err := s.List(root); err != nil || len(list) != 0 {
		t.Fatalf("list returned a tombstone: %v %+v", err, list)
	}

	if _, err := s.Save(a.ID, "save-after-delete", 2, workingDraftTestContent("again")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("save resurrected the draft: %v", err)
	}
	if _, err := s.Delete(a.ID, "delete-again", 2); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.BeginConversion(a.ID, "convert", 2, workingDraftTestFactoryRequest(), "settings-1"); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("conversion started from a tombstone: %v", err)
	}
	if _, err := s.CompleteConversion(a.ID, "complete", "item-1"); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("conversion completed on a tombstone: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(a.ID)
	if err != nil || got.State != workingDraftDeleted || got.Version != 2 {
		t.Fatalf("tombstone did not survive restart: %v %+v", err, got)
	}
	if list, err := reopened.List(root); err != nil || len(list) != 0 {
		t.Fatalf("restarted store listed a tombstone: %v %+v", err, list)
	}
	if _, err := reopened.Save(a.ID, "save-after-restart", 2, workingDraftTestContent("again")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("save resurrected the draft after restart: %v", err)
	}
}

func TestWorkingDraftProjectIsolation(t *testing.T) {
	s, _ := openWorkingDraftStore(t)

	alpha, err := s.Create("/projects/alpha", "alpha-1", workingDraftTestContent("a1"))
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	beta, err := s.Create("/projects/beta", "beta-1", workingDraftTestContent("b1"))
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	second, err := s.Create("/projects/alpha", "alpha-2", workingDraftTestContent("a2"))
	if err != nil {
		t.Fatalf("create second alpha: %v", err)
	}

	alphaList, err := s.List("/projects/alpha")
	if err != nil {
		t.Fatalf("list alpha: %v", err)
	}
	if len(alphaList) != 2 {
		t.Fatalf("want 2 alpha drafts, got %d", len(alphaList))
	}
	for _, d := range alphaList {
		if d.ProjectRoot != "/projects/alpha" {
			t.Fatalf("alpha list crossed projects: %+v", d)
		}
		if d.ID == beta.ID {
			t.Fatal("alpha list contained a beta draft")
		}
	}
	betaList, err := s.List("/projects/beta")
	if err != nil || len(betaList) != 1 || betaList[0].ID != beta.ID {
		t.Fatalf("list beta: %v %+v", err, betaList)
	}
	if other, err := s.List("/projects/gamma"); err != nil || other == nil || len(other) != 0 {
		t.Fatalf("unknown project: %v %+v", err, other)
	}

	// Saving cannot move a draft, and the content carries no project identity.
	saved, err := s.Save(alpha.ID, "alpha-1-save", 1, workingDraftTestContent("a1 moved"))
	if err != nil || saved.ProjectRoot != "/projects/alpha" {
		t.Fatalf("save moved the draft: %v %+v", err, saved)
	}
	if saved.ID != alpha.ID || saved.ID == second.ID {
		t.Fatalf("save changed identity: %+v", saved)
	}

	// A create request id from one project cannot be replayed into another.
	if _, err := s.Create("/projects/beta", "alpha-1", workingDraftTestContent("a1")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("cross-project create replay: %v", err)
	}
	if list, err := s.List("/projects/beta"); err != nil || len(list) != 1 {
		t.Fatalf("cross-project create leaked: %v %+v", err, list)
	}
}

func TestWorkingDraftRestartAndPermissions(t *testing.T) {
	s, dir := openWorkingDraftStore(t)
	const root = "/projects/alpha"

	a, err := s.Create(root, "create", workingDraftTestContent("first"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Save(a.ID, "save-1", 1, workingDraftTestContent("second")); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A conversion durably reserves one frozen request, then associates one item.
	request := workingDraftTestFactoryRequest()
	converting, err := s.BeginConversion(a.ID, "convert-1", 2, request, "settings-7")
	if err != nil || converting.State != workingDraftConverting || converting.Version != 3 {
		t.Fatalf("begin conversion: %v %+v", err, converting)
	}
	if converting.FactoryItemID != "" {
		t.Fatalf("a reserved conversion already has an item: %+v", converting)
	}
	if raw, err := json.Marshal(converting); err != nil {
		t.Fatalf("encode: %v", err)
	} else if strings.Contains(string(raw), request.RequestID) {
		t.Fatal("the frozen factory request is exposed on the draft")
	}

	// Editing is refused while a conversion is in flight.
	if _, err := s.Save(a.ID, "save-during-convert", 3, workingDraftTestContent("third")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("save during conversion: %v", err)
	}
	if _, err := s.Delete(a.ID, "delete-during-convert", 3); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("delete during conversion: %v", err)
	}

	// The intent replays its original reservation, and a changed intent conflicts.
	again, err := s.BeginConversion(a.ID, "convert-1", 2, request, "settings-7")
	if err != nil || again.Version != converting.Version || again.State != workingDraftConverting {
		t.Fatalf("conversion intent replay: %v %+v", err, again)
	}
	if _, err := s.BeginConversion(a.ID, "convert-1", 3, request, "settings-8"); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("reused conversion request id: %v", err)
	}

	done, err := s.CompleteConversion(a.ID, "complete-1", "item-1")
	if err != nil || done.State != workingDraftConverted || done.FactoryItemID != "item-1" || done.Version != 4 {
		t.Fatalf("complete conversion: %v %+v", err, done)
	}
	if replay, err := s.CompleteConversion(a.ID, "complete-1", "item-1"); err != nil || replay.Version != 4 {
		t.Fatalf("completion replay: %v %+v", err, replay)
	}
	if _, err := s.CompleteConversion(a.ID, "complete-1", "item-2"); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("completion replayed with another item: %v", err)
	}
	if _, err := s.Save(a.ID, "save-converted", 4, workingDraftTestContent("third")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("save on a converted draft: %v", err)
	}
	if _, err := s.BeginConversion(a.ID, "convert-2", 4, request, "settings-7"); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("second conversion of a converted draft: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, workingDraftLockName)); !os.IsNotExist(err) {
		t.Fatalf("close left the ownership lock: %v", err)
	}
	// The store is private to the user: no group or other access at all.
	for path, want := range map[string]os.FileMode{dir: 0700, workingDraftTestJournalPath(dir): 0600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("%s has mode %v, want %v", path, fi.Mode().Perm(), want)
		}
	}

	// A later clock proves that replayed times are loaded, not redated.
	later := func() time.Time { return workingDraftTestTime.Add(48 * time.Hour) }
	reopened, err := OpenWorkingDraftStore(dir, later)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if other, err := OpenWorkingDraftStore(dir, workingDraftTestClock()); !errors.Is(err, ErrWorkingDraftUnavailable) || other != nil {
		t.Fatalf("second owner opened the store: %v %+v", err, other)
	}

	got, err := reopened.Get(a.ID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if got.State != workingDraftConverted || got.Version != 4 || got.FactoryItemID != "item-1" {
		t.Fatalf("restarted draft lost its identity: %+v", got)
	}
	if got.Content.Title != "second" {
		t.Fatalf("restarted draft lost its content: %+v", got.Content)
	}
	if !got.UpdatedAt.Equal(done.UpdatedAt) {
		t.Fatalf("restarted draft was redated: %v want %v", got.UpdatedAt, done.UpdatedAt)
	}
	if replay, err := reopened.CompleteConversion(a.ID, "complete-1", "item-1"); err != nil || replay.Version != 4 {
		t.Fatalf("completion replay after restart: %v %+v", err, replay)
	}
	if _, err := reopened.Save(a.ID, "save-after-restart", 4, workingDraftTestContent("third")); !errors.Is(err, ErrWorkingDraftConflict) {
		t.Fatalf("save on a converted draft after restart: %v", err)
	}
	if list, err := reopened.List(root); err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("list after restart: %v %+v", err, list)
	}

	// Close is idempotent, and a closed store serves nothing.
	if err := reopened.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := reopened.Get(a.ID); !errors.Is(err, ErrWorkingDraftUnavailable) {
		t.Fatalf("get on a closed store: %v", err)
	}
	if _, err := reopened.List(root); !errors.Is(err, ErrWorkingDraftUnavailable) {
		t.Fatalf("list on a closed store: %v", err)
	}
	if _, err := reopened.Save(a.ID, "save-closed", 4, workingDraftTestContent("third")); !errors.Is(err, ErrWorkingDraftUnavailable) {
		t.Fatalf("save on a closed store: %v", err)
	}
	if _, err := reopened.Create(root, "create-closed", workingDraftTestContent("third")); !errors.Is(err, ErrWorkingDraftUnavailable) {
		t.Fatalf("create on a closed store: %v", err)
	}
	if _, err := reopened.Delete(a.ID, "delete-closed", 4); !errors.Is(err, ErrWorkingDraftUnavailable) {
		t.Fatalf("delete on a closed store: %v", err)
	}

	// Releasing ownership lets the directory be owned again.
	last, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	defer last.Close()
	if _, err := last.Get(a.ID); err != nil {
		t.Fatalf("get from the new owner: %v", err)
	}
}

func TestWorkingDraftCorruptionFailsClosed(t *testing.T) {
	for _, kind := range []string{"checksum", "schema", "sequence", "version", "state", "garbage"} {
		t.Run(kind, func(t *testing.T) {
			dir := workingDraftTestJournalFixture(t)
			lines := workingDraftTestJournalLines(t, dir)
			if len(lines) != 2 {
				t.Fatalf("want 2 journal records, got %d", len(lines))
			}
			switch kind {
			case "checksum":
				var r workingDraftRecord
				if err := json.Unmarshal(lines[1], &r); err != nil {
					t.Fatalf("decode record: %v", err)
				}
				r.Checksum = strings.Repeat("0", len(r.Checksum))
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatalf("encode record: %v", err)
				}
				lines[1] = b
			case "schema":
				var r workingDraftRecord
				if err := json.Unmarshal(lines[1], &r); err != nil {
					t.Fatalf("decode record: %v", err)
				}
				r.Schema = 99
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatalf("encode record: %v", err)
				}
				lines[1] = b
			case "sequence":
				lines[1] = workingDraftTestRewrite(t, lines[1], func(p *workingDraftPayload) { p.Sequence = 9 })
			case "version":
				lines[1] = workingDraftTestRewrite(t, lines[1], func(p *workingDraftPayload) { p.Draft.Version = 4 })
			case "state":
				lines[1] = workingDraftTestRewrite(t, lines[1], func(p *workingDraftPayload) { p.Draft.State = "unfinished" })
			case "garbage":
				lines = append([][]byte{lines[0], []byte("{not a record}")}, lines[1:]...)
			}
			workingDraftTestWriteJournal(t, dir, lines)
			before, err := os.ReadFile(workingDraftTestJournalPath(dir))
			if err != nil {
				t.Fatalf("read journal: %v", err)
			}

			s, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
			if !errors.Is(err, ErrWorkingDraftUnavailable) {
				t.Fatalf("want unavailable, got %v", err)
			}
			if s != nil {
				t.Fatal("a corrupt store opened")
			}
			after, err := os.ReadFile(workingDraftTestJournalPath(dir))
			if err != nil {
				t.Fatalf("read journal: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("a failed open rewrote the journal instead of failing closed")
			}
			if _, err := os.Stat(filepath.Join(dir, workingDraftLockName)); !os.IsNotExist(err) {
				t.Fatalf("a failed open left the ownership lock: %v", err)
			}
		})
	}

	// An interrupted append is not corruption: the fragment was never
	// acknowledged, so it is dropped and the accepted records stay.
	t.Run("partial-append", func(t *testing.T) {
		dir := workingDraftTestJournalFixture(t)
		f, err := os.OpenFile(workingDraftTestJournalPath(dir), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatalf("open journal: %v", err)
		}
		if _, err := f.WriteString(`{"schema":1,"payload":{"sequence":3`); err != nil {
			t.Fatalf("append fragment: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close journal: %v", err)
		}

		s, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
		if err != nil {
			t.Fatalf("partial append did not open: %v", err)
		}
		list, err := s.List("/projects/alpha")
		if err != nil || len(list) != 1 || list[0].Version != 2 {
			t.Fatalf("partial append lost accepted state: %v %+v", err, list)
		}
		if _, err := s.Save(list[0].ID, "save-2", 2, workingDraftTestContent("third")); err != nil {
			t.Fatalf("save after a dropped fragment: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		reopened, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
		if err != nil {
			t.Fatalf("journal did not survive the dropped fragment: %v", err)
		}
		defer reopened.Close()
		got, err := reopened.Get(list[0].ID)
		if err != nil || got.Version != 3 || got.Content.Title != "third" {
			t.Fatalf("journal is not appendable after the fragment: %v %+v", err, got)
		}
	})
}

func TestWorkingDraftWriteFailureDoesNotClaimSaved(t *testing.T) {
	s, dir := openWorkingDraftStore(t)
	const root = "/projects/alpha"

	a, err := s.Create(root, "create", workingDraftTestContent("first"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := os.ReadFile(workingDraftTestJournalPath(dir))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}

	// Make the journal unusable the way a failing filesystem would, without a
	// production test hook.
	if err := s.file.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
	if _, err := s.Save(a.ID, "save-1", 1, workingDraftTestContent("second")); !errors.Is(err, ErrWorkingDraftUnavailable) {
		t.Fatalf("save on a broken journal: %v", err)
	}

	// The failed save is not claimed: memory still holds the durable version.
	got, err := s.Get(a.ID)
	if err != nil {
		t.Fatalf("get after a failed save: %v", err)
	}
	if got.Version != 1 || got.Content.Title != "first" {
		t.Fatalf("a failed save was published: %+v", got)
	}
	after, err := os.ReadFile(workingDraftTestJournalPath(dir))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a failed save wrote to the journal")
	}

	// An unavailable store refuses every mutation instead of pretending.
	for name, call := range map[string]func() error{
		"save":   func() error { _, e := s.Save(a.ID, "save-2", 1, workingDraftTestContent("second")); return e },
		"create": func() error { _, e := s.Create(root, "create-2", workingDraftTestContent("second")); return e },
		"delete": func() error { _, e := s.Delete(a.ID, "delete-1", 1); return e },
		"convert": func() error {
			_, e := s.BeginConversion(a.ID, "convert-1", 1, workingDraftTestFactoryRequest(), "settings-1")
			return e
		},
		"complete": func() error { _, e := s.CompleteConversion(a.ID, "complete-1", "item-1"); return e },
	} {
		if err := call(); !errors.Is(err, ErrWorkingDraftUnavailable) {
			t.Fatalf("%s succeeded on an unavailable store: %v", name, err)
		}
	}
	_ = s.Close()
}

func TestWorkingDraftConcurrentSaves(t *testing.T) {
	s, dir := openWorkingDraftStore(t)
	const root = "/projects/alpha"
	const workers = 8

	a, err := s.Create(root, "create", workingDraftTestContent("start"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted = map[uint64]string{}
		failures []error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for attempt := 0; attempt < 64; attempt++ {
				current, err := s.Get(a.ID)
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					return
				}
				title := fmt.Sprintf("worker-%d-attempt-%d", worker, attempt)
				next, err := s.Save(a.ID, fmt.Sprintf("save-%d-%d", worker, attempt), current.Version, WorkingDraftContent{Title: title})
				if errors.Is(err, ErrWorkingDraftConflict) {
					continue
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					return
				}
				mu.Lock()
				if previous, dup := accepted[next.Version]; dup {
					failures = append(failures, fmt.Errorf("version %d accepted twice (%q and %q)", next.Version, previous, title))
				}
				accepted[next.Version] = title
				mu.Unlock()
				return
			}
			mu.Lock()
			failures = append(failures, fmt.Errorf("worker %d never had a save accepted", worker))
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for _, err := range failures {
		t.Errorf("concurrent save: %v", err)
	}
	if len(failures) > 0 {
		t.FailNow()
	}

	if len(accepted) != workers {
		t.Fatalf("want %d accepted saves, got %d", workers, len(accepted))
	}
	for version := uint64(2); version <= workers+1; version++ {
		if _, ok := accepted[version]; !ok {
			t.Fatalf("version %d was never accepted: %v", version, accepted)
		}
	}
	final, err := s.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.Version != workers+1 {
		t.Fatalf("final version %d, want %d", final.Version, workers+1)
	}
	if final.Content.Title != accepted[final.Version] {
		t.Fatalf("final content %q is not the last accepted save %q", final.Content.Title, accepted[final.Version])
	}

	// Every accepted save is durable exactly once.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	restored, err := reopened.Get(a.ID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if restored.Version != final.Version || restored.Content.Title != final.Content.Title {
		t.Fatalf("restarted draft %d/%q, want %d/%q", restored.Version, restored.Content.Title, final.Version, final.Content.Title)
	}
}
