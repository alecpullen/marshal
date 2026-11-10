package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvMarker precedes the environment that `webbridge print-env` writes.
const EnvMarker = "\x00marshal-env\x00"

const (
	envStepTimeout   = 10 * time.Second
	envOutputCap     = 4 << 20
	envKillWaitDelay = time.Second
)

// envProjectFiles are the project files whose change invalidates a
// resolution (P4 D7). They are stat'ed whatever the shell.
var envProjectFiles = []string{".envrc", ".tool-versions", "mise.toml", ".mise.toml", ".nvmrc"}

// envHomeFiles are the login shell's startup files: a fixed list, stat'ed
// whatever the shell (P4 D7).
var envHomeFiles = []string{
	".profile", ".bash_profile", ".bash_login", ".bashrc",
	".zshenv", ".zprofile", ".zshrc", ".zlogin",
	filepath.Join(".config", "fish", "config.fish"),
}

// envLocationVars are the bridge's own directory settings. A child must
// resolve the directories the bridge does (decision 4).
var envLocationVars = []string{
	"MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
}

var (
	// errEnvStepTimeout reports a step that ran past its bound.
	errEnvStepTimeout = errors.New("bridge: environment step timed out")
	// errEnvStepOversize reports a step whose output passed envOutputCap.
	errEnvStepOversize = errors.New("bridge: environment step output is over the cap")
)

// WriteEnv writes EnvMarker and then each entry of environ followed by a NUL.
func WriteEnv(w io.Writer, environ []string) error {
	var buf bytes.Buffer
	buf.WriteString(EnvMarker)
	for _, entry := range environ {
		buf.WriteString(entry)
		buf.WriteByte(0)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// parseEnv returns the entries after the last EnvMarker in out; an error when
// out holds no marker.
func parseEnv(out []byte) ([]string, error) {
	at := bytes.LastIndex(out, []byte(EnvMarker))
	if at < 0 {
		return nil, errors.New("bridge: the printer's output holds no environment marker")
	}
	fields := strings.Split(string(out[at+len(EnvMarker):]), "\x00")
	env := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" || !strings.Contains(field, "=") {
			continue
		}
		env = append(env, field)
	}
	return env, nil
}

// ProjectEnv resolves a project's environment for its child (P4 D7). The
// zero values of its fields are the production defaults.
type ProjectEnv struct {
	PrintEnv []string      // argv that prints the environment: the webbridge and "print-env"
	Shell    string        // the login shell; empty means Base's SHELL, else /bin/sh
	Base     []string      // the bridge's environment: the shell starts from it and a fallback returns it; nil means os.Environ()
	Home     string        // whose startup files are fingerprinted; empty means Base's HOME
	Timeout  time.Duration // each step's bound; zero means envStepTimeout

	mu    sync.Mutex
	cache map[string]envCacheEntry
}

// envCacheEntry is one cached resolution: the fingerprint it was made at.
type envCacheEntry struct {
	fingerprint string
	env         []string
}

// NewProjectEnv is a resolver that prints with printEnv.
func NewProjectEnv(printEnv []string) *ProjectEnv {
	return &ProjectEnv{PrintEnv: printEnv}
}

// Resolve returns root's environment, and the reason when it fell back.
func (p *ProjectEnv) Resolve(ctx context.Context, root string) (env []string, warning string) {
	base := p.base()
	fingerprint := envFingerprint(root, p.home(base))

	p.mu.Lock()
	entry, cached := p.cache[root]
	p.mu.Unlock()
	if cached && entry.fingerprint == fingerprint {
		return entry.env, ""
	}

	env, warning = p.resolve(ctx, root, base)
	if warning != "" {
		// Only a clean resolution is cached, so a failure is retried at the
		// next start (decision 5).
		slog.Default().Warn("webbridge: project environment fell back", "root", root, "warning", warning)
		return env, warning
	}

	p.mu.Lock()
	if p.cache == nil {
		p.cache = make(map[string]envCacheEntry)
	}
	p.cache[root] = envCacheEntry{fingerprint: fingerprint, env: env}
	p.mu.Unlock()
	slog.Default().Info("webbridge: resolved project environment", "root", root, "variables", len(env))
	return env, ""
}

