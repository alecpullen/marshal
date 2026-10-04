package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestLocal(t *testing.T) (SecretProvider, string, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "key")
	if err := GenerateKeyFile(key); err != nil {
		t.Fatal(err)
	}
	p, err := NewLocalProvider(state, key)
	if err != nil {
		t.Fatal(err)
	}
	return p, state, key
}

func TestLocalProviderRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newTestLocal(t)
	if err := p.Put(ctx, "local", "git/github", []byte("tok")); err != nil {
		t.Fatal(err)
	}
	if err := p.Put(ctx, "local", "git/gitea", []byte("t2")); err != nil {
		t.Fatal(err)
	}
	if err := p.Put(ctx, "local", "providers/x", []byte("k")); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(ctx, "local", "git/github")
	if err != nil || string(got) != "tok" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	refs, _ := p.List(ctx, "local", "git/")
	if len(refs) != 2 || refs[0] != "git/gitea" {
		t.Fatalf("List = %v", refs)
	}
	if other, _ := p.List(ctx, "someone", ""); len(other) != 0 {
		t.Fatalf("other owner sees %v", other)
	}
	if err := p.Delete(ctx, "local", "git/github"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx, "local", "git/github"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := p.Delete(ctx, "local", "git/github"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestLocalProviderWrongKeyFails(t *testing.T) {
	ctx := context.Background()
	p, state, _ := newTestLocal(t)
	if err := p.Put(ctx, "local", "a", []byte("v")); err != nil {
		t.Fatal(err)
	}
	key2 := filepath.Join(t.TempDir(), "k2")
	if err := GenerateKeyFile(key2); err != nil {
		t.Fatal(err)
	}
	p2, err := NewLocalProvider(state, key2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p2.Get(ctx, "local", "a"); err == nil {
		t.Fatal("wrong key opened the entry")
	}
}

func TestLocalProviderAADBindsPath(t *testing.T) {
	ctx := context.Background()
	p, state, _ := newTestLocal(t)
	p.Put(ctx, "local", "a", []byte("one"))
	p.Put(ctx, "local", "b", []byte("two"))
	file := filepath.Join(state, "secrets", "store.json")
	data, _ := os.ReadFile(file)
	var m map[string]localEntry
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	ka, kb := "marshal/local/a", "marshal/local/b"
	m[ka], m[kb] = m[kb], m[ka]
	data, _ = json.Marshal(m)
	os.WriteFile(file, data, 0o600)
	if _, err := p.Get(ctx, "local", "a"); err == nil {
		t.Fatal("swapped ciphertext opened")
	}
}

func TestLocalProviderFileNotPlaintext(t *testing.T) {
	ctx := context.Background()
	p, state, _ := newTestLocal(t)
	p.Put(ctx, "local", "a", []byte("supersecretvalue"))
	data, _ := os.ReadFile(filepath.Join(state, "secrets", "store.json"))
	if string(data) == "" || bytes.Contains(data, []byte("supersecretvalue")) {
		t.Fatal("store holds plaintext")
	}
}

func TestLocalProviderRefusesKeyInsideStateDir(t *testing.T) {
	state := t.TempDir()
	key := filepath.Join(state, "sub", "key")
	os.MkdirAll(filepath.Dir(key), 0o700)
	if err := GenerateKeyFile(key); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalProvider(state, key); err == nil {
		t.Fatal("key inside state dir accepted")
	}
}

func TestLocalProviderRefusesWideKeyMode(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	GenerateKeyFile(key)
	os.Chmod(key, 0o644)
	if _, err := NewLocalProvider(filepath.Join(root, "state"), key); err == nil {
		t.Fatal("0644 key accepted")
	}
}

func TestLocalProviderRefusesBadKeyLength(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	os.WriteFile(key, []byte("short"), 0o600)
	if _, err := NewLocalProvider(filepath.Join(root, "state"), key); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestGenerateKeyFileRefusesExisting(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := GenerateKeyFile(key); err != nil {
		t.Fatal(err)
	}
	if err := GenerateKeyFile(key); err == nil {
		t.Fatal("overwrote an existing key")
	}
	info, _ := os.Stat(key)
	if info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("mode %o size %d", info.Mode().Perm(), info.Size())
	}
}

func TestEnvProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MARSHAL_TEST_TOKEN", "abc")
	p := NewEnvProvider()
	got, err := p.Get(ctx, "local", "env/MARSHAL_TEST_TOKEN")
	if err != nil || string(got) != "abc" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := p.Get(ctx, "local", "git/x"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("non-env path: %v", err)
	}
	if _, err := p.Get(ctx, "local", "env/MARSHAL_TEST_UNSET"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("unset: %v", err)
	}
	if err := p.Put(ctx, "local", "env/X", []byte("v")); !errors.Is(err, ErrSecretsReadOnly) {
		t.Fatalf("Put: %v", err)
	}
	if err := p.Delete(ctx, "local", "env/X"); !errors.Is(err, ErrSecretsReadOnly) {
		t.Fatalf("Delete: %v", err)
	}
	if refs, _ := p.List(ctx, "local", ""); len(refs) != 0 {
		t.Fatalf("List = %v", refs)
	}
}

func TestSecretRefParse(t *testing.T) {
	for _, ok := range []string{"vault:git/github", "vault:env/X", "vault:ca/ws"} {
		if _, err := ParseSecretRef(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	if p, _ := ParseSecretRef("vault:git/github"); p != "git/github" {
		t.Errorf("path = %q", p)
	}
	for _, bad := range []string{"git/github", "vault:", "vault:/abs", "vault:a/../b", "vault:..", "vault:a//b", "vault:a/./b", "vault:a\\b", ""} {
		if _, err := ParseSecretRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
