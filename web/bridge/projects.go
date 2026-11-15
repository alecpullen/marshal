package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// errInvalidProjectSettings marks a settings request the bridge refuses.
// The HTTP layer maps it to 400.
var errInvalidProjectSettings = errors.New("bridge: invalid project settings")

// validShipTargets is the set a project may name.
var validShipTargets = map[string]bool{"merge": true, "push": true, "patch": true}

// shipTargetValidFor reports whether a ship target can apply to an agent:
// a local checkout merges, a git checkout pushes or exports a patch, and a
// read-only clone only exports a patch. Merge is never allowed for a
// git-sourced agent.
func shipTargetValidFor(a Agent, target string) bool {
	switch {
	case a.SourceKind != "git":
		return target == "merge"
	case a.ReadOnly:
		return target == "patch"
	}
	return target == "push" || target == "patch"
}

// ProjectSettingsByRepo returns the settings of the first project (by root)
// whose intake names repoID.
func (w *Workspace) ProjectSettingsByRepo(repoID string) (ProjectSettings, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	roots := make([]string, 0, len(w.projectSettings))
	for root := range w.projectSettings {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if ps := w.projectSettings[root]; ps.Intake.RepoID == repoID {
			return ps, true
		}
	}
	return ProjectSettings{}, false
}

// ProjectRootByRepo returns the root of the first project (by root) whose
// intake names repoID, or "".
func (w *Workspace) ProjectRootByRepo(repoID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	roots := make([]string, 0, len(w.projectSettings))
	for root := range w.projectSettings {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if w.projectSettings[root].Intake.RepoID == repoID {
			return root
		}
	}
	return ""
}

// workspaceRoots is where an agent's repo template is read and whose
// trust governs it: a local agent's project for both, a git agent's
// prepared checkout and the registered project that names its repo.
func (f *Fleet) workspaceRoots(a Agent) (templateRoot, trustRoot string) {
	if a.SourceKind != "git" {
		return a.Project, a.Project
	}
	sub := a.WorkSubpath
	if sub == "" {
		sub = "work/" + a.ID
	}
	return filepath.Join(f.stateDir, filepath.FromSlash(sub)), f.ws.ProjectRootByRepo(a.SourceRef)
}

// settingsForAgent finds the project settings that govern an agent: its
// own project for a local checkout, the project whose intake names its
// repo for a git one.
func (f *Fleet) settingsForAgent(a Agent) ProjectSettings {
	if a.SourceKind != "git" {
		return f.ws.ProjectSettingsFor(a.Project)
	}
	ps, _ := f.ws.ProjectSettingsByRepo(a.SourceRef)
	return ps
}

// shipDestination is where an agent's work leaves the fleet: the derived
// destination, unless the project names a valid ship target.
func (f *Fleet) shipDestination(a Agent) string {
	if t := f.settingsForAgent(a).ShipTarget; t != "" && shipTargetValidFor(a, t) {
		return t
	}
	return exitDestination(a)
}

