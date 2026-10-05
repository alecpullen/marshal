package bridge

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WSDoc mirrors the engine's parsed workspace file (internal/workspacecfg
// Doc). The bridge never shares a Go type with the engine; this is the
// JSON contract, kept in step with it.
type WSDoc struct {
	Workspace  WSWorkspace         `json:"workspace"`
	Packages   WSPackages          `json:"packages"`
	Mounts     []WSMount           `json:"mounts,omitempty"`
	Files      map[string]WSFile   `json:"files,omitempty"`
	SecretsEnv map[string]string   `json:"secretsEnv,omitempty"`
	Inject     map[string]WSInject `json:"inject,omitempty"`
	Network    WSNetwork           `json:"network"`
	Resources  WSResources         `json:"resources"`
	Policy     WSPolicy            `json:"policy"`
	Setup      WSSetup             `json:"setup"`
}

type WSWorkspace struct {
	Name       string   `json:"name"`
	Base       string   `json:"base"`
	Toolchains []string `json:"toolchains,omitempty"`
	Extends    string   `json:"extends,omitempty"`
}

type WSPackages struct {
	Apt []string `json:"apt,omitempty"`
	Go  []string `json:"go,omitempty"`
	Npm []string `json:"npm,omitempty"`
	Pip []string `json:"pip,omitempty"`
}

type WSMount struct {
	Repo     string `json:"repo,omitempty"`
	Volume   string `json:"volume,omitempty"`
	Target   string `json:"target"`
	Readonly bool   `json:"readonly,omitempty"`
}

type WSFile struct {
	Target   string `json:"target"`
	Readonly bool   `json:"readonly,omitempty"`
}

type WSInject struct {
	Ref    string `json:"ref"`
	Header string `json:"header,omitempty"`
	Format string `json:"format,omitempty"`
}

type WSNetwork struct {
	Mode   string   `json:"mode,omitempty"`
	Egress []string `json:"egress,omitempty"`
}

type WSResources struct {
	CPU     float64 `json:"cpu,omitempty"`
	Memory  string  `json:"memory,omitempty"`
	Disk    string  `json:"disk,omitempty"`
	Timeout string  `json:"timeout,omitempty"`
}

type WSPolicy struct {
	Mode  string   `json:"mode,omitempty"`
	Allow []string `json:"allow,omitempty"`
}

type WSSetup struct {
	Run string `json:"run,omitempty"`
}

// WSSection is one layer's line range in the source.
type WSSection struct {
	Layer     int    `json:"layer"`
	Key       string `json:"key"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// WSDiag is one parse diagnostic.
type WSDiag struct {
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// Resolution errors.
var (
	ErrUntrustedRepoTemplate = errors.New("bridge: repo workspace templates are ignored until the project is trusted")
	ErrWorkspaceInvalid      = errors.New("bridge: workspace has errors")
)

// ErrWorkspaceMerge is a repo template that breaks a merge rule.
type ErrWorkspaceMerge struct{ Field, Reason string }

func (e ErrWorkspaceMerge) Error() string {
	return fmt.Sprintf("bridge: repo workspace cannot change %s: %s", e.Field, e.Reason)
}

// parseCacheSize bounds the parse cache.
const parseCacheSize = 256

type parsedWS struct {
	doc      WSDoc
	sections []WSSection
	diags    []WSDiag
}

// wsParseCache is a small LRU keyed by the sha256 of the source.
type wsParseCache struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
}

type wsCacheEntry struct {
	key string
	val parsedWS
}

func newWSParseCache() *wsParseCache {
	return &wsParseCache{order: list.New(), items: map[string]*list.Element{}}
}

func (c *wsParseCache) get(key string) (parsedWS, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*wsCacheEntry).val, true
	}
	return parsedWS{}, false
}

func (c *wsParseCache) put(key string, val parsedWS) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.Value.(*wsCacheEntry).val = val
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(&wsCacheEntry{key, val})
	for c.order.Len() > parseCacheSize {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*wsCacheEntry).key)
	}
}

func hasWSErrors(diags []WSDiag) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func firstWSError(diags []WSDiag) string {
	for _, d := range diags {
		if d.Severity == "error" {
			return fmt.Sprintf("line %d: %s", d.Line, d.Message)
		}
	}
	return ""
}

// parseWorkspace asks the control agent to parse src, caching by hash.
func (f *Fleet) parseWorkspace(ctx context.Context, src []byte) (WSDoc, []WSSection, []WSDiag, error) {
	sum := sha256.Sum256(src)
	key := hex.EncodeToString(sum[:])
	if v, ok := f.wsParses.get(key); ok {
		return v.doc, v.sections, v.diags, nil
	}
	raw, err := f.controlCall(ctx, "workspace/parse", map[string]any{"source": string(src)}, false)
	if err != nil {
		return WSDoc{}, nil, nil, err
	}
	var res struct {
		Doc         WSDoc       `json:"doc"`
		Sections    []WSSection `json:"sections"`
		Diagnostics []WSDiag    `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return WSDoc{}, nil, nil, fmt.Errorf("bridge: decode workspace/parse: %w", err)
	}
	f.wsParses.put(key, parsedWS{res.Doc, res.Sections, res.Diagnostics})
	return res.Doc, res.Sections, res.Diagnostics, nil
}

