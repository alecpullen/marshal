package plugins

import (
	"path/filepath"
	"testing"
)

const testMCPTOML = `[mcp.servers.docs]
command = "npx"
args = ["-y", "@acme/docs-mcp"]

[mcp.policies]
"mcp.docs.search" = "allow"
"mcp.docs.delete_everything" = "deny"
`

func TestParseMCPFileValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mcp.toml"), testMCPTOML)

	servers, policies, err := parseMCPFile(dir)
	if err != nil {
		t.Fatalf("parseMCPFile: %v", err)
	}
	srv, ok := servers["docs"]
	if !ok {
		t.Fatalf("servers = %v, want docs entry", servers)
	}
	if srv.Command != "npx" || len(srv.Args) != 2 || srv.Args[1] != "@acme/docs-mcp" {
		t.Fatalf("server = %+v", srv)
	}
	if policies["mcp.docs.search"] != "allow" || policies["mcp.docs.delete_everything"] != "deny" {
		t.Fatalf("policies = %v", policies)
	}
}

func TestParseMCPFileMissing(t *testing.T) {
	servers, policies, err := parseMCPFile(t.TempDir())
	if err != nil {
		t.Fatalf("missing mcp.toml should not error: %v", err)
	}
	if servers != nil || policies != nil {
		t.Fatalf("servers=%v policies=%v, want nil, nil", servers, policies)
	}
}

func TestParseMCPFileRejectsUnrestrictedTrust(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mcp.toml"), `[mcp.servers.safe]
command = "npx"

[mcp.servers.evil]
command = "rm"
trust = "unrestricted"
`)

	servers, _, err := parseMCPFile(dir)
	if err != nil {
		t.Fatalf("parseMCPFile: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %v, want only [safe]", servers)
	}
	if _, ok := servers["safe"]; !ok {
		t.Fatalf("servers = %v", servers)
	}
}

func TestParseMCPFileSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mcp.toml"), `[mcp.servers.nocommand]
args = ["x"]

[mcp.servers.ok]
command = "node"

[mcp.policies]
"mcp.ok.read" = "allow"
"mcp.ok.write" = "yolo"
`)

	servers, policies, err := parseMCPFile(dir)
	if err != nil {
		t.Fatalf("parseMCPFile: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %v, want only [ok]", servers)
	}
	if len(policies) != 1 || policies["mcp.ok.read"] != "allow" {
		t.Fatalf("policies = %v, want only mcp.ok.read=allow", policies)
	}
}

func TestParseMCPFileMalformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mcp.toml"), "{{{ not toml")
	if _, _, err := parseMCPFile(dir); err == nil {
		t.Fatal("malformed mcp.toml should error")
	}
}
