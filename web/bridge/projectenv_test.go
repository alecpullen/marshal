package bridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// p6PrinterScript stands in for `webbridge print-env`: it writes the marker
// and then `/usr/bin/env -0`'s output, so whatever a startup file prints
// before the marker is skipped.
const p6PrinterScript = `#!/bin/sh
printf '\000marshal-env\000'
exec /usr/bin/env -0
`

// p6DirenvOverlayScript sets a variable, unsets another and keeps a third.
const p6DirenvOverlayScript = `#!/bin/sh
printf '%s' '{"P6_DIRENV":"on","P6_GOING":null,"P6_LOGIN":"yes"}'
`

// p6Fixture is a project root, a home with a .profile, and a resolver over
// them. Shell is /bin/sh and Base holds HOME (the fixture's home) and a PATH
// of /usr/bin:/bin, as the plan asks.
type p6Fixture struct {
	t       *testing.T
	dir     string
	home    string
	root    string
	printer string
	res     *ProjectEnv
}

func newP6Fixture(t *testing.T, profile string) *p6Fixture {
	t.Helper()

	dir := p6Dir(t)
	f := &p6Fixture{
		t:    t,
		dir:  dir,
		home: filepath.Join(dir, "home"),
		root: filepath.Join(dir, "root"),
	}
	for _, d := range []string{f.home, f.root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("make %s: %v", d, err)
		}
	}
	f.printer = p6Script(t, dir, "printer", p6PrinterScript)
	f.setProfile(profile)
	f.res = &ProjectEnv{
		PrintEnv: []string{f.printer},
		Shell:    "/bin/sh",
		Base:     []string{"HOME=" + f.home, "PATH=/usr/bin:/bin"},
		Timeout:  10 * time.Second,
	}
	return f
}

// setProfile writes the home's .profile.
func (f *p6Fixture) setProfile(body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.home, ".profile"), []byte(body), 0o644); err != nil {
		f.t.Fatalf("write .profile: %v", err)
	}
}

// direnv writes an executable direnv into a directory of its own and returns
// that directory, for a .profile to export on PATH.
func (f *p6Fixture) direnv(body string) string {
	f.t.Helper()
	bin := filepath.Join(f.dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		f.t.Fatalf("make direnv dir: %v", err)
	}
	p6Script(f.t, bin, "direnv", body)
	return bin
}

// noPath is an empty PATH, so no direnv is found on the resolved one.
func (f *p6Fixture) noPath() string {
	f.t.Helper()
	dir := filepath.Join(f.dir, "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("make empty dir: %v", err)
	}
	return dir
}

// envrc puts an .envrc in the project root.
func (f *p6Fixture) envrc() string {
	f.t.Helper()
	path := filepath.Join(f.root, ".envrc")
	if err := os.WriteFile(path, []byte("export P6_ENVRC=1\n"), 0o644); err != nil {
		f.t.Fatalf("write .envrc: %v", err)
	}
	return path
}

func (f *p6Fixture) resolve() ([]string, string) {
	f.t.Helper()
	return f.res.Resolve(context.Background(), f.root)
}

// p6Dir is a short temporary directory: a script's path ends up in the login
// shell's command line and in HOME.
func p6Dir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "p6")
	if err != nil {
		t.Fatalf("make temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// p6Script writes an executable script called name in dir and returns its
// path.
func p6Script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// p6Value reads key out of an environment.
func p6Value(env []string, key string) (string, bool) {
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// p6Lines counts the lines a file holds, zero when it is absent.
func p6Lines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func TestWriteEnvRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("a banner a startup file printed\nand a second line\n")

	environ := []string{"P6_ONE=1", "P6_MULTILINE=two\nlines", "P6_EMPTY="}
	if err := WriteEnv(&buf, environ); err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}

	got, err := parseEnv(buf.Bytes())
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}
	if !slices.Equal(got, environ) {
		t.Fatalf("parseEnv = %q, want %q", got, environ)
	}

	if _, err := parseEnv([]byte("a banner and no marker at all")); err == nil {
		t.Fatal("parseEnv of output with no marker succeeded")
	}
	if got, err := parseEnv([]byte("P6=X\x00" + EnvMarker + "\x00only=last\x00\x00noequals")); err != nil {
		t.Fatalf("parseEnv: %v", err)
	} else if !slices.Equal(got, []string{"only=last"}) {
		t.Fatalf("parseEnv read before the last marker: %q", got)
	}
}