// WSRef names a workspace: "name", "name@3" or "repo:name".
type WSRef struct {
	Source  string // "studio" or "repo"
	Name    string
	Version int
}

// String renders the reference in its canonical form.
func (r WSRef) String() string {
	switch {
	case r.Source == "repo":
		return "repo:" + r.Name
	case r.Version > 0:
		return r.Name + "@" + strconv.Itoa(r.Version)
	}
	return r.Name
}

// ParseWSRef parses a workspace reference.
func ParseWSRef(s string) (WSRef, error) {
	s = strings.TrimSpace(s)
	if name, ok := strings.CutPrefix(s, "repo:"); ok {
		if err := validTemplateName(name); err != nil {
			return WSRef{}, err
		}
		return WSRef{Source: "repo", Name: name}, nil
	}
	name, ver, hasVer := strings.Cut(s, "@")
	if err := validTemplateName(name); err != nil {
		return WSRef{}, err
	}
	ref := WSRef{Source: "studio", Name: name}
	if hasVer {
		n, err := strconv.Atoi(ver)
		if err != nil || n < 1 {
			return WSRef{}, fmt.Errorf("bridge: invalid workspace version %q", ver)
		}
		ref.Version = n
	}
	return ref, nil
}

// Resolved is a workspace reference resolved to a parsed doc.
type Resolved struct {
	Doc     WSDoc
	Name    string
	Version int
	Source  string
	Hash    string
	// ImageName and ImageVersion name the built Studio template whose
	// image the workspace runs on: itself for a Studio reference, the
	// template it extends for a repo reference (empty when it extends
	// none, so there is no image to run).
	ImageName    string
	ImageVersion int
}

func (f *Fleet) studioDoc(ctx context.Context, name string, version int) (WSDoc, int, error) {
	meta, err := f.templates.Meta(name)
	if err != nil {
		return WSDoc{}, 0, err
	}
	if version == 0 {
		version = meta.Published
	}
	if version == 0 {
		return WSDoc{}, 0, fmt.Errorf("%w: %s has no published version", ErrTemplateNotFound, name)
	}
	src, err := f.templates.Read(name, version)
	if err != nil {
		return WSDoc{}, 0, err
	}
	doc, _, diags, err := f.parseWorkspace(ctx, src)
	if err != nil {
		return WSDoc{}, 0, err
	}
	if hasWSErrors(diags) {
		return WSDoc{}, 0, fmt.Errorf("%w: %s@%d: %s", ErrWorkspaceInvalid, name, version, firstWSError(diags))
	}
	return doc, version, nil
}

// ResolveWorkspace resolves ref against the Studio store and, for repo
// references, projectRoot's .marshal/workspaces directory. Repo templates
// are trusted by path only: projectRoot must be a trusted project, and the
// config hash that covers workspace files in the engine is not recomputed.
func (f *Fleet) ResolveWorkspace(ctx context.Context, ref WSRef, projectRoot string) (Resolved, error) {
	return f.ResolveWorkspaceIn(ctx, ref, projectRoot, projectRoot)
}

