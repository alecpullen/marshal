package plugins

import (
	"fmt"
	"os"
	"path/filepath"

	"marshal/internal/app/config"

	"github.com/pelletier/go-toml/v2"
)

// MCPFileName mirrors the [mcp] section of config.toml.
const MCPFileName = "mcp.toml"

type mcpFile struct {
	MCP struct {
		Servers  map[string]config.MCPServerConfig `toml:"servers"`
		Policies map[string]string                 `toml:"policies"`
	} `toml:"mcp"`
}

// parseMCPFile reads dir/mcp.toml into config-ready server and policy
// maps. Servers with an empty command are skipped. Servers declaring
// trust = "unrestricted" are rejected outright: the MCP command
// allow-list is a hard boundary that plugin authors cannot self-elevate
// past (users can still elevate a server by hand in config.toml).
// Policies with values other than allow/confirm/deny are skipped. A
// missing file yields nil maps; a malformed file is an error.
func parseMCPFile(dir string) (servers map[string]config.MCPServerConfig, policies map[string]string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, MCPFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", MCPFileName, err)
	}
	var f mcpFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", MCPFileName, err)
	}
	for name, srv := range f.MCP.Servers {
		if srv.Command == "" || srv.Trust == "unrestricted" {
			continue
		}
		if servers == nil {
			servers = map[string]config.MCPServerConfig{}
		}
		servers[name] = srv
	}
	for tool, policy := range f.MCP.Policies {
		switch policy {
		case "allow", "confirm", "deny":
			if policies == nil {
				policies = map[string]string{}
			}
			policies[tool] = policy
		}
	}
	return servers, policies, nil
}
