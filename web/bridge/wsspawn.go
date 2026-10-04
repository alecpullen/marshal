package bridge

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Spawn-into-workspace errors.
var (
	// ErrWorkspaceNotBuilt means the workspace's image has not been built.
	ErrWorkspaceNotBuilt = errors.New("bridge: workspace is not built. Build it first")
	// ErrWorkspaceNeedsRuntime means a workspace was asked for with no
	// container runtime to run it.
	ErrWorkspaceNeedsRuntime = errors.New("bridge: workspaces need a container runtime")
	// ErrWorkspaceMountTarget is a mount target the bridge will not grant.
	ErrWorkspaceMountTarget = errors.New("bridge: invalid workspace mount")
)

// ErrSetupFailed is a workspace setup step that exited non-zero.
type ErrSetupFailed struct{ Output string }

func (e ErrSetupFailed) Error() string {
	return "bridge: workspace setup failed:\n" + e.Output
}

// setupOutputTail bounds the setup output carried in an error.
const setupOutputTail = 64 << 10

var (
	mountTargetRe = regexp.MustCompile(`^/[A-Za-z0-9._/\-]*$`)
	volumeNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]*$`)
	fileSourceRe  = regexp.MustCompile(`^[A-Za-z0-9._/\-]+$`)
)

// reservedMountTargets are container paths a workspace may not shadow.
var reservedMountTargets = []string{
	containerWorkDir, containerSocketDir, "/marshal",
	"/var/run/docker.sock", "/run/docker.sock",
}

// wsExtras is the mounts and environment a workspace adds to a container.
type wsExtras struct {
	mounts []string
	env    map[string]string
}

// runtimeInfo is the container runtime to use for workspace work. Under a
// fake runner (tests) it is "docker".
func (f *Fleet) runtimeInfo() (path, name string, ok bool) {
	if f.runner != nil {
		return "docker", "docker", true
	}
	return detectedRuntime()
}

func wsRefOf(w *AgentWorkspace) WSRef {
	return WSRef{Source: w.Source, Name: w.Name, Version: w.Version}
}

// validateMountTarget refuses a target that is relative, carries
// characters that could add options to the --mount spec, or shadows a
// path the bridge owns.
func validateMountTarget(target string) error {
	if target == "/" || !mountTargetRe.MatchString(target) || path.Clean(target) != target {
		return fmt.Errorf("%w: target %q must be a clean absolute path", ErrWorkspaceMountTarget, target)
	}
	clean := path.Clean(target)
	for _, r := range reservedMountTargets {
		if clean == r || strings.HasPrefix(clean, r+"/") {
			return fmt.Errorf("%w: target %q collides with %s", ErrWorkspaceMountTarget, target, r)
		}
	}
	return nil
}

// plainVolumeMount mounts a whole named volume.
func plainVolumeMount(volume, target string, readonly bool) []string {
	spec := fmt.Sprintf("type=volume,source=%s,target=%s", volume, target)
	if readonly {
		spec += ",readonly"
	}
	return []string{"--mount", spec}
}

// workspaceMounts renders a doc's mounts and files as runtime arguments.
// storeName is the Studio template whose file store holds the [files]
// sources.
func (f *Fleet) workspaceMounts(ctx context.Context, doc WSDoc, storeName, runtimeName string) ([]string, error) {
	var args []string
	seen := map[string]bool{}
	claim := func(target string) error {
		if err := validateMountTarget(target); err != nil {
			return err
		}
		if c := path.Clean(target); seen[c] {
			return fmt.Errorf("%w: target %q is used twice", ErrWorkspaceMountTarget, target)
		} else {
			seen[c] = true
		}
		return nil
	}
	for _, m := range doc.Mounts {
		if err := claim(m.Target); err != nil {
			return nil, err
		}
		switch {
		case m.Repo != "" && m.Volume == "":
			repo, ok := f.ws.Repo(m.Repo)
			if !ok {
				return nil, fmt.Errorf("%w: mount names unregistered repo %q", ErrWorkspaceMountTarget, m.Repo)
			}
			if f.git == nil {
				return nil, errors.New("bridge: git is required to mount a repo but was not found at startup")
			}
			cred, err := f.creds.Resolve(ctx, DefaultOwnerID, repo.CredRef)
			if err != nil {
				return nil, fmt.Errorf("resolve credential for %s: %w", repo.ID, err)
			}
			mirror, err := f.git.EnsureMirror(f.stateDir, repo.URL, cred)
			if err != nil {
				return nil, err
			}
			args = append(args, volumeMount(runtimeName, f.stateVolume, m.Target, "repos/"+filepath.Base(mirror), true)...)
		case m.Volume != "" && m.Repo == "":
			if !volumeNameRe.MatchString(m.Volume) {
				return nil, fmt.Errorf("%w: invalid volume name %q", ErrWorkspaceMountTarget, m.Volume)
			}
			// The marshal- namespace belongs to the bridge: its state
			// volume holds fleet.json, credentials and every agent's work.
			if m.Volume == f.stateVolume || strings.HasPrefix(m.Volume, "marshal-") {
				return nil, fmt.Errorf("%w: volume %q is reserved for the bridge", ErrWorkspaceMountTarget, m.Volume)
			}
			args = append(args, plainVolumeMount(m.Volume, m.Target, m.Readonly)...)
		default:
			return nil, fmt.Errorf("%w: a mount names exactly one of repo or volume", ErrWorkspaceMountTarget)
		}
	}
	for src, fm := range doc.Files {
		if err := claim(fm.Target); err != nil {
			return nil, err
		}
		if !fileSourceRe.MatchString(src) || path.IsAbs(src) || path.Clean(src) != src || src == ".." || strings.HasPrefix(src, "../") {
			return nil, fmt.Errorf("%w: invalid file source %q", ErrWorkspaceMountTarget, src)
		}
		args = append(args, volumeMount(runtimeName, f.stateVolume, fm.Target, "workspaces/"+storeName+"/files/"+src, fm.Readonly)...)
	}
	return args, nil
}

// workspaceNetworkEnv is where the egress proxy and CA settings join a
// workspace container (W4.3). It adds nothing yet.
func (f *Fleet) workspaceNetworkEnv(ctx context.Context, a Agent, doc WSDoc) (map[string]string, []string, error) {
	return nil, nil, nil
}

// AgentWorkspaceDoc resolves the workspace an agent runs in: its store
// name (the CA and policy key) and merged doc. ok is false for an agent
// with no workspace. The egress proxy (B7) maps the doc's Network and
// Inject onto its own spec and installs that as Fleet.workspaceEgress, so
// this package does not depend on the proxy's types.
func (f *Fleet) AgentWorkspaceDoc(ctx context.Context, a Agent) (name string, doc WSDoc, ok bool, err error) {
	if a.Workspace == nil {
		return "", WSDoc{}, false, nil
	}
	tr, tt := f.workspaceRoots(a)
	res, err := f.ResolveWorkspaceIn(ctx, wsRefOf(a.Workspace), tr, tt)
	if err != nil {
		return "", WSDoc{}, false, err
	}
	return res.Name, res.Doc, true, nil
}

// buildWorkspaceExtras renders everything a workspace adds to a container.
func (f *Fleet) buildWorkspaceExtras(ctx context.Context, a Agent, doc WSDoc, storeName, runtimeName string) (wsExtras, error) {
	mounts, err := f.workspaceMounts(ctx, doc, storeName, runtimeName)
	if err != nil {
		return wsExtras{}, err
	}
	env, netMounts, err := f.workspaceNetworkEnv(ctx, a, doc)
	if err != nil {
		return wsExtras{}, err
	}
	return wsExtras{mounts: append(mounts, netMounts...), env: env}, nil
}

// workspaceRuntimeExtras is the container config's source for a
// workspace's mounts and env: the spawn's own resolution while it is in
// flight, a fresh resolution when a runtime restarts later.
func (f *Fleet) workspaceRuntimeExtras(a Agent, runtimeName string) ([]string, map[string]string, error) {
	f.wsMu.Lock()
	ex, ok := f.wsExtras[a.ID]
	f.wsMu.Unlock()
	if ok {
		return ex.mounts, ex.env, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tr, tt := f.workspaceRoots(a)
	res, err := f.ResolveWorkspaceIn(ctx, wsRefOf(a.Workspace), tr, tt)
	if err != nil {
		return nil, nil, err
	}
	ex, err = f.buildWorkspaceExtras(ctx, a, res.Doc, res.ImageName, runtimeName)
	if err != nil {
		return nil, nil, err
	}
	return ex.mounts, ex.env, nil
}

// builtImage returns the derived image tag of a resolved workspace, or
// ErrWorkspaceNotBuilt.
func (f *Fleet) builtImage(res Resolved) (string, error) {
	if res.ImageName == "" {
		return "", fmt.Errorf("%w: a repo workspace must extend a Studio template", ErrWorkspaceNotBuilt)
	}
	meta, err := f.templates.Meta(res.ImageName)
	if err != nil {
		return "", err
	}
	v, ok := meta.version(res.ImageVersion)
	if !ok || v.BuildStatus != "ok" || v.ImageTag == "" {
		return "", fmt.Errorf("%w: %s@%d", ErrWorkspaceNotBuilt, res.ImageName, res.ImageVersion)
	}
	return v.ImageTag, nil
}

// repoOverlayAddsImageLayers reports whether a repo workspace asks for
// toolchains or packages beyond its Studio template's: those are image
// layers, which a repo template cannot build.
func (f *Fleet) repoOverlayAddsImageLayers(ctx context.Context, res Resolved) bool {
	if res.Source != "repo" {
		return false
	}
	base, _, err := f.studioDoc(ctx, res.ImageName, res.ImageVersion)
	if err != nil {
		return false
	}
	d := res.Doc
	return len(d.Workspace.Toolchains) != len(base.Workspace.Toolchains) ||
		len(d.Packages.Apt) != len(base.Packages.Apt) || len(d.Packages.Go) != len(base.Packages.Go) ||
		len(d.Packages.Npm) != len(base.Packages.Npm) || len(d.Packages.Pip) != len(base.Packages.Pip)
}

// applyResources sets the profile's caps from a workspace's [resources].
func applyResources(p *RuntimeProfile, r WSResources) {
	if r.CPU > 0 {
		p.CPUs = r.CPU
	}
	if r.Memory != "" {
		if n, err := parseWSSize(r.Memory); err == nil && n > 0 {
			p.MemoryMB = int(n >> 20)
		}
	}
}

// tail returns the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// runWorkspaceSetup runs a workspace's [setup] command once in a one-shot
// container that shares the agent's image, mounts and environment. A
// non-zero exit aborts the spawn.
func (f *Fleet) runWorkspaceSetup(ctx context.Context, cfg ContainerConfig, run string) error {
	tr := newContainerTransport(cfg)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	out, err := f.runRuntime(cfg.RuntimeName, tr.buildSetupArgs(run)...)
	if err != nil {
		return ErrSetupFailed{Output: tail(string(out), setupOutputTail)}
	}
	return nil
}

// armWorkspaceTimeout wires a workspace's deadline to the agent's first
// prompt. It is a no-op without a [resources].timeout.
func (f *Fleet) armWorkspaceTimeout(rt *agentRuntime, a Agent) {
	if a.Workspace == nil || a.Workspace.Timeout == "" {
		return
	}
	d, err := time.ParseDuration(a.Workspace.Timeout)
	if err != nil || d <= 0 {
		return
	}
	var once sync.Once
	rt.reg.OnPrompt = func(sessionID string) {
		once.Do(func() {
			fire := func() { f.workspaceTimeout(rt, sessionID) }
			var stop func()
			if f.afterFunc != nil {
				stop = f.afterFunc(d, fire)
			} else {
				t := time.AfterFunc(d, fire)
				stop = func() { t.Stop() }
			}
			rt.timeoutMu.Lock()
			rt.timeoutStop = stop
			rt.timeoutMu.Unlock()
		})
	}
}

func (rt *agentRuntime) stopTimeout() {
	rt.timeoutMu.Lock()
	stop := rt.timeoutStop
	rt.timeoutStop = nil
	rt.timeoutMu.Unlock()
	if stop != nil {
		stop()
	}
}

// workspaceTimeout cancels the turn and pauses the agent when its
// deadline fires, unless the runtime has since been replaced.
func (f *Fleet) workspaceTimeout(rt *agentRuntime, sessionID string) {
	cur, err := f.runtimeForAgent(rt.id)
	if err != nil || cur != rt {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = rt.reg.Cancel(ctx, sessionID)
	_ = f.Pause(rt.id)
	f.auditf(AuditEvent{Event: AuditAgentTimeout, OwnerID: DefaultOwnerID, AgentID: rt.id})
}

// workspaceMountedMirrors is the set of mirror directories that Studio
// workspaces mount, so a prune cannot reclaim a library an agent may be
// handed. A template counts through its latest published version and any
// version a persisted agent runs on. When a source cannot be parsed the
// answer is every registered repo's mirror: keeping too much is the safe
// failure for a delete.
func (f *Fleet) workspaceMountedMirrors() map[string]bool {
	live := map[string]bool{}
	metas, err := f.templates.List()
	if err != nil || len(metas) == 0 {
		return live
	}
	versions := map[string]map[int]bool{}
	for _, m := range metas {
		versions[m.Name] = map[int]bool{}
		if m.Published > 0 {
			versions[m.Name][m.Published] = true
		}
	}
	for _, a := range f.ws.Agents() {
		if a.Workspace != nil && a.Workspace.Source == "studio" && a.Workspace.Version > 0 {
			if vs, ok := versions[a.Workspace.Name]; ok {
				vs[a.Workspace.Version] = true
			}
		}
	}
	keepAll := func() {
		for _, r := range f.ws.Repos() {
			live[mirrorDir(f.stateDir, r.URL)] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for name, vs := range versions {
		for n := range vs {
			src, err := f.templates.Read(name, n)
			if err != nil {
				continue
			}
			doc, _, _, err := f.parseWorkspace(ctx, src)
			if err != nil {
				keepAll()
				return live
			}
			for _, m := range doc.Mounts {
				if m.Repo == "" {
					continue
				}
				if r, ok := f.ws.Repo(m.Repo); ok {
					live[mirrorDir(f.stateDir, r.URL)] = true
				}
			}
		}
	}
	return live
}
