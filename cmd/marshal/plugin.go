package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	pluginSkills, err := plugins.ScanBundle(cloneDest)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Plugin:  %s\nSource:  %s\nCommit:  %s\nScope:   %s\n\n", *name, cloneURL, commit, scope.label)
	fmt.Fprintln(stdout, "Passive content (loaded as reference material):")
	for _, s := range pluginSkills {
		fmt.Fprintf(stdout, "  skill %s — %s\n", s.Name, s.Description)
	}
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
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("replace existing plugin: %w", err)
	}
	if err := plugins.CopyDir(cloneDest, dest); err != nil {
		return fmt.Errorf("install plugin files: %w", err)
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
	return errors.New("not implemented")
}
