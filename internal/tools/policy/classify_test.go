package policy

import (
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
