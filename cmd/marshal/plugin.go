package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"marshal/internal/plugins"
)

// runPlugin implements `marshal plugin <install|update|remove|list>`, the
// management CLI for third-party plugin bundles. Phase 1 plugins carry
// skill bundles only.
func runPlugin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("marshal plugin: a subcommand is required (install|update|remove|list)")
	}
	switch args[0] {
	case "install":
		return runPluginInstall(ctx, args[1:], stdin, stdout)
	case "update":
		return runPluginUpdate(ctx, args[1:], stdin, stdout)
	case "remove":
		return runPluginRemove(args[1:], stdout)
	case "list":
		return runPluginList(args[1:], stdout)
	default:
		return fmt.Errorf("marshal plugin: unknown subcommand %q (install|update|remove|list)", args[0])
	}
}

// scopePaths locates the plugin store and lockfile for one install scope.
type scopePaths struct {
	store string
	lock  string
	label string
}

// resolveScope resolves the global (user) scope, or the project scope
// rooted at the current working directory when project is true.
func resolveScope(project bool) (scopePaths, error) {
	if project {
		work, err := os.Getwd()
		if err != nil {
			return scopePaths{}, fmt.Errorf("resolve working directory: %w", err)
		}
		return scopePaths{
			store: plugins.ProjectStoreDir(work),
			lock:  plugins.ProjectLockPath(work),
			label: "project",
		}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return scopePaths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return scopePaths{
		store: plugins.GlobalStoreDir(home),
		lock:  plugins.GlobalLockPath(home),
		label: "global",
	}, nil
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// atomicReplace installs src into dest by copying to a temporary sibling
// directory inside dest's parent, then atomically swapping the temporary
// directory into place. The existing dest directory is renamed aside first
// (rather than removed) so there is never a window where dest is missing:
// it is only removed after the swap succeeds.
func atomicReplace(src, dest string) (retErr error) {
	store := filepath.Dir(dest)
	tmp, err := os.MkdirTemp(store, filepath.Base(dest)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp plugin directory: %w", err)
	}
	// Only clean up the temp directory if the swap did not complete.
	swapped := false
	defer func() {
		if !swapped {
			os.RemoveAll(tmp)
		}
	}()
	if err := plugins.CopyDir(src, tmp); err != nil {
		return fmt.Errorf("copy plugin files to temp directory: %w", err)
	}
	// Atomically swap: rename the existing dest out of the way, then rename
	// the temp into place, then remove the old directory. This avoids the
	// brief window where dest is missing (RemoveAll→Rename).
	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = dest + ".old-" + filepath.Base(tmp)
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("move existing plugin directory aside: %w", err)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		// Try to restore the old directory if the rename failed.
		if old != "" {
			os.Rename(old, dest)
		}
		return fmt.Errorf("rename temp directory to plugin destination: %w", err)
	}
	swapped = true
	// Clean up the old directory after the swap succeeded.
	if old != "" {
		os.RemoveAll(old)
	}
	return nil
}

func runPluginList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.Bool("project", false, "list project-scope plugins instead of global")
	if err := fs.Parse(args); err != nil {
		return err
	}
	scope, err := resolveScope(*project)
	if err != nil {
		return err
	}
	lf, err := plugins.ReadLockfile(scope.lock)
	if err != nil {
		return err
	}
	if len(lf.Plugins) == 0 {
		fmt.Fprintf(stdout, "No %s plugins installed.\n", scope.label)
		return nil
	}
	for _, p := range lf.Plugins {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", p.Name, p.Source, shortCommit(p.Commit))
	}
	return nil
}

func runPluginRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin remove", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.Bool("project", false, "remove from the project scope instead of global")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("marshal plugin remove: exactly one plugin name is required")
	}
	name := rest[0]
	scope, err := resolveScope(*project)
	if err != nil {
		return err
	}
	lf, err := plugins.ReadLockfile(scope.lock)
	if err != nil {
		return err
	}
	if !lf.Remove(name) {
		return fmt.Errorf("plugin %q is not installed in the %s scope", name, scope.label)
	}
	if err := os.RemoveAll(filepath.Join(scope.store, name)); err != nil {
		return fmt.Errorf("remove plugin files: %w", err)
	}
	if err := lf.Write(scope.lock); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed plugin %q from the %s scope.\n", name, scope.label)
	return nil
}