// ResolveWorkspaceIn is ResolveWorkspace with the repo template read from
// templateRoot and trust taken from trustRoot. A git-sourced agent reads
// the prepared checkout but takes trust from the registered project that
// names its repo.
func (f *Fleet) ResolveWorkspaceIn(ctx context.Context, ref WSRef, templateRoot, trustRoot string) (Resolved, error) {
	var doc WSDoc
	version := ref.Version
	imageName, imageVersion := "", 0
	switch ref.Source {
	case "repo":
		if trustRoot == "" || projectTrust(trustRoot) != "trusted" {
			return Resolved{}, ErrUntrustedRepoTemplate
		}
		src, err := os.ReadFile(filepath.Join(templateRoot, ".marshal", "workspaces", ref.Name+".toml"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return Resolved{}, fmt.Errorf("%w: repo:%s", ErrTemplateNotFound, ref.Name)
			}
			return Resolved{}, err
		}
		overlay, _, diags, err := f.parseWorkspace(ctx, src)
		if err != nil {
			return Resolved{}, err
		}
		if hasWSErrors(diags) {
			return Resolved{}, fmt.Errorf("%w: repo:%s: %s", ErrWorkspaceInvalid, ref.Name, firstWSError(diags))
		}
		doc = overlay
		if ext := overlay.Workspace.Extends; ext != "" {
			base, err := ParseWSRef(ext)
			if err != nil {
				return Resolved{}, err
			}
			if base.Source != "studio" {
				return Resolved{}, ErrWorkspaceMerge{Field: "extends", Reason: "must name a Studio template"}
			}
			bdoc, bver, err := f.studioDoc(ctx, base.Name, base.Version)
			if err != nil {
				return Resolved{}, err
			}
			imageName, imageVersion = base.Name, bver
			if doc, err = mergeWS(bdoc, overlay); err != nil {
				return Resolved{}, err
			}
		}
		version = 0
	default:
		var err error
		doc, version, err = f.studioDoc(ctx, ref.Name, ref.Version)
		if err != nil {
			return Resolved{}, err
		}
		imageName, imageVersion = ref.Name, version
	}
	canon, err := json.Marshal(doc)
	if err != nil {
		return Resolved{}, err
	}
	sum := sha256.Sum256(canon)
	return Resolved{Doc: doc, Name: ref.Name, Version: version, Source: ref.Source, Hash: hex.EncodeToString(sum[:]),
		ImageName: imageName, ImageVersion: imageVersion}, nil
}

var wsSizeRe = regexp.MustCompile(`^(\d+)([kmg])$`)

// parseWSSize reads "8g", "512m" or "64k" as bytes.
func parseWSSize(s string) (int64, error) {
	m := wsSizeRe.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return 0, fmt.Errorf("bridge: invalid size %q", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2] {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	case "g":
		n <<= 30
	}
	return n, nil
}

