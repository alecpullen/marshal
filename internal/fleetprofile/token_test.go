package fleetprofile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"marshal/internal/credentials"
)

// neverOpen is an OpenStore that fails the test if it is called.
func neverOpen(t *testing.T) OpenStore {
	t.Helper()
	return func() (credentials.Store, error) {
		t.Fatal("the keychain was opened for a token_cmd profile")
		return nil, errors.New("unreachable")
	}
}

func TestTokenRunsTheCommand(t *testing.T) {
	var ran string
	run := func(_ context.Context, script string, _ io.Writer) ([]byte, error) {
		ran = script
		return []byte("  tok-123\n"), nil
	}
	srv := Server{Name: "home", URL: "https://fleet.example.com", TokenCmd: "pass show fleet/home"}

	token, err := Token(context.Background(), srv, neverOpen(t), run, io.Discard)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "tok-123" {
		t.Fatalf("token = %q, want %q", token, "tok-123")
	}
	if ran != srv.TokenCmd {
		t.Fatalf("ran %q, want %q", ran, srv.TokenCmd)
	}
}

func TestATokenCommandFailureCarriesItsStderr(t *testing.T) {
	run := func(_ context.Context, _ string, stderr io.Writer) ([]byte, error) {
		if _, err := io.WriteString(stderr, "fleet: the token expired\n"); err != nil {
			return nil, err
		}
		return nil, errors.New("exit status 1")
	}
	srv := Server{Name: "home", TokenCmd: "false"}

	_, err := Token(context.Background(), srv, neverOpen(t), run, io.Discard)
	if err == nil {
		t.Fatal("a failing token_cmd answered a token, want an error")
	}
	if !strings.Contains(err.Error(), "fleet: the token expired") {
		t.Fatalf("error = %q, want its first stderr line", err)
	}
}

func TestEmptyTokenOutputFails(t *testing.T) {
	run := func(_ context.Context, _ string, _ io.Writer) ([]byte, error) {
		return []byte(" \n\t\n"), nil
	}
	srv := Server{Name: "home", TokenCmd: "true"}

	_, err := Token(context.Background(), srv, neverOpen(t), run, io.Discard)
	if err == nil {
		t.Fatal("an empty token_cmd output answered a token, want an error")
	}
	if !strings.Contains(err.Error(), "printed nothing") {
		t.Fatalf("error = %q, want it to say nothing was printed", err)
	}
}

func TestATokenCommandIsBounded(t *testing.T) {
	shortened := tokenLimit
	tokenLimit = 20 * time.Millisecond
	t.Cleanup(func() { tokenLimit = shortened })

	run := func(ctx context.Context, _ string, _ io.Writer) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	srv := Server{Name: "home", TokenCmd: "wait-forever"}

	_, err := Token(context.Background(), srv, neverOpen(t), run, io.Discard)
	if err == nil {
		t.Fatal("a token_cmd that never finished answered a token, want an error")
	}
	if want := "token_cmd for home did not finish in 10s"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestTokenFallsBackToTheKeychain(t *testing.T) {
	store := credentials.NewMemStore()
	if err := store.Set(KeychainKey("home"), []byte("  key-123\n")); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	open := func() (credentials.Store, error) { return store, nil }
	run := func(_ context.Context, _ string, _ io.Writer) ([]byte, error) {
		t.Fatal("token_cmd ran for a profile without one")
		return nil, errors.New("unreachable")
	}
	srv := Server{Name: "home", URL: "https://fleet.example.com"}

	token, err := Token(context.Background(), srv, open, run, io.Discard)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "key-123" {
		t.Fatalf("token = %q, want %q", token, "key-123")
	}
	if KeychainKey("home") != "marshal:fleet:home" {
		t.Fatalf("KeychainKey(home) = %q", KeychainKey("home"))
	}
}

func TestNoTokenSaysToLogIn(t *testing.T) {
	store := credentials.NewMemStore()
	open := func() (credentials.Store, error) { return store, nil }
	srv := Server{Name: "home", URL: "https://fleet.example.com"}

	_, err := Token(context.Background(), srv, open, nil, io.Discard)
	if err == nil {
		t.Fatal("an empty keychain answered a token, want an error")
	}
	want := "no token for home: run `marshal fleet login home`"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestNoKeychainSaysToUseTokenCmd(t *testing.T) {
	open := func() (credentials.Store, error) {
		return nil, credentials.ErrKeyringUnavailable
	}
	srv := Server{Name: "home", URL: "https://fleet.example.com"}

	_, err := Token(context.Background(), srv, open, nil, io.Discard)
	if err == nil {
		t.Fatal("a system with no keychain answered a token, want an error")
	}
	want := "no token for home: this system has no keychain; set token_cmd in fleet.toml"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestRunShellRunsSh(t *testing.T) {
	out, err := RunShell(context.Background(), "printf '%s' tok-123", io.Discard)
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	if string(out) != "tok-123" {
		t.Fatalf("output = %q, want %q", out, "tok-123")
	}

	var stderr bytes.Buffer
	out, err = RunShell(context.Background(), "printf 'fleet: no such entry\n' >&2; exit 1", &stderr)
	if err == nil {
		t.Fatal("a script that exited 1 succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "fleet: no such entry") {
		t.Fatalf("error = %q, want its first stderr line", err)
	}
	if !strings.Contains(stderr.String(), "fleet: no such entry") {
		t.Fatalf("stderr = %q, want the line to reach the terminal", stderr.String())
	}
	if len(out) != 0 {
		t.Fatalf("output = %q, want none", out)
	}
}
