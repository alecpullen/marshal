package bridge

import (
	"strings"
	"testing"
)

func TestToolchainInstallers(t *testing.T) {
	for _, spec := range []string{"go@1.23.4", "node@22", "python@3.12", "rust@stable"} {
		env, run, err := toolchainInstall(spec)
		if err != nil || strings.TrimSpace(run) == "" {
			t.Errorf("%s: run=%q err=%v", spec, run, err)
		}
		_ = env
	}
	if _, _, err := toolchainInstall("ruby@3"); err == nil {
		t.Error("an unknown language was accepted")
	}
	for _, bad := range []string{"go", "go@", "go@1; rm -rf /", "go@$(x)"} {
		if _, _, err := toolchainInstall(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
