package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	// ErrSecretNotFound is returned when a ref holds no value.
	ErrSecretNotFound = errors.New("bridge: secret not found")
	// ErrSecretsReadOnly is returned by backends that cannot store values.
	ErrSecretsReadOnly = errors.New("bridge: secrets backend is read-only; configure a secrets backend (--secrets local|openbao)")
	// ErrInjectionUnavailable is returned when a feature needs a writable
	// backend (CA keys, injected credentials) and the backend is read-only.
	ErrInjectionUnavailable = errors.New("bridge: credential injection needs a writable secrets backend (--secrets local|openbao)")
)

// SecretProvider stores secret values by path. Paths are owner-relative:
// a provider keeps them at marshal/<owner>/<path>. Values are never
// logged, and listing returns paths only.
type SecretProvider interface {
	Name() string
	Get(ctx context.Context, owner, ref string) ([]byte, error)
	Put(ctx context.Context, owner, ref string, value []byte) error
	Delete(ctx context.Context, owner, ref string) error
	List(ctx context.Context, owner, prefix string) ([]string, error)
}

const secretRefPrefix = "vault:"

// ParseSecretRef validates a "vault:<path>" reference and returns the
// path. It rejects a missing prefix, an empty path, a leading slash and
// any ".." segment, so a ref can never climb out of the owner's root.
func ParseSecretRef(ref string) (string, error) {
	path, ok := strings.CutPrefix(ref, secretRefPrefix)
	if !ok {
		return "", fmt.Errorf("secret ref %q must start with %q", ref, secretRefPrefix)
	}
	if err := validateSecretPath(path); err != nil {
		return "", fmt.Errorf("secret ref %q: %w", ref, err)
	}
	return path, nil
}

// Reserved and scoped path prefixes. The bridge keeps workspace CA keys
// under "ca/", so the secrets API never reads, writes or lists them;
// credentials may only reference "git/" (or "env/" on the env backend) so a credential cannot be pointed
// at a provider key or a CA.
const (
	reservedCAPrefix    = "ca/"
	credentialPrefix    = "git/"
	envCredentialPrefix = "env/"
)

func isReservedSecretPath(path string) bool {
	return strings.HasPrefix(path, reservedCAPrefix)
}

// parseCredentialRef validates the ref of a vault credential: it must
// live under git/.
func parseCredentialRef(ref string) (string, error) {
	path, err := ParseSecretRef(ref)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, credentialPrefix) && !strings.HasPrefix(path, envCredentialPrefix) {
		return "", fmt.Errorf("secret ref %q: credential secrets must live under %s or %s", ref, credentialPrefix, envCredentialPrefix)
	}
	return path, nil
}

// parseInjectionRef validates an egress injection ref: any secret except
// the bridge's own.
func parseInjectionRef(ref string) (string, error) {
	path, err := ParseSecretRef(ref)
	if err != nil {
		return "", err
	}
	if isReservedSecretPath(path) {
		return "", fmt.Errorf("secret ref %q is reserved", ref)
	}
	return path, nil
}

func validateSecretPath(path string) error {
	if path == "" {
		return errors.New("empty path")
	}
	if strings.HasPrefix(path, "/") {
		return errors.New("path must not start with /")
	}
	if strings.ContainsAny(path, "\x00\\") {
		return errors.New("path contains an illegal character")
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return errors.New("path has an empty, . or .. segment")
		}
	}
	return nil
}

// storagePath is where a provider keeps owner's path.
func storagePath(owner, path string) string {
	return "marshal/" + owner + "/" + path
}

// envProvider reads vault:env/NAME from the process environment. It is
// the default backend: read-only, no state.
type envProvider struct{}

func (envProvider) Name() string { return "env" }

func (envProvider) Get(_ context.Context, _, ref string) ([]byte, error) {
	if err := validateSecretPath(ref); err != nil {
		return nil, err
	}
	name, ok := strings.CutPrefix(ref, "env/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return nil, ErrSecretNotFound
	}
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return nil, ErrSecretNotFound
	}
	return []byte(v), nil
}

func (envProvider) Put(context.Context, string, string, []byte) error { return ErrSecretsReadOnly }
func (envProvider) Delete(context.Context, string, string) error      { return ErrSecretsReadOnly }
func (envProvider) List(context.Context, string, string) ([]string, error) {
	return nil, nil
}

// NewEnvProvider returns the read-only environment backend.
func NewEnvProvider() SecretProvider { return envProvider{} }
