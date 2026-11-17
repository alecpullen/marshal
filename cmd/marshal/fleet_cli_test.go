package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/credentials"
)

// TestFleetServersJSONListsProfilesWithoutTokens holds `marshal fleet servers
// --json` to F1's document: a schema version, each profile's name and origin,
// and no token in either form.
func TestFleetServersJSONListsProfilesWithoutTokens(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, `
[servers.zulu]
url = "https://zulu.example.com"

[servers.alpha]
url = "https://alpha.example.com"
token_cmd = "printf super-secret"
`)

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "servers", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	var doc FleetServers
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not the FleetServers document: %v\n%s", err, stdout.String())
	}
	if doc.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1", doc.SchemaVersion)
	}
	names := make([]string, len(doc.Servers))
	for i, srv := range doc.Servers {
		names[i] = srv.Name
	}
	if want := []string{"alpha", "zulu"}; !reflect.DeepEqual(names, want) {
		t.Errorf("servers = %v, want %v", names, want)
	}
	if got, want := doc.Servers[0].URL, "https://alpha.example.com"; got != want {
		t.Errorf("alpha url = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String(), "super-secret") {
		t.Errorf("the document carries a token:\n%s", stdout.String())
	}
}

// TestFleetTokenPrintsResolvedToken holds `marshal fleet token` to decision 3:
// the token fleetprofile resolves and a newline, on stdout, for a program.
func TestFleetTokenPrintsResolvedToken(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\ntoken_cmd = \"pass show fleet/example\"\n")

	oldRunner, oldStore := fleetTokenRunner, fleetTokenStore
	defer func() { fleetTokenRunner, fleetTokenStore = oldRunner, oldStore }()
	ran := ""
	fleetTokenRunner = func(_ context.Context, script string, _ io.Writer) ([]byte, error) {
		ran = script
		return []byte("resolved-token\n"), nil
	}
	opened := false
	fleetTokenStore = func() (credentials.Store, error) {
		opened = true
		return credentials.NewMemStore(), nil
	}

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "token", "example"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got, want := stdout.String(), "resolved-token\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := ran, "pass show fleet/example"; got != want {
		t.Errorf("token_cmd ran %q, want %q", got, want)
	}
	if opened {
		t.Error("the keychain was opened for a profile whose token comes from token_cmd")
	}
}

// TestFleetTokenRefusesATerminal holds decision 3's guard: a terminal would
// put the token in scrollback, so nothing is resolved or printed.
func TestFleetTokenRefusesATerminal(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\ntoken_cmd = \"printf tok\"\n")

	oldTerminal, oldRunner := fleetStdoutIsTerminal, fleetTokenRunner
	defer func() { fleetStdoutIsTerminal, fleetTokenRunner = oldTerminal, oldRunner }()
	fleetStdoutIsTerminal = func(io.Writer) bool { return true }
	ran := false
	fleetTokenRunner = func(context.Context, string, io.Writer) ([]byte, error) {
		ran = true
		return []byte("tok\n"), nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "token", "example"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() printed a token to a terminal")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("error = %q, want it to refuse a terminal", err)
	}
	if ran {
		t.Error("the token command ran for a refused invocation")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

// TestFleetTokenUnknownProfileExitsOne holds the name check: a profile the file
// lacks fails before a token is resolved, and the error names what it holds.
func TestFleetTokenUnknownProfileExitsOne(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.one]\nurl = \"https://one.example.com\"\n")

	oldRunner, oldStore := fleetTokenRunner, fleetTokenStore
	defer func() { fleetTokenRunner, fleetTokenStore = oldRunner, oldStore }()
	ran := false
	fleetTokenRunner = func(context.Context, string, io.Writer) ([]byte, error) {
		ran = true
		return []byte("tok\n"), nil
	}
	fleetTokenStore = func() (credentials.Store, error) {
		t.Error("the keychain was opened for a profile the file lacks")
		return credentials.NewMemStore(), nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "token", "nope"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a profile the file lacks")
	}
	if !strings.Contains(err.Error(), "one") {
		t.Errorf("error = %q, want it to name the servers the file holds", err)
	}
	if ran {
		t.Error("the token command ran for a profile the file lacks")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}
