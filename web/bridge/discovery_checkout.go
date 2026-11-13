package bridge

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file inspects an existing checkout without changing it. The contract is
// deliberately narrow: discovery looks at an exact list of known paths, opens
// only instruction, documentation and manifest targets through a bounded
// reader, never follows a symlink, and reads Git state through the hardened
// runner with an empty credential. It executes nothing it finds: no Makefile
// target, package script, devcontainer command or workflow step.

const (
	// discoveryReadLimit bounds every known file discovery opens. One byte
	// beyond the limit is read, so an oversized target is detectable without
	// its content ever being decoded, quoted or returned.
	discoveryReadLimit = 1 << 20

	// discoveryDirtyFileLimit caps a worktree's dirtyFiles list. The worktree
	// detail states how many entries the cap left out.
	discoveryDirtyFileLimit = 100

	// discoveryStatusLimit bounds the porcelain status output discovery reads.
	// status --untracked-files=all enumerates every untracked file, so a
	// heavily dirty checkout must not materialise an unbounded string before
	// the dirty-file cap applies.
	discoveryStatusLimit = 1 << 20
)

// Checkout sections, as they appear in a fact's section and kind.
const (
	discoverySectionInstructions = "instructions"
	discoverySectionDocs         = "docs"
	discoverySectionTooling      = "tooling"
	discoverySectionEnvironment  = "environment"
	discoverySectionCI           = "ci"
)

// The frozen capability key each checkout section answers to. Task 4 maps a
// section's facts and gaps onto these keys.
const (
	discoveryKeyInstructions = "checkout_instructions"
	discoveryKeyDocs         = "checkout_docs"
	discoveryKeyTooling      = "checkout_tooling"
	discoveryKeyEnvironment  = "checkout_environment"
	discoveryKeyCI           = "checkout_ci_definition"
)

// The exact paths discovery knows about. Nothing outside these lists is
// looked at, so there is no tree walk and no surprise read.
var (
	discoveryInstructionFiles = []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".cursorrules"}
	discoveryDocFiles         = []string{"docs/README.md", "docs/index.md", "docs/INDEX.md"}
	discoveryManifestFiles    = []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "tsconfig.json"}
	discoveryEnvironmentFiles = []string{"Dockerfile", "docker-compose.yml", "compose.yaml"}
	discoveryWorkflowsDir     = ".github/workflows"
	discoveryDevcontainerDir  = ".devcontainer"
	discoveryReadmePrefix     = "README"
	discoveryAbsenceValue     = "absent"
	discoveryDetectedOrigin   = "detected"
	discoverySourcePrefix     = "checkout:"
)

// checkoutFindings is the result of one read-only inspection of an existing
// checkout: what each frozen section found, and every target it could not
// read. Task 4 assembles the report from these; nothing here is persisted and
// no content was retained.
type checkoutFindings struct {
	Instructions []DiscoveryFact
	Docs         []DiscoveryFact
	Tooling      []DiscoveryFact
	Environment  []DiscoveryFact
	CI           []DiscoveryFact
	Gaps         []DiscoveryGap
}

// facts returns every detected fact in the report's section order.
func (c checkoutFindings) facts() []DiscoveryFact {
	out := make([]DiscoveryFact, 0,
		len(c.Instructions)+len(c.Docs)+len(c.Tooling)+len(c.Environment)+len(c.CI))
	out = append(out, c.Instructions...)
	out = append(out, c.Docs...)
	out = append(out, c.Tooling...)
	out = append(out, c.Environment...)
	out = append(out, c.CI...)
	return out
}

// targetResult is the outcome of looking at one known path.
type targetResult int

const (
	// targetFound is a readable, non-symlink target.
	targetFound targetResult = iota
	// targetMissing means the path does not exist, which is a completed
	// search rather than a failure.
	targetMissing
	// targetUnreadable means the path is there but cannot be inspected: a
	// directory where a file belongs, an unreadable file, an oversized file.
	targetUnreadable
	// targetSkipped is a symlink. It is never read, followed or reported.
	targetSkipped
)

// checkoutInspector inspects one checkout. Every method is read-only: it opens
// files, lists the two directories the contract names, and executes nothing.
type checkoutInspector struct{ dir string }

