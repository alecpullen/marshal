package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// poolContainerPrefix names warm-pool containers.
const poolContainerPrefix = "marshal-pool-"

// poolStartSamples is how many spawn timings are kept per template.
const poolStartSamples = 10

var poolNameRe = regexp.MustCompile(`^marshal-pool-([a-z0-9][a-z0-9-]*)-v(\d+)-(\d+)$`)

// poolEntry is one idle warm container.
type poolEntry struct {
	name, container string
	version, k      int
	workSubpath     string
	socketSubpath   string
	startedAt       time.Time
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func poolSubpath(name string, k int) string { return fmt.Sprintf("pool/%s-%d", name, k) }

type startSample struct {
	warm bool
	ms   int64
}

// poolManager keeps idle containers per published template version
// (spec §5.7) and hands them to git-sourced spawns.
type poolManager struct {
	f *Fleet

	mu   sync.Mutex
	idle map[string][]poolEntry // by "<name>@<n>"
	// reserved is the slot numbers being started, by template name.
	reserved map[string]map[int]bool
	starts   map[string][]startSample
	wg       sync.WaitGroup
}

func newPoolManager(f *Fleet) *poolManager {
	return &poolManager{f: f, idle: map[string][]poolEntry{}, reserved: map[string]map[int]bool{}, starts: map[string][]startSample{}}
}

func poolKey(name string, n int) string { return name + "@" + strconv.Itoa(n) }

// wait blocks until background fills finish. Tests use it.
func (p *poolManager) wait() { p.wg.Wait() }

// fillAsync tops up a version's pool in the background.
func (p *poolManager) fillAsync(name string, n int) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := p.fill(name, n); err != nil {
			slog.Default().Warn("webbridge: fill workspace pool failed", "workspace", name, "version", n, "err", err)
		}
	}()
}

// usedSlots is every slot number of name in use: idle, being started, or
// held by a persisted agent that took a pool container. Callers hold p.mu.
func (p *poolManager) usedSlots(name string) map[int]bool {
	used := map[int]bool{}
	for k := range p.reserved[name] {
		used[k] = true
	}
	for key, entries := range p.idle {
		if strings.HasPrefix(key, name+"@") {
			for _, e := range entries {
				used[e.k] = true
			}
		}
	}
	prefix := "pool/" + name + "-"
	for _, a := range p.f.ws.Agents() {
		if rest, ok := strings.CutPrefix(a.WorkSubpath, prefix); ok {
			if k, err := strconv.Atoi(strings.TrimSuffix(rest, "/work")); err == nil {
				used[k] = true
			}
		}
	}
	return used
}

