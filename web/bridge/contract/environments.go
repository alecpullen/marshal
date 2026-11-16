package contract

// The environments routes (web/bridge/wshttp.go, wsresolve.go, wsstore.go):
// the gallery of workspace templates, their source and parse, and their
// builds. F7 decision 6 makes the template's detail the designer; these are
// the documents it edits.

// EnvWorkspace is the `[workspace]` layer.
type EnvWorkspace struct {
	Name       string   `json:"name"`
	Base       string   `json:"base"`
	Toolchains []string `json:"toolchains,omitempty"`
	Extends    string   `json:"extends,omitempty"`
}

// EnvPackages is the `[packages]` layer.
type EnvPackages struct {
	Apt []string `json:"apt,omitempty"`
	Go  []string `json:"go,omitempty"`
	Npm []string `json:"npm,omitempty"`
	Pip []string `json:"pip,omitempty"`
}

// EnvMount is one entry of `[[mounts]]`.
type EnvMount struct {
	Repo     string `json:"repo,omitempty"`
	Volume   string `json:"volume,omitempty"`
	Target   string `json:"target"`
	Readonly bool   `json:"readonly,omitempty"`
}

// EnvFile is one shared file of `[files]`.
type EnvFile struct {
	Target   string `json:"target"`
	Readonly bool   `json:"readonly,omitempty"`
}

// EnvInject is one credential injection of `[inject]`.
type EnvInject struct {
	Ref    string `json:"ref"`
	Header string `json:"header,omitempty"`
	Format string `json:"format,omitempty"`
}

// EnvNetwork is the `[network]` layer.
type EnvNetwork struct {
	Mode   string   `json:"mode,omitempty"` // open, allowlist or off
	Egress []string `json:"egress,omitempty"`
}

// EnvResources is the `[resources]` layer.
type EnvResources struct {
	CPU     float64 `json:"cpu,omitempty"`
	Memory  string  `json:"memory,omitempty"`
	Disk    string  `json:"disk,omitempty"`
	Timeout string  `json:"timeout,omitempty"`
}

// EnvPolicy is the `[policy]` layer. Auto is optional: absent means the
// document does not set it, while an explicit false is a real value.
type EnvPolicy struct {
	Mode  string   `json:"mode,omitempty"`
	Auto  *bool    `json:"auto,omitempty"`
	Allow []string `json:"allow,omitempty"`
}

// EnvSetup is the `[setup]` layer.
type EnvSetup struct {
	Run string `json:"run,omitempty"`
}

// EnvPreview is the `[preview]` layer: the ports the Studio may expose.
type EnvPreview struct {
	Ports []int `json:"ports,omitempty"`
}

// EnvDoc is a template's parsed document: the designer's two views of one
// thing, since every layer here is a range of the same source.
type EnvDoc struct {
	Workspace  EnvWorkspace         `json:"workspace"`
	Packages   EnvPackages          `json:"packages"`
	Mounts     []EnvMount           `json:"mounts,omitempty"`
	Files      map[string]EnvFile   `json:"files,omitempty"`
	SecretsEnv map[string]string    `json:"secretsEnv,omitempty"`
	Inject     map[string]EnvInject `json:"inject,omitempty"`
	Network    EnvNetwork           `json:"network"`
	Resources  EnvResources         `json:"resources"`
	Policy     EnvPolicy            `json:"policy"`
	Setup      EnvSetup             `json:"setup"`
	Preview    *EnvPreview          `json:"preview,omitempty"`
}

// EnvSection is one layer's 1-based inclusive line range in the source.
type EnvSection struct {
	Layer     int    `json:"layer"`
	Key       string `json:"key"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// EnvDiagnostic is one parse diagnostic.
type EnvDiagnostic struct {
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error or warning
}

// EnvVersion is one built or buildable version of a template.
type EnvVersion struct {
	N           int    `json:"n"`
	At          string `json:"at"`
	By          string `json:"by"`
	SHA256      string `json:"sha256"`
	ImageTag    string `json:"imageTag,omitempty"`
	BuildStatus string `json:"buildStatus"` // pending, building, ok or failed
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	BuildMs     int64  `json:"buildMs,omitempty"`
}

// EnvMeta is a template's own record.
type EnvMeta struct {
	Name      string       `json:"name"`
	OwnerID   string       `json:"ownerId"`
	CreatedAt string       `json:"createdAt"`
	Published int          `json:"published"`
	Pool      int          `json:"pool"`
	Versions  []EnvVersion `json:"versions"`
}

// EnvListItem is one row of GET /api/workspaces.
type EnvListItem struct {
	Source       string       `json:"source"` // studio or repo
	Name         string       `json:"name"`
	Project      string       `json:"project,omitempty"`
	Published    int          `json:"published,omitempty"`
	Pool         int          `json:"pool,omitempty"`
	Versions     []EnvVersion `json:"versions,omitempty"`
	Usage        int          `json:"usage"`
	DraftChanges bool         `json:"draftChanges,omitempty"`
}

// EnvPool is a template's warm pool.
type EnvPool struct {
	Size     int `json:"size"`
	Idle     int `json:"idle"`
	Starting int `json:"starting"`
}

// EnvStarts is the median cold and warm start, in milliseconds; zero means
// no sample yet.
type EnvStarts struct {
	ColdMs int64 `json:"coldMs"`
	WarmMs int64 `json:"warmMs"`
}

// EnvBuilds is GET /api/workspaces/{name}/builds' answer.
type EnvBuilds struct {
	Versions []EnvVersion `json:"versions"`
	Pool     EnvPool      `json:"pool"`
	Starts   EnvStarts    `json:"starts"`
}

// EnvLoaded is GET /api/workspaces/{name}'s answer and what a draft save
// answers: the source, its parse, and the record behind it.
type EnvLoaded struct {
	Source      string          `json:"source"`
	Version     int             `json:"version"`
	Doc         EnvDoc          `json:"doc"`
	Sections    []EnvSection    `json:"sections"`
	Diagnostics []EnvDiagnostic `json:"diagnostics"`
	Meta        EnvMeta         `json:"meta"`
}

// EnvCreate is POST /api/workspaces' body. From is `blank`, `starter:<name>`,
// `devcontainer:<path>` or `snapshot:<agent>`.
type EnvCreate struct {
	Name string `json:"name"`
	From string `json:"from"`
}

// EnvDraft is PUT /api/workspaces/{name}/draft's body.
type EnvDraft struct {
	Source string `json:"source"`
}

// EnvBuildStart is POST /api/workspaces/{name}/builds' body and answer.
type EnvBuildStart struct {
	Version int `json:"version"`
}
