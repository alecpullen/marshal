package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The snapshot-handoff tests drive the real store, the real git seed and the
// real planner routes, but only against t.TempDir() fixtures: a disposable
// repository cloned from a bare fixture, a fake ACP child that commits what the
// seed staged, and a fake factory service backed by the real journal. No
// network, no adapter process, no user file.

// snapshotTestBlob stores content in the bridge's content-addressed store and
// returns its digest, which is what an approval records.
func snapshotTestBlob(t *testing.T, stateDir string, content []byte) string {
	t.Helper()
	dir := filepath.Join(stateDir, plannerDirName, plannerBlobDirName)
	if err := os.MkdirAll(dir, plannerDirMode); err != nil {
		t.Fatalf("create blob directory: %v", err)
	}
	digest := plannerDigest(content)
	if err := os.WriteFile(filepath.Join(dir, digest), content, plannerFileMode); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return digest
}

// snapshotTestEdit builds the hashed core of a snapshot exactly as a capture
// does, so an approval under test is the one a capture would have produced.
func snapshotTestEdit(t *testing.T, base string, files ...SelectedFile) *EditSnapshot {
	t.Helper()
	if files == nil {
		files = []SelectedFile{}
	}
	edit := EditSnapshot{BaseCommit: base, Files: files}
	digest, err := plannerSnapshotDigest(edit)
	if err != nil {
		t.Fatalf("digest snapshot: %v", err)
	}
	edit.SHA256 = digest
	return &edit
}

// seedFixture is one disposable repository with an owned implementer checkout,
// registered on a fleet whose state directory the test also writes blobs into.
type seedFixture struct {
	t        *testing.T
	g        *factoryGit
	fleet    *Fleet
	ws       *Workspace
	work     string
	stateDir string
	base     string
}

// newSeedFixture builds a bare origin with one base commit and a clone of it
// holding the files every snapshot test selects from.
func newSeedFixture(t *testing.T) *seedFixture {
	t.Helper()
	bare := newBareRepoFixture(t)
	work := filepath.Join(t.TempDir(), "checkout")
	gitText(t, "", "clone", bare, work)
	gitText(t, work, "config", "user.email", "test@example.com")
	gitText(t, work, "config", "user.name", "Test")
	seedWriteFile(t, work, "a.txt", "one\ntwo\n", 0o644)
	seedWriteFile(t, work, "sub/b.txt", "bee\n", 0o644)
	seedWriteFile(t, work, "keep.txt", "keep\n", 0o644)
	gitText(t, work, "add", "-A")
	gitText(t, work, "commit", "-m", "base")
	f := newSeedFixtureOn(t, work, bare, t.TempDir())
	return f
}

// newSeedFixtureFromClone builds a fixture over a checkout cloned from an
// existing repository, so a snapshot captured there can be seeded here.
func newSeedFixtureFromClone(t *testing.T, cloneFrom, stateDir string) *seedFixture {
	t.Helper()
	work := filepath.Join(t.TempDir(), "factory-checkout")
	gitText(t, "", "clone", cloneFrom, work)
	gitText(t, work, "config", "user.email", "test@example.com")
	gitText(t, work, "config", "user.name", "Test")
	return newSeedFixtureOn(t, work, "https://example.invalid/origin.git", stateDir)
}

