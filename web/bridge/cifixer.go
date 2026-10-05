package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CI history statuses.
const (
	ciFixed          = "fixed"
	ciNotReproduced  = "didn't reproduce"
	ciGaveUp         = "gave up"
	ciLogTailBytes   = 32 << 10
	ciWorkflowPrefix = ".github/workflows/"
)

// CIHistoryEntry is the record of one CI fixer run.
type CIHistoryEntry struct {
	ID        string    `json:"id"`
	RepoID    string    `json:"repoId"`
	Branch    string    `json:"branch,omitempty"`
	SHA       string    `json:"sha"`
	Check     string    `json:"check"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	PRURL     string    `json:"prUrl,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	CostUSD   float64   `json:"costUsd"`
	CreatedAt time.Time `json:"createdAt"`
}

// diffEntry is one changed file of an agent's diff.
type diffEntry struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// forbiddenPatterns are the added-line markers that disable a test.
var forbiddenAddedLines = []struct {
	name string
	re   *regexp.Regexp
}{
	{"t.Skip(", regexp.MustCompile(`\bt\.Skip(f|Now)?\(`)},
	{"@pytest.mark.skip", regexp.MustCompile(`@pytest\.mark\.skip`)},
	{"@unittest.skip", regexp.MustCompile(`@unittest\.skip`)},
	{"it.skip(", regexp.MustCompile(`\bit\.skip\(`)},
	{"describe.skip(", regexp.MustCompile(`\bdescribe\.skip\(`)},
	{"xit(", regexp.MustCompile(`\bxit\(`)},
	{"test.skip(", regexp.MustCompile(`\btest\.skip\(`)},
	{".only(", regexp.MustCompile(`\.only\(`)},
	{"#[ignore]", regexp.MustCompile(`#\[ignore\]`)},
}

// forbiddenCIConfig are the lines that switch a CI job off.
var forbiddenCIConfig = []struct {
	name string
	re   *regexp.Regexp
}{
	{"if: false", regexp.MustCompile(`\bif:\s*false\b`)},
	{"continue-on-error: true", regexp.MustCompile(`continue-on-error:\s*true\b`)},
}

// testFileGlobs match a file name that is a test.
var testFileGlobs = []string{"*_test.go", "test_*.py", "*_test.py", "*.test.*", "*.spec.*"}

func isCIConfigPath(p string) bool {
	return strings.HasPrefix(p, ciWorkflowPrefix) || strings.HasPrefix(p, ".gitea/workflows/")
}

// testFilePattern reports why a path counts as a test file: the glob it
// matches, or "files under tests/".
func testFilePattern(p string) (string, bool) {
	base := path.Base(p)
	for _, g := range testFileGlobs {
		if ok, _ := path.Match(g, base); ok {
			return g, true
		}
	}
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		if s == "test" || s == "tests" {
			return "files under " + s + "/", true
		}
	}
	return "", false
}

// diffDeletesFile reports whether a single-file unified diff removes the file.
func diffDeletesFile(diff string) bool {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "deleted file mode") || line == "+++ /dev/null" {
			return true
		}
		if strings.HasPrefix(line, "@@") {
			break
		}
	}
	return false
}

// forbiddenChange reports whether an agent's diff tampers with tests or CI
// config, and says which pattern matched in which file. diffs maps a path
// to its unified diff. It is pure, so the guard can be tested exhaustively.
func forbiddenChange(files []diffEntry, diffs map[string]string) (string, bool) {
	for _, file := range files {
		diff := diffs[file.Path]
		if pat, ok := testFilePattern(file.Path); ok && diffDeletesFile(diff) {
			return fmt.Sprintf("deleted %s in %s", pat, file.Path), true
		}
		ci := isCIConfigPath(file.Path)
		if ci && diffDeletesFile(diff) {
			return "deleted CI workflow " + file.Path, true
		}
		for _, line := range strings.Split(diff, "\n") {
			if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
				continue
			}
			added := line[1:]
			for _, m := range forbiddenAddedLines {
				if m.re.MatchString(added) {
					return fmt.Sprintf("%s in %s", m.name, file.Path), true
				}
			}
			if ci {
				for _, m := range forbiddenCIConfig {
					if m.re.MatchString(added) {
						return fmt.Sprintf("%s in %s", m.name, file.Path), true
					}
				}
			}
		}
	}
	return "", false
}

