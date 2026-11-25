package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// newProjectServer is a fleet server with git, as a host that can make a
// project has (docs/studio-flutter/new-project.md).
func newProjectServer(t *testing.T) (*Fleet, *Server) {
	t.Helper()
	f := testFleetWithState(t)
	if f.git == nil {
		t.Skip("git not installed")
	}
	return f, NewServer(f)
}

func postNewProject(t *testing.T, s *Server, body contract.NewProjectRequest) (int, contract.ProjectCreation, string) {
	t.Helper()
	rec := doReq(t, s, http.MethodPost, newProjectPath, body, nil)
	var creation contract.ProjectCreation
	if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted {
		decodeBody(t, rec, &creation)
	}
	return rec.Code, creation, rec.Body.String()
}

// waitCreation reads a creation until its clone has ended.
func waitCreation(t *testing.T, s *Server, id string) contract.ProjectCreation {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rec := doReq(t, s, http.MethodGet, newProjectPath+"/"+id, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET creation = %d %s", rec.Code, rec.Body)
		}
		var c contract.ProjectCreation
		decodeBody(t, rec, &c)
		if c.State != contract.CreationCloning {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the clone did not end")
	return contract.ProjectCreation{}
}

// projectAdded reports whether the fleet stream announced root.
func projectAdded(f *Fleet, root string) bool {
	for _, entry := range f.fleetLog.Tail(fleetStreamKey) {
		var d contract.ProjectAddedDelta
		if json.Unmarshal(entry.Data, &d) == nil && d.Kind == contract.DeltaProjectAdded && d.Project == root {
			return true
		}
	}
	return false
}

func TestABlankProjectIsAnEmptyRepositoryRegisteredAtOnce(t *testing.T) {
	f, s := newProjectServer(t)
	root := filepath.Join(t.TempDir(), "projects", "fresh")

	code, creation, body := postNewProject(t, s, contract.NewProjectRequest{Kind: contract.NewProjectBlank, Root: root})
	if code != http.StatusOK {
		t.Fatalf("POST = %d %s, want 200", code, body)
	}
	if creation.State != contract.CreationReady || creation.Root != root {
		t.Fatalf("creation = %+v, want ready at %s", creation, root)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("no repository at %s: %v", root, err)
	}
	if !slices.Contains(f.ws.Projects(), root) {
		t.Fatalf("projects = %v, want %s registered", f.ws.Projects(), root)
	}
	if !projectAdded(f, root) {
		t.Fatal("no project_added delta for the new project")
	}
}

func TestANewProjectNeverOverwritesAFolder(t *testing.T) {
	f, s := newProjectServer(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []contract.NewProjectKind{contract.NewProjectBlank, contract.NewProjectClone} {
		code, _, body := postNewProject(t, s, contract.NewProjectRequest{Kind: kind, Root: root, URL: "/nowhere.git"})
		if code != http.StatusBadRequest || !strings.Contains(body, "already has files") {
			t.Fatalf("%s into a full folder = %d %s, want 400 naming the files", kind, code, body)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "notes.txt")); err != nil || string(data) != "mine" {
		t.Fatalf("the folder's file = %q, %v; want it untouched", data, err)
	}
	if slices.Contains(f.ws.Projects(), root) {
		t.Fatal("a refused creation registered its root")
	}
}

func TestAnExistingFolderIsRegisteredAsItIs(t *testing.T) {
	f, s := newProjectServer(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, creation, body := postNewProject(t, s, contract.NewProjectRequest{Kind: contract.NewProjectExisting, Root: root})
	if code != http.StatusOK || creation.State != contract.CreationReady {
		t.Fatalf("POST = %d %s, want 200 ready", code, body)
	}
	if !slices.Contains(f.ws.Projects(), root) || !projectAdded(f, root) {
		t.Fatal("the existing folder was not registered and announced")
	}

	missing := filepath.Join(t.TempDir(), "absent")
	code, _, body = postNewProject(t, s, contract.NewProjectRequest{Kind: contract.NewProjectExisting, Root: missing})
	if code != http.StatusBadRequest || !strings.Contains(body, "not a folder") {
		t.Fatalf("a missing folder = %d %s, want 400", code, body)
	}
}

func TestACloneRunsInTheBackgroundAndRegistersWhenDone(t *testing.T) {
	f, s := newProjectServer(t)
	remote := newBareRepoFixture(t)
	root := filepath.Join(t.TempDir(), "projects", "seed")

	code, creation, body := postNewProject(t, s, contract.NewProjectRequest{
		Kind: contract.NewProjectClone, Root: root, URL: remote, Branch: "main",
	})
	if code != http.StatusAccepted || creation.State != contract.CreationCloning || creation.URL != remote {
		t.Fatalf("POST = %d %s, want 202 cloning", code, body)
	}
	done := waitCreation(t, s, creation.ID)
	if done.State != contract.CreationReady || done.Error != "" {
		t.Fatalf("creation = %+v, want ready", done)
	}
	if data, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(data) != "seed\n" {
		t.Fatalf("the clone's README = %q, %v", data, err)
	}
	if !slices.Contains(f.ws.Projects(), root) || !projectAdded(f, root) {
		t.Fatal("the clone was not registered and announced")
	}

	rec := doReq(t, s, http.MethodGet, newProjectPath, nil, nil)
	var options contract.NewProjectOptions
	decodeBody(t, rec, &options)
	if len(options.Creations) != 1 || options.Creations[0].ID != creation.ID {
		t.Fatalf("creations = %+v, want the clone", options.Creations)
	}
}

func TestAFailedCloneSaysWhyAndLeavesNothingBehind(t *testing.T) {
	f, s := newProjectServer(t)
	root := filepath.Join(t.TempDir(), "gone")
	code, creation, body := postNewProject(t, s, contract.NewProjectRequest{
		Kind: contract.NewProjectClone, Root: root, URL: filepath.Join(t.TempDir(), "no-such.git"),
	})
	if code != http.StatusAccepted {
		t.Fatalf("POST = %d %s, want 202", code, body)
	}
	done := waitCreation(t, s, creation.ID)
	if done.State != contract.CreationFailed || !strings.Contains(done.Error, "git clone:") {
		t.Fatalf("creation = %+v, want failed with git's words", done)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the failed clone left %s behind (%v)", root, err)
	}
	if slices.Contains(f.ws.Projects(), root) {
		t.Fatal("a failed clone registered its root")
	}

	// A folder that was there, empty, before the clone is the owner's: it
	// stays.
	empty := t.TempDir()
	_, creation, _ = postNewProject(t, s, contract.NewProjectRequest{
		Kind: contract.NewProjectClone, Root: empty, URL: filepath.Join(t.TempDir(), "no-such.git"),
	})
	if done := waitCreation(t, s, creation.ID); done.State != contract.CreationFailed {
		t.Fatalf("creation = %+v, want failed", done)
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatalf("the owner's empty folder was removed: %v", err)
	}
}

func TestANewProjectRefusesWhatGitWouldMisread(t *testing.T) {
	_, s := newProjectServer(t)
	root := filepath.Join(t.TempDir(), "p")
	for _, tc := range []struct {
		name string
		req  contract.NewProjectRequest
		want string
	}{
		{"an option as the URL", contract.NewProjectRequest{Kind: contract.NewProjectClone, Root: root, URL: "--upload-pack=touch /tmp/x"}, "not a repository URL"},
		{"a URL with a space", contract.NewProjectRequest{Kind: contract.NewProjectClone, Root: root, URL: "https://example.com/a b.git"}, "not a repository URL"},
		{"no URL", contract.NewProjectRequest{Kind: contract.NewProjectClone, Root: root}, "url is required"},
		{"an option as the branch", contract.NewProjectRequest{Kind: contract.NewProjectClone, Root: root, URL: "https://example.com/a.git", Branch: "-b"}, "not a branch name"},
		{"a relative root", contract.NewProjectRequest{Kind: contract.NewProjectBlank, Root: "projects/p"}, "absolute"},
		{"a root outside home", contract.NewProjectRequest{Kind: contract.NewProjectBlank, Root: "/etc/marshal-new"}, "root"},
		{"an unknown kind", contract.NewProjectRequest{Kind: "template", Root: root}, "kind must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, body := postNewProject(t, s, tc.req)
			if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("POST = %d %s, want 400 containing %q", code, body, tc.want)
			}
		})
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("a refused request made %s", root)
	}
}

func TestARootBeingMadeIsNotMadeTwice(t *testing.T) {
	_, s := newProjectServer(t)
	root := filepath.Join(t.TempDir(), "p")
	if _, ok := s.creations.claim(root); !ok {
		t.Fatal("claim of a free root refused")
	}
	code, _, body := postNewProject(t, s, contract.NewProjectRequest{Kind: contract.NewProjectBlank, Root: root})
	if code != http.StatusConflict {
		t.Fatalf("POST = %d %s, want 409", code, body)
	}
}

func TestTheProjectsFolderDefaultsUnderHome(t *testing.T) {
	_, s := newProjectServer(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got := s.resolvedProjectsDir(); got != filepath.Join(home, "projects") {
		t.Fatalf("default = %s, want ~/projects", got)
	}
	s.SetProjectsDir("~/src")
	if got := s.resolvedProjectsDir(); got != filepath.Join(home, "src") {
		t.Fatalf("~/src = %s", got)
	}
	s.SetProjectsDir("/srv/code/")
	rec := doReq(t, s, http.MethodGet, newProjectPath, nil, nil)
	var options contract.NewProjectOptions
	decodeBody(t, rec, &options)
	if options.ProjectsDir != "/srv/code" || options.Creations == nil {
		t.Fatalf("options = %+v, want /srv/code and an empty list", options)
	}
}

func TestHostGitKeepsTheOwnersSetupButNotARepositoryOverride(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	env := hostGitEnv()
	if !slices.Contains(env, "SSH_AUTH_SOCK=/tmp/agent.sock") {
		t.Fatal("the daemon's SSH agent was dropped")
	}
	if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") || slices.Contains(env, "GIT_TERMINAL_PROMPT=1") {
		t.Fatal("prompts are not off")
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_DIR=") {
			t.Fatalf("GIT_DIR kept: %s", kv)
		}
	}
}

func TestProgressLinesSplitOnCarriageReturns(t *testing.T) {
	var seen []string
	p := &progressLines{on: func(l string) { seen = append(seen, l) }, tail: &cappedTail{limit: 64}}
	_, _ = p.Write([]byte("Receiving objects:  10% (1/10)\rReceiving objects: 100% (10/10)\nfatal: early EOF\nwarning: x"))
	if len(seen) != 3 || seen[1] != "Receiving objects: 100% (10/10)" {
		t.Fatalf("lines = %q", seen)
	}
	if got := p.said(); got != "fatal: early EOF" {
		t.Fatalf("said = %q, want the fatal line", got)
	}
}