func runPluginInstall(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.Bool("project", false, "install into the project scope (.marshal/plugins)")
	name := fs.String("name", "", "plugin name (default: derived from the source)")
	ref := fs.String("ref", "", "git ref to install (default: remote default branch HEAD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("marshal plugin install: exactly one source is required (usage: marshal plugin install [--project] [--name N] [--ref R] <github:owner/repo[@ref]|git-url>)")
	}
	source, inlineRef, err := plugins.SplitSourceRef(rest[0])
	if err != nil {
		return err
	}
	if *ref == "" {
		*ref = inlineRef
	}
	cloneURL, derivedName, err := plugins.NormalizeSource(source)
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	if err := plugins.ValidateCloneSource(cloneURL, wd); err != nil {
		return err
	}
	if *name == "" {
		*name = derivedName
	}
	if !plugins.ValidName(*name) {
		return fmt.Errorf("invalid plugin name %q", *name)
	}
	scope, err := resolveScope(*project)
	if err != nil {
		return err
	}
	inst, err := plugins.NewInstaller()
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "marshal-plugin-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	cloneDest := filepath.Join(tmp, "clone")
	commit, err := inst.Clone(ctx, cloneURL, *ref, cloneDest)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(cloneDest, ".git")); err != nil {
		return fmt.Errorf("strip .git from clone: %w", err)
	}
	contents, err := plugins.ScanPlugin(cloneDest)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Plugin:  %s\nSource:  %s\nCommit:  %s\nScope:   %s\n\n", *name, cloneURL, commit, scope.label)
	printPluginSummary(stdout, contents)
	fmt.Fprint(stdout, "\nInstall this plugin? [y/N] ")
	if !confirm(stdin) {
		fmt.Fprintln(stdout, "Aborted.")
		return nil
	}

	hash, err := plugins.HashDir(cloneDest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(scope.store, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(scope.store, *name)
	if err := atomicReplace(cloneDest, dest); err != nil {
		return fmt.Errorf("install plugin %q: %w", *name, err)
	}
	lf, err := plugins.ReadLockfile(scope.lock)
	if err != nil {
		return err
	}
	lf.Upsert(plugins.LockEntry{
		Name:        *name,
		Source:      cloneURL,
		Ref:         *ref,
		Commit:      commit,
		ContentHash: hash,
		InstalledAt: time.Now().UTC(),
	})
	if err := lf.Write(scope.lock); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed plugin %q (%s) into the %s scope.\n", *name, shortCommit(commit), scope.label)
	return nil
}

// confirm reads one line and accepts only an explicit affirmative.
func confirm(stdin io.Reader) bool {
	var line string
	if _, err := fmt.Fscanln(stdin, &line); err != nil {
		return false
	}
	return line == "y" || line == "Y" || line == "yes"
}

func runPluginUpdate(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("plugin update", flag.ContinueOnError)
	fs.SetOutput(stdout)
	project := fs.Bool("project", false, "update project-scope plugins instead of global")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return errors.New("marshal plugin update: at most one plugin name is accepted (omit to update all)")
	}
	scope, err := resolveScope(*project)
	if err != nil {
		return err
	}
	lf, err := plugins.ReadLockfile(scope.lock)
	if err != nil {
		return err
	}
	entries := lf.Plugins
	if len(rest) == 1 {
		entry, ok := lf.Find(rest[0])
		if !ok {
			return fmt.Errorf("plugin %q is not installed in the %s scope", rest[0], scope.label)
		}
		entries = []plugins.LockEntry{entry}
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "No %s plugins installed.\n", scope.label)
		return nil
	}
	inst, err := plugins.NewInstaller()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := updateOne(ctx, inst, scope, lf, entry, stdin, stdout); err != nil {
			return err
		}
	}
	return nil
}

