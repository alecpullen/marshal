package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultConfigValues(t *testing.T) {
	cfg := Default()

	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want marshal", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown"}) {
		t.Fatalf("Project.Languages = %#v, want go and markdown", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "go test ./..." {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "gofmt -w ." {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Commands.Vet != "go vet ./..." {
		t.Fatalf("Commands.Vet = %q", cfg.Commands.Vet)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want false")
	}
	if !cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = false, want true")
	}
	if cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = true, want false")
	}
}

func TestLoadIgnoresMissingConfigFiles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want default marshal", cfg.Project.Name)
	}
}

func TestLoadProjectConfigOverridesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[project]
name = "global"
languages = ["go"]

[commands]
test = "global test"

[privacy]
remote_providers_allowed = true
redact_secrets = false
`)

	writeFile(t, work+"/.marshal/config.toml", `
[project]
name = "project"
languages = ["go", "markdown", "toml"]

[commands]
test = "project test"
format = "project format"

[privacy]
remote_providers_allowed = false
include_gitignored_files = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "project" {
		t.Fatalf("Project.Name = %q, want project", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown", "toml"}) {
		t.Fatalf("Project.Languages = %#v", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "project test" {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "project format" {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want project override false")
	}
	if cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = true, want global override false")
	}
	if !cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = false, want project override true")
	}
}

func TestLoadMalformedConfigReturnsPath(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	path := work + "/.marshal/config.toml"
	writeFile(t, path, "[project\nname = broken")

	_, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err == nil {
		t.Fatal("Load returned nil error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not contain path %q", err.Error(), path)
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
