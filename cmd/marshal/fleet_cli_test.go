package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/credentials"
	"marshal/internal/fleetprofile"
	"marshal/internal/launch"
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

// TestFleetAddWritesAProfile holds `marshal fleet add NAME URL`: the profile it
// writes is the one `fleet servers --json` then lists, and the file keeps the
// comments it already had.
func TestFleetAddWritesAProfile(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "# kept\n[servers.one]\nurl = \"https://one.example.com\"\n")

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "add", "two", "https://two.example.com"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if want := "Added two. Store its token with `marshal fleet login two`.\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	stdout.Reset()
	stderr.Reset()
	if err := run(context.Background(), []string{"fleet", "servers", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run(fleet servers --json) error = %v", err)
	}
	var doc FleetServers
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not the FleetServers document: %v\n%s", err, stdout.String())
	}
	names := make([]string, len(doc.Servers))
	for i, srv := range doc.Servers {
		names[i] = srv.Name
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(names, want) {
		t.Errorf("servers = %v, want %v", names, want)
	}
	if got, want := doc.Servers[1].URL, "https://two.example.com"; got != want {
		t.Errorf("two url = %q, want %q", got, want)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !strings.Contains(string(body), "# kept") {
		t.Errorf("the file lost its comment:\n%s", body)
	}
}

// TestFleetAddTakesACAFileAndNoNotify holds the two flags: --ca-file writes
// ca_file as given, and --no-notify writes the profile notifications off,
// which is a notify = false the loader reads back as false.
func TestFleetAddTakesACAFileAndNoNotify(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[servers.one]\nurl = \"https://one.example.com\"\n")

	var stdout, stderr bytes.Buffer
	args := []string{"fleet", "add", "two", "https://two.example.com", "--ca-file", "/etc/ssl/two.pem", "--no-notify"}
	if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	file, err := fleetprofile.Load(path)
	if err != nil {
		t.Fatalf("load fleet.toml: %v", err)
	}
	two, ok := file.Servers["two"]
	if !ok {
		t.Fatalf("the file does not hold the profile add wrote: %v", file)
	}
	if got, want := two.CAFile, "/etc/ssl/two.pem"; got != want {
		t.Errorf("ca_file = %q, want %q", got, want)
	}
	if two.Notify {
		t.Error("notify = true for a profile added with --no-notify")
	}
	if !file.Servers["one"].Notify {
		t.Error("notify = false for a profile added without --no-notify")
	}
}

// TestFleetAddRefusesTheServerFlag holds add to the file alone: --server would
// name a bridge rather than a profile to write, so it is refused before any
// file is touched.
func TestFleetAddRefusesTheServerFlag(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[servers.one]\nurl = \"https://one.example.com\"\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"fleet", "add", "two", "https://two.example.com", "--server", "prod"}
	err = run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() accepted --server for add")
	}
	if !strings.Contains(err.Error(), "--server") {
		t.Errorf("error = %q, want it to refuse --server", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("the file changed for a refused invocation:\n%s", after)
	}
}

// TestFleetRemoveDeletesTheProfileAndToken holds `marshal fleet remove NAME`:
// the profile's table and the keychain token login stored for it both go, and
// the rest of the file, comments included, stays.
func TestFleetRemoveDeletesTheProfileAndToken(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "# kept\n[servers.one]\nurl = \"https://one.example.com\"\n\n[servers.two]\nurl = \"https://two.example.com\"\n")

	store := credentials.NewMemStore()
	if err := store.Set(fleetprofile.KeychainKey("two"), []byte("tok")); err != nil {
		t.Fatalf("seed the token: %v", err)
	}
	oldStore := fleetLoginStore
	defer func() { fleetLoginStore = oldStore }()
	fleetLoginStore = func() (credentials.Store, error) { return store, nil }

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "remove", "two"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if want := "Removed two.\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	file, err := fleetprofile.Load(path)
	if err != nil {
		t.Fatalf("load fleet.toml: %v", err)
	}
	if _, ok := file.Servers["two"]; ok {
		t.Errorf("the file still holds two: %s", path)
	}
	if _, ok := file.Servers["one"]; !ok {
		t.Error("remove deleted a profile other than the one it named")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !strings.Contains(string(body), "# kept") {
		t.Errorf("the file lost its comment:\n%s", body)
	}

	if _, err := store.Get(fleetprofile.KeychainKey("two")); !errors.Is(err, credentials.ErrNotFound) {
		t.Errorf("the token survived the removal: %v", err)
	}
}