// newSeedFixtureOn registers the checkout, its repository and one implementer
// worker on a fresh fleet. Its session/commit commits the index the seed
// staged, and adds nothing of its own: staging is the seed's responsibility.
func newSeedFixtureOn(t *testing.T, work, origin, stateDir string) *seedFixture {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if err := ws.PutRepo(Repo{ID: "repo", URL: origin, Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	fleet := NewFleet(ws, "", nil, stateDir, Limits{}, "")
	f := &seedFixture{t: t, fleet: fleet, ws: ws, work: work, stateDir: stateDir, g: newFactoryGit(fleet)}
	f.base = gitText(t, work, "rev-parse", "HEAD")
	return f
}

// worker registers the owned implementer agent for one item and returns it.
func (f *seedFixture) worker(itemID string) FactoryWorker {
	f.t.Helper()
	worker := FactoryWorker{AgentID: "agent-1", SessionID: "s-1", Role: "implementer", SpawnEffectID: "spawn-1"}
	a := Agent{ID: worker.AgentID, SessionID: worker.SessionID, Project: f.work, SourceKind: "git", SourceRef: "repo", FactoryItemID: itemID, FactoryRole: worker.Role, FactorySpawnEffectID: worker.SpawnEffectID}
	if err := f.ws.PutAgent(a); err != nil {
		f.t.Fatal(err)
	}
	tr := &captureTransport{onRequest: func(method string) error {
		if method != "session/commit" {
			return nil
		}
		cmd := exec.Command("git", "commit", "-m", "seed")
		cmd.Dir = f.work
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("commit: %v: %s", err, out)
		}
		return nil
	}}
	c := &Child{Transport: tr}
	r := NewRegistry(c)
	if err := c.Start(); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(c.Stop)
	r.track(worker.SessionID, AgentPath(f.work))
	rt := &agentRuntime{id: worker.AgentID, root: f.work, sessionID: worker.SessionID, child: c, reg: r, sourceKind: "git"}
	fleet := f.fleet
	fleet.runtimes[worker.AgentID] = rt
	fleet.sessionAgent[worker.SessionID] = worker.AgentID
	return worker
}

// item is one work item on the owned branch, at the base commit, approved with
// the selection under test.
func (f *seedFixture) item(id, base string, edit *EditSnapshot) FactoryItem {
	f.t.Helper()
	conf := FactoryConfig{RepoID: "repo", ProjectRoot: f.work, TargetBranch: "main", Native: true, NoRequiredCIAcknowledged: true, MaxRounds: 5}
	return FactoryItem{
		ID: id, RepoID: "repo", Branch: "marshal/factory/" + id, BaseSHA: base,
		Approvals: []FactoryApproval{{Revision: 1, Plan: "the approved plan\n", Spec: "the approved spec\n", PlanHash: factoryApprovalHash("the approved plan\n", "the approved spec\n", edit), Config: conf, EditSnapshot: edit, ApprovedAt: time.Now().UTC()}},
	}
}

func seedWriteFile(t *testing.T, dir, rel, content string, mode os.FileMode) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// seedCommittedPaths lists the paths one commit changed, sorted, so a test can
// prove the seed staged exactly its selection and nothing else.
func seedCommittedPaths(t *testing.T, dir, rev string) []string {
	t.Helper()
	out := gitText(t, dir, "show", "--name-only", "--pretty=format:", rev)
	paths := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths
}

func seedBranchExists(dir, branch string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "refs/heads/"+branch)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func TestFactorySnapshotSeedSelectedOnly(t *testing.T) {
	ctx := context.Background()
	f := newSeedFixture(t)
	worker := f.worker("item-1")

	modified := "one\nchanged\n"
	created := "brand new\n"
	modifyBlob := snapshotTestBlob(t, f.stateDir, []byte(modified))
	createBlob := snapshotTestBlob(t, f.stateDir, []byte(created))
	edit := snapshotTestEdit(t, f.base,
		SelectedFile{Path: "a.txt", Operation: plannerFileModify, BaseSHA256: plannerDigest([]byte("one\ntwo\n")), ContentSHA256: modifyBlob},
		SelectedFile{Path: "added.txt", Operation: plannerFileCreate, ContentSHA256: createBlob},
		SelectedFile{Path: "sub/b.txt", Operation: plannerFileDelete, BaseSHA256: plannerDigest([]byte("bee\n"))},
	)
	item := f.item("item-1", f.base, edit)

	head, tree, err := f.g.Seed(ctx, item, worker)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if head == f.base || tree == "" {
		t.Fatalf("head=%q tree=%q", head, tree)
	}

	a, err := os.ReadFile(filepath.Join(f.work, "a.txt"))
	if err != nil || string(a) != modified {
		t.Fatalf("a.txt = %q, %v; want the selected bytes", a, err)
	}
	added, err := os.ReadFile(filepath.Join(f.work, "added.txt"))
	if err != nil || string(added) != created {
		t.Fatalf("added.txt = %q, %v; want the created bytes", added, err)
	}
	if _, err := os.Stat(filepath.Join(f.work, "sub/b.txt")); !os.IsNotExist(err) {
		t.Fatalf("the selected deletion survived: %v", err)
	}
	// An unselected file is neither changed nor removed by a seed.
	keep, err := os.ReadFile(filepath.Join(f.work, "keep.txt"))
	if err != nil || string(keep) != "keep\n" {
		t.Fatalf("unselected keep.txt = %q, %v", keep, err)
	}
	if status := gitText(t, f.work, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("seed left the checkout dirty: %q", status)
	}
	want := []string{"a.txt", "added.txt", "docs/marshal-factory/item-1/plan.md", "docs/marshal-factory/item-1/spec.md", "sub/b.txt"}
	if got := seedCommittedPaths(t, f.work, head); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("committed paths = %v, want %v", got, want)
	}
}

