package inproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"marshal/internal/acpwire"
	"marshal/internal/plugins"
)

// The two scopes an install and a removal take.
const (
	pluginScopeGlobal  = "global"
	pluginScopeProject = "project"
)

// pluginScan is one staged plugin clone: the temporary parent that owns it,
// the clone inside it, and what the scan read from it. hash is the reviewed
// content hash, which the install compares the clone against.
type pluginScan struct {
	parent, cloneDir, name, source, commit, hash string
}

// discardPluginScans deletes every staged clone's parent. It takes them under
// mu and removes them after releasing it, so a slow filesystem does not block
// the session's other readers.
func (s *Session) discardPluginScans() {
	s.mu.Lock()
	staged := s.pluginScans
	s.pluginScans = nil
	s.mu.Unlock()
	for _, scan := range staged {
		os.RemoveAll(scan.parent)
	}
}

// Plugins lists the installed plugins: the global lockfile's, then the project
// lockfile's when the session is trusted. A lockfile that fails to read is
// skipped, so the list never fails.
func (s *Session) Plugins(ctx context.Context) ([]acpwire.PluginEntry, error) {
	out := make([]acpwire.PluginEntry, 0)
	if lf, err := plugins.ReadLockfile(s.pluginLockPath(pluginScopeGlobal)); err == nil {
		out = appendPluginEntries(out, lf, pluginScopeGlobal)
	}
	if s.state.Trusted() {
		if lf, err := plugins.ReadLockfile(s.projectPluginLockPath()); err == nil {
			out = appendPluginEntries(out, lf, pluginScopeProject)
		}
	}
	return out, nil
}

// appendPluginEntries copies one lockfile's entries onto the wire with scope.
func appendPluginEntries(out []acpwire.PluginEntry, lf *plugins.Lockfile, scope string) []acpwire.PluginEntry {
	for _, e := range lf.Plugins {
		out = append(out, acpwire.PluginEntry{
			Name:   e.Name,
			Source: e.Source,
			Ref:    e.Ref,
			Commit: e.Commit,
			Scope:  scope,
		})
	}
	return out
}

// ScanPlugin clones source into a staging directory and scans it, without
// installing anything. The clone is the panel's: the same steps in the same
// order, so a source that fails there fails here with the same sentence.
func (s *Session) ScanPlugin(ctx context.Context, source string) (acpwire.PluginScan, error) {
	if s.closed.Load() {
		return acpwire.PluginScan{}, errSessionClosed
	}

	cloneURL, name, err := plugins.NormalizeSource(source)
	if err != nil {
		return acpwire.PluginScan{}, err
	}
	// Every derived name is validated, including the github: branch: a name is
	// a single directory inside the plugin store.
	if !plugins.ValidName(name) {
		return acpwire.PluginScan{}, fmt.Errorf("invalid plugin name %q derived from %q", name, source)
	}
	// Validate the clone source against the workspace root before cloning,
	// matching the CLI and ACP install paths.
	if err := plugins.ValidateCloneSource(cloneURL, s.state.WorkingDir); err != nil {
		return acpwire.PluginScan{}, err
	}
	inst, err := plugins.NewInstaller()
	if err != nil {
		return acpwire.PluginScan{}, err
	}
	parent, err := os.MkdirTemp("", "marshal-plugin-tui-*")
	if err != nil {
		return acpwire.PluginScan{}, err
	}
	cloneDir := filepath.Join(parent, "clone")
	commit, err := inst.Clone(ctx, cloneURL, "", cloneDir)
	if err != nil {
		os.RemoveAll(parent)
		return acpwire.PluginScan{}, err
	}
	_ = os.RemoveAll(filepath.Join(cloneDir, ".git"))
	contents, err := plugins.ScanPlugin(cloneDir)
	if err != nil {
		os.RemoveAll(parent)
		return acpwire.PluginScan{}, err
	}
	// The hash covers exactly what was reviewed: .git is gone, so a re-clone of
	// the same commit hashes the same.
	hash, err := plugins.HashDir(cloneDir)
	if err != nil {
		os.RemoveAll(parent)
		return acpwire.PluginScan{}, err
	}

	s.mu.Lock()
	if s.closed.Load() || ctx.Err() != nil {
		s.mu.Unlock()
		os.RemoveAll(parent)
		if err := ctx.Err(); err != nil {
			return acpwire.PluginScan{}, err
		}
		return acpwire.PluginScan{}, errSessionClosed
	}
	s.pluginSeq++
	token := fmt.Sprintf("scan-%d", s.pluginSeq)
	if s.pluginScans == nil {
		s.pluginScans = make(map[string]*pluginScan)
	}
	s.pluginScans[token] = &pluginScan{
		parent:   parent,
		cloneDir: cloneDir,
		name:     name,
		source:   cloneURL,
		commit:   commit,
		hash:     hash,
	}
	s.mu.Unlock()

	return acpwire.PluginScan{
		Token:    token,
		Name:     name,
		Source:   cloneURL,
		Commit:   commit,
		Contents: pluginContentsOf(contents),
	}, nil
}

