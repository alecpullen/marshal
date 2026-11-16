package buildinfo

import (
	"runtime/debug"
	"testing"
)

// stubBuildInfo makes ReadBuildInfo return info, ok for the duration of the
// test, and restores the real reader afterwards.
func stubBuildInfo(t *testing.T, info *debug.BuildInfo, ok bool) {
	t.Helper()
	previous := ReadBuildInfo
	t.Cleanup(func() { ReadBuildInfo = previous })
	ReadBuildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
}

// settings builds a BuildInfo from alternating key/value pairs.
func settings(pairs ...string) *debug.BuildInfo {
	info := &debug.BuildInfo{}
	for i := 0; i+1 < len(pairs); i += 2 {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: pairs[i], Value: pairs[i+1]})
	}
	return info
}

func TestAStampedVersionWins(t *testing.T) {
	stubBuildInfo(t, settings("vcs.revision", "0123456789abcdef0123456789abcdef01234567", "vcs.modified", "true"), true)

	if got, want := Version("0.9.0"), "0.9.0"; got != want {
		t.Fatalf("Version(\"0.9.0\") = %q, want %q", got, want)
	}
}

func TestASourceBuildNamesItsCommit(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name     string
		revision string
		want     string
	}{
		{name: "forty characters are cut to twelve", revision: revision, want: "dev+0123456789ab"},
		{name: "a short revision is used whole", revision: "abc", want: "dev+abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubBuildInfo(t, settings("vcs.revision", tt.revision), true)

			if got := Version(""); got != tt.want {
				t.Fatalf("Version(\"\") = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAModifiedTreeIsDirty(t *testing.T) {
	stubBuildInfo(t, settings(
		"vcs.revision", "0123456789abcdef0123456789abcdef01234567",
		"vcs.modified", "true",
	), true)

	if got, want := Version(""), "dev+0123456789ab+dirty"; got != want {
		t.Fatalf("Version(\"\") = %q, want %q", got, want)
	}
}

// TestACleanTreeIsNotDirty pins the other half of the +dirty rule: Go stamps
// "false" for a clean tree, and only the exact value "true" may add +dirty.
// Without this case, a rule that appended +dirty for any non-empty
// vcs.modified would pass every other test and mark every clean checkout
// dirty.
func TestACleanTreeIsNotDirty(t *testing.T) {
	stubBuildInfo(t, settings(
		"vcs.revision", "0123456789abcdef0123456789abcdef01234567",
		"vcs.modified", "false",
	), true)

	if got, want := Version(""), "dev+0123456789ab"; got != want {
		t.Fatalf("Version(\"\") = %q, want %q", got, want)
	}
}

func TestNoVCSInfoIsDev(t *testing.T) {
	stubBuildInfo(t, settings(), true)

	if got, want := Version(""), "dev"; got != want {
		t.Fatalf("Version(\"\") = %q, want %q", got, want)
	}
}

func TestNoBuildInfoIsDev(t *testing.T) {
	stubBuildInfo(t, nil, false)

	if got, want := Version(""), "dev"; got != want {
		t.Fatalf("Version(\"\") = %q, want %q", got, want)
	}
}

func TestIsSource(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "dev", want: true},
		{version: "dev+abc", want: true},
		{version: "dev+abc+dirty", want: true},
		{version: "0.9.0", want: false},
		{version: "v1.2.3-rc1", want: false},
		{version: "", want: false},
	}

	for _, tt := range tests {
		if got := IsSource(tt.version); got != tt.want {
			t.Errorf("IsSource(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}
