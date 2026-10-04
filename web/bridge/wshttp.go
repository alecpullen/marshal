package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (s *Server) workspaceRoutes() {
	s.mux.HandleFunc("GET /api/workspaces", s.listWorkspaces)
	s.mux.HandleFunc("POST /api/workspaces", s.createWorkspace)
	s.mux.HandleFunc("GET /api/workspaces/{name}", s.getWorkspace)
	s.mux.HandleFunc("DELETE /api/workspaces/{name}", s.deleteWorkspace)
	s.mux.HandleFunc("PUT /api/workspaces/{name}/draft", s.putWorkspaceDraft)
	s.mux.HandleFunc("POST /api/workspaces/{name}/patch", s.patchWorkspaceDraft)
	s.mux.HandleFunc("POST /api/workspaces/{name}/publish", s.publishWorkspace)
	s.mux.HandleFunc("GET /api/workspaces/{name}/diff", s.diffWorkspace)
	s.mux.HandleFunc("PUT /api/workspaces/{name}/pool", s.setWorkspacePool)
	s.mux.HandleFunc("POST /api/workspaces/{name}/builds", s.startWorkspaceBuild)
	s.mux.HandleFunc("GET /api/workspaces/{name}/builds", s.listWorkspaceBuilds)
	s.mux.HandleFunc("GET /api/workspaces/{name}/builds/{n}/events", s.workspaceBuildEvents)
}

// workspaceEntry is one row of GET /api/workspaces.
type workspaceEntry struct {
	Source    string            `json:"source"` // "studio" or "repo"
	Name      string            `json:"name"`
	Project   string            `json:"project,omitempty"`
	Published int               `json:"published,omitempty"`
	Pool      int               `json:"pool,omitempty"`
	Versions  []TemplateVersion `json:"versions,omitempty"`
	// Usage counts the agents that run on this workspace.
	Usage int `json:"usage"`
}

// workspaceUsage counts agents by "<source>:<name>".
func (f *Fleet) workspaceUsage() map[string]int {
	use := map[string]int{}
	for _, a := range f.ws.Agents() {
		if a.Workspace != nil {
			use[a.Workspace.Source+":"+a.Workspace.Name]++
		}
	}
	return use
}

// templateInUse reports whether any agent runs on a Studio template.
func (f *Fleet) templateInUse(name string) bool { return f.workspaceUsage()["studio:"+name] > 0 }

// ListWorkspaces returns Studio templates plus the repo templates of every
// trusted project.
func (f *Fleet) ListWorkspaces() ([]workspaceEntry, error) {
	metas, err := f.templates.List()
	if err != nil {
		return nil, err
	}
	use := f.workspaceUsage()
	out := []workspaceEntry{}
	for _, m := range metas {
		out = append(out, workspaceEntry{Source: "studio", Name: m.Name, Published: m.Published, Pool: m.Pool, Versions: m.Versions, Usage: use["studio:"+m.Name]})
	}
	for _, root := range f.ws.Projects() {
		if projectTrust(root) != "trusted" {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(root, ".marshal", "workspaces", "*.toml"))
		sort.Strings(files)
		for _, file := range files {
			name := strings.TrimSuffix(filepath.Base(file), ".toml")
			if validTemplateName(name) != nil {
				continue
			}
			out = append(out, workspaceEntry{Source: "repo", Name: name, Project: root, Usage: use["repo:"+name]})
		}
	}
	return out, nil
}

// workspaceFormat renders a doc to canonical source via the control agent.
func (f *Fleet) workspaceFormat(ctx context.Context, doc WSDoc) ([]byte, error) {
	raw, err := f.controlCall(ctx, "workspace/format", map[string]any{"doc": doc}, false)
	if err != nil {
		return nil, err
	}
	var res struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("bridge: decode workspace/format: %w", err)
	}
	return []byte(res.Source), nil
}