// resolve runs the login shell, then direnv, then the bridge's own location
// variables. It returns base for a login shell that failed, and the login
// environment for an overlay that failed (decision 5).
func (p *ProjectEnv) resolve(ctx context.Context, root string, base []string) ([]string, string) {
	timeout := p.timeout()
	command, ok := printCommand(p.PrintEnv)
	if !ok {
		return base, "the webbridge path cannot be quoted for the login shell"
	}

	argv := []string{p.shell(base), "-l", "-c", command}
	out, err := runEnvStep(ctx, timeout, root, base, argv)
	if err != nil {
		return base, loginShellWarning(err, timeout)
	}
	env, err := parseEnv(out)
	if err != nil {
		return base, "the login shell printed no environment"
	}

	env, warning := applyDirenv(ctx, timeout, root, env)
	return overlayLocationVars(env, base), warning
}

func (p *ProjectEnv) base() []string {
	if p.Base != nil {
		return p.Base
	}
	return os.Environ()
}

func (p *ProjectEnv) shell(base []string) string {
	if p.Shell != "" {
		return p.Shell
	}
	if shell, ok := envLookup(base, "SHELL"); ok && shell != "" {
		return shell
	}
	return "/bin/sh"
}

func (p *ProjectEnv) home(base []string) string {
	if p.Home != "" {
		return p.Home
	}
	home, _ := envLookup(base, "HOME")
	return home
}

func (p *ProjectEnv) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return envStepTimeout
}

// envFingerprint is a resolution's cache key: every project and startup file
// by path and modification time, or "absent" (P4 D7). A file appearing or
// disappearing changes it as a changed time does.
func envFingerprint(root, home string) string {
	var b strings.Builder
	stat := func(path string) {
		b.WriteString(path)
		b.WriteByte('=')
		if fi, err := os.Stat(path); err == nil {
			b.WriteString(strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		} else {
			b.WriteString("absent")
		}
		b.WriteByte('\n')
	}
	for _, name := range envProjectFiles {
		stat(filepath.Join(root, name))
	}
	for _, name := range envHomeFiles {
		if home == "" {
			b.WriteString(name)
			b.WriteString("=absent\n")
			continue
		}
		stat(filepath.Join(home, name))
	}
	return b.String()
}

// printCommand builds the login shell's command: exec followed by each
// PrintEnv element in single quotes (decision 1). A path holding a single
// quote cannot be quoted for every shell, so it is a fallback, not a guess.
func printCommand(printEnv []string) (string, bool) {
	if len(printEnv) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString("exec")
	for _, arg := range printEnv {
		if strings.Contains(arg, "'") {
			return "", false
		}
		b.WriteString(" '")
		b.WriteString(arg)
		b.WriteByte('\'')
	}
	return b.String(), true
}

// loginShellWarning names how the login shell failed, never what it printed
// (decision 6).
func loginShellWarning(err error, timeout time.Duration) string {
	var exit *exec.ExitError
	switch {
	case errors.Is(err, errEnvStepTimeout):
		return fmt.Sprintf("the login shell timed out after %s", timeout)
	case errors.Is(err, errEnvStepOversize):
		return fmt.Sprintf("the login shell printed more than %d MiB", envOutputCap>>20)
	case errors.As(err, &exit):
		return fmt.Sprintf("the login shell exited with status %d", exit.ExitCode())
	default:
		return "the login shell could not start"
	}
}

// applyDirenv overlays the project's direnv export (decision 3). No .envrc, or
// no direnv on the resolved PATH, means nothing to overlay and no warning.
func applyDirenv(ctx context.Context, timeout time.Duration, root string, env []string) ([]string, string) {
	if _, err := os.Stat(filepath.Join(root, ".envrc")); err != nil {
		return env, ""
	}
	bin, ok := findExecutable("direnv", envValue(env, "PATH"))
	if !ok {
		return env, ""
	}

	out, err := runEnvStep(ctx, timeout, root, env, []string{bin, "export", "json"})
	if err != nil {
		return env, direnvWarning(err, timeout)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return env, ""
	}

	var overlay map[string]*string
	if err := json.Unmarshal(out, &overlay); err != nil {
		return env, "direnv export printed something other than JSON"
	}
	return applyEnvOverlay(env, overlay), ""
}

// direnvWarning names how the overlay failed, never what it printed.
func direnvWarning(err error, timeout time.Duration) string {
	var exit *exec.ExitError
	switch {
	case errors.Is(err, errEnvStepTimeout):
		return fmt.Sprintf("direnv export timed out after %s", timeout)
	case errors.As(err, &exit):
		return fmt.Sprintf("direnv export exited with status %d", exit.ExitCode())
	default:
		return "direnv export printed something other than JSON"
	}
}

// findExecutable searches pathValue's directories in order for an executable
// regular file called name.
func findExecutable(name, pathValue string) (string, bool) {
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		fi, err := os.Stat(candidate)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, true
	}
	return "", false
}

