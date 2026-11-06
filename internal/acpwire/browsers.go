package acpwire

// These are what the browser surfaces read: the resume picker's session list
// and the project's memories. Times are Unix milliseconds (see Millis).

// SessionEntry is one stored session the resume picker lists (C11, plan 8b).
// UpdatedAt is Unix milliseconds.
type SessionEntry struct {
	SessionID    string `json:"sessionId"`
	Title        string `json:"title,omitempty"`
	UpdatedAt    int64  `json:"updatedAt,omitempty"`
	MessageCount int    `json:"messageCount,omitempty"`
}

// MemoryEntry is one of the project's memories. CreatedAt is Unix
// milliseconds.
type MemoryEntry struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	Confidence string `json:"confidence,omitempty"`
	Scope      string `json:"scope,omitempty"`
	CreatedAt  int64  `json:"createdAt,omitempty"`
}

// PluginEntry is one installed plugin, from the global or the project
// lockfile. Scope is "global" or "project".
type PluginEntry struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Scope  string `json:"scope"`
}

// PluginScan is a plugin cloned and scanned for review, not yet installed.
// Token names the staged clone that InstallPlugin and DiscardPluginScan take.
// Source is the normalized clone URL the scan fetched.
type PluginScan struct {
	Token    string         `json:"token"`
	Name     string         `json:"name"`
	Source   string         `json:"source"`
	Commit   string         `json:"commit,omitempty"`
	Contents PluginContents `json:"contents"`
}

// PluginContents is everything a scanned plugin would add.
type PluginContents struct {
	Skills      []PluginItem               `json:"skills,omitempty"`
	Commands    []PluginItem               `json:"commands,omitempty"`
	Hooks       []PluginHook               `json:"hooks,omitempty"`
	MCPServers  map[string]PluginMCPServer `json:"mcpServers,omitempty"`
	MCPPolicies map[string]string          `json:"mcpPolicies,omitempty"`
}

// PluginItem is one passive entry: a skill or a slash command.
type PluginItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PluginHook is a scanned hook, as config.HookConfig holds it.
type PluginHook struct {
	Event     string `json:"event"`
	Matcher   string `json:"matcher,omitempty"`
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeoutMs,omitempty"`
}

// PluginMCPServer is a scanned MCP server, as config.MCPServerConfig holds it.
type PluginMCPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Trust   string            `json:"trust,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Type    string            `json:"type,omitempty"`
	Auth    string            `json:"auth,omitempty"`
}
