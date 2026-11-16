package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The snapshot half of the planner: the user's handoff. A capture freezes the
// plan and spec text the user approved, plus the repository-relative files
// they selected, into an immutable record and a private content-addressed blob
// store under the bridge's own state directory.
//
// What this file refuses to do is as load-bearing as what it does:
//
//   - It never writes to the planner's workspace and never creates a commit, so
//     a model editing concurrently is not surprised by a capture.
//   - It never treats the workspace as the durable store: bytes live under
//     stateDir, so a review still works after the workspace is gone.
//   - It never silently drops a selected path. A path it will not capture is
//     rejected with a reason the caller can show the user.
//   - It never claims an atomic filesystem snapshot. Each file is read twice
//     and refused if it changed, and the user reviews the frozen bytes and
//     their hashes before confirming.
//
// Filename exclusions are a safety net, not secret detection: nothing here
// claims to find every credential, which is why the review shows the selected
// contents.
const (
	// plannerSnapshotPlanLimit and plannerSnapshotSpecLimit bound the text a
	// user hands over as the approved plan and specification.
	plannerSnapshotPlanLimit = 512 << 10
	plannerSnapshotSpecLimit = 512 << 10
	// plannerSnapshotContextLimit bounds the extra prompt text a user supplies
	// alongside a prompt.
	plannerSnapshotContextLimit = 64 << 10
	// plannerSnapshotFileLimit, plannerSnapshotTotalLimit and
	// plannerSnapshotFileCount bound one capture.
	plannerSnapshotFileLimit  = 1 << 20
	plannerSnapshotTotalLimit = 10 << 20
	plannerSnapshotFileCount  = 100
	// plannerSnapshotPathLimit bounds how many paths one request may name,
	// before any of them costs an Lstat or a git call.
	plannerSnapshotPathLimit = 512
	// plannerSnapshotDiffLimit caps one file's diff text.
	plannerSnapshotDiffLimit = 256 << 10
	// plannerSnapshotBinaryProbe is how much of a file is inspected for a NUL
	// byte before its diff is reported as binary.
	plannerSnapshotBinaryProbe = 8000

	plannerSnapshotDirName = "snapshots"
	plannerBlobDirName     = "blobs"

	plannerArtifactPlan = "plan"
	plannerArtifactSpec = "spec"

	plannerFileCreate = "create"
	plannerFileModify = "modify"
	plannerFileDelete = "delete"
)

// Snapshot errors. A file that changes mid-capture is the caller's signal to
// retry; a snapshot whose stored bytes no longer hash to what it recorded is
// corrupt, and is reported rather than repaired.
var (
	errPlannerFileChanged     = errors.New("file changed during capture; retry")
	errPlannerSnapshotCorrupt = errors.New("snapshot corrupt")
	// errPlannerBaseTooLarge marks a base-commit object bigger than a capture
	// may hold: it is refused before it is read, and reported as a rejection
	// rather than failing the whole capture.
	errPlannerBaseTooLarge = errors.New("base content exceeds the capture limit")
)

