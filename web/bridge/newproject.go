package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// newProjectPath is where the Studio asks a host to make a project: blank,
// cloned, or a folder already there (docs/studio-flutter/new-project.md).
const newProjectPath = "/api/projects/new"

// cloneTimeout bounds one clone. A clone that has not finished by then is
// stopped and reported failed, rather than holding its root forever.
const cloneTimeout = 30 * time.Minute

// keptCreations is how many finished creations the bridge remembers, so a
// sheet that was closed can still be told how its clone ended.
const keptCreations = 32

// defaultProjectsDir is the projects folder when [daemon] projects_dir sets
// none, under the daemon user's home.
const defaultProjectsDir = "~/projects"

// projectCreations is what the bridge remembers of the projects it was asked
// to make. Memory only: a restart forgets them (new-project decision 4).
type projectCreations struct {
	mu    sync.Mutex
	byID  map[string]*contract.ProjectCreation
	order []string // oldest first
	// making is each root a creation is under way in, so a second one for
	// the same directory is refused rather than racing it.
	making map[string]string
}

func newProjectCreations() *projectCreations {
	return &projectCreations{
		byID:   make(map[string]*contract.ProjectCreation),
		making: make(map[string]string),
	}
}

// claim reserves root for a creation, or says which one holds it.
func (c *projectCreations) claim(root string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.making[root]; ok {
		return id, false
	}
	c.making[root] = ""
	return "", true
}

// release frees root once its creation has ended, either way.
func (c *projectCreations) release(root string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.making, root)
}

// record keeps a creation, dropping the oldest finished ones past
// keptCreations. A clone still running is never dropped.
func (c *projectCreations) record(creation contract.ProjectCreation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := creation
	c.byID[creation.ID] = &kept
	c.order = append(c.order, creation.ID)
	if _, ok := c.making[creation.Root]; ok {
		c.making[creation.Root] = creation.ID
	}
	finished := 0
	for _, id := range c.order {
		if c.byID[id].State != contract.CreationCloning {
			finished++
		}
	}
	for i := 0; finished > keptCreations && i < len(c.order); {
		id := c.order[i]
		if c.byID[id].State == contract.CreationCloning {
			i++
			continue
		}
		delete(c.byID, id)
		c.order = append(c.order[:i], c.order[i+1:]...)
		finished--
	}
}

// update changes the creation id names, if it is still remembered.
func (c *projectCreations) update(id string, change func(*contract.ProjectCreation)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if creation, ok := c.byID[id]; ok {
		change(creation)
	}
}

func (c *projectCreations) get(id string) (contract.ProjectCreation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	creation, ok := c.byID[id]
	if !ok {
		return contract.ProjectCreation{}, false
	}
	return *creation, true
}

// list is every remembered creation, newest first.
func (c *projectCreations) list() []contract.ProjectCreation {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]contract.ProjectCreation, 0, len(c.order))
	for i := len(c.order) - 1; i >= 0; i-- {
		out = append(out, *c.byID[c.order[i]])
	}
	return out
}

// SetProjectsDir sets the folder a new project's name is put under, as
// [daemon] projects_dir gives it. Empty is ~/projects; a leading ~ is the
// daemon user's home.
func (s *Server) SetProjectsDir(dir string) {
	s.projectsDir = dir
}

