package bridge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestResolveReadsSecretFromEnvAtUseTime(t *testing.T) {
	const token = "s3cr3t-token"
	os.Setenv("GH_TOKEN", token)
	defer os.Unsetenv("GH_TOKEN")

	store := NewCredentialStore([]Credential{
		{ID: "gh", Kind: "pat", EnvVar: "GH_TOKEN", OwnerID: "local"},
	})
	cred, err := store.Resolve("local", "gh")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Kind != "pat" {
		t.Fatalf("Kind = %q, want pat", cred.Kind)
	}
	if cred.literal != token {
		t.Fatalf("literal = %q, want %q", cred.literal, token)
	}
}

func TestResolveRejectsAnotherOwnersCredential(t *testing.T) {
	store := NewCredentialStore([]Credential{
		{ID: "gh", Kind: "pat", EnvVar: "GH_TOKEN", OwnerID: "alice"},
	})
	if _, err := store.Resolve("bob", "gh"); err != ErrUnknownCredential {
		t.Fatalf("Resolve error = %v, want ErrUnknownCredential", err)
	}
}

func TestResolveMissingEnvIsAnError(t *testing.T) {
	os.Unsetenv("GH_TOKEN")
	store := NewCredentialStore([]Credential{
		{ID: "gh", Kind: "pat", EnvVar: "GH_TOKEN", OwnerID: "local"},
	})
	if _, err := store.Resolve("local", "gh"); err == nil {
		t.Fatalf("Resolve with unset env = nil error, want error")
	}
}

func TestEmptyCredRefResolvesToAnonymous(t *testing.T) {
	store := NewCredentialStore(nil)
	cred, err := store.Resolve("local", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Kind != "none" {
		t.Fatalf("Kind = %q, want none", cred.Kind)
	}
}

func TestCredentialIsNotSerialised(t *testing.T) {
	cred := Credential{
		ID:      "gh",
		Kind:    "pat",
		EnvVar:  "GH_TOKEN",
		OwnerID: "local",
		literal: "super-secret",
	}
	data, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Fatalf("secret serialised into JSON: %s", data)
	}
}
