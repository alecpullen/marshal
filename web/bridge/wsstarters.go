package bridge

// Starter templates, in the workspace file format (spec §4.1). They are
// plain strings: the bridge stores them without parsing.
var workspaceStarters = map[string]string{
	"go-service": `[workspace]
name = "go-service"
base = "debian:bookworm-slim"
toolchains = ["go@1.23.4"]

[packages]
apt = ["git", "build-essential", "ca-certificates"]

[network]
mode = "allowlist"
egress = ["proxy.golang.org", "sum.golang.org", "github.com"]

[resources]
cpu = 2
memory = "4g"
timeout = "2h"

[policy]
allow = ["go test *", "go vet *"]
`,
	"node-app": `[workspace]
name = "node-app"
base = "debian:bookworm-slim"
toolchains = ["node@22"]

[packages]
apt = ["git", "build-essential", "ca-certificates"]

[network]
mode = "allowlist"
egress = ["registry.npmjs.org", "github.com"]

[resources]
cpu = 2
memory = "4g"
timeout = "2h"

[policy]
allow = ["npm test *", "npm run *"]
`,
	"python-uv": `[workspace]
name = "python-uv"
base = "debian:bookworm-slim"
toolchains = ["python@3.12"]

[packages]
apt = ["git", "build-essential", "curl", "ca-certificates"]

[network]
mode = "allowlist"
egress = ["pypi.org", "files.pythonhosted.org", "github.com"]

[resources]
cpu = 2
memory = "4g"
timeout = "2h"

[policy]
allow = ["uv run *", "pytest *"]
`,
	"rust-crate": `[workspace]
name = "rust-crate"
base = "debian:bookworm-slim"
toolchains = ["rust@1.82"]

[packages]
apt = ["git", "build-essential", "curl", "ca-certificates"]

[network]
mode = "allowlist"
egress = ["crates.io", "static.crates.io", "index.crates.io", "github.com"]

[resources]
cpu = 4
memory = "8g"
timeout = "2h"

[policy]
allow = ["cargo test *", "cargo check *"]
`,
	"minimal": `[workspace]
name = "minimal"
base = "debian:bookworm-slim"

[network]
mode = "open"
`,
}

// starterNames lists the starters in a stable order.
var starterNames = []string{"go-service", "node-app", "python-uv", "rust-crate", "minimal"}