// fill starts containers until the version has meta.Pool idle ones. Only
// the latest published, built version is pooled.
func (p *poolManager) fill(name string, n int) error {
	meta, err := p.f.templates.Meta(name)
	if err != nil {
		return err
	}
	if meta.Pool == 0 || meta.Published != n {
		return nil
	}
	v, ok := meta.version(n)
	if !ok || v.BuildStatus != "ok" || v.ImageTag == "" {
		return nil
	}
	// An older version's idle containers are stale once a newer one is
	// built and published.
	p.dropExcept(name, n)
	// A pooled container is wired to the egress proxy when it starts (see
	// start). A workspace that can only be enforced through the proxy gets
	// no warm pool while the proxy is not running: its agents would
	// otherwise run on an open network without credential injection.
	doc, _, err := p.f.studioDoc(context.Background(), name, n)
	if err != nil {
		return err
	}
	if p.f.egress == nil && workspaceNeedsProxy(doc) {
		return nil
	}

	key := poolKey(name, n)
	for {
		p.mu.Lock()
		if len(p.idle[key])+len(p.reserved[name]) >= meta.Pool {
			p.mu.Unlock()
			return nil
		}
		used := p.usedSlots(name)
		// A slot whose directory exists is taken even if no idle entry or
		// agent record says so yet: a spawn that took a container has not
		// been persisted by the time its refill starts.
		k := 0
		for used[k] || dirExists(filepath.Join(p.f.stateDir, poolSubpath(name, k))) {
			k++
		}
		if p.reserved[name] == nil {
			p.reserved[name] = map[int]bool{}
		}
		p.reserved[name][k] = true
		p.mu.Unlock()

		entry, err := p.start(name, n, k, v.ImageTag)
		p.mu.Lock()
		delete(p.reserved[name], k)
		if err == nil {
			p.idle[key] = append(p.idle[key], entry)
		}
		p.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

// start runs one idle container for slot k.
func (p *poolManager) start(name string, n, k int, image string) (poolEntry, error) {
	f := p.f
	rtPath, rtName, ok := f.runtimeInfo()
	if !ok {
		return poolEntry{}, ErrWorkspaceNeedsRuntime
	}
	lctx, cancel := f.lifeContext()
	defer cancel()
	doc, _, err := f.studioDoc(lctx, name, n)
	if err != nil {
		return poolEntry{}, err
	}
	entry := poolEntry{
		name: name, version: n, k: k, startedAt: time.Now(),
		container:     fmt.Sprintf("%s%s-v%d-%d", poolContainerPrefix, name, n, k),
		workSubpath:   poolSubpath(name, k) + "/work",
		socketSubpath: poolSubpath(name, k) + "/sock",
	}
	pseudo := Agent{
		ID: fmt.Sprintf("pool-%s-v%d-%d", name, n, k), SourceKind: "git", Profile: DefaultRuntimeProfile(),
		ContainerName: entry.container, WorkSubpath: entry.workSubpath, SocketSubpath: entry.socketSubpath,
	}
	pseudo.Profile.Image = image
	applyResources(&pseudo.Profile, doc.Resources)
	// The container is wired under the slot's pseudo ID. The agent that
	// takes it over is served the same proxy policy through an alias.
	var wiring egressWiring
	if f.egress != nil {
		if wiring, err = f.egressWire(lctx, pseudo, name, egressSpecFor(doc), "", pseudo.ID); err != nil {
			return poolEntry{}, err
		}
	}
	cfg, err := f.containerConfigWith(pseudo, rtPath, rtName, wiring)
	if err != nil {
		f.dropPoolEgress(pseudo.ID)
		return poolEntry{}, err
	}
	ex, err := f.buildWorkspaceExtras(lctx, pseudo, doc, name, rtName)
	if err != nil {
		f.dropPoolEgress(pseudo.ID)
		return poolEntry{}, err
	}
	cfg.ExtraMounts, cfg.ExtraEnv = ex.mounts, ex.env
	for _, sub := range []string{entry.workSubpath, entry.socketSubpath} {
		if err := os.MkdirAll(filepath.Join(f.stateDir, sub), 0o700); err != nil {
			f.dropPoolEgress(pseudo.ID)
			return poolEntry{}, fmt.Errorf("bridge: create pool dir: %w", err)
		}
	}
	tr := newContainerTransport(cfg)
	if f.runner != nil {
		tr.run = f.runner
	}
	if out, err := tr.exec(tr.buildRunArgs()...); err != nil {
		os.RemoveAll(filepath.Join(f.stateDir, poolSubpath(name, k)))
		f.dropPoolEgress(pseudo.ID)
		return poolEntry{}, fmt.Errorf("bridge: start pool container: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if f.egress != nil {
		f.egressAttachedAs(pseudo.ID, entry.container)
	}
	return entry, nil
}

// take removes an idle entry for the version, if there is one.
func (p *poolManager) take(name string, n int) (poolEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := poolKey(name, n)
	entries := p.idle[key]
	if len(entries) == 0 {
		return poolEntry{}, false
	}
	e := entries[0]
	p.idle[key] = entries[1:]
	return e, true
}

// discard kills a pool container and removes its directories.
func (p *poolManager) discard(e poolEntry) {
	p.f.dropPoolEgress(poolPseudoID(e.container))
	if _, name, ok := p.f.runtimeInfo(); ok {
		if out, err := p.f.runRuntime(name, "kill", e.container); err != nil {
			slog.Default().Warn("webbridge: kill pool container failed", "container", e.container, "err", err, "out", strings.TrimSpace(string(out)))
		}
	}
	_ = os.RemoveAll(filepath.Join(p.f.stateDir, poolSubpath(e.name, e.k)))
}

// dropExcept discards idle containers of name that are not version keep.
func (p *poolManager) dropExcept(name string, keep int) {
	p.mu.Lock()
	var gone []poolEntry
	for key, entries := range p.idle {
		if strings.HasPrefix(key, name+"@") && key != poolKey(name, keep) {
			gone = append(gone, entries...)
			delete(p.idle, key)
		}
	}
	p.mu.Unlock()
	for _, e := range gone {
		p.discard(e)
	}
}

// drop discards every idle container of a template.
func (p *poolManager) drop(name string) { p.dropExcept(name, -1) }

// resize applies a changed pool size: surplus idle containers go, a
// shortfall is filled.
func (p *poolManager) resize(name string) {
	meta, err := p.f.templates.Meta(name)
	if err != nil {
		return
	}
	key := poolKey(name, meta.Published)
	p.mu.Lock()
	var surplus []poolEntry
	if entries := p.idle[key]; len(entries) > meta.Pool {
		surplus = append(surplus, entries[meta.Pool:]...)
		p.idle[key] = entries[:meta.Pool]
	}
	p.mu.Unlock()
	for _, e := range surplus {
		p.discard(e)
	}
	if meta.Pool > 0 && meta.Published > 0 {
		p.fillAsync(name, meta.Published)
	}
}

// idleCount is the number of idle containers for a template's published version.
func (p *poolManager) idleCount(name string, n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle[poolKey(name, n)])
}

// status is a template's pool health for the build panel.
func (p *poolManager) status(m TemplateMeta) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]int{"size": m.Pool, "idle": len(p.idle[poolKey(m.Name, m.Published)]), "starting": len(p.reserved[m.Name])}
}

