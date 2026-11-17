package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/daemon"
	"marshal/internal/repo"
)

// daemonSpawnClient is the part of daemon.Client that `spawn` needs, so a test
// answers with a scripted daemon instead of opening a socket.
type daemonSpawnClient interface {
	Spawn(ctx context.Context, req daemon.SpawnRequest) (daemon.SpawnResult, error)
}

// daemonSpawnFor is the socket seam the status and pin paths use, narrowed to
// the one call spawn makes.
var daemonSpawnFor = func(socket string) daemonSpawnClient {
	return daemon.NewClient(socket)
}

// daemonSpawnTimeout bounds a spawn. It is far longer than the status timeout
// because the daemon starts a process (and may resolve a workspace) before it
// answers; a wedged daemon still ends with this rather than hanging.
const daemonSpawnTimeout = 2 * time.Minute

// runDaemonSpawn implements `marshal daemon spawn`: it asks the running local
// daemon for one agent in a project, which is the programmatic way onto the
// same fleet `marshal fleet` shows.
//
// It exists because the fleet has no other command-shaped door. The Studio is
// a browser surface and the TUI is interactive, so anything that is not a
// person at a keyboard — a script, a tray menu, another agent — had no way to
// start an agent. The request goes through the daemon's own spawn path, so the
// budget gate, isolation, routing and the project's defaults all apply exactly
// as they do for a spawn from the UI.
//
// Usage: marshal daemon spawn [--project DIR] [--prompt TEXT] [--json]
func runDaemonSpawn(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "the project to spawn in (default: the working directory's repository root)")
	name := fs.String("name", "", "the agent's name (default: the daemon names it)")
	mode := fs.String("mode", "", "the starting mode, act or ask (default: the project's)")
	auto := fs.Bool("auto", false, "start with the Auto modifier on (Act only; default: the project's)")
	isolated := fs.Bool("isolated", false, "run in an isolated worktree (default: the project's)")
	prompt := fs.String("prompt", "", "the first prompt to send once the agent exists")
	branch := fs.String("branch", "", "the branch to create for the agent")
	baseRef := fs.String("base-ref", "", "the ref the agent's branch starts from")
	workspace := fs.String("workspace", "", "the workspace reference to run in (default: the project's)")
	profile := fs.String("profile", "", "the routing profile to resolve the agent's models through")
	asJSON := fs.Bool("json", false, "write the spawn result as one JSON document")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon spawn: unexpected argument %q", rest[0])
	}

	// The mode is checked here rather than by the daemon so a typo fails
	// before a socket is opened; the bridge re-checks it on its own, and the
	// project's default applies when none is given.
	switch *mode {
	case "", "act", "ask":
	default:
		return fmt.Errorf("marshal daemon spawn: mode must be act or ask, got %q", *mode)
	}

	root, err := daemonSpawnRoot(*project)
	if err != nil {
		return err
	}
	req := daemon.SpawnRequest{
		Project:   root,
		Name:      *name,
		Mode:      *mode,
		Prompt:    *prompt,
		Branch:    *branch,
		BaseRef:   *baseRef,
		Workspace: *workspace,
	}
	// Both booleans are pointers on the wire so that "not asked for" stays
	// distinct from an explicit false: only a flag that was given is sent,
	// and otherwise the project's default decides.
	if *auto {
		on := true
		req.Auto = &on
	}
	if *isolated {
		on := true
		req.Isolated = &on
	}
	if p := strings.TrimSpace(*profile); p != "" {
		routing, err := json.Marshal(map[string]string{"profile": p})
		if err != nil {
			return fmt.Errorf("marshal daemon spawn: encode routing: %w", err)
		}
		req.Routing = routing
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	socket := daemon.PathsFor(home).Socket

	spawnCtx, cancel := context.WithTimeout(ctx, daemonSpawnTimeout)
	defer cancel()
	res, err := daemonSpawnFor(socket).Spawn(spawnCtx, req)
	if err != nil {
		// A transport failure means the daemon never answered; a refusal is
		// the daemon's own sentence, so it is reported as itself.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("the daemon is not answering on %s: %w", socket, err)
		}
		return fmt.Errorf("marshal daemon spawn: %w", err)
	}

	if *asJSON {
		return json.NewEncoder(stdout).Encode(res)
	}
	fmt.Fprintf(stdout, "spawned %s\n", res.AgentID)
	// A warning is a non-fatal start problem: the agent exists, so the ID is
	// still the answer to the command, and the warning says what went wrong.
	if warning := strings.TrimSpace(res.Warning); warning != "" {
		fmt.Fprintf(stdout, "warning: %s\n", warning)
	}
	return nil
}

// daemonSpawnRoot resolves the project a spawn names: the argument, or the
// working directory, anchored at its git repository root — the daemon serves
// projects, and a project is anchored at its git root (P4 D2), exactly as a pin
// is.
func daemonSpawnRoot(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("marshal daemon spawn: resolve the working directory: %w", err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("marshal daemon spawn: resolve %q: %w", dir, err)
	}
	return repo.Root(abs), nil
}