func TestTheLoginShellsExportsReachTheEnvironment(t *testing.T) {
	f := newP6Fixture(t, "echo p6-banner\nexport P6_LOGIN=yes\n")

	env, warning := f.resolve()
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if got, ok := p6Value(env, "P6_LOGIN"); !ok || got != "yes" {
		t.Fatalf("P6_LOGIN = %q (set %t), want yes", got, ok)
	}
	if got, ok := p6Value(env, "HOME"); !ok || got != f.home {
		t.Fatalf("HOME = %q (set %t), want %q", got, ok, f.home)
	}
}

func TestDirenvOverlaysTheLoginEnvironment(t *testing.T) {
	f := newP6Fixture(t, "")
	bin := f.direnv(p6DirenvOverlayScript)
	f.setProfile(fmt.Sprintf("export PATH=\"%s\"\nexport P6_GOING=away\nexport P6_LOGIN=yes\n", bin))
	f.envrc()

	env, warning := f.resolve()
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if got, ok := p6Value(env, "P6_DIRENV"); !ok || got != "on" {
		t.Fatalf("P6_DIRENV = %q (set %t), want on", got, ok)
	}
	if got, ok := p6Value(env, "P6_GOING"); ok {
		t.Fatalf("P6_GOING = %q, want it unset", got)
	}
	if got, ok := p6Value(env, "P6_LOGIN"); !ok || got != "yes" {
		t.Fatalf("P6_LOGIN = %q (set %t), want yes", got, ok)
	}
}

func TestNoDirenvMeansNoOverlayAndNoWarning(t *testing.T) {
	f := newP6Fixture(t, "")
	f.setProfile(fmt.Sprintf("export PATH=\"%s\"\nexport P6_LOGIN=yes\n", f.noPath()))
	f.envrc()

	env, warning := f.resolve()
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if got, ok := p6Value(env, "P6_LOGIN"); !ok || got != "yes" {
		t.Fatalf("P6_LOGIN = %q (set %t), want yes", got, ok)
	}
}

func TestATimedOutShellFallsBack(t *testing.T) {
	// The profile sleeps past the step's bound and leaves a background sleep
	// holding the step's output open, so only a process-group kill ends it.
	f := newP6Fixture(t, "sleep 5 &\nsleep 5\n")
	f.res.Timeout = 300 * time.Millisecond

	start := time.Now()
	env, warning := f.resolve()
	elapsed := time.Since(start)

	if !strings.HasPrefix(warning, "the login shell timed out after ") {
		t.Fatalf("warning = %q, want a timeout", warning)
	}
	if !slices.Equal(env, f.res.Base) {
		t.Fatalf("env = %q, want Base %q", env, f.res.Base)
	}
	if limit := f.res.Timeout + 2*time.Second; elapsed > limit {
		t.Fatalf("resolve took %s, want under %s", elapsed, limit)
	}
}

func TestAFailingShellFallsBack(t *testing.T) {
	f := newP6Fixture(t, "echo p6-secret-banner\nexit 3\n")

	env, warning := f.resolve()
	if !strings.Contains(warning, "status 3") {
		t.Fatalf("warning = %q, want it to name status 3", warning)
	}
	if strings.Contains(warning, "p6-secret-banner") {
		t.Fatalf("warning carries the profile's output: %q", warning)
	}
	if !slices.Equal(env, f.res.Base) {
		t.Fatalf("env = %q, want Base %q", env, f.res.Base)
	}
}

