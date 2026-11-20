package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"marshal/internal/fleetprofile"
)

// noHostWait makes add and remove stop waiting for the daemon's report at once:
// a test has no daemon keeping a tunnel, and the wait would only slow it down.
func noHostWait(t *testing.T) {
	t.Helper()
	old := fleetHostWait
	t.Cleanup(func() { fleetHostWait = old })
	fleetHostWait = 0
}

// TestFleetAddWritesAHostTable holds `marshal fleet add NAME DEST`: the
// [hosts.NAME] table it writes is one the loader reads back, and the file keeps
// the comments and tables it already had.
func TestFleetAddWritesAHostTable(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	noHostWait(t)
	path := writeFleetFile(t, dir, "# kept\n[hosts.one]\nssh = \"me@one.example\"\n")

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"fleet", "add", "two", "me@two.example"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	want := "Added two. marshal fleet --host two opens its fleet once its tunnel is live; marshal daemon status shows it.\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	file, err := fleetprofile.Load(path)
	if err != nil {
		t.Fatalf("load fleet.toml: %v", err)
	}
	two, ok := file.Hosts["two"]
	if !ok {
		t.Fatalf("the file does not hold the host add wrote: %v", file.Hosts)
	}
	if got, want := two.SSH, "me@two.example"; got != want {
		t.Errorf("ssh = %q, want %q", got, want)
	}
	if got, want := two.Marshal, ""; got != want {
		t.Errorf("marshal = %q, want %q: no --marshal means marshal on the remote PATH", got, want)
	}
	if !two.Notify {
		t.Error("notify = false for a host added without --no-notify")
	}
	if _, ok := file.Hosts["one"]; !ok {
		t.Error("add deleted a host other than the one it named")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !strings.Contains(string(body), "# kept") {
		t.Errorf("the file lost its comment:\n%s", body)
	}
	if !strings.Contains(string(body), "[hosts.two]\nssh = \"me@two.example\"") {
		t.Errorf("fleet.toml does not hold the host's table:\n%s", body)
	}
}

// TestFleetAddTakesMarshalAndNoNotify holds the two flags: --marshal writes the
// remote binary's path, and --no-notify writes notify = false, which the loader
// reads back as false.
func TestFleetAddTakesMarshalAndNoNotify(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	noHostWait(t)
	path := writeFleetFile(t, dir, "[hosts.one]\nssh = \"me@one.example\"\n")

	var stdout, stderr bytes.Buffer
	args := []string{"fleet", "add", "two", "me@two.example", "--marshal", "~/.local/bin/marshal", "--no-notify"}
	if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	file, err := fleetprofile.Load(path)
	if err != nil {
		t.Fatalf("load fleet.toml: %v", err)
	}
	two, ok := file.Hosts["two"]
	if !ok {
		t.Fatalf("the file does not hold the host add wrote: %v", file.Hosts)
	}
	if got, want := two.Marshal, "~/.local/bin/marshal"; got != want {
		t.Errorf("marshal = %q, want %q", got, want)
	}
	if two.Notify {
		t.Error("notify = true for a host added with --no-notify")
	}
	if !file.Hosts["one"].Notify {
		t.Error("notify = false for a host added without --no-notify")
	}
}

// TestFleetAddRefusesTheServerFlag holds add to the file alone: --host would
// name a tunnel rather than a host to write, so it is refused before any file
// is touched.
func TestFleetAddRefusesTheServerFlag(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	noHostWait(t)
	path := writeFleetFile(t, dir, "[hosts.one]\nssh = \"me@one.example\"\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"fleet", "add", "two", "me@two.example", "--host", "other"}
	err = run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() accepted --host for add")
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("error = %q, want it to refuse --host", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("the file changed for a refused invocation:\n%s", after)
	}
}

// TestFleetRemoveDeletesTheHostTable holds `marshal fleet remove NAME`: the
// host's table goes, and the rest of the file, comments included, stays.
func TestFleetRemoveDeletesTheHostTable(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	noHostWait(t)
	path := writeFleetFile(t, dir, "# kept\n[hosts.one]\nssh = \"me@one.example\"\n\n[hosts.two]\nssh = \"me@two.example\"\n")

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
	if _, ok := file.Hosts["two"]; ok {
		t.Errorf("the file still holds two: %s", path)
	}
	if _, ok := file.Hosts["one"]; !ok {
		t.Error("remove deleted a host other than the one it named")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !strings.Contains(string(body), "# kept") {
		t.Errorf("the file lost its comment:\n%s", body)
	}
}

// TestFleetRemoveUnknownNameExitsOne holds the name check: a host the file
// lacks fails, and the file stays as it was.
func TestFleetRemoveUnknownNameExitsOne(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	noHostWait(t)
	path := writeFleetFile(t, dir, "[hosts.one]\nssh = \"me@one.example\"\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = run(context.Background(), []string{"fleet", "remove", "nope"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a host the file lacks")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the host asked for", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fleet.toml: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("the file changed for a host it lacks:\n%s", after)
	}
}