// fileTarget is one known file in a section. read selects whether the file is
// opened through the bounded reader (instructions, documentation, manifests)
// or only its presence and size are recorded (environment definitions).
type fileTarget struct {
	rel    string
	detail string
	read   bool
}

// inspectCheckout inspects dir without changing it. The root itself is the
// operator's registered project root, so it is stat'ed normally; everything
// discovery reaches below it is lstat'ed and never followed.
func inspectCheckout(dir string) (checkoutFindings, error) {
	var out checkoutFindings
	info, err := os.Stat(dir)
	if err != nil {
		return out, fmt.Errorf("bridge: inspect checkout: %w", err)
	}
	if !info.IsDir() {
		return out, fmt.Errorf("bridge: inspect checkout: %s is not a directory", dir)
	}

	in := checkoutInspector{dir: dir}
	add := func(facts []DiscoveryFact, gaps []DiscoveryGap) []DiscoveryFact {
		out.Gaps = append(out.Gaps, gaps...)
		sortFacts(facts)
		return facts
	}
	out.Instructions = add(in.instructions())
	out.Docs = add(in.docs())
	out.Tooling = add(in.tooling())
	out.Environment = add(in.environment())
	out.CI = add(in.ci())
	return out, nil
}

// sortFacts orders a section's facts by value so the report is deterministic.
func sortFacts(facts []DiscoveryFact) {
	sort.Slice(facts, func(i, j int) bool { return facts[i].Value < facts[j].Value })
}

// fact builds a detected fact for one relative path.
func discoveryFact(section, rel, detail string, size int64) DiscoveryFact {
	return DiscoveryFact{
		Origin:  discoveryDetectedOrigin,
		Section: section,
		Kind:    section,
		Value:   rel,
		Source:  discoverySourcePrefix + strings.TrimSuffix(rel, "/"),
		Size:    size,
		Detail:  detail,
	}
}

// discoverySkippedCandidateNote qualifies an absence fact whose search
// skipped a symlinked candidate. The symlink itself is neither read nor
// reported, but the search did not establish that the thing is missing.
const discoverySkippedCandidateNote = "a candidate was skipped because it is a symlink"

// absentFact records that a search completed and found nothing. It is emitted
// only when every candidate in the section was inspected: an unreadable or
// oversized target produces a gap instead, so absence is never inferred from a
// failed read.
func absentFact(section, root string, searched ...string) DiscoveryFact {
	return DiscoveryFact{
		Origin:  discoveryDetectedOrigin,
		Section: section,
		Kind:    section,
		Value:   discoveryAbsenceValue,
		Source:  discoverySourcePrefix + root,
		Detail:  "searched " + strings.Join(searched, ", ") + " in the checkout",
	}
}

// absentFactSkipped records a completed search that skipped a symlinked
// candidate. The fact stays an absence fact, but its detail says the search
// was not exhaustive, so no recommendation is derived from it.
func absentFactSkipped(section, root string, searched ...string) DiscoveryFact {
	fact := absentFact(section, root, searched...)
	fact.Detail += "; " + discoverySkippedCandidateNote
	return fact
}

// unreadableCause names why one known target could not be inspected. The cause
// selects the capability state, because the state is what tells the operator
// which fix applies: access to the file, adapter support, or a retry.
type unreadableCause int

const (
	// causeAccess is a filesystem access failure: the target could not be
	// opened, lstat'ed or listed by this process. No adapter change helps; the
	// bridge is missing permission.
	causeAccess unreadableCause = iota
	// causeShape is a target the adapter does not read at all, such as a
	// directory where a file belongs. The fix is adapter support.
	causeShape
	// causeSize is a target past discoveryReadLimit, which the adapter
	// deliberately does not read. The fix is adapter support.
	causeSize
	// causeRace is a target that changed between the lstat and the read. The
	// adapter support is there; a retry may succeed.
	causeRace
)

// state is the capability state that names the fix for this cause.
func (c unreadableCause) state() DiscoveryCapabilityState {
	switch c {
	case causeAccess:
		return DiscoveryCapabilityInsufficientPermission
	case causeRace:
		return DiscoveryCapabilityTemporarilyUnavailable
	}
	// causeShape and causeSize are both shapes this adapter version does not
	// read, and neither a retry nor a permission change alters them.
	return DiscoveryCapabilityUnsupported
}

// unreadable is one known target discovery could not inspect: why it failed,
// and the cause that decides the capability state the gap reports.
type unreadable struct {
	cause  unreadableCause
	reason string
}

