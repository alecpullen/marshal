// Package workspacecfg parses, validates, patches and formats workspace
// files: the TOML documents that describe a sandbox workspace (toolchains,
// packages, mounts, secrets, network, resources, policy and setup).
//
// The package is stateless. Parse turns source text into a typed Doc,
// per-layer line ranges and diagnostics; Patch re-renders one layer's
// section into the source without touching the rest; Format renders a
// whole Doc canonically.
package workspacecfg

// Severity classifies a Diagnostic.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Doc is the typed form of a workspace file. Fields marshal to camelCase
// JSON and to the TOML names of the file format.
type Doc struct {
	Workspace  Workspace            `json:"workspace" toml:"workspace"`
	Packages   Packages             `json:"packages" toml:"packages"`
	Mounts     []Mount              `json:"mounts" toml:"mounts"`
	Files      map[string]FileMount `json:"files" toml:"files"`
	SecretsEnv map[string]string    `json:"secretsEnv" toml:"-"`
	Inject     map[string]Inject    `json:"inject" toml:"-"`
	Network    Network              `json:"network" toml:"network"`
	Resources  Resources            `json:"resources" toml:"resources"`
	Policy     Policy               `json:"policy" toml:"policy"`
	Setup      Setup                `json:"setup" toml:"setup"`
}

// Workspace is the [workspace] table: layer 1 (base) and layer 2
// (toolchains).
type Workspace struct {
	Name       string   `json:"name" toml:"name,omitempty"`
	Base       string   `json:"base" toml:"base,omitempty"`
	Toolchains []string `json:"toolchains" toml:"toolchains,omitempty"`
	Extends    string   `json:"extends" toml:"extends,omitempty"`
}

// Packages is the [packages] table (layer 3).
type Packages struct {
	Apt []string `json:"apt" toml:"apt,omitempty"`
	Go  []string `json:"go" toml:"go,omitempty"`
	Npm []string `json:"npm" toml:"npm,omitempty"`
	Pip []string `json:"pip" toml:"pip,omitempty"`
}

// Mount is one [[mounts]] entry (layer 4). Exactly one of Repo and Volume
// is set.
type Mount struct {
	Repo     string `json:"repo" toml:"repo,omitempty"`
	Volume   string `json:"volume" toml:"volume,omitempty"`
	Target   string `json:"target" toml:"target"`
	Readonly bool   `json:"readonly" toml:"readonly,omitempty"`
}

// FileMount is one [files] entry (layer 5).
type FileMount struct {
	Target   string `json:"target" toml:"target"`
	Readonly bool   `json:"readonly" toml:"readonly,omitempty"`
}

// Inject is one [secrets.inject] entry (layer 6).
type Inject struct {
	Ref    string `json:"ref" toml:"ref"`
	Header string `json:"header" toml:"header,omitempty"`
	Format string `json:"format" toml:"format,omitempty"`
}

// Network is the [network] table (layer 7).
type Network struct {
	Mode   string   `json:"mode" toml:"mode,omitempty"`
	Egress []string `json:"egress" toml:"egress,omitempty"`
}

// Resources is the [resources] table (layer 8).
type Resources struct {
	CPU     float64 `json:"cpu" toml:"cpu,omitempty"`
	Memory  string  `json:"memory" toml:"memory,omitempty"`
	Disk    string  `json:"disk" toml:"disk,omitempty"`
	Timeout string  `json:"timeout" toml:"timeout,omitempty"`
}

// Policy is the [policy] table (side panel, layer 0).
type Policy struct {
	Mode  string   `json:"mode" toml:"mode,omitempty"`
	Allow []string `json:"allow" toml:"allow,omitempty"`
}

// Setup is the [setup] table (layer 9).
type Setup struct {
	Run string `json:"run" toml:"run,omitempty"`
}

// Section is the source line range (1-based, inclusive) of one layer.
type Section struct {
	Layer     int    `json:"layer"`
	Key       string `json:"key"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// Diagnostic is one parse or validation finding. Line is 1-based.
type Diagnostic struct {
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}
