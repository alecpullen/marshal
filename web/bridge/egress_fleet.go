package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// egressMonitorInterval is how often the sidecar's liveness is checked.
var egressMonitorInterval = 15 * time.Second

// egressWiring is what an agent needs to reach the proxy.
type egressWiring struct {
	Env     map[string]string
	Network string
	// Volumes are read-only state-volume subpaths (container mode).
	Volumes []VolumeSubpath
}

// VolumeSubpath mounts a subpath of the state volume at Target.
type VolumeSubpath struct {
	Subpath  string
	Target   string
	ReadOnly bool
}

// egressRuntimeName is the container runtime's name, or false in process
// mode.
func (f *Fleet) egressRuntimeName() (string, bool) {
	if f.egressRuntime != nil {
		return f.egressRuntime()
	}
	if f.runner != nil {
		return "docker", true // tests: the fake runner intercepts everything
	}
	_, name, ok := detectedRuntime()
	return name, ok
}

// StartEgress brings up the egress proxy: a sidecar container on an
// internal network when a container runtime exists, an in-process proxy
// on loopback otherwise. A failure is logged and leaves egress off, so
// agents spawn as they did before rather than not at all.
// egressSecretRefresh is how often injected secret values are re-read.
var egressSecretRefresh = time.Minute

func (f *Fleet) StartEgress(ctx context.Context) (err error) {
	if f.egress != nil {
		return nil
	}
	f.egressMu.Lock()
	f.egressWanted = true
	f.egressMu.Unlock()
	defer func() {
		f.egressMu.Lock()
		if err != nil {
			f.egressErr = err
		} else {
			f.egressErr = nil
		}
		f.egressMu.Unlock()
	}()
	h := newEgressHost(f)
	if err := h.loadTokenKey(); err != nil {
		return fmt.Errorf("egress: %w", err)
	}
	rtName, containers := f.egressRuntimeName()
	if containers {
		h.mode = egressModeContainer
		h.proxyAddr = fmt.Sprintf("%s:%d", egressContainer, egressPort)
		if err := h.listenControl(); err != nil {
			return err
		}
		if err := f.ensureSidecar(ctx, h, rtName); err != nil {
			h.close()
			return fmt.Errorf("egress sidecar: %w", err)
		}
		interval := egressMonitorInterval
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			f.monitorSidecar(h, rtName, interval)
		}()
	} else {
		h.mode = egressModeProcess
		if err := h.startProcessProxy(); err != nil {
			return fmt.Errorf("egress: %w", err)
		}
	}
	f.egress = h
	h.loadCAGens(ctx)
	h.wg.Add(1)
	go h.refreshLoop(egressSecretRefresh)
	slog.Default().Info("webbridge: egress proxy started", "mode", h.mode, "addr", h.proxyAddr)
	return nil
}

// egressDown returns why egress was requested but is not running, or nil
// when it is running or was never requested.
func (f *Fleet) egressDown() error {
	f.egressMu.Lock()
	defer f.egressMu.Unlock()
	if f.egress != nil || !f.egressWanted {
		return nil
	}
	if f.egressErr != nil {
		return f.egressErr
	}
	return errors.New("egress proxy is not running")
}

func (f *Fleet) stopEgress() {
	if f.egress != nil {
		f.egress.close()
	}
}

