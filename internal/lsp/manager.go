package lsp

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
)

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
	return &Manager{root: root, servers: servers, log: log, state: map[string]*serverState{}}
}

func (m *Manager) Name() string { return "lsp-manager" }

// Run spawns configured servers and supervises them until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	for lang, spec := range m.servers {
		m.startServer(ctx, lang, spec)
	}
	<-ctx.Done()
	m.shutdownAll()
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
	if err := client.Initialize(ctx, "file://"+m.root); err != nil {
		m.log.Debug("lsp initialize", "lang", lang, "err", err)
		return
	}
	m.mu.Lock()
	m.state[lang] = &serverState{client: client, ready: true}
	m.mu.Unlock()
}

func (m *Manager) shutdownAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.state {
		if st.client != nil {
			st.client.Close()
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
