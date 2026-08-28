package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// derivedTagPrefix is the repository-local tag prefix for derived images.
// The full tag is derivedTagPrefix + a short hash of the base digest and
// marshal version, so an upstream push of the base invalidates the cache.
const derivedTagPrefix = "marshal-derived-"

// derivedDockerfile emits a two-line Dockerfile that copies the marshal
// binary out of the agent image into the declared base. The base carries
// the project's toolchain; the agent image supplies marshal itself.
func derivedDockerfile(base, agentImage string) string {
	return "FROM " + base + "\n" +
		"COPY --from=" + agentImage + " /usr/local/bin/marshal /usr/local/bin/marshal\n"
}

// derivedTag hashes the base digest plus the marshal version to produce a
// deterministic, collision-free tag. It keys on the base DIGEST, not the
// tag, so an upstream push of the base invalidates the derived image —
// keying on the tag would silently serve a stale derivation.
func derivedTag(baseDigest, version string) string {
	sum := sha256.Sum256([]byte(baseDigest + ":" + version))
	return derivedTagPrefix + hex.EncodeToString(sum[:])[:16]
}

// runRuntime executes one container-runtime command through the seam.
// f.runner is nil in production (use exec.Command); tests inject a fake.
func (f *Fleet) runRuntime(runtime string, args ...string) ([]byte, error) {
	if f.runner != nil {
		return f.runner(runtime, args...)
	}
	cmd := exec.Command(runtime, args...)
	cmd.Env = clientEnv()
	return cmd.CombinedOutput()
}

// buildDerived runs `docker build` feeding the Dockerfile on stdin, so no
// temporary build context is written to disk. The build context is the
// current directory; the Dockerfile only does `FROM <base>` and
// `COPY --from=<agentImage>`, so no context files are needed. The runner
// seam cannot carry stdin, so the production path (nil runner) uses
// exec.Command directly.
func (f *Fleet) buildDerived(runtime, tag, base, dockerfile string) error {
	if f.runner != nil {
		_, err := f.runner(runtime, "build", "-t", tag, "-f", "-", ".")
		return err
	}
	cmd := exec.Command(runtime, "build", "-t", tag, "-f", "-", ".")
	cmd.Env = clientEnv()
	cmd.Stdin = strings.NewReader(dockerfile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (%s)", err, out)
	}
	return nil
}

// baseDigest resolves a base image reference to its content digest via
// `docker inspect`. The digest, not the tag, keys the derived image so an
// upstream push invalidates the cache.
func (f *Fleet) baseDigest(runtime, base string) (string, error) {
	out, err := f.runRuntime(runtime, "inspect", "--format", "{{index .RepoDigests 0}}", base)
	if err != nil {
		return "", fmt.Errorf("bridge: resolve digest of %s: %w (%s)", base, err, out)
	}
	digest := strings.TrimSpace(string(out))
	// RepoDigests look like "repo@sha256:..."; keep only the digest part.
	if i := strings.LastIndex(digest, "@"); i >= 0 {
		digest = digest[i+1:]
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("bridge: unexpected digest for %s: %q", base, digest)
	}
	return digest, nil
}

// ensureDerivedImage returns a tag for an image that carries marshal on
// top of the declared base, building it only when absent. A build failure
// returns an error and no image — falling back to the default would run
// the agent in an environment the repo did not ask for.
func (f *Fleet) ensureDerivedImage(ctx context.Context, base string) (string, error) {
	var runtime string
	if f.runner != nil {
		runtime = "docker" // test mode: fake runner intercepts all commands
	} else {
		_, name, ok := detectedRuntime()
		if !ok {
			return "", fmt.Errorf("bridge: no container runtime to derive %s", base)
		}
		runtime = name
	}

	digest, err := f.baseDigest(runtime, base)
	if err != nil {
		return "", err
	}
	tag := derivedTag(digest, f.buildVersion)

	// The derived image exists when `image inspect` succeeds. Any other
	// outcome means it is absent and must be built.
	if _, err := f.runRuntime(runtime, "image", "inspect", tag); err == nil {
		return tag, nil
	}

	agentImage := defaultAgentImageFor(f.buildVersion)
	if err := f.buildDerived(runtime, tag, base, derivedDockerfile(base, agentImage)); err != nil {
		return "", fmt.Errorf("bridge: derive %s: %w", base, err)
	}
	return tag, nil
}