// ensureSidecar creates the network and image if missing and makes sure
// the sidecar container is running, reusing a healthy one.
func (f *Fleet) ensureSidecar(ctx context.Context, h *egressHost, rt string) error {
	if _, err := f.runRuntime(rt, "network", "inspect", egressNetwork); err != nil {
		if out, err := f.runRuntime(rt, "network", "create", "--internal", egressNetwork); err != nil {
			return fmt.Errorf("create network %s: %w (%s)", egressNetwork, err, out)
		}
	}
	tag := egressImageTag(f.buildVersion)
	if _, err := f.runRuntime(rt, "image", "inspect", tag); err != nil {
		if err := f.buildEgressImage(h, rt, tag); err != nil {
			return err
		}
	}
	out, err := f.runRuntime(rt, "inspect", "--format", "{{.State.Running}} {{.Config.Image}}", egressContainer)
	if err == nil {
		fields := strings.Fields(string(out))
		if len(fields) == 2 && fields[0] == "true" && strings.HasSuffix(fields[1], tag) {
			return nil // reattach: already running the right image
		}
		_, _ = f.runRuntime(rt, "rm", "-f", egressContainer)
	}
	if out, err := f.runRuntime(rt, egressSidecarArgs(rt, f.stateVolume, tag)...); err != nil {
		return fmt.Errorf("start %s: %w (%s)", egressContainer, err, out)
	}
	// The sidecar needs a route out: attach it to the default network too.
	outside := "bridge"
	if rt == "podman" {
		outside = "podman"
	}
	if out, err := f.runRuntime(rt, "network", "connect", outside, egressContainer); err != nil {
		return fmt.Errorf("connect %s to %s: %w (%s)", egressContainer, outside, err, out)
	}
	return nil
}

func egressImageTag(version string) string {
	v := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, version)
	if v == "" {
		v = "dev"
	}
	return "marshal-egress:" + v
}

// egressSidecarArgs is the `run` invocation for the sidecar. It joins the
// internal network only; ensureSidecar attaches the outside network
// afterwards.
func egressSidecarArgs(rtName, volume, image string) []string {
	args := []string{"run", "-d", "--rm", "--name", egressContainer, "--network", egressNetwork}
	args = append(args, volumeMount(rtName, volume, egressMountDir, egressSubpath, false)...)
	return append(args, image, "egress",
		"--listen", fmt.Sprintf(":%d", egressPort),
		"--control", "unix://"+egressMountDir+"/control.sock")
}

const egressDockerfile = `FROM debian:stable-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY webbridge /usr/local/bin/webbridge
ENTRYPOINT ["/usr/local/bin/webbridge"]
`