// plannerCommitPattern is the shape of a base commit id: a lowercase hex
// object name, sha1 or sha256. Anything else is refused rather than handed to
// git as an argument.
var plannerCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// plannerSnapshotReadFile is the production reader: the bytes of one path and
// its lstat. Capture calls it twice and compares the two results, so a file
// that changes underneath the capture is refused rather than half-captured.
func plannerSnapshotReadFile(path string) ([]byte, os.FileInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// plannerGitHead resolves the base commit a capture is taken against: the
// planner workspace's HEAD, read through the bridge's git runner. Capture
// never moves it.
func plannerGitHead(fleet *Fleet) func(ctx context.Context, dir string) (string, error) {
	return func(ctx context.Context, dir string) (string, error) {
		if fleet == nil || fleet.git == nil {
			return "", fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
		}
		out, err := fleet.git.runCtx(ctx, dir, Credential{}, "rev-parse", "HEAD")
		if err != nil {
			return "", fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
		commit := strings.TrimSpace(string(out))
		if !plannerCommitPattern.MatchString(commit) {
			return "", fmt.Errorf("%w: the planner workspace's HEAD is not a commit id", errPlannerUnavailable)
		}
		return commit, nil
	}
}

// plannerGitBaseFile reads one path as it was at the base commit. exists is
// false when the path is not in that commit, which is what makes an untracked
// file a create and a removed tracked file a delete.
func plannerGitBaseFile(fleet *Fleet) func(ctx context.Context, dir, commit, rel string) ([]byte, bool, error) {
	return func(ctx context.Context, dir, commit, rel string) ([]byte, bool, error) {
		if fleet == nil || fleet.git == nil {
			return nil, false, fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
		}
		spec := commit + ":" + rel
		// A commit that does not resolve is a repository error, and it is what
		// makes a failed path lookup below readable: only when the commit is
		// sound does a missing object name mean "not in this commit". Reading
		// every failure as absence would relabel a tracked file as a create.
		if _, err := fleet.git.runCtx(ctx, dir, Credential{}, "cat-file", "-e", commit+"^{commit}"); err != nil {
			return nil, false, fmt.Errorf("%w: base commit %s does not resolve: %v", errPlannerUnavailable, commit, err)
		}
		if _, err := fleet.git.runCtx(ctx, dir, Credential{}, "cat-file", "-e", spec); err != nil {
			return nil, false, nil
		}
		// The base side is read into memory, so an object larger than any
		// capture may hold is refused before it is materialised.
		size, err := fleet.git.runCtx(ctx, dir, Credential{}, "cat-file", "-s", spec)
		if err != nil {
			return nil, false, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
		if n, parseErr := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64); parseErr == nil && n > plannerSnapshotFileLimit {
			return nil, false, fmt.Errorf("%w: base content exceeds %d bytes", errPlannerBaseTooLarge, plannerSnapshotFileLimit)
		}
		out, err := fleet.git.runCtx(ctx, dir, Credential{}, "show", spec)
		if err != nil {
			return nil, false, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
		return out, true, nil
	}
}

// plannerGitIgnored reports whether a path that is not in the base commit is
// ignored by the repository's own rules. An ignored untracked file is not
// something a handoff captures: it is build output or a local secret.
func plannerGitIgnored(fleet *Fleet) func(ctx context.Context, dir, rel string) (bool, error) {
	return func(ctx context.Context, dir, rel string) (bool, error) {
		if fleet == nil || fleet.git == nil {
			return false, fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
		}
		out, err := fleet.git.runCtx(ctx, dir, Credential{}, "ls-files", "--others", "--ignored", "--exclude-standard", "--", rel)
		if err != nil {
			return false, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == rel {
				return true, nil
			}
		}
		return false, nil
	}
}

// plannerDigest is the lowercase hex SHA-256 the blob store and the snapshot
// metadata are keyed by.
func plannerDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CreateSnapshot freezes the plan and spec text and the selected files. It
// takes the request context: a capture the caller abandoned stops instead of
// writing a snapshot nobody asked for. Blobs are written before the record, so
// an interrupted capture leaves unreachable bytes, never a record that names
// missing ones.
func (s *PlannerService) CreateSnapshot(ctx context.Context, id string, req PlannerSnapshotRequest) (PlannerSnapshotReply, error) {
	rec, err := s.plannerRecord(id)
	if err != nil {
		return PlannerSnapshotReply{}, err
	}
	if len(req.Plan) > plannerSnapshotPlanLimit {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: plan exceeds %d bytes", errPlannerInvalid, plannerSnapshotPlanLimit)
	}
	if len(req.Spec) > plannerSnapshotSpecLimit {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: spec exceeds %d bytes", errPlannerInvalid, plannerSnapshotSpecLimit)
	}
	artifacts := plannerSnapshotArtifacts(req.Plan, req.Spec)
	if len(artifacts) == 0 && len(req.Paths) == 0 {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: a snapshot needs plan text, spec text or at least one selected path", errPlannerInvalid)
	}
	// Bounded before the loop: an unbounded list would let one request drive a
	// git call per junk path.
	if len(req.Paths) > plannerSnapshotPathLimit {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: a capture selects at most %d paths", errPlannerInvalid, plannerSnapshotPathLimit)
	}
	dir := workspaceDirFor(s.stateDir, rec.WorkspaceID)
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: the planner's workspace is not available", errPlannerUnavailable)
	}
	base, err := s.snapshotHead(ctx, dir)
	if err != nil {
		return PlannerSnapshotReply{}, err
	}

	var (
		files     []SelectedFile
		diffs     []plannerSnapshotDiff
		rejected  []PlannerRejection
		total     int
		seenPaths = make(map[string]bool, len(req.Paths))
	)
	for _, raw := range req.Paths {
		if err := ctx.Err(); err != nil {
			return PlannerSnapshotReply{}, err
		}
		rel, reason := plannerSelectedPath(raw)
		if reason != "" {
			rejected = append(rejected, PlannerRejection{Path: raw, Reason: reason})
			continue
		}
		if seenPaths[rel] {
			rejected = append(rejected, PlannerRejection{Path: rel, Reason: "duplicate path"})
			continue
		}
		seenPaths[rel] = true
		if len(files) >= plannerSnapshotFileCount {
			rejected = append(rejected, PlannerRejection{Path: rel, Reason: fmt.Sprintf("a snapshot holds at most %d files", plannerSnapshotFileCount)})
			continue
		}
		captured, reason, err := s.captureSelectedFile(ctx, dir, base, rel)
		if err != nil {
			return PlannerSnapshotReply{}, err
		}
		if reason != "" {
			rejected = append(rejected, PlannerRejection{Path: rel, Reason: reason})
			continue
		}
		if total+len(captured.content) > plannerSnapshotTotalLimit {
			rejected = append(rejected, PlannerRejection{Path: rel, Reason: fmt.Sprintf("a snapshot holds at most %d bytes total", plannerSnapshotTotalLimit)})
			continue
		}
		total += len(captured.content)
		// A delete has no content and no ContentSHA256, so there is nothing to
		// store. A file with bytes — including an empty one, whose digest is
		// the digest of no bytes — names a blob the record must be able to
		// verify, so it is written here.
		if captured.file.ContentSHA256 != "" {
			if s.beforeBlobWrite != nil {
				s.beforeBlobWrite()
			}
			if err := ctx.Err(); err != nil {
				return PlannerSnapshotReply{}, err
			}
			if err := s.writeBlob(captured.content); err != nil {
				return PlannerSnapshotReply{}, err
			}
		}
		files = append(files, captured.file)
		diffs = append(diffs, plannerSnapshotDiff{Path: captured.file.Path, Diff: captured.diff})
	}
	if len(files) == 0 && len(artifacts) == 0 {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: no selected file could be captured and no plan or spec text was supplied", errPlannerInvalid)
	}

	snapshotID, err := newPlannerID()
	if err != nil {
		return PlannerSnapshotReply{}, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
	}
	edit := EditSnapshot{BaseCommit: base, Files: files}
	if edit.Files == nil {
		edit.Files = []SelectedFile{}
	}
	if edit.SHA256, err = plannerSnapshotDigest(edit); err != nil {
		return PlannerSnapshotReply{}, err
	}
	snapshot := plannerSnapshotRecord{
		ID:           snapshotID,
		PlannerID:    rec.ID,
		ProjectRoot:  rec.ProjectRoot,
		EditSnapshot: edit,
		Artifacts:    artifacts,
		Diffs:        diffs,
		CreatedAt:    s.nowUTC(),
	}
	// The record is written last and never rewritten: a snapshot is the
	// evidence a review is held to.
	if err := s.writeSnapshot(ctx, snapshot); err != nil {
		return PlannerSnapshotReply{}, err
	}
	if rejected == nil {
		rejected = []PlannerRejection{}
	}
	return PlannerSnapshotReply{PlannerSnapshotView: plannerSnapshotView(snapshot), Rejected: rejected}, nil
}

// Snapshot answers one captured handoff for review, verifying that every byte
// it references is still the byte it recorded. A missing or changed blob is
// reported as corruption; nothing here repairs a snapshot into looking sound.
func (s *PlannerService) Snapshot(id, snapshotID string) (PlannerSnapshotView, error) {
	snapshot, err := s.readSnapshot(id, snapshotID)
	if err != nil {
		return PlannerSnapshotView{}, err
	}
	if err := s.verifySnapshot(snapshot); err != nil {
		return PlannerSnapshotView{}, err
	}
	return plannerSnapshotView(snapshot), nil
}

// Context is the bounded briefing a planner record supports: the approved plan
// and spec it captured, the artifacts it merely references, and its recorded
// decisions and unresolved questions. A source the record does not hold is
// named in Absent and appears nowhere else.
func (s *PlannerService) Context(id string) (PlannerContextView, error) {
	rec, err := s.plannerRecord(id)
	if err != nil {
		return PlannerContextView{}, err
	}
	snapshots, err := s.listSnapshots(id)
	if err != nil {
		return PlannerContextView{}, err
	}
	view := PlannerContextView{
		Artifacts:  []PlannerContextArtifact{},
		Decisions:  []PlannerContextDecision{},
		Unresolved: []PlannerContextQuestion{},
		Absent:     []string{},
	}

	// The newest capture wins per kind: an approved plan is the one the user
	// last froze, not an older draft of it.
	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].CreatedAt.Equal(snapshots[j].CreatedAt) {
			return snapshots[i].ID < snapshots[j].ID
		}
		return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt)
	})
	approved := map[string]struct {
		snapshotID string
		artifact   PlannerSnapshotArtifact
	}{}
	for _, snapshot := range snapshots {
		for _, artifact := range snapshot.Artifacts {
			if _, ok := approved[artifact.Kind]; ok {
				continue
			}
			// A briefing may only report as approved what still hashes to its
			// record: a stale or edited snapshot is skipped, so an older
			// verified capture can answer instead of a corrupt newer one.
			if plannerDigest([]byte(artifact.Content)) != artifact.SHA256 {
				continue
			}
			approved[artifact.Kind] = struct {
				snapshotID string
				artifact   PlannerSnapshotArtifact
			}{snapshotID: snapshot.ID, artifact: artifact}
		}
	}
	for _, kind := range []string{plannerArtifactPlan, plannerArtifactSpec} {
		found, ok := approved[kind]
		if !ok {
			view.Absent = append(view.Absent, kind)
			continue
		}
		view.Artifacts = append(view.Artifacts, PlannerContextArtifact{
			Kind:       kind,
			SHA256:     found.artifact.SHA256,
			Source:     "snapshot:" + found.snapshotID,
			Approved:   true,
			SnapshotID: found.snapshotID,
		})
	}
	// A record artifact is a reference the planner produced on its own: it is
	// reported as an unapproved proposal, with no content and no hash this
	// bridge can vouch for.
	for _, artifact := range rec.Artifacts {
		kind := artifact.Kind
		if kind == "" {
			kind = "artifact"
		}
		view.Artifacts = append(view.Artifacts, PlannerContextArtifact{
			Kind:     kind,
			Source:   "planner:" + rec.ID,
			Approved: false,
			Ref:      artifact.Ref,
		})
	}
	for _, decision := range rec.Requests {
		source := "request:" + decision.RequestID
		if decision.State == plannerDecisionPending {
			view.Unresolved = append(view.Unresolved, PlannerContextQuestion{
				Source:    source,
				RequestID: decision.RequestID,
				Method:    decision.Method,
				State:     decision.State,
				At:        decision.At,
			})
			continue
		}
		view.Decisions = append(view.Decisions, PlannerContextDecision{
			Source:    source,
			RequestID: decision.RequestID,
			Method:    decision.Method,
			State:     decision.State,
			At:        decision.At,
		})
	}
	if len(view.Decisions) == 0 {
		view.Absent = append(view.Absent, "decisions")
	}
	if len(view.Unresolved) == 0 {
		view.Absent = append(view.Absent, "unresolved")
	}
	return view, nil
}