// resolvedProjectsDir is the projects folder as an absolute path.
func (s *Server) resolvedProjectsDir() string {
	dir := strings.TrimSpace(s.projectsDir)
	if dir == "" {
		dir = defaultProjectsDir
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	return filepath.Clean(dir)
}

func (s *Server) newProjectRoutes() {
	s.mux.HandleFunc("GET "+newProjectPath, s.newProjectOptions)
	s.mux.HandleFunc("POST "+newProjectPath, s.createProject)
	s.mux.HandleFunc("GET "+newProjectPath+"/{id}", s.projectCreation)
}

// newProjectOptions serves GET /api/projects/new: the projects folder, and
// the creations this bridge remembers.
func (s *Server) newProjectOptions(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	writeJSON(w, http.StatusOK, contract.NewProjectOptions{
		ProjectsDir: s.resolvedProjectsDir(),
		Creations:   s.creations.list(),
	})
}

// projectCreation serves GET /api/projects/new/{id}: one creation, which the
// sheet reads while its clone runs.
func (s *Server) projectCreation(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	creation, ok := s.creations.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such project creation"})
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

// createProject serves POST /api/projects/new. An existing folder and a
// blank project are registered before it answers (200); a clone answers 202
// while git runs, and its creation says how it ends.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body contract.NewProjectRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	bad := func(format string, args ...any) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf(format, args...)})
	}
	root := strings.TrimSpace(body.Root)
	if root == "" {
		bad("root is required")
		return
	}
	root = filepath.Clean(root)
	if err := ValidateProjectRoot(root); err != nil {
		bad("%v", err)
		return
	}
	creation := contract.ProjectCreation{
		ID: newCreationID(), Kind: body.Kind, Root: root,
		StartedAt: s.fleet.now().UnixMilli(),
	}

	if body.Kind == contract.NewProjectExisting {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			bad("%s is not a folder on this host", root)
			return
		}
		if err := s.registerNewProject(root); err != nil {
			bad("%v", err)
			return
		}
		creation.State = contract.CreationReady
		s.creations.record(creation)
		writeJSON(w, http.StatusOK, creation)
		return
	}
	if body.Kind != contract.NewProjectBlank && body.Kind != contract.NewProjectClone {
		bad("kind must be blank, clone or existing")
		return
	}

	var url, branch string
	var cred *Credential
	if body.Kind == contract.NewProjectClone {
		url = strings.TrimSpace(body.URL)
		if err := checkCloneURL(url); err != nil {
			bad("%v", err)
			return
		}
		branch = strings.TrimSpace(body.Branch)
		if branch != "" && (strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\r\n")) {
			bad("branch %q is not a branch name", branch)
			return
		}
		if ref := strings.TrimSpace(body.CredRef); ref != "" {
			resolved, err := s.fleet.creds.Resolve(r.Context(), DefaultOwnerID, ref)
			if err != nil {
				bad("credential %q: %v", ref, err)
				return
			}
			cred = &resolved
		}
		creation.URL = url
	}
	git := s.fleet.git
	if git == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "git is not installed on this host"})
		return
	}
	existed, err := freshRoot(root)
	if err != nil {
		bad("%v", err)
		return
	}
	if _, ok := s.creations.claim(root); !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": root + " is already being created"})
		return
	}

	if body.Kind == contract.NewProjectBlank {
		defer s.creations.release(root)
		if err := s.initBlank(r.Context(), git, root); err != nil {
			if !existed {
				_ = os.RemoveAll(root)
			}
			bad("%v", err)
			return
		}
		if err := s.registerNewProject(root); err != nil {
			bad("%v", err)
			return
		}
		creation.State = contract.CreationReady
		s.creations.record(creation)
		writeJSON(w, http.StatusOK, creation)
		return
	}

	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		s.creations.release(root)
		bad("make %s: %v", filepath.Dir(root), err)
		return
	}
	creation.State = contract.CreationCloning
	s.creations.record(creation)
	go s.runClone(git, creation, branch, cred, existed)
	writeJSON(w, http.StatusAccepted, creation)
}

// registerNewProject registers root as AddProject does for an attach, and
// tells every open Studio to read the projects again (decision 7).
func (s *Server) registerNewProject(root string) error {
	if err := s.fleet.ws.AddProject(root); err != nil {
		return err
	}
	s.fleet.emit(contract.ProjectAddedDelta{Kind: contract.DeltaProjectAdded, Project: root})
	return nil
}

// initBlank makes root, if it is absent, and an empty repository in it, with
// the host's own git so init.defaultBranch applies (decision 5).
func (s *Server) initBlank(ctx context.Context, git *gitRunner, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", root, err)
	}
	if _, err := git.runHost(ctx, root, nil, "init"); err != nil {
		return err
	}
	return nil
}

// runClone clones creation's URL into its root, keeping git's progress on
// the creation, and registers the root once it is there. A failed clone
// removes the directory it made, and only that (decision 4).
func (s *Server) runClone(git *gitRunner, creation contract.ProjectCreation, branch string, cred *Credential, existed bool) {
	defer s.creations.release(creation.Root)
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()
	if done := s.fleet.done; done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	args := []string{"clone", "--progress"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", creation.URL, creation.Root)
	progress := func(line string) {
		s.creations.update(creation.ID, func(c *contract.ProjectCreation) { c.Progress = line })
	}
	_, err := git.runHost(ctx, "", &hostGitRun{cred: cred, progress: progress}, args...)
	if err == nil {
		err = s.registerNewProject(creation.Root)
		if err != nil {
			err = fmt.Errorf("cloned, but could not register %s: %w", creation.Root, err)
		}
	} else if !existed {
		_ = os.RemoveAll(creation.Root)
	}
	s.creations.update(creation.ID, func(c *contract.ProjectCreation) {
		c.Progress = ""
		if err != nil {
			c.State = contract.CreationFailed
			c.Error = err.Error()
			return
		}
		c.State = contract.CreationReady
	})
}

// freshRoot checks that root can be made into a project: absent, or an empty
// folder. It reports whether the folder was already there, so a failure
// removes only what it made (decision 6).
func freshRoot(root string) (bool, error) {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is a file", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	if len(entries) > 0 {
		return false, fmt.Errorf("%s already has files in it: add it as an existing folder instead", root)
	}
	return true, nil
}

