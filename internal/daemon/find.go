package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FindWebbridge is the webbridge beside the running marshal, else the one
// on PATH; override wins when set.
func FindWebbridge(override string) (string, error) {
	if override != "" {
		if err := executable(override); err != nil {
			return "", fmt.Errorf("webbridge %s: %w", override, err)
		}
		return override, nil
	}

	self, selfErr := os.Executable()
	sibling := ""
	if selfErr == nil {
		sibling, found := webbridgeBeside(self)
		if found {
			return sibling, nil
		}
	}

	if onPath, err := exec.LookPath("webbridge"); err == nil {
		return onPath, nil
	}

	looked := "PATH"
	if sibling != "" {
		looked = sibling + " or PATH"
	}
	return "", fmt.Errorf("no webbridge found: looked in %s; build it with `go build ./cmd/webbridge`", looked)
}

// webbridgeBeside is the webbridge in exe's directory. A symlinked marshal
// (a bin dir on PATH, say) means the real binary's directory: that is
// where webbridge is installed next to it. It returns the path it looked
// at whether or not it found one.
func webbridgeBeside(exe string) (string, bool) {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	path := filepath.Join(filepath.Dir(exe), "webbridge")
	if executable(path) != nil {
		return path, false
	}
	return path, true
}

// executable reports why path cannot be run as a program, or nil.
func executable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}