// errPlannerSymlink marks the one refusal a capture reports as "symlink".
var errPlannerSymlink = errors.New("symlink")

// plannerNoSymlinkComponent refuses a path whose existing elements are
// symlinks. A leaf symlink would be caught by the caller's own mode check, but
// joining a path through a directory symlink reads whatever that directory
// points at, which need not be inside the workspace at all.
func plannerNoSymlinkComponent(root, rel string) error {
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
			return errPlannerSymlink
		}
	}
	return nil
}

// plannerCapturedFile is one file a capture froze, with the bytes whose blob is
// written next and the diff text the review will show.
type plannerCapturedFile struct {
	file    SelectedFile
	content []byte
	diff    string
}

// captureSelectedFile reads one selected path twice and freezes its metadata.
// A non-empty reason means the path was refused; an error means the capture
// itself could not continue.
func (s *PlannerService) captureSelectedFile(ctx context.Context, dir, base, rel string) (plannerCapturedFile, string, error) {
	// Every existing component is checked, not just the leaf: joining a path
	// through a directory symlink would read a file the user never selected,
	// possibly outside the workspace.
	if err := plannerNoSymlinkComponent(dir, rel); err != nil {
		if errors.Is(err, errPlannerSymlink) {
			return plannerCapturedFile{}, "symlink", nil
		}
		return plannerCapturedFile{}, "", err
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	info, statErr := os.Lstat(abs)
	onDisk := statErr == nil
	if onDisk {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// A symlink is not followed: what it points at is not what the
			// user selected, and it can point outside the checkout.
			return plannerCapturedFile{}, "symlink", nil
		case !info.Mode().IsRegular():
			return plannerCapturedFile{}, "not a regular file", nil
		}
	}
	baseBytes, atBase, err := s.snapshotBaseFile(ctx, dir, base, rel)
	if err != nil {
		if errors.Is(err, errPlannerBaseTooLarge) {
			return plannerCapturedFile{}, fmt.Sprintf("file exceeds the %d byte limit", plannerSnapshotFileLimit), nil
		}
		return plannerCapturedFile{}, "", err
	}
	operation := ""
	switch {
	case onDisk && atBase:
		operation = plannerFileModify
	case onDisk:
		operation = plannerFileCreate
	case atBase:
		operation = plannerFileDelete
	default:
		return plannerCapturedFile{}, "not present at the base commit or in the workspace", nil
	}
	if operation == plannerFileCreate {
		ignored, err := s.snapshotIgnored(ctx, dir, rel)
		if err != nil {
			return plannerCapturedFile{}, "", err
		}
		if ignored {
			return plannerCapturedFile{}, "ignored untracked file", nil
		}
	}
	if onDisk && info.Size() > plannerSnapshotFileLimit {
		return plannerCapturedFile{}, fmt.Sprintf("file exceeds the %d byte limit", plannerSnapshotFileLimit), nil
	}

	selected := SelectedFile{Path: rel, Operation: operation}
	var content []byte
	if operation == plannerFileDelete {
		// The file is gone from the workspace: only its base bytes remain, and
		// they live in git, not in this bridge's store.
		selected.BaseSHA256 = plannerDigest(baseBytes)
	} else {
		var fileInfo os.FileInfo
		var err error
		content, fileInfo, err = s.readFileTwice(abs, rel)
		if err != nil {
			return plannerCapturedFile{}, "", err
		}
		if len(content) > plannerSnapshotFileLimit {
			return plannerCapturedFile{}, fmt.Sprintf("file exceeds the %d byte limit", plannerSnapshotFileLimit), nil
		}
		selected.ContentSHA256 = plannerDigest(content)
		selected.Executable = fileInfo.Mode().Perm()&0o111 != 0
		if operation == plannerFileModify {
			selected.BaseSHA256 = plannerDigest(baseBytes)
		}
	}
	return plannerCapturedFile{
		file:    selected,
		content: content,
		diff:    unifiedFileDiff(rel, operation, baseBytes, content),
	}, "", nil
}

