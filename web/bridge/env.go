package bridge

import (
	"fmt"
	"os"
	"strings"
)

// clientRuntimeVars are passed to the docker/podman CLI itself. They
// affect only how the CLI reaches its daemon — never the agent — so
// inheriting them is safe and necessary: without DOCKER_HOST a remote
// or rootless daemon is unreachable.
//
// This mirrors internal/sandbox/container.go's buildContainerEnv, which
// web/ cannot import because it must stay stdlib-only.
var clientRuntimeVars = []string{
	"DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
	"DOCKER_CONFIG", "DOCKER_BUILDKIT", "DOCKER_CONTEXT",
	"CONTAINER_HOST", "CONTAINER_SSHKEY", "XDG_RUNTIME_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	"HOME", "PATH",
}

// providerKeySuffixes identify credentials an agent legitimately needs.
// Only variables matching one of these are handed to a container, so an
// operator's unrelated host secrets stay on the host.
var providerKeySuffixes = []string{"_API_KEY", "_API_TOKEN", "_AUTH_TOKEN"}

// providerKeyExact covers provider variables that match no suffix rule.
var providerKeyExact = []string{
	"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OLLAMA_HOST",
	"MARSHAL_ACP_LOG_LEVEL",
}

// clientEnv builds the environment for the container-runtime CLI.
// HOME and PATH are included because the CLI needs them to find its
// config and helper binaries.
func clientEnv() []string {
	var env []string
	for _, key := range clientRuntimeVars {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// InheritedAgentEnv collects provider credentials from the bridge's own
// environment to hand to agents.
//
// This is explicit supply, not ambient inheritance: only recognised
// provider variables cross the boundary, and the S2 credential broker
// replaces this wholesale with per-owner, per-repo resolution.
func InheritedAgentEnv() map[string]string {
	out := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		if isProviderKey(key) {
			out[key] = value
		}
	}
	return out
}

func isProviderKey(key string) bool {
	for _, exact := range providerKeyExact {
		if key == exact {
			return true
		}
	}
	for _, suffix := range providerKeySuffixes {
		if strings.HasSuffix(key, suffix) {
			// AWS_SECRET_ACCESS_KEY and friends are cloud credentials,
			// not model-provider credentials.
			if strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GOOGLE_") {
				return false
			}
			return true
		}
	}
	return false
}

// ParseAgentEnv turns repeatable KEY=VALUE flag values into a map.
func ParseAgentEnv(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		key, value, ok := strings.Cut(p, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("bridge: --agent-env %q is not KEY=VALUE", p)
		}
		out[key] = value
	}
	return out, nil
}