func TestFactorySnapshotBaseConflict(t *testing.T) {
	ctx := context.Background()
	f := newSeedFixture(t)
	worker := f.worker("item-1")
	blob := snapshotTestBlob(t, f.stateDir, []byte("one\nchanged\n"))
	// The selection was taken against a commit this checkout is not at.
	edit := snapshotTestEdit(t, strings.Repeat("a", 40),
		SelectedFile{Path: "a.txt", Operation: plannerFileModify, BaseSHA256: plannerDigest([]byte("one\ntwo\n")), ContentSHA256: blob},
	)
	item := f.item("item-1", f.base, edit)

	_, _, err := f.g.Seed(ctx, item, worker)
	if err == nil || !strings.Contains(err.Error(), "snapshot base does not match approved base") {
		t.Fatalf("seed = %v, want the base mismatch", err)
	}
	if seedBranchExists(f.work, item.Branch) {
		t.Fatal("a refused seed created the owned branch")
	}
	if a, _ := os.ReadFile(filepath.Join(f.work, "a.txt")); string(a) != "one\ntwo\n" {
		t.Fatalf("a refused seed wrote a.txt: %q", a)
	}
	if status := gitText(t, f.work, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("a refused seed changed the checkout: %q", status)
	}
}

func TestFactorySnapshotCorruptBlob(t *testing.T) {
	ctx := context.Background()
	content := "one\nchanged\n"
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, f *seedFixture, digest string)
	}{
		{"missing", func(t *testing.T, f *seedFixture, digest string) {
			if err := os.Remove(filepath.Join(f.stateDir, plannerDirName, plannerBlobDirName, digest)); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered", func(t *testing.T, f *seedFixture, digest string) {
			if err := os.WriteFile(filepath.Join(f.stateDir, plannerDirName, plannerBlobDirName, digest), []byte("tampered\n"), plannerFileMode); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeedFixture(t)
			worker := f.worker("item-1")
			blob := snapshotTestBlob(t, f.stateDir, []byte(content))
			edit := snapshotTestEdit(t, f.base,
				SelectedFile{Path: "a.txt", Operation: plannerFileModify, BaseSHA256: plannerDigest([]byte("one\ntwo\n")), ContentSHA256: blob},
			)
			item := f.item("item-1", f.base, edit)
			tc.damage(t, f, blob)

			_, _, err := f.g.Seed(ctx, item, worker)
			if err == nil || !strings.Contains(err.Error(), "blob") {
				t.Fatalf("seed = %v, want the corrupt blob", err)
			}
			if seedBranchExists(f.work, item.Branch) {
				t.Fatal("a corrupt blob still created the owned branch")
			}
			if a, _ := os.ReadFile(filepath.Join(f.work, "a.txt")); string(a) != "one\ntwo\n" {
				t.Fatalf("a corrupt blob still wrote a.txt: %q", a)
			}
		})
	}
}