// readFileTwice is what keeps a capture honest under a concurrent edit: the
// same file read twice must have the same identity, size, mode and bytes, or
// the user is told to retry rather than handed a half-captured file.
func (s *PlannerService) readFileTwice(abs, rel string) ([]byte, os.FileInfo, error) {
	read := s.readFile
	if read == nil {
		read = plannerSnapshotReadFile
	}
	first, firstInfo, err := read(abs)
	if err != nil {
		// The cause carries the absolute path; the caller only needs the
		// repository-relative one.
		return nil, nil, fmt.Errorf("%w: the file %s could not be read", errPlannerUnavailable, rel)
	}
	// The leaf was validated before the read; if it is a symlink now, the
	// bytes that came back may be another file's, so the capture is refused
	// rather than frozen.
	if firstInfo != nil && firstInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: %s", errPlannerFileChanged, rel)
	}
	second, secondInfo, err := read(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the file %s could not be read", errPlannerUnavailable, rel)
	}
	if firstInfo == nil || secondInfo == nil ||
		!os.SameFile(firstInfo, secondInfo) ||
		firstInfo.Size() != secondInfo.Size() ||
		firstInfo.Mode() != secondInfo.Mode() ||
		!bytes.Equal(first, second) {
		return nil, nil, fmt.Errorf("%w: %s", errPlannerFileChanged, rel)
	}
	return second, secondInfo, nil
}

