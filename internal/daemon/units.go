package daemon

import (
	"bytes"
	"encoding/xml"
	"sort"
	"strings"
)

// LaunchdPlist renders the launchd agent for u. Every string is
// XML-escaped, so a path holding '&', '<' or a quote still parses.
func LaunchdPlist(u UnitSpec) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")

	b.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(Label) + "</string>\n")

	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range append([]string{u.Webbridge}, u.Args()...) {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")

	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, key := range sortedEnvKeys(u.Env) {
		b.WriteString("\t\t<key>" + xmlEscape(key) + "</key>\n")
		b.WriteString("\t\t<string>" + xmlEscape(u.Env[key]) + "</string>\n")
	}
	b.WriteString("\t</dict>\n")

	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n")
	b.WriteString("\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n")
	b.WriteString("\t</dict>\n")

	// Both streams go to one log: a daemon that wedges at startup has
	// to say why somewhere the user can find it.
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlEscape(u.Paths.Log) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlEscape(u.Paths.Log) + "</string>\n")

	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// SystemdUnit renders the user unit for u.
func SystemdUnit(u UnitSpec) []byte {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Marshal daemon\n")

	b.WriteString("\n[Service]\n")
	argv := append([]string{u.Webbridge}, u.Args()...)
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = systemdQuote(arg)
	}
	b.WriteString("ExecStart=" + strings.Join(quoted, " ") + "\n")
	for _, key := range sortedEnvKeys(u.Env) {
		b.WriteString("Environment=" + systemdQuote(key+"="+u.Env[key]) + "\n")
	}
	b.WriteString("Restart=on-failure\n")
	// These two take a bare prefix ("append:") and a path, so the value
	// is not a quoted word: a leading quote would hide the prefix. The
	// percent is still escaped, because systemd expands specifiers in
	// the path.
	b.WriteString("StandardOutput=append:" + systemdSpecifier(u.Paths.Log) + "\n")
	b.WriteString("StandardError=append:" + systemdSpecifier(u.Paths.Log) + "\n")

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return []byte(b.String())
}

// xmlEscape escapes text content for both XML and the plist syntax,
// which has no character data outside strings.
func xmlEscape(s string) string {
	var b bytes.Buffer
	// EscapeString escapes exactly the five characters that would
	// otherwise end or break a string element.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// systemdQuote wraps one ExecStart or Environment word in double quotes,
// escaping the three characters systemd reads inside them: the
// backslash, the closing quote, and the percent that opens a specifier.
func systemdQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// systemdSpecifier escapes the one character systemd interprets in a
// value that is not a quoted word: the percent that opens a specifier.
// It exists for StandardOutput= and StandardError=, which take a bare
// prefix followed by a path and so cannot be quoted as a whole.
func systemdSpecifier(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	return strings.ReplaceAll(s, "%", "%%")
}

// sortedEnvKeys keeps a render stable: the same spec always produces the
// same bytes.
func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