func TestFactorySnapshotSymlinkRace(t *testing.T) {
	ctx := context.Background()
	f := newSeedFixture(t)
	outside := t.TempDir()
	// A component of the selected path is a symlink: the capture refused
	// symlinks, so this one appeared after the selection was reviewed.
	if err := os.Symlink(outside, filepath.Join(f.work, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitText(t, f.work, "add", "link")
	gitText(t, f.work, "commit", "-m", "symlink component")
	base := gitText(t, f.work, "rev-parse", "HEAD")
	worker := f.worker("item-1")
	blob := snapshotTestBlob(t, f.stateDir, []byte("escape\n"))
	edit := snapshotTestEdit(t, base,
		SelectedFile{Path: "link/escape.txt", Operation: plannerFileCreate, ContentSHA256: blob},
	)
	item := f.item("item-1", base, edit)

	_, _, err := f.g.Seed(ctx, item, worker)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("seed = %v, want the symlink refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("a refused seed wrote through the symlink: %v", err)
	}
	if seedBranchExists(f.work, item.Branch) {
		t.Fatal("a refused seed created the owned branch")
	}
}

func TestFactorySnapshotInterruptedSeed(t *testing.T) {
	f := newSeedFixture(t)
	first := snapshotTestBlob(t, f.stateDir, []byte("first\n"))
	second := snapshotTestBlob(t, f.stateDir, []byte("second\n"))
	edit := snapshotTestEdit(t, f.base,
		SelectedFile{Path: "first.txt", Operation: plannerFileCreate, ContentSHA256: first},
		SelectedFile{Path: "second.txt", Operation: plannerFileCreate, ContentSHA256: second},
	)
	worker := f.worker("item-1")

	// The store item carries the approved selection and one dispatched seed, so
	// the effect path under test is the real one.
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	req := factoryReq()
	req.RequestID = "interrupt"
	created, err := store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	itemID := created.ID
	worker = f.worker(itemID)
	seedItem := f.item(itemID, f.base, edit)
	item, err := store.Update(itemID, "prepare", created.Version, json.RawMessage(`{"prepare":true}`), func(dst *FactoryItem) error {
		dst.Approvals = seedItem.Approvals
		dst.Grants = []FactoryGrant{{ID: "initial", AdditionalRounds: 5}}
		dst.BaseSHA = f.base
		dst.Branch = seedItem.Branch
		dst.Workers = []FactoryWorker{worker}
		dst.State = "preparing"
		dst.Effects = []FactoryEffect{{ID: itemID + "-1", Kind: "seed", Status: "dispatched", WorkerRole: "implementer", Round: 0, ApprovalRevision: 1, Worker: &dst.Workers[0]}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// The interruption happens after the first selected file was written, which
	// is the point at which the worktree is already half-applied.
	f.g.beforeSeedWrite = func(path string) error {
		if path == "second.txt" {
			return errors.New("injected seed interruption")
		}
		return nil
	}
	effects := &factoryEffects{fleet: f.fleet, git: f.g}
	c := NewFactoryController(store, effects, time.Now)
	if err := c.finishDispatch(context.Background(), item, item.Effects[len(item.Effects)-1]); err != nil {
		t.Fatal(err)
	}
	if a, err := os.ReadFile(filepath.Join(f.work, "first.txt")); err != nil || string(a) != "first\n" {
		t.Fatalf("first file was not applied before the interruption: %q, %v", a, err)
	}

	saved, err := store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	last := saved.Effects[len(saved.Effects)-1]
	if last.Status != "uncertain" {
		t.Fatalf("interrupted seed effect status = %q, want uncertain", last.Status)
	}
	if saved.Decision == nil || saved.Decision.Kind != "recovery" {
		t.Fatalf("interrupted seed recorded decision %+v, want a recovery decision", saved.Decision)
	}
	// The uncertain record keeps the cause, so reconciliation names what went
	// wrong rather than only that something did.
	if !strings.Contains(saved.Decision.Reason, "injected seed interruption") {
		t.Fatalf("uncertain record lost the cause: %+v", saved.Decision)
	}
	// A blind reapply is refused while the effect is uncertain: exactly one
	// unsettled effect exists, which is what validation and the retry gate read.
	if unsettled := countUnsettledEffects(saved); unsettled != 1 {
		t.Fatalf("interrupted seed left %d unsettled effects, want 1", unsettled)
	}
}

func countUnsettledEffects(i FactoryItem) int {
	n := 0
	for _, e := range i.Effects {
		switch e.Status {
		case "prepared", "dispatched", "uncertain":
			n++
		}
	}
	return n
}

func TestFactorySnapshotApprovalHash(t *testing.T) {
	plan, spec := "the plan\n", "the spec\n"
	if got, want := factoryApprovalHash(plan, spec, nil), FactoryPlanHash(plan, spec); got != want {
		t.Fatalf("an approval without a snapshot hashes %q, want the legacy digest %q", got, want)
	}
	blob := plannerDigest([]byte("content\n"))
	edit := snapshotTestEdit(t, strings.Repeat("0", 40),
		SelectedFile{Path: "a.txt", Operation: plannerFileModify, BaseSHA256: plannerDigest([]byte("old\n")), ContentSHA256: blob},
	)
	got := factoryApprovalHash(plan, spec, edit)
	if got == FactoryPlanHash(plan, spec) {
		t.Fatal("a snapshot-bearing approval kept the legacy digest")
	}
	// The digest is a versioned canonical JSON document: a verifier in another
	// process hashes exactly these bytes.
	canonical := fmt.Sprintf(`{"version":2,"plan":%q,"spec":%q,"editSnapshotSha256":%q}`, plan, spec, edit.SHA256)
	if want := plannerDigest([]byte(canonical)); got != want {
		t.Fatalf("digest = %q, want %q over %s", got, want, canonical)
	}
	// The plan and spec alone no longer move the digest; the selection does.
	other := snapshotTestEdit(t, strings.Repeat("1", 40),
		SelectedFile{Path: "a.txt", Operation: plannerFileModify, BaseSHA256: plannerDigest([]byte("old\n")), ContentSHA256: blob},
	)
	if factoryApprovalHash(plan, spec, other) == got {
		t.Fatal("two different selections hashed the same")
	}

	// An approval built from a snapshot-bearing draft carries the versioned
	// digest, and the store accepts it.
	item := loopItem()
	item.State = "draft"
	item.Approvals = nil
	item.Draft.EditSnapshot = edit
	if err := factoryApplyCommand(&item, FactoryCommand{Kind: "approve", RequestID: "approve-snapshot"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if item.Approvals[0].PlanHash != factoryApprovalHash(item.Draft.Plan, item.Draft.Spec, edit) {
		t.Fatalf("approval hash = %q, want the versioned digest", item.Approvals[0].PlanHash)
	}
	if err := validateFactoryItem(item); err != nil {
		t.Fatalf("a snapshot-bearing approval failed validation: %v", err)
	}
	// The same approval recorded under the legacy digest is refused: the two
	// forms are never interchangeable.
	tampered := factoryCopy(item)
	tampered.Approvals[0].PlanHash = FactoryPlanHash(tampered.Approvals[0].Plan, tampered.Approvals[0].Spec)
	if err := validateFactoryItem(tampered); err == nil {
		t.Fatal("an approval carrying a snapshot accepted the legacy digest")
	}

	// The snapshot is deep-cloned into the approval: the draft and the approval
	// never share one selection.
	if item.Approvals[0].EditSnapshot == item.Draft.EditSnapshot {
		t.Fatal("the approval aliased the draft's snapshot")
	}
	item.Draft.EditSnapshot.Files[0].Path = "mutated.txt"
	if item.Approvals[0].EditSnapshot.Files[0].Path == "mutated.txt" {
		t.Fatal("the approval's selection aliased the draft's")
	}
}

func TestFactoryLegacyApprovalReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := factoryReq()
	created, err := store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Update(created.ID, "approve", created.Version, json.RawMessage(`{"approve":true}`), func(dst *FactoryItem) error {
		a := FactoryApproval{Revision: 1, Plan: dst.Draft.Plan, Spec: dst.Draft.Spec, PlanHash: FactoryPlanHash(dst.Draft.Plan, dst.Draft.Spec), Config: dst.Draft.Config, ApprovedAt: time.Now()}
		dst.Approvals = []FactoryApproval{a}
		dst.Grants = []FactoryGrant{{ID: "initial", Reason: "initial approval", AdditionalRounds: 5}}
		dst.State = "queued"
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.Approvals[0].EditSnapshot != nil {
		t.Fatal("a legacy approval gained a snapshot")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// The journal record carries no snapshot field at all, so the bytes a
	// pre-snapshot bridge wrote are the bytes this one replays.
	raw, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("editSnapshot")) {
		t.Fatal("the legacy journal gained an editSnapshot field")
	}

	replayed, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatalf("a legacy journal failed to replay: %v", err)
	}
	defer replayed.Close()
	got, err := replayed.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Approvals) != 1 || got.Approvals[0].PlanHash != FactoryPlanHash(got.Draft.Plan, got.Draft.Spec) {
		t.Fatalf("replayed approval = %+v, want the legacy digest", got.Approvals)
	}
	if got.Version != item.Version || got.State != "queued" {
		t.Fatalf("replayed item = version %d state %q", got.Version, got.State)
	}
}

func TestFactorySnapshotRouteRetryPreservesSelection(t *testing.T) {
	blob := plannerDigest([]byte("content\n"))
	edit := snapshotTestEdit(t, strings.Repeat("0", 40),
		SelectedFile{Path: "a.txt", Operation: plannerFileModify, BaseSHA256: plannerDigest([]byte("old\n")), ContentSHA256: blob},
	)
	now := time.Now().UTC()
	item := loopItem()
	item.State = "needs_input"
	item.Decision = &FactoryDecision{ID: "block", Kind: "conflict", Reason: "the run stalled"}
	item.Approvals[0].Config.ImplementerRouting = json.RawMessage(`{"profile":"chosen"}`)
	item.Approvals[0].Config.ReviewerRouting = json.RawMessage(`{"profile":"chosen"}`)
	item.Approvals[0].EditSnapshot = edit
	item.Approvals[0].PlanHash = factoryApprovalHash(item.Approvals[0].Plan, item.Approvals[0].Spec, edit)
	if err := validateFactoryItem(item); err != nil {
		t.Fatalf("fixture approval: %v", err)
	}

	// A route-only retry moves the revision, not the approved work: the new
	// approval carries the same selection and the same digest.
	routeRetry := factoryCopy(item)
	if err := factoryApplyCommand(&routeRetry, FactoryCommand{
		RequestID: "retry-route", Kind: "retry", DecisionID: "block", Reason: "the implementer model stalls",
		AdditionalRounds: 1, ImplementerRouting: json.RawMessage(`{"profile":"alternate"}`),
	}, now); err != nil {
		t.Fatal(err)
	}
	if len(routeRetry.Approvals) != 2 {
		t.Fatalf("route-only retry appended %d approvals, want 1", len(routeRetry.Approvals)-1)
	}
	carried := routeRetry.Approvals[1]
	if carried.EditSnapshot == nil || carried.EditSnapshot.SHA256 != edit.SHA256 {
		t.Fatalf("route-only retry dropped the selection: %+v", carried.EditSnapshot)
	}
	if carried.PlanHash != item.Approvals[0].PlanHash {
		t.Fatalf("route-only retry changed the digest to %q", carried.PlanHash)
	}
	if carried.EditSnapshot == item.Approvals[0].EditSnapshot {
		t.Fatal("route-only retry aliased the previous approval's selection")
	}
	if err := validateFactoryItem(routeRetry); err != nil {
		t.Fatalf("route-only retry produced an invalid item: %v", err)
	}

	// A replacement is taken as submitted: one that names no selection drops it.
	replacement := FactoryCreate{RequestID: "replacement", Title: item.Title, Plan: "a different plan\n", Spec: "a different spec\n", Config: item.Approvals[0].Config}
	planRetry := factoryCopy(item)
	if err := factoryApplyCommand(&planRetry, FactoryCommand{
		RequestID: "retry-plan", Kind: "retry", DecisionID: "block", Reason: "replace the plan",
		AdditionalRounds: 1, Replacement: &replacement,
	}, now); err != nil {
		t.Fatal(err)
	}
	dropped := planRetry.Approvals[len(planRetry.Approvals)-1]
	if dropped.EditSnapshot != nil {
		t.Fatalf("a replacement without a selection kept %+v", dropped.EditSnapshot)
	}
	if dropped.PlanHash != FactoryPlanHash(replacement.Plan, replacement.Spec) {
		t.Fatalf("a snapshot-free replacement hashed %q, want the legacy digest", dropped.PlanHash)
	}
	if err := validateFactoryItem(planRetry); err != nil {
		t.Fatalf("replacement retry produced an invalid item: %v", err)
	}

	// A replacement that names the selection keeps it, under the versioned
	// digest, exactly as an approval does.
	withSelection := replacement
	withSelection.RequestID = "replacement-selection"
	withSelection.EditSnapshot = edit
	keptRetry := factoryCopy(item)
	if err := factoryApplyCommand(&keptRetry, FactoryCommand{
		RequestID: "retry-selection", Kind: "retry", DecisionID: "block", Reason: "same plan, same selection",
		AdditionalRounds: 1, Replacement: &withSelection,
	}, now); err != nil {
		t.Fatal(err)
	}
	kept := keptRetry.Approvals[len(keptRetry.Approvals)-1]
	if kept.EditSnapshot == nil || kept.EditSnapshot.SHA256 != edit.SHA256 {
		t.Fatalf("a replacement with a selection lost it: %+v", kept.EditSnapshot)
	}
	if kept.PlanHash != factoryApprovalHash(withSelection.Plan, withSelection.Spec, edit) {
		t.Fatalf("replacement hash = %q, want the versioned digest", kept.PlanHash)
	}
	if err := validateFactoryItem(keptRetry); err != nil {
		t.Fatalf("selection replacement produced an invalid item: %v", err)
	}
}

// recordingFactory is a FactoryAPI whose create path is the real journal, so a
// handoff test proves the store's own request replay instead of a mock's.
type recordingFactory struct {
	store   *FactoryStore
	creates int
}

func (f *recordingFactory) Create(req FactoryCreate) (FactoryItem, error) {
	f.creates++
	return f.store.Create(req, time.Now().UTC())
}
func (f *recordingFactory) Get(id string) (FactoryItem, error) { return f.store.Get(id) }
func (f *recordingFactory) List() []FactoryItem                { return f.store.List() }
func (f *recordingFactory) Command(_ context.Context, id string, cmd FactoryCommand) (FactoryItem, error) {
	return FactoryItem{}, ErrFactoryUnavailable
}
func (f *recordingFactory) PrepareDraft(req FactoryCreate) (FactoryCreate, error) {
	return req, nil
}
func (f *recordingFactory) CreateDraftAuthorized(req, _ FactoryCreate, _ factoryPrincipal) (FactoryItem, error) {
	return f.store.Create(req, time.Now().UTC())
}
func (f *recordingFactory) ReplaceDraftAuthorized(string, FactoryCreate, uint64, factoryPrincipal) (FactoryItem, error) {
	return FactoryItem{}, ErrFactoryUnavailable
}
func (f *recordingFactory) CommandAuthorized(context.Context, string, FactoryCommand, factoryPrincipal) (FactoryItem, error) {
	return FactoryItem{}, ErrFactoryUnavailable
}

// handoffFixture is one planner with a captured snapshot and a real journal
// behind the handoff route. The snapshot and the handoff must name the same
// planner, so the view a capture created is returned rather than a second one.
func handoffFixture(t *testing.T) (*plannerTestEnv, PlannerView, PlannerSnapshotReply, *recordingFactory) {
	t.Helper()
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	snapshotWriteFile(t, dir, "a.txt", "one\nchanged\n", 0o644)
	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Plan: "the approved plan\n", Spec: "the approved spec\n", Paths: []string{"a.txt"}})
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	factory := &recordingFactory{store: store}
	env.svc.setFactory(factory)
	return env, view, reply, factory
}

// handoffBody is a normal factory create request for this env's project.
func handoffBody(env *plannerTestEnv, requestID string) map[string]any {
	return map[string]any{
		"requestId": requestID,
		"title":     "Add the thing",
		"config": map[string]any{
			"repoId":                   env.repo.ID,
			"projectRoot":              env.root,
			"targetBranch":             "main",
			"native":                   true,
			"noRequiredCiAcknowledged": true,
		},
	}
}

func (e *plannerTestEnv) handoff(id, snapshotID string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatalf("encode handoff body: %v", err)
	}
	return e.do(http.MethodPost, "/api/planners/"+id+"/snapshots/"+snapshotID+"/handoff", string(raw))
}

func decodeHandoffItem(t *testing.T, w *httptest.ResponseRecorder) FactoryItem {
	t.Helper()
	var body struct {
		Item FactoryItem `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode handoff item: %v (body %s)", err, w.Body.String())
	}
	return body.Item
}

func TestFactorySnapshotHandoffDedup(t *testing.T) {
	env, view, reply, factory := handoffFixture(t)

	first := env.handoff(view.ID, reply.ID, handoffBody(env, "handoff-1"))
	if first.Code != http.StatusCreated {
		t.Fatalf("handoff: status %d, body %s", first.Code, first.Body.String())
	}
	item := decodeHandoffItem(t, first)
	// The draft is built from the bytes the capture froze, never from a
	// workspace, and it carries the reviewed selection.
	plan := snapshotArtifactOfKind(t, reply, plannerArtifactPlan)
	spec := snapshotArtifactOfKind(t, reply, plannerArtifactSpec)
	if item.Draft.Plan != plan.Content || item.Draft.Spec != spec.Content {
		t.Fatalf("draft text = %q/%q, want the snapshot's artifacts %q/%q", item.Draft.Plan, item.Draft.Spec, plan.Content, spec.Content)
	}
	if item.Draft.EditSnapshot == nil || item.Draft.EditSnapshot.SHA256 != reply.SHA256 {
		t.Fatalf("draft selection = %+v, want snapshot %s", item.Draft.EditSnapshot, reply.SHA256)
	}
	if len(item.Draft.EditSnapshot.Files) != 1 || item.Draft.EditSnapshot.Files[0].Path != "a.txt" {
		t.Fatalf("draft selection files = %+v", item.Draft.EditSnapshot.Files)
	}

	// A replayed request ID replays the recorded draft: the store's own replay
	// identity is what keeps a retried handoff from creating a second item.
	// The status distinguishes a replay from a creation only for the real
	// factory service, so this fixture accepts either.
	second := env.handoff(view.ID, reply.ID, handoffBody(env, "handoff-1"))
	if second.Code != http.StatusCreated && second.Code != http.StatusOK {
		t.Fatalf("replayed handoff: status %d, body %s", second.Code, second.Body.String())
	}
	again := decodeHandoffItem(t, second)
	if again.ID != item.ID {
		t.Fatalf("a replayed handoff created a second item: %q then %q", item.ID, again.ID)
	}
	if got := factory.List(); len(got) != 1 {
		t.Fatalf("the journal holds %d items, want 1", len(got))
	}
}

func TestFactorySnapshotHandoffRejectsMismatchAndCorruption(t *testing.T) {
	env, view, reply, factory := handoffFixture(t)

	// A configuration naming another repository is a conflict, and creates
	// nothing.
	foreign := handoffBody(env, "handoff-foreign")
	foreign["config"].(map[string]any)["repoId"] = "another-repo"
	if got := env.handoff(view.ID, reply.ID, foreign); got.Code != http.StatusConflict {
		t.Fatalf("foreign repository: status %d, body %s", got.Code, got.Body.String())
	}
	foreignRoot := handoffBody(env, "handoff-root")
	foreignRoot["config"].(map[string]any)["projectRoot"] = t.TempDir()
	if got := env.handoff(view.ID, reply.ID, foreignRoot); got.Code != http.StatusConflict {
		t.Fatalf("foreign project: status %d, body %s", got.Code, got.Body.String())
	}
	// An unknown snapshot or planner is not found.
	if got := env.handoff(view.ID, "0123456789abcdef0123456789abcdef", handoffBody(env, "handoff-unknown")); got.Code != http.StatusNotFound {
		t.Fatalf("unknown snapshot: status %d, body %s", got.Code, got.Body.String())
	}
	if got := env.handoff("0123456789abcdef0123456789abcdef", reply.ID, handoffBody(env, "handoff-planner")); got.Code != http.StatusNotFound {
		t.Fatalf("unknown planner: status %d, body %s", got.Code, got.Body.String())
	}
	if got := factory.List(); len(got) != 0 {
		t.Fatalf("a refused handoff created %d items", len(got))
	}

	// A snapshot whose stored bytes no longer hash to what it recorded is
	// corruption, reported rather than repaired, and it creates nothing.
	file := reply.Files[0]
	blob := filepath.Join(env.stateDir, plannerDirName, plannerBlobDirName, file.ContentSHA256)
	if err := os.WriteFile(blob, []byte("tampered\n"), plannerFileMode); err != nil {
		t.Fatal(err)
	}
	if got := env.handoff(view.ID, reply.ID, handoffBody(env, "handoff-corrupt")); got.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt snapshot: status %d, body %s", got.Code, got.Body.String())
	}
	if got := factory.List(); len(got) != 0 {
		t.Fatalf("a corrupt handoff created %d items", len(got))
	}
}

func TestFactorySnapshotNoPlannerWorkspaceNeeded(t *testing.T) {
	ctx := context.Background()
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	snapshotWriteFile(t, dir, "a.txt", "one\nchanged\n", 0o644)
	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Plan: "the approved plan\n", Spec: "the approved spec\n", Paths: []string{"a.txt"}})
	base := reply.BaseCommit

	// A factory checkout at the same base commit, over the bridge state
	// directory the capture wrote its blobs into.
	f := newSeedFixtureFromClone(t, dir, env.stateDir)
	if got := gitText(t, f.work, "rev-parse", "HEAD"); got != base {
		t.Fatalf("clone base = %q, want %q", got, base)
	}
	// The planner workspace is gone before anything is reviewed or seeded.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	env.svc.setFactory(&recordingFactory{store: store})
	got := env.handoff(view.ID, reply.ID, handoffBody(env, "handoff-1"))
	if got.Code != http.StatusCreated {
		t.Fatalf("handoff without the workspace: status %d, body %s", got.Code, got.Body.String())
	}
	item := decodeHandoffItem(t, got)
	plan := snapshotArtifactOfKind(t, reply, plannerArtifactPlan)
	if item.Draft.Plan != plan.Content || item.Draft.EditSnapshot == nil {
		t.Fatalf("draft = %+v, want the captured text %q and selection", item.Draft, plan.Content)
	}
	// Review still works: the record and its blobs are the durable store.
	if review := env.getSnapshot(view.ID, reply.ID); review.Code != http.StatusOK {
		t.Fatalf("review without the workspace: status %d, body %s", review.Code, review.Body.String())
	}

	// Seeding works from the blob store alone.
	seedItem := f.item(item.ID, base, item.Draft.EditSnapshot)
	worker := f.worker(item.ID)
	head, _, err := f.g.Seed(ctx, seedItem, worker)
	if err != nil {
		t.Fatalf("seed without the planner workspace: %v", err)
	}
	a, err := os.ReadFile(filepath.Join(f.work, "a.txt"))
	if err != nil || string(a) != "one\nchanged\n" {
		t.Fatalf("seeded a.txt = %q, %v; want the captured bytes", a, err)
	}
	if paths := seedCommittedPaths(t, f.work, head); len(paths) != 3 {
		t.Fatalf("seeded commit paths = %v, want the selection and the two documents", paths)
	}
}
