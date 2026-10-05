package bridge

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// toolchainVersionRe bounds a toolchain version so it is safe to place in
// a Dockerfile RUN line.
var toolchainVersionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.\-]*$`)

// toolchainInstaller returns the ENV lines and the RUN body that install
// one toolchain version on a Debian-based image (spec §5.5).
type toolchainInstaller func(version string) (envLines []string, run string)

// toolchainInstallers is the installer table, keyed by language.
var toolchainInstallers = map[string]toolchainInstaller{
	"go": func(v string) ([]string, string) {
		return []string{"PATH=/usr/local/go/bin:$PATH"},
			`ARCH="$(dpkg --print-architecture)" && curl -fsSL "https://go.dev/dl/go` + v + `.linux-${ARCH}.tar.gz" | tar -C /usr/local -xz`
	},
	"node": func(v string) ([]string, string) {
		major := v
		if i := strings.Index(v, "."); i >= 0 {
			major = v[:i]
		}
		dist := "https://nodejs.org/dist/latest-v" + major + ".x"
		return nil,
			`ARCH="$(dpkg --print-architecture | sed 's/amd64/x64/')" && ` +
				`FILE="$(curl -fsSL ` + dist + `/SHASUMS256.txt | grep -o "node-v[0-9.]*-linux-${ARCH}.tar.xz" | head -n 1)" && ` +
				`test -n "$FILE" && curl -fsSL ` + dist + `/"$FILE" | tar -C /usr/local --strip-components=1 -xJ`
	},
	"python": func(v string) ([]string, string) {
		return []string{"UV_PYTHON_INSTALL_DIR=/opt/uv-python"},
			`curl -fsSL https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh && uv python install ` + v
	},
	"rust": func(v string) ([]string, string) {
		return []string{"RUSTUP_HOME=/usr/local/rustup", "CARGO_HOME=/usr/local/cargo", "PATH=/usr/local/cargo/bin:$PATH"},
			`curl --proto '=https' --tlsv1.2 -fsSL https://sh.rustup.rs | sh -s -- -y --no-modify-path --profile minimal --default-toolchain ` + v
	},
}

// toolchainInstall resolves "<lang>@<version>" to its installer output.
func toolchainInstall(spec string) (envLines []string, run string, err error) {
	lang, version, ok := strings.Cut(spec, "@")
	if !ok || version == "" {
		return nil, "", fmt.Errorf("bridge: toolchain %q must be <lang>@<version>", spec)
	}
	inst, ok := toolchainInstallers[lang]
	if !ok {
		langs := make([]string, 0, len(toolchainInstallers))
		for l := range toolchainInstallers {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		return nil, "", fmt.Errorf("bridge: unknown toolchain language %q (supported: %s)", lang, strings.Join(langs, ", "))
	}
	if !toolchainVersionRe.MatchString(version) {
		return nil, "", fmt.Errorf("bridge: invalid toolchain version %q", version)
	}
	envLines, run = inst(version)
	return envLines, run, nil
}
