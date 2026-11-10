package daemon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// awkwardSpec is a spec whose every string needs escaping: a space, a
// percent, a quote and an ampersand.
func awkwardSpec() UnitSpec {
	return UnitSpec{
		Webbridge: "/opt/my marshal/bin/webbridge",
		Marshal:   `/opt/ma%rshal's "marshal" & co/bin/marshal`,
		Paths: Paths{
			Dir:      "/data/a & b/daemon",
			Socket:   "/data/a & b/daemon/marshal.sock",
			StateDir: "/data/a & b/daemon/state 50%",
			Registry: "/data/a & b/daemon/fleet.json",
			Log:      "/data/a & b/daemon/webbridge 100%.log",
		},
		Env: map[string]string{
			"PATH":             "/usr/bin:/opt/a & b/bin",
			"MARSHAL_DATA_DIR": "/data/a & b",
		},
		IdleSessionTimeout: 45 * time.Minute,
	}
}

// wantArgv is decision 10's order, webbridge first.
func wantArgv(u UnitSpec) []string {
	return append([]string{u.Webbridge}, u.Args()...)
}

func TestThePlistNamesEverything(t *testing.T) {
	spec := awkwardSpec()
	plist := string(LaunchdPlist(spec))

	// It is XML: every token decodes, and the DTD is present.
	if err := decodeAll(plist); err != nil {
		t.Fatalf("the plist does not parse: %v", err)
	}
	if !strings.Contains(plist, `<!DOCTYPE plist`) {
		t.Errorf("the plist has no DTD:\n%s", plist)
	}

	// Every argument, in decision 10's order, in the ProgramArguments
	// array, escaped so the plist still parses.
	values := plistStrings(t, plist)
	want := append([]string{Label}, wantArgv(spec)...)
	if len(values) < len(want) {
		t.Fatalf("plist holds %d strings, want at least %d:\n%s", len(values), len(want), plist)
	}
	for i, expected := range want {
		if values[i] != expected {
			t.Errorf("string %d = %q, want %q", i, values[i], expected)
		}
	}

	// The idle session timeout is always written into the unit, so the
	// unit states the value it runs with.
	for _, pair := range []string{
		"<string>--idle-session-timeout</string>",
		"<string>" + spec.IdleSessionTimeout.String() + "</string>",
	} {
		if !strings.Contains(plist, pair) {
			t.Errorf("the plist does not carry %q:\n%s", pair, plist)
		}
	}

	// The escaping is what made that decode possible: the raw text must
	// hold the entities and not the characters they stand for.
	for _, tc := range []struct{ raw, escaped string }{
		{"a & b", "a &amp; b"},
		{`"marshal"`, "&#34;marshal&#34;"},
		{"50%", "50%"},
	} {
		if !strings.Contains(plist, tc.escaped) {
			t.Errorf("the plist does not hold %q:\n%s", tc.escaped, plist)
		}
		if tc.raw != tc.escaped && strings.Contains(plist, tc.raw) {
			t.Errorf("the plist holds the unescaped %q:\n%s", tc.raw, plist)
		}
	}

	// Every key decision 10 names.
	for _, key := range []string{
		"<key>Label</key>",
		"<key>ProgramArguments</key>",
		"<key>EnvironmentVariables</key>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(plist, key) {
			t.Errorf("the plist is missing %q:\n%s", key, plist)
		}
	}

	// Both streams log to the daemon's log file.
	if got := strings.Count(plist, "<string>"+xmlEscape(spec.Paths.Log)+"</string>"); got != 2 {
		t.Errorf("the log path appears %d times, want 2 (stdout and stderr)", got)
	}

	// The environment is sorted, so a render is stable.
	dataKey := strings.Index(plist, "<key>MARSHAL_DATA_DIR</key>")
	pathKey := strings.Index(plist, "<key>PATH</key>")
	if dataKey < 0 || pathKey < 0 || dataKey > pathKey {
		t.Errorf("the plist environment is not sorted:\n%s", plist)
	}

	again := string(LaunchdPlist(awkwardSpec()))
	if again != plist {
		t.Errorf("two renders of one spec differ")
	}
}

func TestTheSystemdUnitNamesEverything(t *testing.T) {
	spec := awkwardSpec()
	unit := string(SystemdUnit(spec))

	// ExecStart holds webbridge and every argument, in decision 10's
	// order, each double-quoted and escaped for systemd.
	wantExec := `ExecStart="/opt/my marshal/bin/webbridge"` +
		` "--profile" "local"` +
		` "--socket" "/data/a & b/daemon/marshal.sock"` +
		` "--state-dir" "/data/a & b/daemon/state 50%%"` +
		` "--workspace" "/data/a & b/daemon/fleet.json"` +
		` "--addr" "off"` +
		` "--marshal-bin" "/opt/ma%%rshal's \"marshal\" & co/bin/marshal"` +
		` "--idle-session-timeout" "45m0s"`
	if !strings.Contains(unit, wantExec) {
		t.Errorf("ExecStart line missing or mis-escaped:\nwant: %s\ngot:\n%s", wantExec, unit)
	}

	// One Environment= line per variable, quoted the same way, sorted.
	wantEnv := []string{
		`Environment="MARSHAL_DATA_DIR=/data/a & b"`,
		`Environment="PATH=/usr/bin:/opt/a & b/bin"`,
	}
	envAt := -1
	for _, line := range wantEnv {
		at := strings.Index(unit, line)
		if at < 0 {
			t.Errorf("the unit is missing %q:\n%s", line, unit)
			continue
		}
		if at < envAt {
			t.Errorf("the unit's environment is not sorted:\n%s", unit)
		}
		envAt = at
	}

	for _, line := range []string{
		"[Unit]\nDescription=Marshal daemon\n",
		"[Service]\n",
		"Restart=on-failure\n",
		`StandardOutput=append:/data/a & b/daemon/webbridge 100%%.log` + "\n",
		`StandardError=append:/data/a & b/daemon/webbridge 100%%.log` + "\n",
		"[Install]\nWantedBy=default.target\n",
	} {
		if !strings.Contains(unit, line) {
			t.Errorf("the unit is missing %q:\n%s", line, unit)
		}
	}

	again := string(SystemdUnit(awkwardSpec()))
	if again != unit {
		t.Errorf("two renders of one spec differ")
	}
}

// decodeAll reads the whole document as tokens, which is what "parses as
// XML" means here.
func decodeAll(doc string) error {
	dec := xml.NewDecoder(bytes.NewReader([]byte(doc)))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// plistStrings is every <string> element's text, in document order.
func plistStrings(t *testing.T, doc string) []string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	var out []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("the plist does not parse: %v", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "string" {
			continue
		}
		var text string
		if err := dec.DecodeElement(&text, &start); err != nil {
			t.Fatalf("decode <string>: %v", err)
		}
		out = append(out, text)
	}
}