// updateOne re-clones one plugin's source, and when the commit moved,
// shows the new contents and asks before swapping files and re-pinning.
func updateOne(ctx context.Context, inst *plugins.Installer, scope scopePaths, lf *plugins.Lockfile, entry plugins.LockEntry, stdin io.Reader, stdout io.Writer) error {
	// The lockfile source is re-cloned here, so re-validate it against the
	// workspace root (the same base used at install time) to prevent a
	// tampered lockfile from cloning an arbitrary local path.
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	if err := plugins.ValidateCloneSource(entry.Source, wd); err != nil {
		return fmt.Errorf("update %s: %w", entry.Name, err)
	}

	tmp, err := os.MkdirTemp("", "marshal-plugin-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	cloneDest := filepath.Join(tmp, "clone")
	commit, err := inst.Clone(ctx, entry.Source, entry.Ref, cloneDest)
	if err != nil {
		return fmt.Errorf("update %s: %w", entry.Name, err)
	}
	if commit == entry.Commit {
		fmt.Fprintf(stdout, "%s is already up to date (%s).\n", entry.Name, shortCommit(commit))
		return nil
	}
	if err := os.RemoveAll(filepath.Join(cloneDest, ".git")); err != nil {
		return fmt.Errorf("strip .git from clone: %w", err)
	}
	contents, err := plugins.ScanPlugin(cloneDest)
	if err != nil {
		return fmt.Errorf("update %s: %w", entry.Name, err)
	}

	fmt.Fprintf(stdout, "Plugin %s: %s -> %s\n", entry.Name, shortCommit(entry.Commit), shortCommit(commit))
	printPluginSummary(stdout, contents)
	fmt.Fprint(stdout, "\nUpdate this plugin? [y/N] ")
	if !confirm(stdin) {
		fmt.Fprintln(stdout, "Skipped.")
		return nil
	}

	hash, err := plugins.HashDir(cloneDest)
	if err != nil {
		return err
	}
	dest := filepath.Join(scope.store, entry.Name)
	if err := atomicReplace(cloneDest, dest); err != nil {
		return fmt.Errorf("update %s: %w", entry.Name, err)
	}
	entry.Commit = commit
	entry.ContentHash = hash
	entry.InstalledAt = time.Now().UTC()
	lf.Upsert(entry)
	if err := lf.Write(scope.lock); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Updated plugin %q to %s.\n", entry.Name, shortCommit(commit))
	return nil
}

// printPluginSummary renders the confirm-on-install review: passive
// content first, then every executable item spelled out (hook command
// lines, MCP server commands, MCP tool policies) so the user sees
// exactly what could run or be auto-approved. Executable entries still
// pass through the permission engine at runtime; this summary is the
// human trust decision point.
func printPluginSummary(stdout io.Writer, c plugins.Contents) {
	fmt.Fprintln(stdout, "Passive content (loaded as reference material):")
	if len(c.Skills) == 0 && len(c.Commands) == 0 {
		fmt.Fprintln(stdout, "  (none)")
	}
	for _, s := range c.Skills {
		fmt.Fprintf(stdout, "  skill %s — %s\n", s.Name, s.Description)
	}
	for _, cmd := range c.Commands {
		fmt.Fprintf(stdout, "  command /%s — %s\n", cmd.Name, cmd.Description)
	}
	fmt.Fprintln(stdout, "Executable content (runs through the permission engine):")
	if len(c.Hooks) == 0 && len(c.MCPServers) == 0 && len(c.MCPPolicies) == 0 {
		fmt.Fprintln(stdout, "  (none)")
	}
	for _, h := range c.Hooks {
		fmt.Fprintf(stdout, "  hook %s [%s]: %s\n", h.Event, h.Matcher, h.Command)
	}
	names := make([]string, 0, len(c.MCPServers))
	for name := range c.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		srv := c.MCPServers[name]
		fmt.Fprintf(stdout, "  mcp server %s: %s %s\n", name, srv.Command, strings.Join(srv.Args, " "))
	}
	tools := make([]string, 0, len(c.MCPPolicies))
	for tool := range c.MCPPolicies {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		fmt.Fprintf(stdout, "  mcp policy %s = %s\n", tool, c.MCPPolicies[tool])
	}
}