// starterSource returns a starter's text renamed to name.
func starterSource(id, name string) ([]byte, bool) {
	src, ok := workspaceStarters[id]
	if !ok {
		return nil, false
	}
	return []byte(strings.Replace(src, `name = "`+id+`"`, `name = "`+name+`"`, 1)), true
}

var errInvalidWorkspace = errors.New("bridge: invalid workspace request")

// CreateTemplate creates a Studio template from a blank doc, a starter, a
// devcontainer image or an agent snapshot.
func (f *Fleet) CreateTemplate(ctx context.Context, name, from string) (TemplateMeta, error) {
	if err := validTemplateName(name); err != nil {
		return TemplateMeta{}, err
	}
	var src []byte
	var snapshotOf string
	switch {
	case from == "" || from == "blank":
		src, _ = starterSource("minimal", name)
	case strings.HasPrefix(from, "starter:"):
		var ok bool
		if src, ok = starterSource(strings.TrimPrefix(from, "starter:"), name); !ok {
			return TemplateMeta{}, fmt.Errorf("%w: unknown starter %q", errInvalidWorkspace, from)
		}
	case strings.HasPrefix(from, "devcontainer:"):
		root := strings.TrimPrefix(from, "devcontainer:")
		known := false
		for _, p := range f.ws.Projects() {
			known = known || p == root
		}
		if !known {
			return TemplateMeta{}, fmt.Errorf("%w: %s is not a registered project", errInvalidWorkspace, root)
		}
		image, reason, ok := devcontainerImage(root)
		if !ok {
			if reason == "" {
				reason = "no devcontainer.json image found"
			}
			return TemplateMeta{}, fmt.Errorf("%w: %s", errInvalidWorkspace, reason)
		}
		var err error
		if src, err = f.workspaceFormat(ctx, WSDoc{Workspace: WSWorkspace{Name: name, Base: image}}); err != nil {
			return TemplateMeta{}, err
		}
	case strings.HasPrefix(from, "snapshot:"):
		tag, err := f.snapshotAgent(strings.TrimPrefix(from, "snapshot:"), name)
		if err != nil {
			return TemplateMeta{}, err
		}
		if src, err = f.workspaceFormat(ctx, WSDoc{Workspace: WSWorkspace{Name: name, Base: tag}}); err != nil {
			return TemplateMeta{}, err
		}
		snapshotOf = strings.TrimPrefix(from, "snapshot:")
	default:
		return TemplateMeta{}, fmt.Errorf("%w: from must be blank, starter:<id>, devcontainer:<root> or snapshot:<agentId>", errInvalidWorkspace)
	}
	meta, err := f.templates.Create(name, src, DefaultOwnerID)
	if err != nil {
		return TemplateMeta{}, err
	}
	f.auditf(AuditEvent{Event: AuditWorkspaceCreated, OwnerID: DefaultOwnerID, Detail: name})
	if snapshotOf != "" {
		f.auditf(AuditEvent{Event: AuditWorkspaceSnapshot, OwnerID: DefaultOwnerID, AgentID: snapshotOf, Detail: name})
	}
	return meta, nil
}