// registeredProject checks root is a registered project.
func (f *Fleet) registeredProject(root string) error {
	for _, p := range f.ws.Projects() {
		if p == root {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not a registered project", errInvalidProjectSettings, root)
}

// PutProjectSettings validates and stores a project's settings, mirrors
// the intake label onto the registered repo's watcher, and audits it.
func (f *Fleet) PutProjectSettings(root string, ps ProjectSettings) error {
	_, err := f.putProjectSettings(root, ps)
	return err
}

// putProjectSettings is PutProjectSettings returning the settings as stored,
// which is the validated form: the same value the workspace holds, so a
// caller's ETag names what was written rather than a later racing read.
func (f *Fleet) putProjectSettings(root string, ps ProjectSettings) (ProjectSettings, error) {
	ps, repo, err := f.validateProjectSettings(root, ps)
	if err != nil {
		return ProjectSettings{}, err
	}
	prev := f.ws.ProjectSettingsFor(root)
	if err := f.ws.PutProjectSettings(root, ps); err != nil {
		return ProjectSettings{}, err
	}
	f.applyProjectSettings(root, ps, prev, repo)
	return ps, nil
}

// putProjectSettingsIfMatch is the conditional form. It evaluates the
// precondition before it validates the body, so a caller writing from a stale
// read is told its view is stale (ok false, the HTTP layer's 412) even when
// the body it sends would no longer validate: it is describing a project
// another writer has since changed, and "validate this again" would be the
// wrong instruction. The store then repeats the comparison while holding the
// lock that performs the write, so the check and the write cannot be split by
// a racing writer, and neither the intake watcher nor the audit record is
// touched unless that write lands.
//
// On a mismatch it returns the settings it saw, which is what the caller's
// ETag is compared against, so the 412 can name the value that won.
func (f *Fleet) putProjectSettingsIfMatch(root string, ps ProjectSettings, expected string) (ProjectSettings, bool, error) {
	if err := f.registeredProject(root); err != nil {
		return ProjectSettings{}, false, err
	}
	current, err := f.ws.ProjectSettingsETag(root)
	if err != nil {
		return ProjectSettings{}, false, err
	}
	if current != expected {
		return f.ws.ProjectSettingsFor(root), false, nil
	}
	ps, repo, err := f.validateProjectSettings(root, ps)
	if err != nil {
		return ProjectSettings{}, false, err
	}
	ok, prev, err := f.ws.PutProjectSettingsIfMatch(root, ps, expected)
	if err != nil {
		return ProjectSettings{}, false, err
	}
	if !ok {
		return prev, false, nil
	}
	f.applyProjectSettings(root, ps, prev, repo)
	return ps, true, nil
}

// validateProjectSettings checks and canonicalizes one settings write. Both the
// unconditional and the conditional route go through it, so they accept
// exactly the same writes. It returns the settings to store, and the repo the
// intake names when one is named.
func (f *Fleet) validateProjectSettings(root string, ps ProjectSettings) (ProjectSettings, Repo, error) {
	if err := f.registeredProject(root); err != nil {
		return ProjectSettings{}, Repo{}, err
	}
	if ps.Workspace != "" {
		if _, err := ParseWSRef(ps.Workspace); err != nil {
			return ProjectSettings{}, Repo{}, fmt.Errorf("%w: workspace: %v", errInvalidProjectSettings, err)
		}
	}
	if ps.Mode != "" || ps.Auto != nil {
		mode, auto, _, err := canonicalModeTuple(ps.Mode, ps.Auto)
		if err != nil {
			return ProjectSettings{}, Repo{}, fmt.Errorf("%w: %v", errInvalidProjectSettings, err)
		}
		ps.Mode, ps.Auto = mode, auto
	}
	if ps.ShipTarget != "" && !validShipTargets[ps.ShipTarget] {
		return ProjectSettings{}, Repo{}, fmt.Errorf("%w: shipTarget must be merge, push or patch", errInvalidProjectSettings)
	}
	if len(ps.Routing) > 0 && string(ps.Routing) != "null" {
		var obj map[string]json.RawMessage
		if json.Unmarshal(ps.Routing, &obj) != nil {
			return ProjectSettings{}, Repo{}, fmt.Errorf("%w: routing must be a JSON object", errInvalidProjectSettings)
		}
	} else {
		ps.Routing = nil
	}
	var repo Repo
	if id := ps.Intake.RepoID; id != "" {
		var ok bool
		if repo, ok = f.ws.Repo(id); !ok {
			return ProjectSettings{}, Repo{}, fmt.Errorf("%w: intake.repoId %q is not a registered repo", errInvalidProjectSettings, id)
		}
	} else if len(ps.Intake.Labels) > 0 {
		return ProjectSettings{}, Repo{}, fmt.Errorf("%w: intake.labels need an intake.repoId", errInvalidProjectSettings)
	}

	if err := f.validateAutomations(ps.Automations); err != nil {
		return ProjectSettings{}, Repo{}, err
	}
	// The defaults are validated here, not in the store: Workspace.PutProjectSettings
	// stays a dumb store, exactly as it is for Automations. A zero limit is
	// "unset" and passes; factoryValidateDefaults fills nothing in.
	if err := factoryValidateDefaults(ps.FactoryDefaults); err != nil {
		return ProjectSettings{}, Repo{}, fmt.Errorf("%w: factoryDefaults: %v", errInvalidProjectSettings, err)
	}
	return ps, repo, nil
}

// applyProjectSettings performs the side effects of a settings write that has
// already been stored: the intake label is mirrored onto the registered repo's
// watcher, and the change is audited. prev is the value the write replaced.
//
// It reports no error on purpose. The settings are committed before it runs,
// so a watcher failure cannot be undone by failing the request: the caller
// would be told its write did not happen, would retry, and would then be
// refused with 412 because the write it made is exactly what moved the tag.
// The failure is logged instead, and the audit entry is still written, because
// the user's change did take effect.
func (f *Fleet) applyProjectSettings(root string, ps, prev ProjectSettings, repo Repo) {
	// The poller supports one label, so the first one is the watcher's.
	if ps.Intake.RepoID != "" && !sameIntake(prev.Intake, ps.Intake) {
		repo.Watch = len(ps.Intake.Labels) > 0
		repo.WatchLabel = ""
		if repo.Watch {
			repo.WatchLabel = ps.Intake.Labels[0]
		}
		if err := f.ws.PutRepo(repo); err != nil {
			slog.Default().Warn("webbridge: mirror intake label onto repo watcher",
				"project", root, "repo", repo.ID, "err", err)
		}
	}
	f.auditf(AuditEvent{Event: AuditProjectSettings, OwnerID: DefaultOwnerID, RepoID: ps.Intake.RepoID, Detail: filepath.Base(root)})
}

func sameIntake(a, b ProjectIntake) bool {
	if a.RepoID != b.RepoID || len(a.Labels) != len(b.Labels) {
		return false
	}
	for i := range a.Labels {
		if a.Labels[i] != b.Labels[i] {
			return false
		}
	}
	return true
}

// MirrorHealth is one repo's mirror freshness.
type MirrorHealth struct {
	RepoID string `json:"repoId"`
	// Present is false when the repo has no mirror yet.
	Present bool `json:"present"`
	// AgeSeconds is the time since the mirror last fetched.
	AgeSeconds int64  `json:"ageSeconds,omitempty"`
	Head       string `json:"head,omitempty"`
}

// WorkspaceHealth is whether a project's default workspace is usable.
type WorkspaceHealth struct {
	Ref      string `json:"ref"`
	Resolves bool   `json:"resolves"`
	Built    bool   `json:"built"`
	Error    string `json:"error,omitempty"`
}

// ProjectHealth is GET /api/projects/health.
type ProjectHealth struct {
	// GateRunnable is "yes", "no" (the last verify was skipped: nothing
	// to run) or "unknown" (no agent of the project has verified).
	GateRunnable      string           `json:"gateRunnable"`
	MirrorFresh       []MirrorHealth   `json:"mirrorFresh"`
	OrphanWorktrees   []string         `json:"orphanWorktrees"`
	Trust             string           `json:"trust"`
	WorkspaceResolves *WorkspaceHealth `json:"workspaceResolves,omitempty"`
	// Verify is the project's verify-gate commands from [sdd.verify] in
	// .marshal/config.toml. Empty strings mean unset, in which case the
	// gate is skipped (nothing to run) unless the engine detects a default.
	// Only reported for a trusted project, since the engine ignores an
	// untrusted project's config.
	Verify VerifyCommands `json:"verify"`
}

// VerifyCommands is a project's configured gate commands.
type VerifyCommands struct {
	Build string `json:"build"`
	Test  string `json:"test"`
}

// ProjectHealth reports a registered project's health.
func (f *Fleet) ProjectHealth(ctx context.Context, root string) (ProjectHealth, error) {
	if err := f.registeredProject(root); err != nil {
		return ProjectHealth{}, err
	}
	ps := f.ws.ProjectSettingsFor(root)
	h := ProjectHealth{GateRunnable: "unknown", MirrorFresh: []MirrorHealth{}, OrphanWorktrees: []string{}, Trust: projectTrust(root)}

	for _, st := range f.ProjectStatus() {
		if st.Root == root {
			h.Trust = string(st.Trust)
			if st.OrphanWorktrees != nil {
				h.OrphanWorktrees = st.OrphanWorktrees
			}
		}
	}

	if h.Trust == "trusted" {
		h.Verify = readVerifyCommands(root)
	}

	// The most recent stored gate among the project's agents.
	var latest time.Time
	f.gatesMu.Lock()
	for _, a := range f.ws.Agents() {
		if a.Project != root && (a.SourceKind != "git" || ps.Intake.RepoID == "" || a.SourceRef != ps.Intake.RepoID) {
			continue
		}
		if rec, ok := f.gates[a.ID]; ok && rec.Result != nil && rec.At.After(latest) {
			latest = rec.At
			h.GateRunnable = "yes"
			if rec.Result.Skipped {
				h.GateRunnable = "no"
			}
		}
	}
	f.gatesMu.Unlock()

	repos := f.ws.Repos()
	if ps.Intake.RepoID != "" {
		repos = nil
		if r, ok := f.ws.Repo(ps.Intake.RepoID); ok {
			repos = []Repo{r}
		}
	}
	for _, r := range repos {
		h.MirrorFresh = append(h.MirrorFresh, f.mirrorHealth(r))
	}

	if ps.Workspace != "" {
		wh := &WorkspaceHealth{Ref: ps.Workspace}
		ref, err := ParseWSRef(ps.Workspace)
		if err == nil {
			var res Resolved
			if res, err = f.ResolveWorkspace(ctx, ref, root); err == nil {
				wh.Resolves = true
				_, berr := f.builtImage(res)
				wh.Built = berr == nil
				if berr != nil {
					wh.Error = berr.Error()
				}
			}
		}
		if err != nil {
			wh.Error = err.Error()
		}
		h.WorkspaceResolves = wh
	}
	return h, nil
}

// mirrorHealth reads a repo's mirror: whether it exists, how long since it
// fetched, and its head branch.
func (f *Fleet) mirrorHealth(r Repo) MirrorHealth {
	mh := MirrorHealth{RepoID: r.ID}
	dir := mirrorDir(f.stateDir, r.URL)
	fi, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil {
		return mh
	}
	mh.Present = true
	mtime := fi.ModTime()
	// FETCH_HEAD moves on every fetch; HEAD only when the default branch does.
	if ff, err := os.Stat(filepath.Join(dir, "FETCH_HEAD")); err == nil && ff.ModTime().After(mtime) {
		mtime = ff.ModTime()
	}
	if age := f.now().Sub(mtime); age > 0 {
		mh.AgeSeconds = int64(age.Seconds())
	}
	if f.git != nil {
		if head, err := f.git.mirrorHead(dir); err == nil {
			mh.Head = head
		}
	}
	return mh
}

func (s *Server) projectSettingsRoutes() {
	s.mux.HandleFunc("GET /api/projects/settings", s.getProjectSettings)
	s.mux.HandleFunc("PUT /api/projects/settings", s.putProjectSettings)
	s.mux.HandleFunc("GET /api/projects/health", s.getProjectHealth)
}

func (s *Server) getProjectSettings(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if err := s.fleet.registeredProject(root); err != nil {
		writeErr(w, err)
		return
	}
	// The tag and the body come from one read, so a client that PUTs with
	// this If-Match is comparing against exactly what it was shown.
	ps := s.fleet.ws.ProjectSettingsFor(root)
	writeSettings(w, http.StatusOK, ps)
}

// settingsIfMatch reads the ETag a conditional settings write names. A missing
// header is the legacy unconditional write. The tag is opaque to clients, but
// it has exactly one form — the quoted tag a GET returns — so a wildcard, a
// list, an unquoted value or anything else is a malformed request (400), not a
// conflict: the bridge cannot compare a version it never emitted. Hex digits
// in upper case are still a well-formed tag, so they are compared rather than
// rejected; they simply never equal the lower-case tag the bridge emits, which
// makes such a write a conflict (412), not a malformed request.
func settingsIfMatch(r *http.Request) (string, bool, error) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) > 1 {
		return "", false, fmt.Errorf("%w: If-Match must name exactly one ETag", errInvalidProjectSettings)
	}
	tag := values[0]
	if len(tag) != 66 || tag[0] != '"' || tag[65] != '"' {
		return "", false, fmt.Errorf("%w: If-Match must be one quoted ETag", errInvalidProjectSettings)
	}
	for i := 1; i < 65; i++ {
		c := tag[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return "", false, fmt.Errorf("%w: If-Match must be one quoted ETag", errInvalidProjectSettings)
		}
	}
	return tag, true, nil
}

