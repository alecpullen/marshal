package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// errInvalidProjectSettings marks a settings request the bridge refuses.
// The HTTP layer maps it to 400.
var errInvalidProjectSettings = errors.New("bridge: invalid project settings")

// validApprovalModes mirrors the engine's interaction modes.
var validApprovalModes = map[string]bool{"plan": true, "default": true, "edit": true, "copilot": true, "auto": true}

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
	if err := f.registeredProject(root); err != nil {
		return err
	}
	if ps.Workspace != "" {
		if _, err := ParseWSRef(ps.Workspace); err != nil {
			return fmt.Errorf("%w: workspace: %v", errInvalidProjectSettings, err)
		}
	}
	if ps.Mode != "" && !validApprovalModes[ps.Mode] {
		return fmt.Errorf("%w: mode %q is not one of plan, default, edit, copilot, auto", errInvalidProjectSettings, ps.Mode)
	}
	if ps.ShipTarget != "" && !validShipTargets[ps.ShipTarget] {
		return fmt.Errorf("%w: shipTarget must be merge, push or patch", errInvalidProjectSettings)
	}
	if len(ps.Routing) > 0 && string(ps.Routing) != "null" {
		var obj map[string]json.RawMessage
		if json.Unmarshal(ps.Routing, &obj) != nil {
			return fmt.Errorf("%w: routing must be a JSON object", errInvalidProjectSettings)
		}
	} else {
		ps.Routing = nil
	}
	var repo Repo
	if id := ps.Intake.RepoID; id != "" {
		var ok bool
		if repo, ok = f.ws.Repo(id); !ok {
			return fmt.Errorf("%w: intake.repoId %q is not a registered repo", errInvalidProjectSettings, id)
		}
	} else if len(ps.Intake.Labels) > 0 {
		return fmt.Errorf("%w: intake.labels need an intake.repoId", errInvalidProjectSettings)
	}

	prev := f.ws.ProjectSettingsFor(root)
	if err := f.ws.PutProjectSettings(root, ps); err != nil {
		return err
	}
	// The poller supports one label, so the first one is the watcher's.
	if ps.Intake.RepoID != "" && !sameIntake(prev.Intake, ps.Intake) {
		repo.Watch = len(ps.Intake.Labels) > 0
		repo.WatchLabel = ""
		if repo.Watch {
			repo.WatchLabel = ps.Intake.Labels[0]
		}
		if err := f.ws.PutRepo(repo); err != nil {
			return err
		}
	}
	f.auditf(AuditEvent{Event: AuditProjectSettings, OwnerID: DefaultOwnerID, RepoID: ps.Intake.RepoID, Detail: filepath.Base(root)})
	return nil
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
			h.Trust = st.Trust
			if st.OrphanWorktrees != nil {
				h.OrphanWorktrees = st.OrphanWorktrees
			}
		}
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
	writeJSON(w, http.StatusOK, s.fleet.ws.ProjectSettingsFor(root))
}

func (s *Server) putProjectSettings(w http.ResponseWriter, r *http.Request) {
	var ps ProjectSettings
	if !decodeJSON(w, r, &ps) {
		return
	}
	root := r.URL.Query().Get("root")
	if err := s.fleet.PutProjectSettings(root, ps); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.ws.ProjectSettingsFor(root))
}

func (s *Server) getProjectHealth(w http.ResponseWriter, r *http.Request) {
	h, err := s.fleet.ProjectHealth(r.Context(), r.URL.Query().Get("root"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}