// unreadableAccess reports a target this process could not open, lstat or list.
func unreadableAccess(reason string) unreadable {
	return unreadable{cause: causeAccess, reason: reason}
}

// detail names the target, the cause, and (for access and race failures) what
// would fix it. The wording deliberately never reads as absence.
func (u unreadable) detail(rel string) string {
	switch u.cause {
	case causeAccess:
		return fmt.Sprintf("%s could not be inspected: the bridge lacks filesystem access to it (%s)",
			rel, u.reason)
	case causeRace:
		return fmt.Sprintf("%s could not be inspected: %s; a retry may succeed", rel, u.reason)
	}
	return fmt.Sprintf("%s could not be inspected: %s", rel, u.reason)
}

// gapFor records one target discovery could not read. Discovery cannot make a
// fact from it, so the target becomes a gap carrying the capability state its
// cause implies.
func gapFor(key, section, rel string, u unreadable) DiscoveryGap {
	return DiscoveryGap{
		Key:     key,
		Section: section,
		State:   u.cause.state(),
		Detail:  u.detail(rel),
	}
}

// inspectFiles inspects one section's known files and records a gap for every
// target that exists but cannot be read. It also reports whether any candidate
// was skipped because it is a symlink, so the caller can qualify its absence
// fact.
func (in checkoutInspector) inspectFiles(section, key string, targets []fileTarget) (facts []DiscoveryFact, gaps []DiscoveryGap, skipped bool) {
	for _, target := range targets {
		fact, outcome, u := in.inspectFile(section, target.rel, target.detail, target.read)
		switch outcome {
		case targetFound:
			facts = append(facts, fact)
		case targetUnreadable:
			gaps = append(gaps, gapFor(key, section, target.rel, u))
		case targetSkipped:
			skipped = true
		}
	}
	return facts, gaps, skipped
}

// inspectFile reports one known file. A symlink is skipped without a read; a
// non-regular file, an unreadable file, or a file past discoveryReadLimit is
// unreadable and becomes a gap in the caller.
func (in checkoutInspector) inspectFile(section, rel, detail string, read bool) (DiscoveryFact, targetResult, unreadable) {
	abs := filepath.Join(in.dir, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return DiscoveryFact{}, targetMissing, unreadable{}
		}
		return DiscoveryFact{}, targetUnreadable, unreadableAccess("it cannot be inspected")
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return DiscoveryFact{}, targetSkipped, unreadable{}
	case !info.Mode().IsRegular():
		// A directory, socket or device where a file belongs is a shape this
		// adapter does not read.
		return DiscoveryFact{}, targetUnreadable, unreadable{cause: causeShape, reason: "it is not a regular file"}
	}
	if read {
		if u, ok := readBoundedFile(abs, info); !ok {
			return DiscoveryFact{}, targetUnreadable, u
		}
	}
	return discoveryFact(section, rel, detail, info.Size()), targetFound, unreadable{}
}

// readBoundedFile opens abs through a LimitReader at discoveryReadLimit+1 and
// reports whether the target stayed within the limit. The bytes are discarded:
// only the bound and the readability of the file are established, which is the
// minimum discovery needs and the most it may take.
func readBoundedFile(abs string, known os.FileInfo) (unreadable, bool) {
	f, err := os.Open(abs)
	if err != nil {
		return unreadableAccess("it cannot be read"), false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(known, opened) {
		// The path was swapped between the lstat and the open: refuse rather
		// than read something discovery did not inspect.
		return unreadable{cause: causeRace, reason: "it changed while being inspected"}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, discoveryReadLimit+1))
	if err != nil {
		return unreadableAccess("it cannot be read"), false
	}
	if len(data) > discoveryReadLimit {
		return unreadable{
			cause:  causeSize,
			reason: fmt.Sprintf("it is larger than the %d MiB inspection limit", discoveryReadLimit>>20),
		}, false
	}
	return unreadable{}, true
}

