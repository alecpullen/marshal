package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"marshal/internal/buildinfo"
)

// Build metadata, injected at release time via -ldflags -X. GoReleaser sets
// these for every published archive; a plain `go build` leaves them empty and
// versionString falls back to the VCS stamps Go embeds automatically.
var (
	version = ""
	commit  = ""
	date    = ""
)

// versionString renders the one-line version banner printed by --version. Its
// version word is the one run computes for the whole binary (decision 2), so a
// source build names its commit here too; the commit and date after it are
// unchanged.
func versionString() string {
	v, c, d := buildinfo.Version(version), commit, date
	if c == "" || d == "" {
		vcsRev, vcsTime := buildStamps()
		if c == "" {
			c = vcsRev
		}
		if d == "" {
			d = vcsTime
		}
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return fmt.Sprintf("marshal %s (commit %s, built %s, %s, %s/%s)",
		v, c, d, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// buildStamps reads the vcs.revision and vcs.time settings the Go toolchain
// embeds when building from a git checkout.
func buildStamps() (rev, buildTime string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			buildTime = s.Value
		}
	}
	return rev, buildTime
}