// inferCommand returns the last log line that looks like a shell command
// echo ("$ cmd" or "+ cmd"), or "".
func inferCommand(log string) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if rest, ok := strings.CutPrefix(l, "$ "); ok {
			return strings.TrimSpace(rest)
		}
		if rest, ok := strings.CutPrefix(l, "+ "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func matchesAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

func isGlob(s string) bool { return strings.ContainsAny(s, `*?[\`) }

// ciSeen reports whether a (repo, sha, check) was already handled.
func (f *Fleet) ciSeen(repoID, sha, check string) bool {
	for _, h := range f.ciHistory().all() {
		if h.RepoID == repoID && h.SHA == sha && h.Check == check {
			return true
		}
	}
	return false
}

// onCheckEvent starts a fix for the first new failed check of a commit on
// a watched branch. Failures are logged: an event has no caller to tell.
func (f *Fleet) onCheckEvent(ctx context.Context, ev checkEvent) {
	if err := f.fixCI(ctx, ev); err != nil {
		slog.Default().Warn("webbridge: CI fixer", "repo", ev.repoID, "sha", ev.sha, "err", err)
	}
}

func (f *Fleet) fixCI(ctx context.Context, ev checkEvent) error {
	root, ci, ok := f.ciFixerSettings(ev.repoID)
	if !ok || !matchesAny(ci.Branches, ev.ref) {
		return nil
	}
	repo, ok := f.ws.Repo(ev.repoID)
	if !ok {
		return ErrUnknownRepo
	}
	forge, cred, err := f.forgeFor(ctx, repo)
	if err != nil {
		return err
	}
	checks, err := forge.ListFailedChecks(ctx, repo, ev.sha, cred)
	if err != nil {
		f.noteRateLimit(repo.ID, err)
		return err
	}
	for _, check := range checks {
		key := fmt.Sprintf("ci:%s:%s:%s", repo.ID, ev.sha, check.Name)
		if !f.autoClaim(key) {
			continue
		}
		if f.ciSeen(repo.ID, ev.sha, check.Name) {
			f.autoRelease(key)
			continue
		}
		// Only the first new failure: its fix may well fix the others, and
		// the next event picks up whatever is still red.
		if err := f.startCIFix(ctx, root, ci, repo, forge, cred, ev, check, key); err != nil {
			f.autoRelease(key)
			return err
		}
		return nil
	}
	return nil
}

func (f *Fleet) startCIFix(ctx context.Context, root string, ci *CIFixerSettings, repo Repo, forge Forge, cred Credential, ev checkEvent, check CheckInfo, key string) error {
	log, err := forge.CheckLog(ctx, repo, check, cred)
	if err != nil {
		// The fixer can still try from the check's name.
		slog.Default().Warn("webbridge: CI fixer log", "repo", repo.ID, "check", check.Name, "err", err)
		log = ""
	}
	tail := tailString(log, ciLogTailBytes)
	req := RecipeRunRequest{
		Project: root, RepoID: repo.ID, Ref: ev.sha,
		Inputs: map[string]string{
			"check":   sanitizeInput(check.Name),
			"log":     sanitizeInput(tail),
			"command": sanitizeInput(inferCommand(tail)),
		},
		Origin: OriginCI,
		OnDone: func(res RecipeResult) {
			defer f.autoRelease(key)
			f.finishCI(repo, ci, ev, check, res)
		},
	}
	if ci.MaxMinutes > 0 || ci.MaxUSD > 0 {
		req.Limits = &RecipeLimits{MaxMinutes: ci.MaxMinutes, MaxUSD: ci.MaxUSD}
	}
	_, err = f.autoRunRecipe(ctx, "fix-ci", req)
	return err
}

// agentDiff reads an agent's changed files and each file's diff.
func (f *Fleet) agentDiffs(ctx context.Context, id string) ([]diffEntry, map[string]string, error) {
	raw, err := f.autoDiff(ctx, id, "")
	if err != nil {
		return nil, nil, err
	}
	var list struct {
		Files []diffEntry `json:"files"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, nil, fmt.Errorf("decode diff: %w", err)
	}
	diffs := make(map[string]string, len(list.Files))
	for _, file := range list.Files {
		raw, err := f.autoDiff(ctx, id, file.Path)
		if err != nil {
			return nil, nil, err
		}
		var one struct {
			Diff string `json:"diff"`
		}
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, nil, fmt.Errorf("decode diff of %s: %w", file.Path, err)
		}
		diffs[file.Path] = one.Diff
	}
	return list.Files, diffs, nil
}

