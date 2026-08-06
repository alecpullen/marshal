package policy

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestClassifyCommand_Destructive(t *testing.T) {
	cases := []struct {
		cmd  string
		want registry.RiskLevel
	}{
		{"rm -rf /tmp/x", registry.RiskDestructive},
		{"rm -r -f /tmp/x", registry.RiskDestructive},
		{"rm -fr /tmp/x", registry.RiskDestructive},
		{"git clean -fdx", registry.RiskDestructive},
		{"chmod -R 777 /tmp/x", registry.RiskDestructive},
		{"chmod --recursive 777 /tmp/x", registry.RiskDestructive},
		{"git reset --hard", registry.RiskDestructive},
	}
	for _, c := range cases {
		got, err := ClassifyCommand(c.cmd)
		if err != nil {
			t.Errorf("%q: %v", c.cmd, err)
			continue
		}
		if got.Risk != c.want {
			t.Errorf("%q: got %v, want %v", c.cmd, got.Risk, c.want)
		}
	}
}

func TestClassifyCommand_Benign(t *testing.T) {
	cases := []string{
		"ls -la",
		"go test ./...",
		"git status",
		"git diff",
		"echo hello",
	}
	for _, c := range cases {
		got, err := ClassifyCommand(c)
		if err != nil {
			t.Errorf("%q: %v", c, err)
			continue
		}
		if got.Risk == registry.RiskDestructive {
			t.Errorf("%q: got %v, want anything but %v", c, got.Risk, registry.RiskDestructive)
		}
	}
}

func TestClassifyCommand_QuotedArgs(t *testing.T) {
	got, err := ClassifyCommand(`echo "hello world"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Risk == registry.RiskDestructive {
		t.Errorf("quoted echo: got %v, want anything but %v", got.Risk, registry.RiskDestructive)
	}
}

func TestClassifyEnvironmentCommands(t *testing.T) {
	env := []struct{ cmd, reasonPart string }{
		{"go clean -cache", "go clean"},
		{"go clean -modcache", "go clean"},
		{"npm cache clean", "npm cache clean"},
		{"pip cache purge", "pip cache"},
		{"pip cache remove requests", "pip cache"},
		{"yarn cache clean", "yarn cache clean"},
		{"git config --global user.name x", "git config --global"},
		{"brew install ripgrep", "brew install"},
		{"brew uninstall wget", "brew uninstall"},
		{"brew cleanup", "brew cleanup"},
		{"apt-get install curl", "apt"},
		{"apt remove nginx", "apt"},
		{"dnf install vim", "dnf"},
		{"yum remove httpd", "yum"},
	}
	for _, tc := range env {
		cls, err := ClassifyCommand(tc.cmd)
		if err != nil {
			t.Fatalf("%s: %v", tc.cmd, err)
		}
		if cls.Risk != registry.RiskEnvironment {
			t.Errorf("%s: risk = %s, want environment", tc.cmd, cls.Risk)
		}
		if !strings.Contains(cls.Reason, tc.reasonPart) {
			t.Errorf("%s: reason = %q, want it to contain %q", tc.cmd, cls.Reason, tc.reasonPart)
		}
	}

	ordinary := []string{
		"go clean",
		"go clean ./...",
		"git config --local user.name x",
		"git config user.name x",
		"npm cache ls",
		"brew list",
		"apt list --installed",
	}
	for _, cmd := range ordinary {
		cls, err := ClassifyCommand(cmd)
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if cls.Risk == registry.RiskEnvironment {
			t.Errorf("%s: misclassified as environment", cmd)
		}
	}
}