// buildEgressImage builds the sidecar image from a temp context holding
// this very binary, so the sidecar always matches the bridge.
func (f *Fleet) buildEgressImage(h *egressHost, rt, tag string) error {
	exe, err := h.executable()
	if err != nil {
		return fmt.Errorf("locate webbridge binary: %w", err)
	}
	dir, err := os.MkdirTemp("", "marshal-egress-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := copyExecutable(exe, filepath.Join(dir, "webbridge")); err != nil {
		return fmt.Errorf("stage webbridge binary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(egressDockerfile), 0o644); err != nil {
		return err
	}
	if out, err := f.runRuntime(rt, "build", "-t", tag, dir); err != nil {
		return fmt.Errorf("build %s: %w (%s)", tag, err, out)
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// monitorSidecar restarts the sidecar if it exits.
func (f *Fleet) monitorSidecar(h *egressHost, rt string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			out, err := f.runRuntime(rt, "inspect", "--format", "{{.State.Running}}", egressContainer)
			if err == nil && strings.TrimSpace(string(out)) == "true" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := f.ensureSidecar(ctx, h, rt); err != nil {
				slog.Default().Warn("webbridge: egress sidecar restart failed", "err", err)
			}
			cancel()
		}
	}
}

// egressPrepare registers the agent with the proxy and returns how to
// wire it. ok is false when egress is off. An error means the agent must
// not start: a workspace asked for credential injection that cannot work.
func (f *Fleet) egressPrepare(ctx context.Context, a Agent) (egressWiring, bool, error) {
	h := f.egress
	if h == nil {
		// Egress was asked for but the proxy is not running. A workspace
		// that restricts the network or injects credentials must not get
		// an unrestricted agent instead, so refuse; open workspaces keep
		// working as they did before the proxy existed.
		if err := f.egressDown(); err != nil && f.workspaceEgress != nil {
			_, spec, werr := f.workspaceEgress(ctx, a)
			if werr != nil {
				return egressWiring{}, false, werr
			}
			if spec.Mode == EgressModeAllowlist || spec.Mode == EgressModeOff || len(spec.Inject) > 0 {
				return egressWiring{}, false, fmt.Errorf("agent %s needs the egress proxy (network policy %q) but it is not running: %w", a.ID, spec.Mode, err)
			}
		}
		return egressWiring{}, false, nil
	}
	ws, spec := "", EgressSpec{}
	if f.workspaceEgress != nil {
		var err error
		if ws, spec, err = f.workspaceEgress(ctx, a); err != nil {
			return egressWiring{}, false, err
		}
	}
	if ws == "" {
		ws = "default"
	}
	token := h.register(ctx, a.ID, ws, spec)
	w := egressWiring{Env: h.proxyEnv(a.ID, token)}
	h.mu.Lock()
	injecting := len(h.agents[a.ID].policy.Inject) > 0
	h.mu.Unlock()
	if injecting {
		ca, err := f.caFor(ctx, ws)
		if err != nil {
			h.remove(a.ID)
			return egressWiring{}, false, fmt.Errorf("agent %s needs credential injection: %w", a.ID, err)
		}
		h.setCA(a.ID, ca.id())
		if h.mode == egressModeContainer {
			const dir = "/marshal/ca"
			for k, v := range caEnv(dir+"/"+ws+"-bundle.pem", dir+"/"+ws+".pem") {
				w.Env[k] = v
			}
			w.Volumes = append(w.Volumes, VolumeSubpath{Subpath: "ca", Target: dir, ReadOnly: true})
		} else {
			ca := filepath.Join(f.stateDir, "ca")
			for k, v := range caEnv(filepath.Join(ca, ws+"-bundle.pem"), filepath.Join(ca, ws+".pem")) {
				w.Env[k] = v
			}
		}
	}
	if h.mode == egressModeContainer {
		w.Network = egressNetwork
	}
	return w, true, nil
}

// egressAttached pins the agent's policy to its container address once
// it is running.
func (f *Fleet) egressAttached(a Agent) {
	h := f.egress
	if h == nil || h.mode != egressModeContainer {
		return
	}
	rt, ok := f.egressRuntimeName()
	if !ok {
		return
	}
	format := fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, egressNetwork)
	out, err := f.runRuntime(rt, "inspect", "--format", format, containerNameFor(a.ID))
	ip := strings.TrimSpace(string(out))
	if err != nil || ip == "" {
		slog.Default().Warn("webbridge: could not read agent address on the egress network; the proxy will check the token only", "agent", a.ID, "err", err)
		return
	}
	h.setIP(a.ID, ip)
}

// egressProcessMode reports whether enforcement is advisory: there is no
// container isolation, so a process may bypass the proxy.
func (f *Fleet) egressProcessMode() bool {
	return f.egress == nil || f.egress.mode != egressModeContainer
}

// egressRefresh re-derives live agents' policies after a secret changed.
func (f *Fleet) egressRefresh() {
	if f.egress == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.egress.refreshAll(ctx)
}

// noteBlocked records that agentID tried to reach a refused host and
// appends a network_block delta, once per (agent, host) per ten minutes.
func (f *Fleet) noteBlocked(agentID, host string) {
	host = normalizeHost(host)
	key := agentID + "|" + host
	now := time.Now()
	f.blockedMu.Lock()
	if t, ok := f.blockedSeen[key]; ok && now.Sub(t) < 10*time.Minute {
		f.blockedMu.Unlock()
		return
	}
	f.blockedSeen[key] = now
	for k, t := range f.blockedSeen { // forget old entries
		if now.Sub(t) >= 10*time.Minute {
			delete(f.blockedSeen, k)
		}
	}
	f.blockedMu.Unlock()
	ws := ""
	if h := f.egress; h != nil {
		h.mu.Lock()
		if a, ok := h.agents[agentID]; ok {
			ws = a.workspace
		}
		h.mu.Unlock()
	}
	_, _ = f.fleetLog.Append(fleetStreamKey, networkBlockDelta{
		Kind: "network_block", SessionID: agentID, AgentID: agentID, Host: host, Workspace: ws, At: now.UnixMilli(),
	})
}

// networkBlockDelta is the fleet delta for a refused request.
type networkBlockDelta struct {
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Host      string `json:"host"`
	Workspace string `json:"workspace,omitempty"`
	At        int64  `json:"at"`
}