func (s *PlannerService) snapshotHead(ctx context.Context, dir string) (string, error) {
	if s.gitHead == nil {
		return "", fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
	}
	return s.gitHead(ctx, dir)
}

func (s *PlannerService) snapshotBaseFile(ctx context.Context, dir, base, rel string) ([]byte, bool, error) {
	if s.gitBaseFile == nil {
		return nil, false, fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
	}
	return s.gitBaseFile(ctx, dir, base, rel)
}

func (s *PlannerService) snapshotIgnored(ctx context.Context, dir, rel string) (bool, error) {
	if s.gitIgnored == nil {
		return false, fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
	}
	return s.gitIgnored(ctx, dir, rel)
}

// plannerSelectedPath turns one caller-supplied path into the repository-
// relative slash path a capture uses, or explains why it is refused. Every
// refusal is reported: a selection is never quietly narrowed.
func plannerSelectedPath(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "empty path"
	}
	if strings.ContainsRune(trimmed, '\\') {
		return "", "not a slash path"
	}
	if path.IsAbs(trimmed) {
		return "", "absolute path"
	}
	rel := path.Clean(trimmed)
	if rel == "." {
		return "", "not a file path"
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "path escapes the workspace"
	}
	for _, part := range strings.Split(rel, "/") {
		switch part {
		case ".git":
			return "", ".git is excluded"
		case ".marshal":
			return "", ".marshal is excluded"
		}
	}
	base := path.Base(rel)
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return "", ".env files are excluded"
	case strings.HasSuffix(base, ".pem"):
		return "", "*.pem files are excluded"
	case strings.HasSuffix(base, ".key"):
		return "", "*.key files are excluded"
	}
	return rel, ""
}

