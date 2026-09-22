package postmortem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setConfigDir points the user-global config dir (and therefore the
// postmortems tree) at a temp dir, then restores the environment.
func setConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", dir)
	// XDG_CONFIG_HOME would otherwise win over the home argument; UserDir
	// checks MARSHAL_CONFIG_DIR first, so clearing XDG keeps the override
	// unambiguous.
	t.Setenv("XDG_CONFIG_HOME", "")
	return dir
}

func TestWriteCreatesDirsAndFile(t *testing.T) {
	base := setConfigDir(t)

	report, err := Build(testState(t, testConfig(), nil), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	path, err := Write(report, "/home/tester", "my-repo", testSessionID)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := filepath.Join(base, "postmortems", "my-repo", testSessionID+".json")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report not created: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("report is not valid JSON: %v\n%s", err, data)
	}
	version, ok := decoded["schema_version"].(float64)
	if !ok || int(version) != SchemaVersion {
		t.Errorf("schema_version = %v, want %d", decoded["schema_version"], SchemaVersion)
	}
	if observations, present := decoded["agent_observations"]; !present || observations != nil {
		t.Errorf("agent_observations = %v, want explicit null", observations)
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	setConfigDir(t)

	first, err := Build(testState(t, testConfig(), nil), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first.Session.Turns = 1
	path, err := Write(first, "", "repo", testSessionID)
	if err != nil {
		t.Fatalf("Write first: %v", err)
	}

	second, err := Build(testState(t, testConfig(), nil), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second.Session.Turns = 42
	if _, err := Write(second, "", "repo", testSessionID); err != nil {
		t.Fatalf("Write second: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded struct {
		Session struct {
			Turns int `json:"turns"`
		} `json:"session"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Session.Turns != 42 {
		t.Errorf("turns = %d, want the second write (42) to win", decoded.Session.Turns)
	}
	// The atomic write must not leave its temp file behind.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("temp file left behind after rename")
	}
}

func TestWriteSanitizesTraversalSessionID(t *testing.T) {
	base := setConfigDir(t)

	report, err := Build(testState(t, testConfig(), nil), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	path, err := Write(report, "", "repo", "../../etc/passwd")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	dir := filepath.Join(base, "postmortems", "repo")
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("path escaped the postmortems dir: %q", path)
	}
	if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Fatalf("path = %q, want it under %q", path, dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report not created: %v", err)
	}
}

func TestWritePreservesNormalSessionID(t *testing.T) {
	base := setConfigDir(t)

	report, err := Build(testState(t, testConfig(), nil), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	path, err := Write(report, "", "repo", "sess_123")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := filepath.Join(base, "postmortems", "repo", "sess_123.json")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestSessionSlugSanitizes(t *testing.T) {
	cases := map[string]string{
		"sess_123":         "sess_123",
		"Sess-ABC":         "Sess-ABC",
		"../../etc/passwd": "etc-passwd",
		"..":               "session",
		".":                "session",
		"":                 "session",
		"a/b\\c":           "a-b-c",
	}
	for in, want := range cases {
		if got := SessionSlug(in); got != want {
			t.Errorf("SessionSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectSlugSanitizes(t *testing.T) {
	cases := map[string]string{
		"/home/me/My Repo!":      "my-repo",
		"/home/me/marshal":       "marshal",
		"/home/me/Foo__Bar--Baz": "foo-bar-baz",
		"---":                    "default",
		"":                       "default",
		"/":                      "default",
	}
	for in, want := range cases {
		if got := ProjectSlug(in); got != want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