// snapshotAgent commits a running container agent's filesystem to an image
// and returns its tag. Only container agents qualify.
func (f *Fleet) snapshotAgent(agentID, name string) (string, error) {
	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		return "", err
	}
	if !rt.containerized {
		return "", fmt.Errorf("%w: agent %s does not run in a container", errInvalidWorkspace, agentID)
	}
	a, ok := f.ws.Agent(agentID)
	if !ok {
		return "", fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	container := containerNameFor(agentID)
	if a.ContainerName != "" {
		container = a.ContainerName
	}
	runtime, err := f.runtimeName()
	if err != nil {
		return "", err
	}
	tag := "marshal-ws/" + name + "-snap:" + strconv.FormatInt(f.now().Unix(), 10)
	if out, err := f.runRuntime(runtime, "commit", container, tag); err != nil {
		return "", fmt.Errorf("bridge: snapshot %s: %w (%s)", agentID, err, strings.TrimSpace(string(out)))
	}
	return tag, nil
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	entries, err := s.fleet.ListWorkspaces()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		From string `json:"from"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	meta, err := s.fleet.CreateTemplate(r.Context(), body.Name, body.From)
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

// writeWorkspaceErr is writeErr plus the request-shape error.
func writeWorkspaceErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvalidWorkspace) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeErr(w, err)
}

// workspaceView is a template's source with its parse.
type workspaceView struct {
	Source      string       `json:"source"`
	Version     int          `json:"version"`
	Doc         WSDoc        `json:"doc"`
	Sections    []WSSection  `json:"sections"`
	Diagnostics []WSDiag     `json:"diagnostics"`
	Meta        TemplateMeta `json:"meta"`
}

func (f *Fleet) viewTemplate(ctx context.Context, name string, version int, draftFallback bool) (workspaceView, error) {
	meta, err := f.templates.Meta(name)
	if err != nil {
		return workspaceView{}, err
	}
	src, err := f.templates.Read(name, version)
	if errors.Is(err, ErrTemplateNoDraft) && version == 0 && draftFallback && meta.Published > 0 {
		version = meta.Published
		src, err = f.templates.Read(name, version)
	}
	if err != nil {
		return workspaceView{}, err
	}
	doc, sections, diags, err := f.parseWorkspace(ctx, src)
	if err != nil {
		return workspaceView{}, err
	}
	return workspaceView{Source: string(src), Version: version, Doc: doc, Sections: sections, Diagnostics: diags, Meta: meta}, nil
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be a non-negative integer"})
			return
		}
		version = n
	}
	view, err := s.fleet.viewTemplate(r.Context(), r.PathValue("name"), version, r.URL.Query().Get("version") == "")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) putWorkspaceDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	if err := s.fleet.templates.SaveDraft(name, []byte(body.Source)); err != nil {
		writeErr(w, err)
		return
	}
	view, err := s.fleet.viewTemplate(r.Context(), name, 0, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) patchWorkspaceDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Layer int             `json:"layer"`
		Value json.RawMessage `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	src, err := s.fleet.templates.Read(name, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, err := s.fleet.controlCall(r.Context(), "workspace/patch", map[string]any{"source": string(src), "layer": body.Layer, "value": body.Value}, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	var res struct {
		Source      string      `json:"source"`
		Doc         WSDoc       `json:"doc"`
		Sections    []WSSection `json:"sections"`
		Diagnostics []WSDiag    `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		writeErr(w, fmt.Errorf("bridge: decode workspace/patch: %w", err))
		return
	}
	if err := s.fleet.templates.SaveDraft(name, []byte(res.Source)); err != nil {
		writeErr(w, err)
		return
	}
	meta, _ := s.fleet.templates.Meta(name)
	writeJSON(w, http.StatusOK, workspaceView{Source: res.Source, Doc: res.Doc, Sections: res.Sections, Diagnostics: res.Diagnostics, Meta: meta})
}

func (s *Server) publishWorkspace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	view, err := s.fleet.viewTemplate(r.Context(), name, 0, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	if hasWSErrors(view.Diagnostics) {
		writeErr(w, fmt.Errorf("%w: %s", ErrWorkspaceInvalid, firstWSError(view.Diagnostics)))
		return
	}
	v, err := s.fleet.templates.Publish(name, DefaultOwnerID)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditWorkspacePublished, OwnerID: DefaultOwnerID, Detail: name + "@" + strconv.Itoa(v.N)})
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.fleet.templates.Delete(name, s.fleet.templateInUse); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.pools.drop(name)
	s.fleet.auditf(AuditEvent{Event: AuditWorkspaceDeleted, OwnerID: DefaultOwnerID, Detail: name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setWorkspacePool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Size int `json:"size"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	if err := s.fleet.templates.SetPool(name, body.Size); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.pools.resize(name)
	writeJSON(w, http.StatusOK, map[string]int{"size": body.Size})
}

func (s *Server) diffWorkspace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	read := func(key string) (string, int, bool) {
		n := 0
		if v := r.URL.Query().Get(key); v != "" {
			var err error
			if n, err = strconv.Atoi(v); err != nil || n < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": key + " must be a non-negative integer"})
				return "", 0, false
			}
		}
		src, err := s.fleet.templates.Read(name, n)
		if err != nil {
			writeErr(w, err)
			return "", 0, false
		}
		return string(src), n, true
	}
	a, an, ok := read("a")
	if !ok {
		return
	}
	b, bn, ok := read("b")
	if !ok {
		return
	}
	label := func(n int) string {
		if n == 0 {
			return name + " (draft)"
		}
		return name + "@" + strconv.Itoa(n)
	}
	writeJSON(w, http.StatusOK, map[string]string{"diff": unifiedDiff(a, b, label(an), label(bn))})
}

// startWorkspaceBuild claims a build slot for a version and builds in the
// background.
func (s *Server) startWorkspaceBuild(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version int `json:"version"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	meta, err := s.fleet.templates.Meta(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	n := body.Version
	if n == 0 {
		n = meta.Published
	}
	if _, ok := meta.version(n); !ok {
		writeErr(w, fmt.Errorf("%w: %s has no version %d to build", ErrTemplateNotFound, name, n))
		return
	}
	if err := s.fleet.StartBuild(name, n); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"version": n})
}

func (s *Server) listWorkspaceBuilds(w http.ResponseWriter, r *http.Request) {
	meta, err := s.fleet.templates.Meta(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": meta.Versions,
		"pool":     s.fleet.pools.status(meta),
		"starts":   s.fleet.pools.startMedians(meta.Name),
	})
}

func (s *Server) workspaceBuildEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "build number must be a positive integer"})
		return
	}
	if _, err := s.fleet.templates.Meta(name); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.buildLog.ServeSSEKeyFromStart(w, r, buildKey(name, n))
}

// unifiedDiff renders a line diff of a and b as unified text, with three
// lines of context, from a longest-common-subsequence edit script.
func unifiedDiff(a, b, labelA, labelB string) string {
	al, bl := splitLines(a), splitLines(b)
	// lcs[i][j] is the LCS length of al[i:] and bl[j:].
	lcs := make([][]int, len(al)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(bl)+1)
	}
	for i := len(al) - 1; i >= 0; i-- {
		for j := len(bl) - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < len(al) || j < len(bl) {
		switch {
		case i < len(al) && j < len(bl) && al[i] == bl[j]:
			ops = append(ops, op{' ', al[i]})
			i++
			j++
		case i < len(al) && (j == len(bl) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', al[i]})
			i++
		default:
			ops = append(ops, op{'+', bl[j]})
			j++
		}
	}
	changed := false
	for _, o := range ops {
		changed = changed || o.kind != ' '
	}
	if !changed {
		return ""
	}
	const context = 3
	var out strings.Builder
	out.WriteString("--- " + labelA + "\n+++ " + labelB + "\n")
	// Line numbers at the start of each op.
	aAt, bAt := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for k, o := range ops {
		aAt[k+1], bAt[k+1] = aAt[k], bAt[k]
		if o.kind != '+' {
			aAt[k+1]++
		}
		if o.kind != '-' {
			bAt[k+1]++
		}
	}
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		// A hunk starts `context` ops before the first change and runs
		// until a gap of more than 2*context unchanged ops.
		start := k - context
		if start < 0 {
			start = 0
		}
		end, lastChange := k, k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				lastChange = end
			} else if end-lastChange > 2*context {
				break
			}
			end++
		}
		end = lastChange + context + 1
		if end > len(ops) {
			end = len(ops)
		}
		aCount, bCount := aAt[end]-aAt[start], bAt[end]-bAt[start]
		aStart, bStart := aAt[start]+1, bAt[start]+1
		if aCount == 0 {
			aStart--
		}
		if bCount == 0 {
			bStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for _, o := range ops[start:end] {
			out.WriteString(string(o.kind) + o.text + "\n")
		}
		k = end
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