// plannerSnapshotArtifacts freezes the non-empty documents the user supplied.
func plannerSnapshotArtifacts(plan, spec string) []PlannerSnapshotArtifact {
	out := make([]PlannerSnapshotArtifact, 0, 2)
	for _, doc := range []struct {
		kind string
		text string
	}{{plannerArtifactPlan, plan}, {plannerArtifactSpec, spec}} {
		text := strings.TrimSpace(doc.text)
		if text == "" {
			continue
		}
		out = append(out, PlannerSnapshotArtifact{Kind: doc.kind, Content: doc.text, SHA256: plannerDigest([]byte(doc.text))})
	}
	return out
}

// plannerSnapshotDigest is the snapshot's own hash: SHA-256 over the canonical
// JSON of the base commit and the ordered file metadata. The hash is deliberately
// blind to artifact text and diff text, so it identifies the edit, not the
// prose around it.
func plannerSnapshotDigest(edit EditSnapshot) (string, error) {
	files := edit.Files
	if files == nil {
		files = []SelectedFile{}
	}
	canonical := struct {
		BaseCommit string         `json:"baseCommit"`
		Files      []SelectedFile `json:"files"`
	}{BaseCommit: edit.BaseCommit, Files: files}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode snapshot digest: %v", errPlannerUnavailable, err)
	}
	return plannerDigest(data), nil
}

// plannerSnapshotDiff is one file's frozen review text, keyed by path.
type plannerSnapshotDiff struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

// plannerSnapshotRecord is the immutable on-disk snapshot: the hashed core, the
// frozen documents, the review text and where it came from. ProjectRoot never
// leaves this record: no route reports it.
type plannerSnapshotRecord struct {
	ID          string `json:"id"`
	PlannerID   string `json:"plannerId"`
	ProjectRoot string `json:"projectRoot"`
	EditSnapshot
	Artifacts []PlannerSnapshotArtifact `json:"artifacts"`
	Diffs     []plannerSnapshotDiff     `json:"diffs,omitempty"`
	CreatedAt time.Time                 `json:"createdAt"`
}

// plannerSnapshotView projects a record for review. Diffs are looked up by
// path, so a record's file order is preserved exactly as it was captured.
func plannerSnapshotView(snapshot plannerSnapshotRecord) PlannerSnapshotView {
	diffs := make(map[string]string, len(snapshot.Diffs))
	for _, diff := range snapshot.Diffs {
		diffs[diff.Path] = diff.Diff
	}
	files := make([]PlannerSnapshotFile, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		files = append(files, PlannerSnapshotFile{SelectedFile: file, Diff: diffs[file.Path]})
	}
	artifacts := snapshot.Artifacts
	if artifacts == nil {
		artifacts = []PlannerSnapshotArtifact{}
	}
	return PlannerSnapshotView{
		ID:         snapshot.ID,
		SHA256:     snapshot.SHA256,
		BaseCommit: snapshot.BaseCommit,
		Artifacts:  artifacts,
		Files:      files,
		CreatedAt:  snapshot.CreatedAt,
	}
}

// plannerRecord resolves a planner ID to its record, preferring the live
// instance's fresher copy. It is the identity every snapshot route starts
// from: a snapshot belongs to a planner this bridge knows.
func (s *PlannerService) plannerRecord(id string) (PlannerRecord, error) {
	if inst, ok := s.live(id); ok {
		return inst.snapshot(), nil
	}
	rec, err := s.store.read(id)
	if err != nil {
		if errors.Is(err, errPlannerInvalid) {
			return PlannerRecord{}, fmt.Errorf("%w: %s", errPlannerNotFound, id)
		}
		return PlannerRecord{}, err
	}
	return rec, nil
}

func (s *PlannerService) blobDir() string {
	return filepath.Join(s.stateDir, plannerDirName, plannerBlobDirName)
}

func (s *PlannerService) blobPath(digest string) string {
	return filepath.Join(s.blobDir(), digest)
}

// writeBlob stores bytes by their SHA-256 in the bridge's own private store.
// The store is content-addressed and immutable: existing bytes are the record's
// evidence and are never rewritten.
func (s *PlannerService) writeBlob(data []byte) error {
	dir := s.blobDir()
	if err := os.MkdirAll(dir, plannerDirMode); err != nil {
		return fmt.Errorf("%w: create blob directory: %v", errPlannerUnavailable, err)
	}
	target := s.blobPath(plannerDigest(data))
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	return plannerWriteAtomic(target, data)
}

func (s *PlannerService) snapshotDir() string {
	return filepath.Join(s.stateDir, plannerDirName, plannerSnapshotDirName)
}

func (s *PlannerService) snapshotPath(id string) (string, error) {
	if !validPlannerID(id) {
		return "", fmt.Errorf("%w: %q is not a snapshot id", errPlannerInvalid, id)
	}
	return filepath.Join(s.snapshotDir(), id+".json"), nil
}