// TestFleetRemoveUnknownNameExitsOne holds the name check: a profile the file
// lacks fails, and the file and the keychain stay as they were.
func TestFleetRemoveUnknownNameExitsOne(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[servers.one]\nurl = \"https://one.example.com\"\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}

	oldStore := fleetLoginStore
	defer func() { fleetLoginStore = oldStore }()
	fleetLoginStore = func() (credentials.Store, error) {
		t.Error("the keychain was opened for a profile the file lacks")
		return credentials.NewMemStore(), nil
	}

	var stdout, stderr bytes.Buffer
	err = run(context.Background(), []string{"fleet", "remove", "nope"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a profile the file lacks")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the profile asked for", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("the file changed for a profile it lacks:\n%s", after)
	}
}

// TestFleetAddWithARelay holds `marshal fleet add NAME --relay HUB` (D20): the
// profile is written with relay in place of url, and the two forms that make
// no sense for a relayed profile are refused without touching the file.
func TestFleetAddWithARelay(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[servers.hub]\nurl = \"https://hub.example.com\"\n")

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "add", "edge", "--relay", "hub"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if want := "Added edge. Store its token with `marshal fleet login edge`.\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	file, err := fleetprofile.Load(path)
	if err != nil {
		t.Fatalf("load fleet.toml: %v", err)
	}
	edge, ok := file.Servers["edge"]
	if !ok {
		t.Fatalf("the file does not hold the profile add wrote: %v", file)
	}
	if edge.Relay != "hub" || edge.URL != "" {
		t.Errorf("edge = %+v, want relay hub and no url", edge)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !strings.Contains(string(body), "relay = \"hub\"") {
		t.Errorf("fleet.toml does not hold relay = \"hub\":\n%s", body)
	}
	if !strings.Contains(string(body), "[servers.hub]\nurl = \"https://hub.example.com\"") {
		t.Errorf("add rewrote the hub's table:\n%s", body)
	}

	before := string(body)
	for _, args := range [][]string{
		{"fleet", "add", "other", "--relay", "hub", "--ca-file", "/etc/ssl/other.pem"},
		{"fleet", "add", "other", "https://other.example.com", "--relay", "hub"},
	} {
		stdout.Reset()
		stderr.Reset()
		err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if err == nil {
			t.Fatalf("run(%v) returned no error, want a refusal", args)
		}
		if !strings.Contains(err.Error(), "--relay") {
			t.Errorf("run(%v) error = %q, want it to name --relay", args, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if string(after) != before {
		t.Errorf("a refused add changed the file:\n%s", after)
	}
}

// TestFleetServersJSONReportsTheRelay holds F1's document to D20: a relayed
// profile reports its relay and carries no url.
func TestFleetServersJSONReportsTheRelay(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, `
[servers.hub]
url = "https://hub.example.com"

[servers.edge]
relay = "hub"
`)

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "servers", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	var doc FleetServers
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not the FleetServers document: %v\n%s", err, stdout.String())
	}
	byName := make(map[string]FleetServer, len(doc.Servers))
	for _, srv := range doc.Servers {
		byName[srv.Name] = srv
	}
	hub, ok := byName["hub"]
	if !ok {
		t.Fatalf("the document does not list hub: %v", doc.Servers)
	}
	if hub.URL != "https://hub.example.com" || hub.Relay != "" {
		t.Errorf("hub = %+v, want its url and no relay", hub)
	}
	edge, ok := byName["edge"]
	if !ok {
		t.Fatalf("the document does not list edge: %v", doc.Servers)
	}
	if edge.Relay != "hub" {
		t.Errorf("edge relay = %q, want %q", edge.Relay, "hub")
	}
	if edge.URL != "" {
		t.Errorf("edge url = %q, want none: a relayed profile has no url", edge.URL)
	}
}

// TestTheFleetScreenRefusesARelayedProfile holds D20's refusal: the screen
// runs on a server's own url, so a relayed profile stops before the runner.
func TestTheFleetScreenRefusesARelayedProfile(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.hub]\nurl = \"https://hub.example.com\"\n\n[servers.edge]\nrelay = \"hub\"\n")

	oldServer := fleetServerRunner
	defer func() { fleetServerRunner = oldServer }()
	fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
		t.Fatal("the fleet screen ran on a relayed profile")
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "--server", "edge"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a relayed profile")
	}
	if want := "edge is reached through hub, and the fleet screen does not relay yet"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to hold %q", err, want)
	}
}
