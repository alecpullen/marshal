package lsp

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// initializeTimeout bounds how long we wait for a language server to respond
// to the LSP initialize handshake. If a server hangs during init, the
// goroutine returns cleanly and the language is simply never marked ready.
const initializeTimeout = 30 * time.Second

type ServerSpec struct {
	Command string
	Args    []string
}

// DefaultServers is the built-in language→server map.
var DefaultServers = map[string]ServerSpec{
	"go":         {Command: "gopls"},
	"typescript": {Command: "typescript-language-server", Args: []string{"--stdio"}},
	"python":     {Command: "pyright-langserver", Args: []string{"--stdio"}},
	"rust":       {Command: "rust-analyzer"},
}

// DetectServers resolves the effective server map: defaults overlaid with
// configured specs, keeping a language when it is not disabled AND either it is
// explicitly configured or its default command resolves on PATH.
func DetectServers(configured map[string]ServerSpec, disabled map[string]bool) map[string]ServerSpec {
	out := map[string]ServerSpec{}
	for lang, spec := range DefaultServers {
		if disabled[lang] {
			continue
		}
		if _, err := exec.LookPath(spec.Command); err == nil {
			out[lang] = spec
		}
	}
	for lang, spec := range configured {
		if disabled[lang] {
			delete(out, lang)
			continue
		}
		out[lang] = spec // explicit config always included
	}
	return out
}

type serverState struct {
	client *Client
	cmd    *exec.Cmd
	ready  bool
}

type Manager struct {
	root    string
	servers map[string]ServerSpec
	log     *slog.Logger

	mu    sync.Mutex
	state map[string]*serverState
}

func NewManager(root string, servers map[string]ServerSpec, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Manager{root: root, servers: servers, log: log, state: map[string]*serverState{}}
}

func (m *Manager) Name() string { return "lsp-manager" }

// Root returns the workspace root for this manager.
func (m *Manager) Root() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root
}

// Run spawns configured servers and supervises them until ctx is cancelled.
// Each server is started in its own goroutine so a hung Initialize does not
// block other languages from starting.
func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for lang, spec := range m.servers {
		wg.Add(1)
		go func(lang string, spec ServerSpec) {
			defer wg.Done()
			m.startServer(ctx, lang, spec)
		}(lang, spec)
	}
	<-ctx.Done()
	m.shutdownAll()
	wg.Wait()
	return nil
}

func (m *Manager) startServer(ctx context.Context, lang string, spec ServerSpec) {
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = m.root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.log.Debug("lsp stdin", "lang", lang, "err", err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.log.Debug("lsp stdout", "lang", lang, "err", err)
		return
	}
	if err := cmd.Start(); err != nil {
		m.log.Debug("lsp start", "lang", lang, "err", err)
		return
	}
	client := newClient(stdin, stdout)
	initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()
	if err := client.Initialize(initCtx, toFileURI("", m.root)); err != nil {
		m.log.Debug("lsp initialize", "lang", lang, "err", err)
		// Reap the child process on init failure so it does not become a zombie.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return
	}

	// If the parent context was cancelled during Initialize, clean up
	// inline to avoid leaking the process. Without this, the client is
	// never registered in m.state and shutdownAll never reaps it.
	if err := ctx.Err(); err != nil {
		client.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		m.log.Debug("lsp context cancelled after init", "lang", lang, "err", err)
		return
	}

	m.mu.Lock()
	m.state[lang] = &serverState{client: client, cmd: cmd, ready: true}
	m.mu.Unlock()
}

func (m *Manager) shutdownAll() {
	m.mu.Lock()
	states := make([]*serverState, 0, len(m.state))
	for _, st := range m.state {
		states = append(states, st)
	}
	m.mu.Unlock()

	for _, st := range states {
		if st.client != nil {
			st.client.Close()
		}
		if st.cmd != nil && st.cmd.Process != nil {
			_ = st.cmd.Process.Kill()
			_ = st.cmd.Wait()
		}
	}
}

// ServerFor returns the client for a language and whether it is ready.
func (m *Manager) ServerFor(lang string) (*Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[lang]
	if !ok || !st.ready {
		return nil, false
	}
	return st.client, true
}