func TestAFailedOverlayKeepsTheLoginEnvironment(t *testing.T) {
	f := newP6Fixture(t, "")
	bin := f.direnv("#!/bin/sh\nexit 1\n")
	f.setProfile(fmt.Sprintf("export PATH=\"%s\"\nexport P6_LOGIN=yes\necho run >> \"$HOME/runs\"\n", bin))
	f.envrc()

	env, warning := f.resolve()
	if warning != "direnv export exited with status 1" {
		t.Fatalf("warning = %q, want the direnv exit status", warning)
	}
	if got, ok := p6Value(env, "P6_LOGIN"); !ok || got != "yes" {
		t.Fatalf("P6_LOGIN = %q (set %t), want the login environment kept", got, ok)
	}

	// The failed overlay is not cached, so the next resolve runs the shell
	// again.
	if _, warning := f.resolve(); warning == "" {
		t.Fatal("the second resolve reported no warning")
	}
	if got := p6Lines(t, filepath.Join(f.home, "runs")); got != 2 {
		t.Fatalf("the login shell ran %d times, want 2", got)
	}
}

func TestTheLocationVariablesStayTheBridges(t *testing.T) {
	f := newP6Fixture(t, "export MARSHAL_DATA_DIR=/login/data\nexport P6_LOGIN=yes\n")
	f.res.Base = append(f.res.Base, "MARSHAL_DATA_DIR=/bridge/data")

	env, warning := f.resolve()
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if got, _ := p6Value(env, "MARSHAL_DATA_DIR"); got != "/bridge/data" {
		t.Fatalf("MARSHAL_DATA_DIR = %q, want the bridge's", got)
	}
	if got, ok := p6Value(env, "P6_LOGIN"); !ok || got != "yes" {
		t.Fatalf("P6_LOGIN = %q (set %t), want yes", got, ok)
	}
}

func TestAChangedEnvrcResolvesAgain(t *testing.T) {
	f := newP6Fixture(t, "")
	f.setProfile(fmt.Sprintf("export PATH=\"%s\"\necho run >> \"$HOME/runs\"\n", f.noPath()))
	envrc := f.envrc()
	runs := filepath.Join(f.home, "runs")

	for i := 0; i < 2; i++ {
		if _, warning := f.resolve(); warning != "" {
			t.Fatalf("resolve %d: warning = %q, want none", i, warning)
		}
	}
	if got := p6Lines(t, runs); got != 1 {
		t.Fatalf("the login shell ran %d times, want 1", got)
	}

	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(envrc, later, later); err != nil {
		t.Fatalf("touch .envrc: %v", err)
	}
	if _, warning := f.resolve(); warning != "" {
		t.Fatalf("resolve after the .envrc changed: warning = %q, want none", warning)
	}
	if got := p6Lines(t, runs); got != 2 {
		t.Fatalf("a changed .envrc ran the login shell %d times, want 2", got)
	}

	if err := os.WriteFile(filepath.Join(f.root, ".nvmrc"), []byte("20\n"), 0o644); err != nil {
		t.Fatalf("write .nvmrc: %v", err)
	}
	if _, warning := f.resolve(); warning != "" {
		t.Fatalf("resolve after .nvmrc appeared: warning = %q, want none", warning)
	}
	if got := p6Lines(t, runs); got != 3 {
		t.Fatalf("a new .nvmrc ran the login shell %d times, want 3", got)
	}
}

func TestAFallbackIsNotCached(t *testing.T) {
	f := newP6Fixture(t, "if [ ! -f \"$HOME/fixed\" ]; then exit 3; fi\nexport P6_FIXED=yes\n")

	if _, warning := f.resolve(); !strings.Contains(warning, "status 3") {
		t.Fatalf("warning = %q, want it to name status 3", warning)
	}

	// Fixing the profile without touching a fingerprinted file's time must
	// still resolve cleanly: the fallback was never cached.
	if err := os.WriteFile(filepath.Join(f.home, "fixed"), nil, 0o644); err != nil {
		t.Fatalf("write the fix: %v", err)
	}
	env, warning := f.resolve()
	if warning != "" {
		t.Fatalf("warning = %q, want none after the fix", warning)
	}
	if got, ok := p6Value(env, "P6_FIXED"); !ok || got != "yes" {
		t.Fatalf("P6_FIXED = %q (set %t), want yes", got, ok)
	}
}
