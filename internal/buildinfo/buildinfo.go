// Package buildinfo is a binary's version: the release version stamped into it
// when there is one, else dev+ with the commit it was built from.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// ReadBuildInfo reads the running binary's build info. It is
// debug.ReadBuildInfo; this package's tests replace it to feed the rule a
// build's VCS settings, and a command's tests replace it to pin the version
// that binary reports (a test binary carries none of its own).
var ReadBuildInfo func() (*debug.BuildInfo, bool) = debug.ReadBuildInfo

// revisionLength is how many hex characters of vcs.revision a source build
// names.
const revisionLength = 12

// Version is a binary's version: stamped when not empty, else
// dev+<12 hex of vcs.revision>, with +dirty when vcs.modified is true, else
// dev (decision 7).
func Version(stamped string) string {
	if stamped != "" {
		return stamped
	}

	info, ok := ReadBuildInfo()
	if !ok || info == nil {
		return "dev"
	}

	revision := setting(info, "vcs.revision")
	if revision == "" {
		return "dev"
	}
	if len(revision) > revisionLength {
		revision = revision[:revisionLength]
	}

	version := "dev+" + revision
	if setting(info, "vcs.modified") == "true" {
		version += "+dirty"
	}
	return version
}

// IsSource reports whether v names a source build: dev, or dev+….
func IsSource(v string) bool {
	return v == "dev" || strings.HasPrefix(v, "dev+")
}

// setting returns the value of the named build setting, or the empty string
// when the build carries no such setting.
func setting(info *debug.BuildInfo, key string) string {
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}
