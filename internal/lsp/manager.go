package lsp

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/repo"
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

// serverLang is the language key a file's server is registered under. The
// TypeScript server also serves JavaScript.
func serverLang(path string) string {
	if l := repo.DetectLanguage(path); l == "javascript" {
		return "typescript"
	} else {
		return l
	}
}

// languageID is the LSP languageId for didOpen.
func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx":
		return "typescriptreact"
	case ".jsx":
		return "javascriptreact"
	}
	return repo.DetectLanguage(path)
}

// DetectRepoLanguages walks root (skipping repo.IsDefaultIgnoredDir
// directories) and returns the server languages of the first limit files.
func DetectRepoLanguages(root string, limit int) map[string]bool {
	langs := map[string]bool{}
	n := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && repo.IsDefaultIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if n >= limit {
			return filepath.SkipAll
		}
		n++
		if l := serverLang(path); l != "" {
			langs[l] = true
		}
		return nil
	})
	return langs
}

// repoLanguageWalkLimit bounds DetectRepoLanguages at manager start.
const repoLanguageWalkLimit = 5000

// ServersForRoot keeps the servers whose language occurs in root.
func ServersForRoot(servers map[string]ServerSpec, root string) map[string]ServerSpec {
	langs := DetectRepoLanguages(root, repoLanguageWalkLimit)
	out := map[string]ServerSpec{}
	for lang, spec := range servers {
		if langs[lang] {
			out[lang] = spec
		}
	}
	return out
}

type serverState struct {
	client   *Client
	ready    bool
	disabled string
}

type Manager struct {
	root    string
	servers map[string]ServerSpec
	log     *slog.Logger

	mu    sync.Mutex
	state map[string]*serverState

	ws *Workspace

	// start launches one language server. NewManager defaults it to
	// startProcess; tests substitute fakes.
	start func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error)

	// afterInit is a test-only hook invoked after Initialize succeeds but
	// before the post-init ctx.Err() guard. It lets tests deterministically
	// cancel the parent context in the narrow window that the new
	// cancellation branch reaps the process. Nil in production.
	afterInit func()
}

func NewManager(root string, servers map[string]ServerSpec, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	m := &Manager{root: root, servers: servers, log: log, state: map[string]*serverState{}}
	for lang := range servers {
		m.state[lang] = &serverState{}
	}
	m.start = m.startProcess
	m.ws = NewWorkspace(m)
	return m
}

func (m *Manager) Name() string { return "lsp-manager" }

// Root returns the workspace root for this manager.
func (m *Manager) Root() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root
}

func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for lang, spec := range m.servers {
		wg.Add(1)
		go func(lang string, spec ServerSpec) {
			defer wg.Done()
			m.supervise(ctx, lang, spec)
		}(lang, spec)
	}
	wg.Wait()
	return nil
}

// supervise runs one server, restarting it once if it fails or exits; a
// second failure disables the language for this manager's life.
func (m *Manager) supervise(ctx context.Context, lang string, spec ServerSpec) {
	for attempt := 1; ; attempt++ {
		client, stop, err := m.start(ctx, lang, spec)
		if ctx.Err() != nil {
			if stop != nil {
				stop()
			}
			return
		}
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			m.setState(lang, client, true, "")
			select {
			case <-ctx.Done():
				stop()
				m.setState(lang, nil, false, "")
				return
			case <-client.Done():
				stop()
				m.setState(lang, nil, false, "")
				reason = "server exited"
			}
		}
		m.log.Warn("lsp server failed", "lang", lang, "attempt", attempt, "reason", reason)
		if attempt >= 2 {
			m.setState(lang, nil, false, strings.TrimSpace(spec.Command+": "+reason))
			return
		}
	}
}

func (m *Manager) setState(lang string, c *Client, ready bool, disabled string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[lang] = &serverState{client: c, ready: ready, disabled: disabled}
}

// WaitReady polls until lang's server is ready, the language is disabled or
// unconfigured, timeout elapses, or ctx ends.
func (m *Manager) WaitReady(ctx context.Context, lang string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		m.mu.Lock()
		st, ok := m.state[lang]
		ready, disabled := ok && st.ready, ok && st.disabled != ""
		m.mu.Unlock()
		switch {
		case ready:
			return true
		case !ok, disabled, ctx.Err() != nil, !time.Now().Before(deadline):
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Disabled returns why lang was disabled, or "".
func (m *Manager) Disabled(lang string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.state[lang]; ok {
		return st.disabled
	}
	return ""
}

// Languages returns the configured server languages, sorted.
func (m *Manager) Languages() []string {
	out := make([]string, 0, len(m.servers))
	for l := range m.servers {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// startProcess starts one language server and completes the Initialize
// handshake. It returns the connected client and a stop function that closes
// the client and kills and waits for the process. On any failure it returns
// the error, having reaped the child process.
func (m *Manager) startProcess(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = m.root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.log.Debug("lsp stdin", "lang", lang, "err", err)
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.log.Debug("lsp stdout", "lang", lang, "err", err)
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		m.log.Debug("lsp start", "lang", lang, "err", err)
		return nil, nil, err
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
		return nil, nil, err
	}

	// Test hook: lets a test cancel the parent context in the window between
	// a successful Initialize and the post-init cancellation guard below,
	// so the new cancellation branch can be exercised deterministically.
	if m.afterInit != nil {
		m.afterInit()
	}

	stop := func() {
		client.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	// If the parent context was cancelled during Initialize, clean up
	// inline to avoid leaking the process. Without this, the process is
	// never registered with the supervisor.
	if err := ctx.Err(); err != nil {
		stop()
		m.log.Debug("lsp context cancelled after init", "lang", lang, "err", err)
		return nil, nil, err
	}

	return client, stop, nil
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

// Workspace returns the manager's document-sync workspace.
func (m *Manager) Workspace() *Workspace { return m.ws }