// writeSettings responds with the settings as stored and the ETag naming that
// exact value, never a later read: a racing writer must not be able to make
// the response describe someone else's write.
func writeSettings(w http.ResponseWriter, status int, ps ProjectSettings) {
	tag, err := projectSettingsETag(ps)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, status, ps)
}

// writeStale answers a conditional write whose If-Match no longer names the
// stored settings: 412, changing nothing. It carries the ETag of the value
// that is stored now, so a client can tell what won and retry against it
// rather than discovering the change with an extra GET.
func writeStale(w http.ResponseWriter, ps ProjectSettings) {
	if tag, err := projectSettingsETag(ps); err == nil {
		w.Header().Set("ETag", tag)
	}
	writeJSON(w, http.StatusPreconditionFailed,
		map[string]string{"error": "project settings changed since the If-Match ETag"})
}

func (s *Server) putProjectSettings(w http.ResponseWriter, r *http.Request) {
	var ps ProjectSettings
	if !decodeJSON(w, r, &ps) {
		return
	}
	root := r.URL.Query().Get("root")
	expected, conditional, err := settingsIfMatch(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Delegation settings are policy: this project's toggle and designated
	// client decide whether a client may answer as the planner, so the write
	// takes the same lock authorization reads them under. An ordinary settings
	// save on this path would be correct without the lock, but sharing it is
	// simpler than deciding, per field, whether this write is the policy one.
	policy := s.factoryAuth
	if conditional {
		var stored ProjectSettings
		var ok bool
		if err := policy.withPolicy(func() error {
			var perr error
			stored, ok, perr = s.fleet.putProjectSettingsIfMatch(root, ps, expected)
			return perr
		}); err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			// The stored settings moved since the caller read them. Nothing
			// was written, mirrored onto the watcher or audited. The response
			// names the value that won, so a client that needs to merge can
			// compare against it without a second round trip.
			writeStale(w, stored)
			return
		}
		writeSettings(w, http.StatusOK, stored)
		return
	}
	var stored ProjectSettings
	if err := policy.withPolicy(func() error {
		var perr error
		stored, perr = s.fleet.putProjectSettings(root, ps)
		return perr
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeSettings(w, http.StatusOK, stored)
}

func (s *Server) getProjectHealth(w http.ResponseWriter, r *http.Request) {
	h, err := s.fleet.ProjectHealth(r.Context(), r.URL.Query().Get("root"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}
