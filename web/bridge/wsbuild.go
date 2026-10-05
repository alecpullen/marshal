package bridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Build limits (spec §5.4): one build per template, two in total.
const maxConcurrentBuilds = 2

// ErrBuildBusy means a build slot is not free.
var ErrBuildBusy = errors.New("bridge: a workspace build is already running")

// buildLimiter enforces the per-template and global build limits.
type buildLimiter struct {
	mu     sync.Mutex
	active map[string]bool
}

func newBuildLimiter() *buildLimiter { return &buildLimiter{active: map[string]bool{}} }

func (b *buildLimiter) acquire(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[name] || len(b.active) >= maxConcurrentBuilds {
		return ErrBuildBusy
	}
	b.active[name] = true
	return nil
}

func (b *buildLimiter) release(name string) {
	b.mu.Lock()
	delete(b.active, name)
	b.mu.Unlock()
}

var (
	baseImageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@\-]*$`)
	shSafeRe    = regexp.MustCompile(`^[A-Za-z0-9._+:@/=~<>!,\-]+$`)
)

// shQuote single-quotes s for a shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// layerSpec is one image layer of a workspace build.
type layerSpec struct {
	Key        string
	Dockerfile func(parent string) string
	// Hash is the sha256 of the layer's own canonical content; the image
	// tag also folds in the parent tag (see layerTag).
	Hash string
}

func contentHash(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// layerTag derives a layer's image tag from its content hash and parent.
func layerTag(name, key, contentHash, parentTag string) string {
	sum := sha256.Sum256([]byte(contentHash + "\x00" + parentTag))
	return "marshal-ws/" + name + ":" + key + "-" + hex.EncodeToString(sum[:])[:12]
}

// layerDockerfiles returns the layers l1, l2, l3 of doc's image. The
// final labelled layer depends on the version and is added by the build.
func layerDockerfiles(doc WSDoc) ([]layerSpec, error) {
	base := doc.Workspace.Base
	if base == "" {
		return nil, errors.New("bridge: workspace has no base image")
	}
	if !baseImageRe.MatchString(base) {
		return nil, fmt.Errorf("bridge: invalid base image %q", base)
	}

	// l2: toolchains.
	type tc struct {
		Env []string
		Run string
	}
	var tcs []tc
	for _, spec := range doc.Workspace.Toolchains {
		env, run, err := toolchainInstall(spec)
		if err != nil {
			return nil, err
		}
		tcs = append(tcs, tc{env, run})
	}

	// l3: packages.
	var runs []string
	quoteAll := func(kind string, items []string) (string, error) {
		q := make([]string, len(items))
		for i, it := range items {
			if strings.HasPrefix(it, "-") || !shSafeRe.MatchString(it) {
				return "", fmt.Errorf("bridge: invalid %s package %q", kind, it)
			}
			q[i] = shQuote(it)
		}
		return strings.Join(q, " "), nil
	}
	if len(doc.Packages.Apt) > 0 {
		q, err := quoteAll("apt", doc.Packages.Apt)
		if err != nil {
			return nil, err
		}
		runs = append(runs, "RUN apt-get update && apt-get install -y --no-install-recommends "+q+" && rm -rf /var/lib/apt/lists/*")
	}
	for _, p := range []struct {
		kind, cmd string
		items     []string
	}{
		{"go", "go install", doc.Packages.Go},
		{"npm", "npm i -g", doc.Packages.Npm},
		{"pip", "pip install", doc.Packages.Pip},
	} {
		if len(p.items) == 0 {
			continue
		}
		q, err := quoteAll(p.kind, p.items)
		if err != nil {
			return nil, err
		}
		runs = append(runs, "RUN "+p.cmd+" "+q)
	}

	l2 := func(parent string) string {
		var b strings.Builder
		b.WriteString("FROM " + parent + "\n")
		if len(tcs) == 0 {
			return b.String()
		}
		b.WriteString(`RUN test -f /etc/debian_version || { echo "workspace toolchains need a Debian-based base image" >&2; exit 1; }` + "\n")
		b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates xz-utils && rm -rf /var/lib/apt/lists/*\n")
		for _, t := range tcs {
			b.WriteString("RUN " + t.Run + "\n")
			for _, e := range t.Env {
				b.WriteString("ENV " + e + "\n")
			}
		}
		return b.String()
	}
	l3 := func(parent string) string {
		return "FROM " + parent + "\n" + strings.Join(append(runs, ""), "\n")
	}
	return []layerSpec{
		{Key: "l1", Hash: contentHash(map[string]any{"base": base}), Dockerfile: func(string) string { return "FROM " + base + "\n" }},
		{Key: "l2", Hash: contentHash(tcs), Dockerfile: l2},
		{Key: "l3", Hash: contentHash(runs), Dockerfile: l3},
	}, nil
}

// finalDockerfile adds the labels to the last layer.
func finalDockerfile(parent, name string, n int) string {
	return "FROM " + parent + "\nLABEL marshal.workspace=" + name + " marshal.version=" + strconv.Itoa(n) + "\n"
}

// lineWriter splits written bytes into lines for fn.
type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.fn(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.fn(strings.TrimRight(string(w.buf), "\r\n"))
		w.buf = nil
	}
}

// buildDerivedStream is buildDerived with the combined output streamed to
// out line by line. With a runner (tests) the runner's output is written
// once it returns.
func (f *Fleet) buildDerivedStream(runtime, tag, dockerfile string, out io.Writer) error {
	if f.runner != nil {
		o, err := f.runner(runtime, "build", "-t", tag, "-f", "-", ".")
		if len(o) > 0 {
			out.Write(o)
		}
		return err
	}
	ctxDir, err := os.MkdirTemp("", "marshal-ws-build-*")
	if err != nil {
		return fmt.Errorf("bridge: create build context: %w", err)
	}
	defer os.RemoveAll(ctxDir)
	cmd := exec.Command(runtime, "build", "-t", tag, "-f", "-", ctxDir)
	cmd.Env = clientEnv()
	cmd.Stdin = strings.NewReader(dockerfile)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			io.WriteString(out, sc.Text()+"\n")
		}
	}()
	werr := cmd.Wait()
	pw.Close()
	<-done
	return werr
}

// runtimeName is the container runtime's name, "docker" under a fake runner.
func (f *Fleet) runtimeName() (string, error) {
	if f.runner != nil {
		return "docker", nil
	}
	_, name, ok := detectedRuntime()
	if !ok {
		return "", errors.New("bridge: no container runtime to build workspaces")
	}
	return name, nil
}

// buildKey is the build log's event-log key.
func buildKey(name string, n int) string { return "build:" + name + ":" + strconv.Itoa(n) }

func (f *Fleet) buildLogf(name string, n int, format string, args ...any) {
	f.buildLog.Append(buildKey(name, n), map[string]any{"line": fmt.Sprintf(format, args...), "at": time.Now().UTC()})
}

// lifeContext is cancelled when the fleet closes. Callers must call the
// cancel func when done so the watcher goroutine does not outlive them.
func (f *Fleet) lifeContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-f.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// failInterruptedBuilds resets builds a restart left in `building`. Run
// from ReattachAll, before any new build can start.
func (f *Fleet) failInterruptedBuilds() {
	if f.templates == nil {
		return
	}
	got, err := f.templates.FailInterrupted()
	if err != nil {
		slog.Default().Warn("webbridge: reset interrupted builds", "err", err)
	}
	for name, vs := range got {
		for _, n := range vs {
			f.buildLogf(name, n, "build interrupted by a bridge restart")
		}
	}
}

// BuildWorkspace builds template name's version n and waits for it.
func (f *Fleet) BuildWorkspace(ctx context.Context, name string, n int) error {
	if err := f.builds.acquire(name); err != nil {
		return err
	}
	defer f.builds.release(name)
	return f.runBuild(ctx, name, n)
}

// StartBuild validates and claims a build slot, then builds in the
// background. The slot is held until the build finishes.
func (f *Fleet) StartBuild(name string, n int) error {
	if err := f.builds.acquire(name); err != nil {
		return err
	}
	go func() {
		defer f.builds.release(name)
		ctx, cancel := f.lifeContext()
		defer cancel()
		_ = f.runBuild(ctx, name, n)
	}()
	return nil
}

// runBuild runs a build whose slot the caller holds.
func (f *Fleet) runBuild(ctx context.Context, name string, n int) (err error) {
	start := time.Now()
	status, tag := "failed", ""
	var size int64
	defer func() {
		ms := time.Since(start).Milliseconds()
		if err != nil {
			f.buildLogf(name, n, "build failed: %v", err)
		}
		_ = f.templates.SetBuild(name, n, status, tag, size, ms)
		f.buildLog.Append(buildKey(name, n), map[string]any{"done": true, "status": status})
		f.auditf(AuditEvent{Event: AuditWorkspaceBuild, OwnerID: DefaultOwnerID, Detail: name + "@" + strconv.Itoa(n) + " " + status})
		if status == "ok" {
			f.pools.fillAsync(name, n)
		}
	}()
	_ = f.templates.SetBuild(name, n, "building", "", 0, 0)

	doc, _, err := f.studioDoc(ctx, name, n)
	if err != nil {
		return err
	}
	rt, err := f.runtimeName()
	if err != nil {
		return err
	}
	layers, err := layerDockerfiles(doc)
	if err != nil {
		return err
	}
	out := &lineWriter{fn: func(l string) { f.buildLogf(name, n, "%s", l) }}
	parent := ""
	for _, l := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		tag := layerTag(name, l.Key, l.Hash, parent)
		if _, ierr := f.runRuntime(rt, "image", "inspect", tag); ierr == nil {
			f.buildLogf(name, n, "cached %s", l.Key)
			parent = tag
			continue
		}
		f.buildLogf(name, n, "building %s", l.Key)
		berr := f.buildDerivedStream(rt, tag, l.Dockerfile(parent), out)
		out.flush()
		if berr != nil {
			return fmt.Errorf("layer %s: %w", l.Key, berr)
		}
		parent = tag
	}
	finalTag := "marshal-ws/" + name + ":v" + strconv.Itoa(n)
	f.buildLogf(name, n, "building final")
	berr := f.buildDerivedStream(rt, finalTag, finalDockerfile(parent, name, n), out)
	out.flush()
	if berr != nil {
		return fmt.Errorf("final layer: %w", berr)
	}
	f.buildLogf(name, n, "adding marshal")
	derived, err := f.ensureDerivedImage(ctx, finalTag)
	if err != nil {
		return err
	}
	tag = derived
	if o, serr := f.runRuntime(rt, "image", "inspect", "--format", "{{.Size}}", derived); serr == nil {
		size, _ = strconv.ParseInt(strings.TrimSpace(string(o)), 10, 64)
	}
	status = "ok"
	f.buildLogf(name, n, "built %s", derived)
	return nil
}