// writeSnapshot persists a record once. A second write of the same ID is
// refused: a snapshot is evidence, not a document that is updated in place.
func (s *PlannerService) writeSnapshot(ctx context.Context, snapshot plannerSnapshotRecord) error {
	target, err := s.snapshotPath(snapshot.ID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, plannerDirMode); err != nil {
		return fmt.Errorf("%w: create snapshot directory: %v", errPlannerUnavailable, err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("%w: encode snapshot: %v", errPlannerUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plannerWriteImmutable(target, data); err != nil {
		return err
	}
	return nil
}

// plannerWriteImmutable writes one file that must never be replaced: a fsynced
// temp file in the same directory, linked into place so an existing file is
// refused rather than overwritten, then the directory synced.
func plannerWriteImmutable(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return fmt.Errorf("%w: create temp file: %v", errPlannerUnavailable, err)
	}
	name := tmp.Name()
	fail := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", errPlannerUnavailable, cause)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(plannerFileMode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", errPlannerUnavailable, err)
	}
	if err := os.Link(name, target); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: write snapshot %s: %v", errPlannerUnavailable, filepath.Base(target), err)
	}
	_ = os.Remove(name)
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// readSnapshot loads one snapshot that belongs to this planner. A record that
// does not decode is corrupt, not missing: a reader must not be told a handoff
// it holds was never made.
func (s *PlannerService) readSnapshot(plannerID, snapshotID string) (plannerSnapshotRecord, error) {
	target, err := s.snapshotPath(snapshotID)
	if err != nil {
		return plannerSnapshotRecord{}, fmt.Errorf("%w: snapshot %s", errPlannerNotFound, snapshotID)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return plannerSnapshotRecord{}, fmt.Errorf("%w: snapshot %s", errPlannerNotFound, snapshotID)
		}
		return plannerSnapshotRecord{}, fmt.Errorf("%w: read snapshot: %v", errPlannerUnavailable, err)
	}
	var snapshot plannerSnapshotRecord
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return plannerSnapshotRecord{}, fmt.Errorf("%w: snapshot %s does not decode", errPlannerSnapshotCorrupt, snapshotID)
	}
	if snapshot.ID != snapshotID || snapshot.PlannerID != plannerID {
		return plannerSnapshotRecord{}, fmt.Errorf("%w: snapshot %s", errPlannerNotFound, snapshotID)
	}
	return snapshot, nil
}

// listSnapshots loads every readable snapshot of one planner, newest first by
// creation time left to the caller.
func (s *PlannerService) listSnapshots(plannerID string) ([]plannerSnapshotRecord, error) {
	entries, err := os.ReadDir(s.snapshotDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read snapshot directory: %v", errPlannerUnavailable, err)
	}
	out := make([]plannerSnapshotRecord, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validPlannerID(id) {
			continue
		}
		snapshot, err := s.readSnapshot(plannerID, id)
		if err != nil {
			// A snapshot of another planner is simply not this planner's; a
			// corrupt one is reported by reading it directly.
			continue
		}
		out = append(out, snapshot)
	}
	return out, nil
}

// verifySnapshot holds a stored snapshot to its own hashes: the edit digest,
// every artifact and every content blob must still hash to what was recorded.
func (s *PlannerService) verifySnapshot(snapshot plannerSnapshotRecord) error {
	digest, err := plannerSnapshotDigest(snapshot.EditSnapshot)
	if err != nil {
		return err
	}
	if digest != snapshot.SHA256 {
		return fmt.Errorf("%w: snapshot %s does not match its recorded digest", errPlannerSnapshotCorrupt, snapshot.ID)
	}
	for _, artifact := range snapshot.Artifacts {
		if plannerDigest([]byte(artifact.Content)) != artifact.SHA256 {
			return fmt.Errorf("%w: snapshot %s artifact %s does not match its hash", errPlannerSnapshotCorrupt, snapshot.ID, artifact.Kind)
		}
	}
	for _, file := range snapshot.Files {
		if file.ContentSHA256 == "" {
			continue
		}
		data, err := os.ReadFile(s.blobPath(file.ContentSHA256))
		if err != nil {
			return fmt.Errorf("%w: snapshot %s is missing the contents of %s", errPlannerSnapshotCorrupt, snapshot.ID, file.Path)
		}
		if plannerDigest(data) != file.ContentSHA256 {
			return fmt.Errorf("%w: snapshot %s stored different bytes for %s", errPlannerSnapshotCorrupt, snapshot.ID, file.Path)
		}
	}
	return nil
}