// presentRegular reports whether rel exists below the checkout as a regular
// file rather than a symlink or a directory.
func (in checkoutInspector) presentRegular(rel string) bool {
	info, err := os.Lstat(filepath.Join(in.dir, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

// instructions inspects the known instruction files plus README*, which is the
// contract's only glob. The glob is resolved from a single listing of the
// checkout root, never a walk.
func (in checkoutInspector) instructions() ([]DiscoveryFact, []DiscoveryGap) {
	const section = discoverySectionInstructions
	names := append([]string(nil), discoveryInstructionFiles...)
	searched := append(append([]string(nil), discoveryInstructionFiles...), discoveryReadmePrefix+"*")

	listErr := error(nil)
	readmeSkipped := false
	entries, err := os.ReadDir(in.dir)
	if err != nil {
		listErr = err
	} else {
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, discoveryReadmePrefix) || entry.IsDir() {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				readmeSkipped = true
				continue
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	sort.Strings(searched)

	targets := make([]fileTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, fileTarget{rel: name, read: true})
	}
	facts, gaps, skipped := in.inspectFiles(section, discoveryKeyInstructions, targets)
	skipped = skipped || readmeSkipped
	if listErr != nil {
		// The fixed names may have been inspected, but README* is unknown, so
		// the search did not complete: a gap, never an absence fact.
		gaps = append(gaps, gapFor(discoveryKeyInstructions, section, "README*",
			unreadableAccess("the checkout root could not be listed")))
		return facts, gaps
	}
	if len(facts) == 0 && len(gaps) == 0 {
		if skipped {
			facts = append(facts, absentFactSkipped(section, ".", searched...))
		} else {
			facts = append(facts, absentFact(section, ".", searched...))
		}
	}
	return facts, gaps
}

// docs inspects the three known documentation entry points. The docs directory
// itself is lstat'ed first: a symlinked docs/ must not lead discovery outside
// the checkout.
func (in checkoutInspector) docs() ([]DiscoveryFact, []DiscoveryGap) {
	const section = discoverySectionDocs
	searched := append([]string(nil), discoveryDocFiles...)
	_, outcome := in.dirTarget("docs")
	switch outcome {
	case targetMissing:
		return []DiscoveryFact{absentFact(section, "docs", searched...)}, nil
	case targetSkipped:
		return []DiscoveryFact{absentFactSkipped(section, "docs", searched...)}, nil
	case targetUnreadable:
		return nil, []DiscoveryGap{gapFor(discoveryKeyDocs, section, "docs",
			unreadableAccess("it cannot be inspected"))}
	}

	targets := make([]fileTarget, 0, len(discoveryDocFiles))
	for _, rel := range discoveryDocFiles {
		targets = append(targets, fileTarget{rel: rel, read: true})
	}
	facts, gaps, skipped := in.inspectFiles(section, discoveryKeyDocs, targets)
	if len(facts) == 0 && len(gaps) == 0 {
		if skipped {
			facts = append(facts, absentFactSkipped(section, "docs", searched...))
		} else {
			facts = append(facts, absentFact(section, "docs", searched...))
		}
	}
	return facts, gaps
}

// tooling inspects the known manifests. The language detail is the minimum a
// manifest can tell discovery: package.json is TypeScript only when a
// tsconfig.json sits beside it.
func (in checkoutInspector) tooling() ([]DiscoveryFact, []DiscoveryGap) {
	const section = discoverySectionTooling
	typescript := in.presentRegular("tsconfig.json")
	targets := make([]fileTarget, 0, len(discoveryManifestFiles))
	for _, rel := range discoveryManifestFiles {
		targets = append(targets, fileTarget{rel: rel, detail: discoveryManifestLanguage(rel, typescript), read: true})
	}
	facts, gaps, skipped := in.inspectFiles(section, discoveryKeyTooling, targets)
	if len(facts) == 0 && len(gaps) == 0 {
		if skipped {
			facts = append(facts, absentFactSkipped(section, ".", discoveryManifestFiles...))
		} else {
			facts = append(facts, absentFact(section, ".", discoveryManifestFiles...))
		}
	}
	return facts, gaps
}

// discoveryManifestLanguage names the tooling a manifest identifies.
func discoveryManifestLanguage(rel string, typescript bool) string {
	switch rel {
	case "go.mod":
		return "Go"
	case "package.json":
		if typescript {
			return "TypeScript"
		}
		return "JavaScript"
	case "pyproject.toml":
		return "Python"
	case "Cargo.toml":
		return "Rust"
	case "tsconfig.json":
		return "TypeScript"
	}
	return ""
}

// environment inspects the known environment definitions. The devcontainer
// directory is reported by presence only: nothing inside it is listed, opened
// or run.
func (in checkoutInspector) environment() ([]DiscoveryFact, []DiscoveryGap) {
	const section = discoverySectionEnvironment
	var facts []DiscoveryFact
	var gaps []DiscoveryGap
	skipped := false

	_, outcome := in.dirTarget(discoveryDevcontainerDir)
	switch outcome {
	case targetFound:
		facts = append(facts, discoveryFact(section, discoveryDevcontainerDir+"/",
			"container-based development environment", 0))
	case targetUnreadable:
		gaps = append(gaps, gapFor(discoveryKeyEnvironment, section, discoveryDevcontainerDir+"/",
			unreadableAccess("it cannot be inspected")))
	case targetSkipped:
		skipped = true
	}

	targets := make([]fileTarget, 0, len(discoveryEnvironmentFiles))
	for _, rel := range discoveryEnvironmentFiles {
		targets = append(targets, fileTarget{rel: rel})
	}
	fileFacts, fileGaps, fileSkipped := in.inspectFiles(section, discoveryKeyEnvironment, targets)
	facts = append(facts, fileFacts...)
	gaps = append(gaps, fileGaps...)
	skipped = skipped || fileSkipped

	if len(facts) == 0 && len(gaps) == 0 {
		searched := append([]string{discoveryDevcontainerDir + "/"}, discoveryEnvironmentFiles...)
		if skipped {
			facts = append(facts, absentFactSkipped(section, ".", searched...))
		} else {
			facts = append(facts, absentFact(section, ".", searched...))
		}
	}
	return facts, gaps
}

// ci inspects the workflow directory. Workflow files are detected by name
// only: discovery has no YAML parser, and a definition's content is not a
// fact this slice reports.
func (in checkoutInspector) ci() ([]DiscoveryFact, []DiscoveryGap) {
	const section = discoverySectionCI
	searched := []string{discoveryWorkflowsDir + "/*.yml", discoveryWorkflowsDir + "/*.yaml"}

	entries, outcome := in.readDirTarget(discoveryWorkflowsDir)
	switch outcome {
	case targetMissing:
		return []DiscoveryFact{absentFact(section, discoveryWorkflowsDir, searched...)}, nil
	case targetSkipped:
		return []DiscoveryFact{absentFactSkipped(section, discoveryWorkflowsDir, searched...)}, nil
	case targetUnreadable:
		return nil, []DiscoveryGap{gapFor(discoveryKeyCI, section, discoveryWorkflowsDir,
			unreadableAccess("it cannot be inspected"))}
	}

	var facts []DiscoveryFact
	skipped := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			skipped = true
			continue
		}
		rel := discoveryWorkflowsDir + "/" + name
		facts = append(facts, discoveryFact(section, rel, "", 0))
	}
	if len(facts) == 0 {
		if skipped {
			facts = append(facts, absentFactSkipped(section, discoveryWorkflowsDir, searched...))
		} else {
			facts = append(facts, absentFact(section, discoveryWorkflowsDir, searched...))
		}
	}
	return facts, nil
}

// dirTarget lstats every component of rel below the checkout and refuses to
// follow a symlink at any of them. A component that exists but is not a
// directory cannot hold the targets, so it reads as missing: the search
// completed and there is nothing there.
func (in checkoutInspector) dirTarget(rel string) (os.FileInfo, targetResult) {
	cur := in.dir
	var info os.FileInfo
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, targetMissing
			}
			return nil, targetUnreadable
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, targetSkipped
		}
		if !fi.IsDir() {
			return nil, targetMissing
		}
		info = fi
	}
	return info, targetFound
}

