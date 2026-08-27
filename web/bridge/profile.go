package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultAgentImage is the fallback agent image. It must carry a C
// toolchain: marshal needs CGO_ENABLED=1 for the tree-sitter dependency
// used by Go symbol extraction, so a scratch or distroless base cannot
// run an agent.
const defaultAgentImage = "ghcr.io/marshal/agent:latest"

// Default resource caps. Zero would mean unlimited, which lets one
// runaway test suite starve the whole fleet.
const (
	defaultAgentCPUs     = 2.0
	defaultAgentMemoryMB = 4096
)

// RuntimeProfile is the container shape for one agent: which image and
// how much of the machine it may use.
type RuntimeProfile struct {
	Image    string  `json:"image,omitempty"`
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int     `json:"memoryMb,omitempty"`
}

// DefaultRuntimeProfile is the profile used when a project declares
// nothing and the operator overrides nothing.
func DefaultRuntimeProfile() RuntimeProfile {
	return RuntimeProfile{
		Image:    defaultAgentImage,
		CPUs:     defaultAgentCPUs,
		MemoryMB: defaultAgentMemoryMB,
	}
}

// devcontainer is the subset of devcontainer.json we understand.
type devcontainer struct {
	Image string          `json:"image"`
	Build json.RawMessage `json:"build"`
}

// ResolveProfile decides the runtime profile for a project directory.
//
// Precedence: an explicit override field wins; else the project's
// .devcontainer/devcontainer.json "image"; else the default. Unset
// fields in override fall through rather than zeroing the result.
//
// Only devcontainer's "image" is honoured. A devcontainer that builds
// from a Dockerfile or declares features falls back to the default with
// a reason — full devcontainer support is out of scope.
func ResolveProfile(projectDir string, override RuntimeProfile) (RuntimeProfile, string) {
	out := DefaultRuntimeProfile()
	reason := "default image"

	if img, why, ok := devcontainerImage(projectDir); ok {
		out.Image = img
		reason = why
	} else if why != "" {
		reason = why
	}

	if strings.TrimSpace(override.Image) != "" {
		out.Image = override.Image
		reason = "operator override"
	}
	if override.CPUs > 0 {
		out.CPUs = override.CPUs
	}
	if override.MemoryMB > 0 {
		out.MemoryMB = override.MemoryMB
	}
	return out, reason
}

// devcontainerImage reads .devcontainer/devcontainer.json. The bool is
// false when no usable image was found; the string always explains what
// happened, so the UI can tell the operator why they got the default.
func devcontainerImage(projectDir string) (image, reason string, ok bool) {
	path := filepath.Join(projectDir, ".devcontainer", "devcontainer.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}

	var dc devcontainer
	if err := json.Unmarshal(data, &dc); err != nil {
		// devcontainer.json is conventionally JSONC. Retry without
		// line comments before giving up.
		if err2 := json.Unmarshal(stripLineComments(data), &dc); err2 != nil {
			return "", "devcontainer.json is not parseable; using default image", false
		}
	}
	if strings.TrimSpace(dc.Image) != "" {
		return dc.Image, "devcontainer.json image", true
	}
	if len(dc.Build) > 0 {
		return "", "devcontainer.json builds from a Dockerfile, which is unsupported; using default image", false
	}
	return "", "devcontainer.json declares no image; using default image", false
}

// stripLineComments removes // comments that start outside a string
// literal. It handles the common JSONC case; block comments and
// trailing commas are not supported and fall through to the default.
func stripLineComments(data []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case !inString && ch == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
			continue
		}
		out = append(out, ch)
	}
	return out
}

// String renders the profile for logs and the fleet UI.
func (p RuntimeProfile) String() string {
	return fmt.Sprintf("%s (%.3g cpu, %d MiB)", p.Image, p.CPUs, p.MemoryMB)
}