// applyEnvOverlay applies direnv's JSON: a string sets a variable, null unsets
// it.
func applyEnvOverlay(env []string, overlay map[string]*string) []string {
	out := make([]string, 0, len(env)+len(overlay))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		value, overlaid := overlay[key]
		if !ok || !overlaid {
			out = append(out, entry)
			continue
		}
		delete(overlay, key)
		if value != nil {
			out = append(out, key+"="+*value)
		}
	}

	keys := make([]string, 0, len(overlay))
	for key := range overlay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := overlay[key]; value != nil {
			out = append(out, key+"="+*value)
		}
	}
	return out
}

// overlayLocationVars makes the bridge's directory settings win over whatever
// the login shell exported (decision 4).
func overlayLocationVars(env, base []string) []string {
	for _, key := range envLocationVars {
		value, ok := envLookup(base, key)
		if !ok {
			continue
		}
		env = envSet(env, key, value)
	}
	return env
}

// envLookup finds key in env, last match first, as a shell would.
func envLookup(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// envValue is envLookup's value alone, empty when env does not hold key.
func envValue(env []string, key string) string {
	value, _ := envLookup(env, key)
	return value
}

// envSet replaces key's last entry in env, or appends one.
func envSet(env []string, key, value string) []string {
	at := -1
	for i, entry := range env {
		if k, _, ok := strings.Cut(entry, "="); ok && k == key {
			at = i
		}
	}
	if at < 0 {
		return append(env, key+"="+value)
	}
	env[at] = key + "=" + value
	return env
}

// runEnvStep runs argv in dir with env for at most timeout, capturing its
// standard output up to envOutputCap and discarding its standard error. Its
// errors let Resolve tell a timeout, an exit status and an oversize output
// apart.
func runEnvStep(ctx context.Context, timeout time.Duration, dir string, env, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("bridge: an environment step needs a command")
	}

	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(stepCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	// A warning never carries a step's output, so standard error is dropped.
	cmd.Stderr = nil
	out := &envStepOutput{max: envOutputCap}
	cmd.Stdout = out
	configureEnvStep(cmd)

	err := cmd.Run()
	if out.overflow {
		return nil, errEnvStepOversize
	}
	if err != nil {
		if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			return nil, errEnvStepTimeout
		}
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// envStepOutput captures a step's standard output up to max bytes. It refuses
// the write that would pass the cap, which the step's process sees as a
// closed pipe.
type envStepOutput struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (o *envStepOutput) Write(p []byte) (int, error) {
	if room := o.max - o.buf.Len(); len(p) > room {
		if room > 0 {
			o.buf.Write(p[:room])
		}
		o.overflow = true
		return 0, errEnvStepOversize
	}
	return o.buf.Write(p)
}