// checkCloneURL refuses a URL git could read as something else: empty, an
// option, or one with whitespace or control characters in it.
func checkCloneURL(url string) error {
	if url == "" {
		return errors.New("url is required")
	}
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("%q is not a repository URL", url)
	}
	for _, r := range url {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("%q is not a repository URL", url)
		}
	}
	return nil
}

func newCreationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "np-" + hex.EncodeToString(b[:])
}

// hostGitRun is how one host git call is made: with a stored credential
// instead of the host's own setup, and where its progress lines go.
type hostGitRun struct {
	cred     *Credential
	progress func(string)
}

// hostGitEnv is the daemon's own environment for a git the owner asked for:
// their global config, credential helpers, SSH keys and agent (decision 3).
// The variables that would point git at another repository are dropped, and
// prompts are off, since nobody is at a terminal to answer one.
func hostGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_TERMINAL_PROMPT":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

// runHost runs git for a project the owner is making. Hooks and the ext
// transport stay off (hardenedGitArgs). With no stored credential the host's
// own environment is kept (hostGitEnv); with one, it runs as a mirror clone
// does (gitEnv). git's progress, on stderr, is handed to run.progress a line
// at a time, and a failure says what git said last.
func (g *gitRunner) runHost(ctx context.Context, dir string, run *hostGitRun, args ...string) ([]byte, error) {
	if run == nil {
		run = &hostGitRun{}
	}
	env := hostGitEnv()
	if run.cred != nil {
		env = gitEnv(g.askpassBin, *run.cred)
	}
	full := hardenedGitArgs(args...)
	if g.exec != nil {
		out, err := g.exec(dir, env, full...)
		if err != nil {
			return out, fmt.Errorf("git %s: %w", args[0], err)
		}
		return out, nil
	}
	stdout := &cappedBuffer{limit: 64 << 10}
	stderr := &progressLines{on: run.progress, tail: &cappedTail{limit: 4 << 10}}
	cmd := exec.CommandContext(ctx, g.bin, full...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout.buf.Bytes(), fmt.Errorf("git %s: stopped after %s", args[0], cloneTimeout)
		}
		if said := stderr.said(); said != "" {
			return stdout.buf.Bytes(), fmt.Errorf("git %s: %s", args[0], said)
		}
		return stdout.buf.Bytes(), fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.buf.Bytes(), nil
}

// progressLines splits git's stderr into lines — git ends a progress update
// with \r and a message with \n — handing each to on, and keeps the tail for
// the error a failure reports.
type progressLines struct {
	on      func(string)
	tail    *cappedTail
	partial []byte
}

func (p *progressLines) Write(b []byte) (int, error) {
	_, _ = p.tail.Write(b)
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexAny(p.partial, "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(p.partial[:i]))
		p.partial = p.partial[i+1:]
		if line != "" && p.on != nil {
			p.on(line)
		}
	}
	if len(p.partial) > 4<<10 {
		p.partial = p.partial[len(p.partial)-(4<<10):]
	}
	return len(b), nil
}

// said is what git's last words were: its fatal or error line when it wrote
// one, else its last line.
func (p *progressLines) said() string {
	lines := strings.FieldsFunc(p.tail.String(), func(r rune) bool { return r == '\n' || r == '\r' })
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if last == "" {
			last = line
		}
		if strings.HasPrefix(line, "fatal:") || strings.HasPrefix(line, "error:") {
			return line
		}
	}
	return last
}

// cappedTail keeps the last limit bytes written to it.
type cappedTail struct {
	limit int
	buf   []byte
}

func (c *cappedTail) Write(b []byte) (int, error) {
	c.buf = append(c.buf, b...)
	if len(c.buf) > c.limit {
		c.buf = c.buf[len(c.buf)-c.limit:]
	}
	return len(b), nil
}

func (c *cappedTail) String() string { return string(c.buf) }