// agentCostUSD sums the usage ledger's rows for an agent.
func (f *Fleet) agentCostUSD(agentID string, since time.Time) float64 {
	if f.usage == nil {
		return 0
	}
	rows, err := f.usage.Range(since.Add(-time.Minute), f.now().Add(time.Hour))
	if err != nil {
		return 0
	}
	var micro int64
	for _, r := range rows {
		if r.AgentID == agentID {
			micro += r.micro()
		}
	}
	return microToUSD(micro)
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	if s == "" {
		return "check"
	}
	return s
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// finishCI applies the guards to a finished fix-ci run, ships a good fix,
// and records the outcome.
func (f *Fleet) finishCI(repo Repo, ci *CIFixerSettings, ev checkEvent, check CheckInfo, res RecipeResult) {
	ctx, cancel := f.autoContext()
	defer cancel()
	entry := CIHistoryEntry{
		ID: newAutoID(), RepoID: repo.ID, Branch: ev.ref, SHA: ev.sha, Check: check.Name,
		AgentID: res.AgentID, CreatedAt: f.now(),
	}
	discard := func() {
		if res.AgentID == "" {
			return
		}
		if err := f.autoDiscard(ctx, res.AgentID); err != nil {
			slog.Default().Warn("webbridge: CI fixer discard", "agent", res.AgentID, "err", err)
		}
	}
	record := func(status, reason string) {
		entry.Status, entry.Reason = status, reason
		entry.CostUSD = f.agentCostUSD(res.AgentID, entry.CreatedAt.Add(-24*time.Hour))
		if err := f.ciHistory().put(entry.ID, entry); err != nil {
			slog.Default().Warn("webbridge: store CI history", "repo", repo.ID, "err", err)
		}
		f.auditf(AuditEvent{Event: AuditCIFixerResult, OwnerID: DefaultOwnerID, AgentID: res.AgentID,
			RepoID: repo.ID, Detail: status})
		title := "CI fixer: " + status
		text := fmt.Sprintf("%s on %s: %s", check.Name, shortSHA(ev.sha), status)
		if reason != "" {
			text += " (" + reason + ")"
		}
		f.emitAutomation("ci_result", entry.ID, res.AgentID, status, title, text)
	}

	// 1. Reproduced.
	result, parsed := res.Parsed.(CIResult)
	switch {
	case res.Err != "":
		record(ciGaveUp, res.Err)
		discard()
		return
	case !parsed:
		record(ciGaveUp, errNoStructuredResult)
		discard()
		return
	case !result.Reproduced:
		record(ciNotReproduced, strings.TrimSpace(result.Notes))
		discard()
		return
	}

	// 2. No test tampering.
	files, diffs, err := f.agentDiffs(ctx, res.AgentID)
	if err != nil {
		// A diff that cannot be read cannot be checked, so nothing ships.
		record(ciGaveUp, "could not read the agent's diff: "+err.Error())
		return
	}
	if what, bad := forbiddenChange(files, diffs); bad {
		record(ciGaveUp, "forbidden change ("+what+")")
		discard()
		return
	}

	// 3. Fixed.
	if !result.Fixed {
		reason := "the agent did not fix it"
		if n := strings.TrimSpace(result.Notes); n != "" {
			reason += ": " + n
		}
		record(ciGaveUp, reason)
		return
	}

	// 4. Ship, through the same gate as any exit. Automation never overrides.
	base := ev.ref
	if base == "" {
		base = repo.Branch
	}
	push := ci.Push && matchesAny(ci.PushBranches, base)
	opts := ExitOptions{CommitMessage: fmt.Sprintf("fix(ci): %s (%s)", check.Name, shortSHA(ev.sha))}
	if push {
		opts.Branch, opts.NoPR = base, true
	} else {
		opts.Branch = fmt.Sprintf("marshal/fix-ci-%s-%s", shortSHA(ev.sha), slug(check.Name))
	}
	if a, ok := f.ws.Agent(res.AgentID); ok {
		// The PR targets the failing branch, and carries a name a reviewer
		// can read.
		a.Name = fmt.Sprintf("Fix CI: %s @ %s", check.Name, shortSHA(ev.sha))
		a.TargetBranch = base
		if err := f.ws.PutAgent(a); err != nil {
			record(ciGaveUp, "ship failed: "+err.Error())
			return
		}
	}
	exit, err := f.autoExit(ctx, res.AgentID, opts)
	switch {
	case err != nil:
		record(ciGaveUp, "ship failed: "+err.Error())
	case exit.Blocked:
		record(ciGaveUp, "gate failed")
	case exit.Destination != "push":
		record(ciGaveUp, "ship target is "+exit.Destination+", not push")
	default:
		entry.PRURL = exit.PRUrl
		reason := "opened a pull request"
		if push {
			reason = "pushed to " + base
		} else if exit.PRUrl == "" {
			reason = "pushed " + exit.Branch + "; no pull request URL"
		}
		record(ciFixed, reason)
	}
}

// pollCI is the polling fallback: the tip of each watched branch (a glob
// cannot be polled) is checked for failed checks.
func (f *Fleet) pollCI(ctx context.Context, r Repo, ci *CIFixerSettings, forge Forge, cred Credential) error {
	for _, branch := range ci.Branches {
		if isGlob(branch) {
			continue
		}
		sha, err := f.branchHead(ctx, r, branch)
		if err != nil {
			slog.Default().Warn("webbridge: CI fixer poll", "repo", r.ID, "branch", branch, "err", err)
			continue
		}
		checks, err := forge.ListFailedChecks(ctx, r, sha, cred)
		if err != nil {
			f.noteRateLimit(r.ID, err)
			return err
		}
		if len(checks) == 0 {
			continue
		}
		if err := f.fixCI(ctx, checkEvent{repoID: r.ID, ref: branch, sha: sha}); err != nil {
			slog.Default().Warn("webbridge: CI fixer poll", "repo", r.ID, "branch", branch, "err", err)
		}
	}
	return nil
}

// branchHead resolves a branch's tip commit in the repo's mirror.
func (f *Fleet) branchHead(ctx context.Context, r Repo, branch string) (string, error) {
	if f.auto.branchHead != nil {
		return f.auto.branchHead(ctx, r, branch)
	}
	if f.git == nil {
		return "", errors.New("git is not available")
	}
	cred, err := f.creds.Resolve(ctx, DefaultOwnerID, r.CredRef)
	if err != nil {
		return "", err
	}
	mirror, err := f.git.EnsureMirror(f.stateDir, r.URL, cred)
	if err != nil {
		return "", err
	}
	out, err := f.git.run(mirror, Credential{Kind: "none"}, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ---- HTTP ----

// CIHistory lists the CI fixer's history, newest first, optionally for one
// project (the projects whose settings name the entry's repo).
func (f *Fleet) CIHistory(project string) []CIHistoryEntry {
	out := []CIHistoryEntry{}
	for _, h := range f.ciHistory().all() {
		if project == "" || f.projectWatchesCI(project, h.RepoID) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (f *Fleet) projectWatchesCI(project, repoID string) bool {
	ps := f.ws.ProjectSettingsFor(project)
	return ps.Automations != nil && ps.Automations.CIFixer != nil && ps.Automations.CIFixer.RepoID == repoID
}

func (s *Server) listCIHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": s.fleet.CIHistory(r.URL.Query().Get("project"))})
}

func (s *Server) getCIHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	h, err := s.fleet.ciHistory().get(r.PathValue("id"))
	if err != nil {
		writeAutomationErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}
