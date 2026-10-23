package policy

import "testing"

// TestClassifyCommand_Destructive pins the canonical destructive
// classification for the equivalent spellings of a recursive force-delete
// and the other destructive workspace operations.
func TestClassifyCommand_Destructive(t *testing.T) {
	cases := []string{
		"rm -rf /tmp/x",
		"rm -r -f /tmp/x",
		"rm -fr /tmp/x",
		"git clean -fdx",
		"chmod -R 777 /tmp/x",
		"chmod --recursive 777 /tmp/x",
		"git reset --hard",
	}
	for _, cmd := range cases {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellDestructive) {
			t.Errorf("%q: no destructive effect in %+v", cmd, a.Operations)
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
	for _, cmd := range cases {
		a := analyze(t, cmd)
		if hasEffect(a, ShellDestructive) {
			t.Errorf("%q: classified destructive: %+v", cmd, a.Operations)
		}
	}
}

func TestClassifyCommand_QuotedArgs(t *testing.T) {
	a := analyze(t, `echo "hello world"`)
	if hasEffect(a, ShellDestructive) {
		t.Errorf("quoted echo: classified destructive: %+v", a.Operations)
	}
}

// TestClassifyEnvironmentCommands pins the environment tier: cache wipes,
// global configuration and machine package managers mutate state outside the
// workspace, while project-local installs stay routine writes.
func TestClassifyEnvironmentCommands(t *testing.T) {
	env := []string{
		"go clean -cache",
		"go clean -modcache",
		"go install golang.org/x/tools/cmd/stringer@latest",
		"npm cache clean",
		"npm install -g typescript",
		"pip cache purge",
		"pip cache remove requests",
		"pip install requests",
		"pip3 install --user black",
		"yarn cache clean",
		"yarn add lodash",
		"git config --global user.name x",
		"brew uninstall wget",
		"brew cleanup",
		"brew install ripgrep",
		"apt remove nginx",
		"apt install curl",
		"apt-get install curl",
		"dnf install vim",
		"yum remove httpd",
		"yum install vim",
	}
	for _, cmd := range env {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellEnvironment) {
			t.Errorf("%s: no environment effect in %+v", cmd, a.Operations)
		}
	}

	ordinary := []string{
		"go clean",
		"go clean ./...",
		"git config --local user.name x",
		"git config user.name x",
		"npm cache ls",
		"npm install",
		"yarn install",
		"brew list",
		"apt list --installed",
	}
	for _, cmd := range ordinary {
		a := analyze(t, cmd)
		if hasEffect(a, ShellEnvironment) {
			t.Errorf("%s: misclassified as environment: %+v", cmd, a.Operations)
		}
	}
}