// unifiedFileDiff builds one file's unified diff against the base commit
// without parsing git output: a header, then one hunk covering the whole file,
// with unchanged lines as context between the removed and added ones. A file
// whose first plannerSnapshotBinaryProbe bytes hold a NUL byte is reported as
// binary instead, because a byte diff of it would be meaningless.
func unifiedFileDiff(rel, operation string, oldBytes, newBytes []byte) string {
	if plannerLooksBinary(oldBytes) || plannerLooksBinary(newBytes) {
		return fmt.Sprintf("Binary files a/%s and b/%s differ\n", rel, rel)
	}
	oldLabel, newLabel := "a/"+rel, "b/"+rel
	if operation == plannerFileCreate {
		oldLabel = "/dev/null"
	}
	if operation == plannerFileDelete {
		newLabel = "/dev/null"
	}
	oldLines, oldFinalNewline := plannerDiffLines(oldBytes)
	newLines, newFinalNewline := plannerDiffLines(newBytes)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix {
		if oldLines[len(oldLines)-1-suffix] != newLines[len(newLines)-1-suffix] {
			break
		}
		suffix++
	}
	// A final newline that appeared or disappeared is a change even when every
	// line reads the same. Without pulling the last line into the hunk the
	// reviewer would be shown pure context for bytes that differ.
	if oldFinalNewline != newFinalNewline && prefix > 0 && prefix == len(oldLines) && len(oldLines) == len(newLines) {
		prefix--
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldLabel, newLabel)
	// An empty side has no line to start at, so its range starts at 0: that is
	// how git and POSIX spell "@@ -0,0 +1,3 @@".
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", diffRangeStart(len(oldLines)), len(oldLines), diffRangeStart(len(newLines)), len(newLines))
	removed := oldLines[prefix : len(oldLines)-suffix]
	added := newLines[prefix : len(newLines)-suffix]
	for _, line := range oldLines[:prefix] {
		b.WriteString(" " + line + "\n")
	}
	for _, line := range removed {
		b.WriteString("-" + line + "\n")
	}
	if !oldFinalNewline && suffix == 0 && len(removed) > 0 {
		b.WriteString("\\ No newline at end of file\n")
	}
	for _, line := range added {
		b.WriteString("+" + line + "\n")
	}
	if !newFinalNewline && suffix == 0 && len(added) > 0 {
		b.WriteString("\\ No newline at end of file\n")
	}
	if suffix > 0 {
		for _, line := range newLines[len(newLines)-suffix:] {
			b.WriteString(" " + line + "\n")
		}
		if !newFinalNewline {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
	return plannerCapDiff(b.String())
}

// diffRangeStart is the line a hunk range starts at, which is 0 for an empty
// range and 1 otherwise.
func diffRangeStart(lines int) int {
	if lines == 0 {
		return 0
	}
	return 1
}

// plannerLooksBinary reports whether the inspected prefix of data holds a NUL
// byte, which is what git uses to decide a file is not text, or whether the
// bytes are not valid UTF-8: Go's JSON encoder rewrites invalid UTF-8 to
// U+FFFD, so a frozen "exact" diff of such bytes would not describe them.
func plannerLooksBinary(data []byte) bool {
	probe := data
	if len(probe) > plannerSnapshotBinaryProbe {
		probe = probe[:plannerSnapshotBinaryProbe]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}

// plannerDiffLines splits data into lines without the trailing newline of the
// last one, so line counts match what a reader sees, and reports whether the
// data ended with a newline at all.
func plannerDiffLines(data []byte) ([]string, bool) {
	if len(data) == 0 {
		return nil, true
	}
	finalNewline := data[len(data)-1] == '\n'
	text := strings.TrimSuffix(string(data), "\n")
	return strings.Split(text, "\n"), finalNewline
}

// plannerCapDiff bounds one diff, cutting on a rune boundary and saying so.
func plannerCapDiff(diff string) string {
	if len(diff) <= plannerSnapshotDiffLimit {
		return diff
	}
	cut := plannerSnapshotDiffLimit
	for cut > 0 && !utf8.RuneStart(diff[cut]) {
		cut--
	}
	return diff[:cut] + fmt.Sprintf("\n[diff truncated at %d bytes]\n", plannerSnapshotDiffLimit)
}

// handleCreateSnapshot freezes one handoff. The request context is passed on:
// a capture abandoned by the caller stops rather than finishing unseen.
func (svc *PlannerService) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	var body PlannerSnapshotRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	reply, err := svc.CreateSnapshot(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, reply)
}

func (svc *PlannerService) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	view, err := svc.Snapshot(r.PathValue("id"), r.PathValue("snapshotId"))
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (svc *PlannerService) handleContext(w http.ResponseWriter, r *http.Request) {
	view, err := svc.Context(r.PathValue("id"))
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
