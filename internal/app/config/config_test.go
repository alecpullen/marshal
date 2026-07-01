package config

import (
	"reflect"
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