// recordStart keeps one spawn's request-to-session timing.
func (p *poolManager) recordStart(name string, warm bool, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := append(p.starts[name], startSample{warm: warm, ms: d.Milliseconds()})
	if len(s) > poolStartSamples {
		s = s[len(s)-poolStartSamples:]
	}
	p.starts[name] = s
}

// startMedians is the median cold and warm start in milliseconds over the
// last samples; zero means none yet.
func (p *poolManager) startMedians(name string) map[string]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var cold, warm []int64
	for _, s := range p.starts[name] {
		if s.warm {
			warm = append(warm, s.ms)
		} else {
			cold = append(cold, s.ms)
		}
	}
	return map[string]int64{"coldMs": median(cold), "warmMs": median(warm)}
}

func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return xs[len(xs)/2]
}

// adopt takes over running pool containers after a bridge restart: those
// no persisted agent holds become idle entries again, the rest of a
// shrunk or deleted template's are killed.
func (p *poolManager) adopt() {
	f := p.f
	metas, err := f.templates.List()
	if err != nil {
		return
	}
	pooled := false
	byName := map[string]TemplateMeta{}
	for _, m := range metas {
		byName[m.Name] = m
		pooled = pooled || m.Pool > 0
	}
	if !pooled {
		return
	}
	_, rtName, ok := f.runtimeInfo()
	if !ok {
		return
	}
	out, err := f.runRuntime(rtName, "ps", "--filter", "name="+poolContainerPrefix, "--format", "{{.Names}}")
	if err != nil {
		return
	}
	held := map[string]bool{}
	for _, a := range f.ws.Agents() {
		if a.ContainerName != "" {
			held[a.ContainerName] = true
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		cname := strings.TrimSpace(line)
		m := poolNameRe.FindStringSubmatch(cname)
		if m == nil || held[cname] {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		k, _ := strconv.Atoi(m[3])
		e := poolEntry{
			name: m[1], version: n, k: k, container: cname, startedAt: time.Now(),
			workSubpath: poolSubpath(m[1], k) + "/work", socketSubpath: poolSubpath(m[1], k) + "/sock",
		}
		meta, ok := byName[m[1]]
		if ok && !p.wiredForProxy(e, meta) {
			// Started before proxy wiring existed, or while the proxy was
			// down: handing it out would run an agent unrestricted.
			p.discard(e)
			continue
		}
		p.mu.Lock()
		key := poolKey(m[1], n)
		keep := ok && meta.Pool > len(p.idle[key]) && meta.Published == n
		if keep {
			p.idle[key] = append(p.idle[key], e)
		}
		p.mu.Unlock()
		if !keep {
			p.discard(e)
		}
	}
	for _, m := range metas {
		if m.Pool > 0 && m.Published > 0 {
			p.fillAsync(m.Name, m.Published)
		}
	}
}

// workspaceNeedsProxy reports whether a workspace's network policy or
// credential injection can only be enforced through the egress proxy.
func workspaceNeedsProxy(doc WSDoc) bool {
	return doc.Network.Mode == EgressModeOff || doc.Network.Mode == EgressModeAllowlist || len(doc.Inject) > 0
}

// dropPoolEgress forgets a pool container's proxy registration.
func (f *Fleet) dropPoolEgress(pseudoID string) {
	if f.egress != nil && pseudoID != "" {
		f.egress.remove(pseudoID)
	}
}

// wiredForProxy decides whether a surviving pool container may be
// adopted after a restart. With the proxy running it must sit on the
// egress network, and its proxy registration is restored; with the proxy
// down, a workspace that needs the proxy cannot adopt it at all.
func (p *poolManager) wiredForProxy(e poolEntry, meta TemplateMeta) bool {
	f := p.f
	doc, _, err := f.studioDoc(context.Background(), e.name, e.version)
	if err != nil {
		return false
	}
	h := f.egress
	if h == nil {
		return !workspaceNeedsProxy(doc)
	}
	if h.mode == egressModeContainer {
		rt, ok := f.egressRuntimeName()
		if !ok {
			return false
		}
		format := fmt.Sprintf(`{{if index .NetworkSettings.Networks %q}}on{{end}}`, egressNetwork)
		out, err := f.runRuntime(rt, "inspect", "--format", format, e.container)
		if err != nil || strings.TrimSpace(string(out)) != "on" {
			return false
		}
	}
	// Restore the registration the restart dropped.
	id := poolPseudoID(e.container)
	if _, err := f.egressWire(context.Background(), Agent{ID: id}, e.name, egressSpecFor(doc), h.pinnedCA(id), id); err != nil {
		return false
	}
	f.egressAttachedAs(id, e.container)
	return true
}