// readDirTarget reads one known directory, refusing a symlinked component and
// confirming that the directory it opened is the one it inspected.
func (in checkoutInspector) readDirTarget(rel string) ([]os.DirEntry, targetResult) {
	known, outcome := in.dirTarget(rel)
	if outcome != targetFound {
		return nil, outcome
	}
	f, err := os.Open(filepath.Join(in.dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, targetUnreadable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(known, opened) {
		return nil, targetUnreadable
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, targetUnreadable
	}
	return entries, targetFound
}

// discoveryGitState reports a checkout's Git state without changing it.
//
// Every invocation goes through the hardened runner with an empty Credential,
// so no forge PAT reaches a local read's environment. The verbs are an
// allowlist — rev-parse, symbolic-ref, status and for-each-ref — and nothing
// that fetches, checks out, resets, cleans or otherwise writes is ever run.
// status runs with --no-optional-locks so git does not refresh the index
// behind the operator's back.
//
// A detached HEAD is an empty branch, its SHA in head, and "detached HEAD" in
// the detail. dirtyFiles is capped at discoveryDirtyFileLimit; the detail
// states how many entries were omitted, and carries both clauses when the HEAD
// is detached and the list is truncated.
func (f *Fleet) discoveryGitState(ctx context.Context, dir string) (head, branch string, dirty *bool, files []string, detail string, err error) {
	if f.git == nil {
		return "", "", nil, nil, "", fmt.Errorf("bridge: git is not available")
	}
	read := func(args ...string) (string, error) {
		out, err := f.git.runCtx(ctx, dir, Credential{}, args...)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}

	headSHA, err := read("rev-parse", "HEAD")
	if err != nil {
		return "", "", nil, nil, "", fmt.Errorf("bridge: read checkout HEAD: %w", err)
	}
	head = headSHA

	detached := false
	if name, branchErr := read("symbolic-ref", "--quiet", "--short", "HEAD"); branchErr == nil {
		branch = name
	} else {
		// symbolic-ref exits non-zero when HEAD is not a symbolic ref: a
		// detached checkout. Its SHA is still the checkout's head.
		detached = true
	}

	// The local branch inventory is read with the allowlisted for-each-ref
	// verb. A detached checkout whose branches are gone cannot be reattached
	// by name, which is worth saying in the detail.
	localBranches, _ := read("for-each-ref", "--format=%(refname:short)", "refs/heads")

	statusOut, statusTruncated, err := f.git.runCtxLimited(ctx, dir, Credential{}, discoveryStatusLimit,
		"-c", "core.quotepath=false",
		"--no-optional-locks", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return head, branch, nil, nil, "", fmt.Errorf("bridge: read checkout status: %w", err)
	}
	// Porcelain status leads with a two-column state field, so its first space
	// is significant: only trailing line breaks may be trimmed.
	status := string(statusOut)
	if statusTruncated {
		// A cut last line is not a complete porcelain record: drop it rather
		// than report a partial path.
		if i := strings.LastIndexByte(status, '\n'); i >= 0 {
			status = status[:i+1]
		} else {
			status = ""
		}
	}
	status = strings.TrimRight(status, "\r\n")

	isDirty := status != "" || statusTruncated
	dirty = &isDirty
	files, omitted := discoveryDirtyFiles(status)

	var clauses []string
	if detached {
		clauses = append(clauses, "detached HEAD")
		if strings.TrimSpace(localBranches) == "" {
			clauses = append(clauses, "no local branches")
		}
	}
	if omitted > 0 {
		count := fmt.Sprintf("%d", omitted)
		if statusTruncated {
			// The cut output may hide further entries, so the exact count is
			// unknowable: the parsed count is a lower bound.
			count = "at least " + count
		}
		clauses = append(clauses, fmt.Sprintf(
			"%s additional dirty entries omitted (dirtyFiles is capped at %d)",
			count, discoveryDirtyFileLimit))
	}
	return head, branch, dirty, files, strings.Join(clauses, "; "), nil
}

// discoveryDirtyFiles parses porcelain status output into the capped file list
// and the number of entries the cap omitted.
func discoveryDirtyFiles(status string) (files []string, omitted int) {
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue // an empty line, or a status line that carries no path
		}
		path := line[3:]
		if i := strings.Index(path, " -> "); i >= 0 {
			// A rename or copy reports both sides; the destination is the
			// entry that exists now.
			path = path[i+len(" -> "):]
		}
		path = discoveryUnquotePath(path)
		if path == "" {
			continue
		}
		if len(files) >= discoveryDirtyFileLimit {
			omitted++
			continue
		}
		files = append(files, path)
	}
	return files, omitted
}

// discoveryUnquotePath decodes git's C-style quoting for a path that porcelain
// still escapes. status runs with core.quotepath=false, so non-ASCII paths
// arrive literal; this handles the remaining quoted forms (a space or a quote
// in the name) so dirtyFiles carries real paths.
func discoveryUnquotePath(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}
	if unquoted, err := strconv.Unquote(path); err == nil {
		return unquoted
	}
	return path
}
