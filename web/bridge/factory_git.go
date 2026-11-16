package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type factoryGit struct {
	fleet *Fleet
	// beforeSeedWrite runs before each selected file a seed applies, so a test
	// can interrupt a seed at an exact point instead of racing a real failure.
	// A seed that stops here has already changed the worktree, which is what
	// makes the effect journal record an uncertain outcome.
	beforeSeedWrite func(path string) error
}

func newFactoryGit(fleet *Fleet) *factoryGit { return &factoryGit{fleet: fleet} }

var factorySafeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,79}$`)

func (g *factoryGit) checkout(item FactoryItem, worker FactoryWorker) (Agent, *agentRuntime, error) {
	if !factorySafeID.MatchString(item.ID) || item.Branch != "marshal/factory/"+item.ID {
		return Agent{}, nil, fmt.Errorf("factory: unsafe item ID or branch")
	}
	a, ok := g.fleet.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.SessionID != worker.SessionID || a.FactoryParked || a.SourceKind != "git" || a.SourceRef != item.RepoID {
		return Agent{}, nil, fmt.Errorf("factory: checkout ownership mismatch")
	}
	rt, err := g.fleet.runtimeForAgent(worker.AgentID)
	if err != nil {
		return Agent{}, nil, err
	}
	if g.fleet.sessionIDFor(rt) != worker.SessionID || g.fleet.git == nil {
		return Agent{}, nil, fmt.Errorf("factory: checkout runtime mismatch")
	}
	return a, rt, nil
}

func (g *factoryGit) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := g.fleet.git.runCtx(ctx, dir, Credential{}, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *factoryGit) state(ctx context.Context, dir string) (head, tree, branch string, dirty bool, err error) {
	if head, err = g.git(ctx, dir, "rev-parse", "HEAD"); err != nil {
		return
	}
	if tree, err = g.git(ctx, dir, "rev-parse", "HEAD^{tree}"); err != nil {
		return
	}
	if branch, err = g.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil {
		return
	}
	var status string
	if status, err = g.git(ctx, dir, "status", "--porcelain", "--untracked-files=all"); err != nil {
		return
	}
	dirty = status != ""
	return
}

// factorySeedEdit is one selected file a seed will apply: where it goes, what
// it becomes, and the bytes the approval froze. A delete carries no content.
type factorySeedEdit struct {
	Path       string
	Operation  string
	Content    []byte
	Executable bool
}

// Seed creates the one owned branch and commits the exact approved inputs via ACP.
//
// A snapshot-bearing approval is applied as well: every selected file is
// verified against the blob store and the approved base commit before anything
// is written, so a selection that cannot be applied exactly leaves the checkout
// untouched. Bytes come from this bridge's own store, never from the planner's
// workspace, which may no longer exist.
func (g *factoryGit) Seed(ctx context.Context, item FactoryItem, worker FactoryWorker) (head, tree string, err error) {
	a, rt, err := g.checkout(item, worker)
	if err != nil {
		return "", "", err
	}
	if worker.Role != "implementer" || item.BaseSHA == "" || len(item.Approvals) == 0 {
		return "", "", fmt.Errorf("factory: seed requires an approved implementer and base")
	}
	old, _, _, dirty, err := g.state(ctx, a.Project)
	if err != nil { // A prepared checkout starts detached; read HEAD without a branch.
		old, err = g.git(ctx, a.Project, "rev-parse", "HEAD")
		if err != nil {
			return "", "", err
		}
		status, e := g.git(ctx, a.Project, "status", "--porcelain", "--untracked-files=all")
		if e != nil {
			return "", "", e
		}
		dirty = status != ""
	}
	if dirty || old != item.BaseSHA {
		return "", "", fmt.Errorf("factory: seed checkout is dirty or not at approved base")
	}
	ap := item.Approvals[len(item.Approvals)-1]
	if ap.EditSnapshot != nil && ap.EditSnapshot.BaseCommit != item.BaseSHA {
		return "", "", fmt.Errorf("factory: snapshot base does not match approved base")
	}
	// The whole selection is verified before the branch is created: a seed that
	// cannot be applied in full must not leave a half-written branch behind.
	edits, err := g.preflightSeed(ctx, a.Project, item.BaseSHA, ap.EditSnapshot)
	if err != nil {
		return "", "", err
	}
	if _, err = g.git(ctx, a.Project, "show-ref", "--verify", "refs/heads/"+item.Branch); err == nil {
		return "", "", fmt.Errorf("factory: branch already exists")
	}
	if _, err = g.git(ctx, a.Project, "switch", "-c", item.Branch); err != nil {
		return "", "", err
	}
	base := filepath.Join(a.Project, "docs", "marshal-factory")
	dir := filepath.Join(base, item.ID)
	for _, p := range []string{filepath.Join(a.Project, "docs"), base, dir} {
		info, e := os.Lstat(p)
		if e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", "", fmt.Errorf("factory: unsafe docs path %s", p)
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", "", e
		}
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	staged := make([]string, 0, 2+len(edits))
	for name, data := range map[string]string{"plan.md": ap.Plan, "spec.md": ap.Spec} {
		p := filepath.Join(dir, name)
		if _, e := os.Lstat(p); e == nil {
			return "", "", fmt.Errorf("factory: seed file already exists")
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", "", e
		}
		if err = os.WriteFile(p, []byte(data), 0o644); err != nil {
			return "", "", err
		}
		staged = append(staged, "docs/marshal-factory/"+item.ID+"/"+name)
	}
	for _, edit := range edits {
		if g.beforeSeedWrite != nil {
			if err = g.beforeSeedWrite(edit.Path); err != nil {
				return "", "", err
			}
		}
		if err = applySeedEdit(a.Project, edit); err != nil {
			return "", "", err
		}
		staged = append(staged, edit.Path)
	}
	// Exactly the intended paths are staged. Never "git add -A": a seed applies
	// an approved selection, not whatever the checkout happens to hold.
	if _, err = g.git(ctx, a.Project, append([]string{"add", "--"}, staged...)...); err != nil {
		return "", "", err
	}
	if _, err = g.fleet.commitSession(ctx, rt, "Seed factory plan and spec for "+item.ID); err != nil {
		return "", "", err
	}
	head, tree, branch, dirty, err := g.state(ctx, a.Project)
	if err != nil {
		return "", "", err
	}
	if branch != item.Branch || dirty || head == old {
		return "", "", fmt.Errorf("factory: seed commit did not produce a clean owned branch")
	}
	if err = g.verifySeed(ctx, a.Project, head, edits); err != nil {
		return "", "", err
	}
	return head, tree, nil
}

// raw runs git and returns its output untrimmed, because a seed compares bytes.
func (g *factoryGit) raw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return g.fleet.git.runCtx(ctx, dir, Credential{}, args...)
}

// baseFileAt reads one repository-relative path as it was at commit. A path
// that commit does not hold is reported as absent rather than as an error.
func (g *factoryGit) baseFileAt(ctx context.Context, dir, commit, rel string) ([]byte, bool, error) {
	spec := commit + ":" + rel
	if _, err := g.raw(ctx, dir, "cat-file", "-e", spec); err != nil {
		return nil, false, nil
	}
	data, err := g.raw(ctx, dir, "show", spec)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// preflightSeed checks every selected file before the seed mutates anything: the
// approved base is the one the snapshot was taken against, the path stays inside
// the checkout and traverses no symlink, the blob store holds the exact bytes the
// file must become, and the file is at the base commit exactly as the approval
// recorded it. Only a selection that passes all of this is written.
func (g *factoryGit) preflightSeed(ctx context.Context, dir, base string, snap *EditSnapshot) ([]factorySeedEdit, error) {
	if snap == nil {
		return nil, nil
	}
	if err := factoryValidateSnapshot(snap); err != nil {
		return nil, err
	}
	blobs := filepath.Join(g.fleet.stateDir, plannerDirName, plannerBlobDirName)
	edits := make([]factorySeedEdit, 0, len(snap.Files))
	for _, file := range snap.Files {
		if err := factoryInsideCheckout(dir, file.Path); err != nil {
			return nil, err
		}
		if err := factoryNoSymlinkComponent(dir, file.Path); err != nil {
			return nil, err
		}
		atBase, present, err := g.baseFileAt(ctx, dir, base, file.Path)
		if err != nil {
			return nil, err
		}
		switch file.Operation {
		case plannerFileCreate:
			if present {
				return nil, fmt.Errorf("factory: snapshot creates %s, which the approved base already holds", file.Path)
			}
		case plannerFileModify, plannerFileDelete:
			if !present {
				return nil, fmt.Errorf("factory: snapshot %ss %s, which the approved base does not hold", file.Operation, file.Path)
			}
			if plannerDigest(atBase) != file.BaseSHA256 {
				return nil, fmt.Errorf("factory: %s changed since the snapshot was taken", file.Path)
			}
		}
		edit := factorySeedEdit{Path: file.Path, Operation: file.Operation, Executable: file.Executable}
		if file.Operation != plannerFileDelete {
			data, err := os.ReadFile(filepath.Join(blobs, file.ContentSHA256))
			if err != nil {
				return nil, fmt.Errorf("factory: snapshot blob for %s is missing: %v", file.Path, err)
			}
			if plannerDigest(data) != file.ContentSHA256 {
				return nil, fmt.Errorf("factory: snapshot blob for %s does not hash to what the approval recorded", file.Path)
			}
			edit.Content = data
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

// factoryInsideCheckout refuses a path that does not resolve inside root. The
// repository-relative rule already forbids "..", and this is the check that
// holds even if that one is ever relaxed.
func factoryInsideCheckout(root, rel string) error {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return fmt.Errorf("factory: selected path %q is outside the checkout", rel)
	}
	return nil
}

// factoryNoSymlinkComponent refuses a path whose existing elements are symlinks.
// A capture refuses a symlink, so a component that is now one appeared after the
// selection was reviewed, and writing through it would change a file the user
// never selected.
func factoryNoSymlinkComponent(root, rel string) error {
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("factory: selected path %q traverses a symlink", rel)
		}
	}
	return nil
}

// applySeedEdit writes or removes exactly one selected file, through a temp file
// in the same directory so the destination never resolves through a symlink, and
// with the one mode the selection recorded.
func applySeedEdit(root string, edit factorySeedEdit) error {
	if err := factoryInsideCheckout(root, edit.Path); err != nil {
		return err
	}
	if err := factoryNoSymlinkComponent(root, edit.Path); err != nil {
		return err
	}
	abs := filepath.Join(root, filepath.FromSlash(edit.Path))
	info, err := os.Lstat(abs)
	present := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if present && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("factory: selected path %q is not a regular file", edit.Path)
	}
	if edit.Operation == plannerFileDelete {
		if !present {
			return fmt.Errorf("factory: selected path %q is not present to delete", edit.Path)
		}
		return os.Remove(abs)
	}
	parent := filepath.Dir(abs)
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// Re-check after creating the directory: MkdirAll follows whatever it
	// finds, so a component that became a symlink in the meantime is refused
	// here rather than written through.
	if err = factoryNoSymlinkComponent(root, edit.Path); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if edit.Executable {
		mode = 0o755
	}
	tmp, err := os.CreateTemp(parent, ".marshal-seed-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return cause
	}
	if _, err = tmp.Write(edit.Content); err != nil {
		return fail(err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err = tmp.Sync(); err != nil {
		return fail(err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err = os.Rename(name, abs); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// verifySeed holds the commit to the selection it applied: every selected file's
// committed bytes are the frozen bytes, and every deleted path is gone.
func (g *factoryGit) verifySeed(ctx context.Context, dir, head string, edits []factorySeedEdit) error {
	if len(edits) == 0 {
		return nil
	}
	// Status is read once more after the commit: an unselected change the
	// session commit did not take would otherwise leave a silent residue.
	status, err := g.git(ctx, dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("factory: seed left uncommitted changes")
	}
	for _, edit := range edits {
		committed, present, err := g.baseFileAt(ctx, dir, head, edit.Path)
		if err != nil {
			return err
		}
		if edit.Operation == plannerFileDelete {
			if present {
				return fmt.Errorf("factory: seed did not delete %s", edit.Path)
			}
			continue
		}
		if !present || !bytes.Equal(committed, edit.Content) {
			return fmt.Errorf("factory: seed committed different bytes for %s", edit.Path)
		}
	}
	return nil
}

func (g *factoryGit) remoteHead(ctx context.Context, dir, branch string, cred Credential) (string, error) {
	out, err := g.fleet.git.runCtx(ctx, dir, cred, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
		return "", fmt.Errorf("factory: unexpected remote ref response")
	}
	return fields[0], nil
}

// Publish only sends the observed local HEAD when the remote still equals the
// controller's expected SHA. A lost push response is resolved by rereading it.
func (g *factoryGit) Publish(ctx context.Context, item FactoryItem, worker FactoryWorker, expectedRemote string) (head, tree string, err error) {
	a, _, err := g.checkout(item, worker)
	if err != nil {
		return "", "", err
	}
	if worker.Role != "implementer" {
		return "", "", fmt.Errorf("factory: only implementer can publish")
	}
	head, tree, branch, dirty, err := g.state(ctx, a.Project)
	if err != nil {
		return "", "", err
	}
	if branch != item.Branch || dirty {
		return "", "", fmt.Errorf("factory: publication requires clean owned branch")
	}
	repo, ok := g.fleet.ws.Repo(item.RepoID)
	if !ok {
		return "", "", ErrUnknownRepo
	}
	cred, err := g.fleet.creds.Resolve(ctx, DefaultOwnerID, repo.CredRef)
	if err != nil {
		return "", "", err
	}
	remote, err := g.remoteHead(ctx, a.Project, item.Branch, cred)
	if err != nil {
		return "", "", err
	}
	if remote == head {
		return head, tree, nil
	}
	if remote != expectedRemote {
		return "", "", fmt.Errorf("factory: remote branch changed from expected SHA")
	}
	_, pushErr := g.fleet.git.Push(a.Project, item.Branch, cred)
	remote, readErr := g.remoteHead(ctx, a.Project, item.Branch, cred)
	if readErr == nil && remote == head {
		return head, tree, nil
	}
	if pushErr != nil {
		return "", "", fmt.Errorf("factory: push outcome uncertain: %w", pushErr)
	}
	if readErr != nil {
		return "", "", fmt.Errorf("factory: pushed branch but remote verification failed: %w", readErr)
	}
	return "", "", fmt.Errorf("factory: remote did not reach expected head")
}