// pluginContentsOf copies a scanned plugin onto the wire: skills and commands
// by name and description, hooks and MCP servers field by field, policies as
// they are.
func pluginContentsOf(c plugins.Contents) acpwire.PluginContents {
	out := acpwire.PluginContents{}
	for _, skill := range c.Skills {
		out.Skills = append(out.Skills, acpwire.PluginItem{Name: skill.Name, Description: skill.Description})
	}
	for _, command := range c.Commands {
		out.Commands = append(out.Commands, acpwire.PluginItem{Name: command.Name, Description: command.Description})
	}
	for _, hook := range c.Hooks {
		out.Hooks = append(out.Hooks, acpwire.PluginHook{
			Event:     hook.Event,
			Matcher:   hook.Matcher,
			Command:   hook.Command,
			TimeoutMs: hook.TimeoutMS,
		})
	}
	if len(c.MCPServers) > 0 {
		out.MCPServers = make(map[string]acpwire.PluginMCPServer, len(c.MCPServers))
		for name, server := range c.MCPServers {
			out.MCPServers[name] = acpwire.PluginMCPServer{
				Command: server.Command,
				Args:    server.Args,
				Env:     server.Env,
				Trust:   server.Trust,
				URL:     server.URL,
				Headers: server.Headers,
				Type:    server.Type,
				Auth:    server.Auth,
			}
		}
	}
	if len(c.MCPPolicies) > 0 {
		out.MCPPolicies = make(map[string]string, len(c.MCPPolicies))
		for name, policy := range c.MCPPolicies {
			out.MCPPolicies[name] = policy
		}
	}
	return out
}

// InstallPlugin installs the staged clone token names into scope's store, if
// its content still hashes as it did when scanned. The staged clone is deleted
// whatever the outcome.
func (s *Session) InstallPlugin(ctx context.Context, token, scope string) error {
	if err := s.pluginScope(scope); err != nil {
		return err
	}
	s.mu.Lock()
	staged, ok := s.pluginScans[token]
	delete(s.pluginScans, token)
	s.mu.Unlock()
	if !ok {
		return unknownPluginScan(token)
	}
	// The staged clone belongs to this install now: whether the plugin lands
	// or the hash check refuses it, the clone is not needed again.
	defer os.RemoveAll(staged.parent)
	return plugins.InstallReviewed(ctx, plugins.ReviewedInstall{
		Name:         staged.name,
		Source:       staged.source,
		Commit:       staged.commit,
		ReviewedHash: staged.hash,
		CloneDir:     staged.cloneDir,
		StoreDir:     s.pluginStoreDir(scope),
		LockPath:     s.pluginLockPath(scope),
	})
}

// DiscardPluginScan deletes the staged clone token names.
func (s *Session) DiscardPluginScan(ctx context.Context, token string) error {
	s.mu.Lock()
	staged, ok := s.pluginScans[token]
	delete(s.pluginScans, token)
	s.mu.Unlock()
	if !ok {
		return unknownPluginScan(token)
	}
	os.RemoveAll(staged.parent)
	return nil
}

// RemovePlugin deletes an installed plugin and its lockfile entry.
func (s *Session) RemovePlugin(ctx context.Context, name, scope string) error {
	if !plugins.ValidName(name) {
		return fmt.Errorf("invalid plugin name %q", name)
	}
	if err := s.pluginScope(scope); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.pluginStoreDir(scope), name)); err != nil {
		return err
	}
	lockPath := s.pluginLockPath(scope)
	lf, err := plugins.ReadLockfile(lockPath)
	if err != nil {
		return err
	}
	lf.Remove(name)
	return lf.Write(lockPath)
}

// unknownPluginScan is what a token the session no longer stages answers with.
func unknownPluginScan(token string) error {
	return fmt.Errorf("inproc: no plugin scan %q", token)
}

// pluginScope validates an install's or a removal's scope: "global" or
// "project", and "project" only on a trusted session. A project-scope write on
// an untrusted session is refused rather than redirected to global.
func (s *Session) pluginScope(scope string) error {
	if scope != pluginScopeGlobal && scope != pluginScopeProject {
		return fmt.Errorf("invalid scope %q: want %q or %q", scope, pluginScopeGlobal, pluginScopeProject)
	}
	if scope == pluginScopeProject && !s.state.Trusted() {
		return fmt.Errorf("project scope requires a trusted session")
	}
	return nil
}

// pluginStoreDir is the store directory of scope.
func (s *Session) pluginStoreDir(scope string) string {
	if scope == pluginScopeProject {
		return plugins.ProjectStoreDir(s.state.WorkingDir)
	}
	return plugins.GlobalStoreDir(s.pluginHome())
}

// pluginLockPath is the lockfile path of scope.
func (s *Session) pluginLockPath(scope string) string {
	if scope == pluginScopeProject {
		return s.projectPluginLockPath()
	}
	return plugins.GlobalLockPath(s.pluginHome())
}

// projectPluginLockPath is the project lockfile at the session's working
// directory.
func (s *Session) projectPluginLockPath() string {
	return plugins.ProjectLockPath(s.state.WorkingDir)
}

// pluginHome is the home directory the global store and lockfile live under:
// the session's, or the process user's when it has none.
func (s *Session) pluginHome() string {
	if s.homeDir != "" {
		return s.homeDir
	}
	home, _ := os.UserHomeDir()
	return home
}