func appendUnique(base, extra []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(base))
	for _, s := range base {
		seen[s] = true
	}
	for _, s := range extra {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// networkRank orders modes by openness; an unset mode is open.
func networkRank(mode string) int {
	switch mode {
	case "off":
		return 0
	case "allowlist":
		return 1
	}
	return 2
}

// lowerOnly checks that overlay does not raise a size or duration.
func lowerOnly(field, base, overlay string, parse func(string) (float64, error)) error {
	if overlay == "" || base == "" {
		return nil
	}
	o, err := parse(overlay)
	if err != nil {
		return ErrWorkspaceMerge{Field: field, Reason: err.Error()}
	}
	b, err := parse(base)
	if err != nil {
		return ErrWorkspaceMerge{Field: field, Reason: err.Error()}
	}
	if o > b {
		return ErrWorkspaceMerge{Field: field, Reason: fmt.Sprintf("%s is higher than the template's %s", overlay, base)}
	}
	return nil
}

// mergeWS applies a repo template over its Studio base (spec §5.2).
func mergeWS(base, overlay WSDoc) (WSDoc, error) {
	out := base
	if overlay.Workspace.Base != "" && overlay.Workspace.Base != base.Workspace.Base {
		return WSDoc{}, ErrWorkspaceMerge{Field: "base", Reason: "a repo template cannot change the base image"}
	}
	if overlay.Workspace.Name != "" {
		out.Workspace.Name = overlay.Workspace.Name
	}
	out.Workspace.Extends = ""
	out.Workspace.Toolchains = appendUnique(base.Workspace.Toolchains, overlay.Workspace.Toolchains)

	out.Packages = WSPackages{
		Apt: appendUnique(base.Packages.Apt, overlay.Packages.Apt),
		Go:  appendUnique(base.Packages.Go, overlay.Packages.Go),
		Npm: appendUnique(base.Packages.Npm, overlay.Packages.Npm),
		Pip: appendUnique(base.Packages.Pip, overlay.Packages.Pip),
	}

	// mounts and files: append; a target collision is an error.
	targets := map[string]bool{}
	out.Mounts = append([]WSMount(nil), base.Mounts...)
	for _, m := range base.Mounts {
		targets[m.Target] = true
	}
	out.Files = make(map[string]WSFile, len(base.Files)+len(overlay.Files))
	for src, fm := range base.Files {
		out.Files[src] = fm
		targets[fm.Target] = true
	}
	for _, m := range overlay.Mounts {
		if targets[m.Target] {
			return WSDoc{}, ErrWorkspaceMerge{Field: "mounts", Reason: "target " + m.Target + " is already used"}
		}
		targets[m.Target] = true
		out.Mounts = append(out.Mounts, m)
	}
	for src, fm := range overlay.Files {
		if _, dup := base.Files[src]; dup || targets[fm.Target] {
			return WSDoc{}, ErrWorkspaceMerge{Field: "files", Reason: "target " + fm.Target + " is already used"}
		}
		targets[fm.Target] = true
		out.Files[src] = fm
	}
	if len(out.Files) == 0 {
		out.Files = nil
	}

	// secrets: only keys the base declares, with the same reference.
	for k, v := range overlay.SecretsEnv {
		if bv, ok := base.SecretsEnv[k]; !ok || bv != v {
			return WSDoc{}, ErrWorkspaceMerge{Field: "secrets", Reason: k + " is not declared by the Studio template"}
		}
	}
	for host, inj := range overlay.Inject {
		if b, ok := base.Inject[host]; !ok || b != inj {
			return WSDoc{}, ErrWorkspaceMerge{Field: "secrets.inject", Reason: host + " is not declared by the Studio template"}
		}
	}

	// resources: lower only.
	size := func(s string) (float64, error) { n, err := parseWSSize(s); return float64(n), err }
	dur := func(s string) (float64, error) { d, err := time.ParseDuration(s); return float64(d), err }
	if err := lowerOnly("resources.memory", base.Resources.Memory, overlay.Resources.Memory, size); err != nil {
		return WSDoc{}, err
	}
	if err := lowerOnly("resources.disk", base.Resources.Disk, overlay.Resources.Disk, size); err != nil {
		return WSDoc{}, err
	}
	if err := lowerOnly("resources.timeout", base.Resources.Timeout, overlay.Resources.Timeout, dur); err != nil {
		return WSDoc{}, err
	}
	if overlay.Resources.CPU > 0 && base.Resources.CPU > 0 && overlay.Resources.CPU > base.Resources.CPU {
		return WSDoc{}, ErrWorkspaceMerge{Field: "resources.cpu", Reason: "cpu is higher than the template's"}
	}
	if overlay.Resources.CPU > 0 {
		out.Resources.CPU = overlay.Resources.CPU
	}
	if overlay.Resources.Memory != "" {
		out.Resources.Memory = overlay.Resources.Memory
	}
	if overlay.Resources.Disk != "" {
		out.Resources.Disk = overlay.Resources.Disk
	}
	if overlay.Resources.Timeout != "" {
		out.Resources.Timeout = overlay.Resources.Timeout
	}

	// network: tighten only; egress appends.
	if overlay.Network.Mode != "" {
		if networkRank(overlay.Network.Mode) > networkRank(base.Network.Mode) {
			return WSDoc{}, ErrWorkspaceMerge{Field: "network.mode", Reason: overlay.Network.Mode + " is looser than the template's " + orOpen(base.Network.Mode)}
		}
		out.Network.Mode = overlay.Network.Mode
	}
	out.Network.Egress = appendUnique(base.Network.Egress, overlay.Network.Egress)

	// policy: allow appends; the mode may only be set where the base has none.
	if overlay.Policy.Mode != "" {
		if base.Policy.Mode != "" && base.Policy.Mode != overlay.Policy.Mode {
			return WSDoc{}, ErrWorkspaceMerge{Field: "policy.mode", Reason: "a repo template cannot change the template's mode"}
		}
		out.Policy.Mode = overlay.Policy.Mode
	}
	out.Policy.Allow = appendUnique(base.Policy.Allow, overlay.Policy.Allow)

	// setup: Studio first, then repo.
	switch {
	case base.Setup.Run != "" && overlay.Setup.Run != "":
		out.Setup.Run = base.Setup.Run + "\n" + overlay.Setup.Run
	case overlay.Setup.Run != "":
		out.Setup.Run = overlay.Setup.Run
	}
	return out, nil
}

func orOpen(mode string) string {
	if mode == "" {
		return "open"
	}
	return mode
}
