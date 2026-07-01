# Marshal MVP Milestones A-B Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Marshal's initial Go project skeleton and a minimal Bubble Tea TUI shell.

**Architecture:** `cmd/marshal/main.go` delegates to `internal/app`. Config, logging, session state, and TUI live in focused `internal/app/*` packages so later provider, tool, and agent milestones can plug into stable boundaries.

**Tech Stack:** Go 1.26.1, standard library `log/slog`, `github.com/pelletier/go-toml/v2`, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles/textinput`.

## Global Constraints

- Module name is `marshal`.
- CLI entrypoint remains `cmd/marshal/main.go`.
- Config load order is defaults, then `~/.config/marshal/config.toml`, then `.marshal/config.toml`.
- Project config wins over global config.
- Missing config files are ignored.
- Malformed config files return an error that includes the failed path.
- Default project name is `marshal`.
- Default languages are `go` and `markdown`.
- Default test command is `go test ./...`.
- Default format command is `gofmt -w .`.
- Default vet command is `go vet ./...`.
- Default privacy values are `remote_providers_allowed = false`, `redact_secrets = true`, and `include_gitignored_files = false`.
- The first TUI does not call an LLM, execute tools, persist data, index the repo, or apply patches.
- Tests are written before implementation for each behavior.
- Final task must mark completed Milestone A and B items in `docs/10-mvp-implementation-checklist.md`.

---

## File Structure

- Create: `.gitignore` to keep local binaries and editor/build artifacts out of the new repository.
- Modify: `go.mod` to add TOML, Bubble Tea, and Bubbles dependencies when their implementation tasks need them.
- Modify: `cmd/marshal/main.go` to delegate to `internal/app.Run`.
- Create: `internal/app/config/config.go` for config types, defaults, path discovery, TOML load, and merge behavior.
- Create: `internal/app/config/config_test.go` for default, merge, missing-file, and malformed-file tests.
- Create: `internal/app/session/session.go` for app state, messages, and shutdown context.
- Create: `internal/app/session/session_test.go` for message ordering and shutdown tests.
- Create: `internal/app/logging/logging.go` for `slog` setup.
- Create: `internal/app/app.go` for top-level run wiring and CLI-friendly error returns.
- Create: `internal/app/tui/model.go` for Bubble Tea model, update logic, and view rendering.
- Create: `internal/app/tui/model_test.go` for message submission, whitespace behavior, and quit behavior.
- Modify: `docs/10-mvp-implementation-checklist.md` at the end to mark completed Milestone A and B items.

---

### Task 1: Initialize Git Repository

**Files:**
- Create: `.gitignore`

**Interfaces:**
- Consumes: Existing workspace files.
- Produces: A git repository with an initial baseline commit.

- [ ] **Step 1: Initialize git**

Run:

```bash
git init
```

Expected: repository initialized under `.git`.

- [ ] **Step 2: Add `.gitignore`**

Create `.gitignore`:

```gitignore
# Local build outputs
/marshal
/bin/

# Go test/build artifacts
*.test
*.out
coverage.txt

# Editor and OS files
.DS_Store
.idea/
```

- [ ] **Step 3: Verify repository state**

Run:

```bash
git status --short
```

Expected: source files and `.gitignore` are untracked; the local `marshal` binary is ignored.

- [ ] **Step 4: Commit baseline**

Run:

```bash
git add .gitignore cmd docs go.mod
git commit -m "chore: initialize marshal repository"
```

Expected: initial commit succeeds.

---

### Task 2: Config Defaults

**Files:**
- Create: `internal/app/config/config_test.go`
- Create: `internal/app/config/config.go`

**Interfaces:**
- Produces: `config.Default() config.Config`
- Produces: `config.Config` with `Project`, `Commands`, `Profile`, `Privacy`, and `Indexing` fields.

- [ ] **Step 1: Write failing defaults test**

Create `internal/app/config/config_test.go`:

```go
package config

import (
	"reflect"
	"testing"
)

func TestDefaultConfigValues(t *testing.T) {
	cfg := Default()

	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want marshal", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown"}) {
		t.Fatalf("Project.Languages = %#v, want go and markdown", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "go test ./..." {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "gofmt -w ." {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Commands.Vet != "go vet ./..." {
		t.Fatalf("Commands.Vet = %q", cfg.Commands.Vet)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want false")
	}
	if !cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = false, want true")
	}
	if cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/app/config -run TestDefaultConfigValues -count=1
```

Expected: FAIL because package `internal/app/config` or `Default` is not defined.

- [ ] **Step 3: Implement config defaults**

Create `internal/app/config/config.go`:

```go
package config

type Config struct {
	Project  ProjectConfig  `toml:"project"`
	Commands CommandsConfig `toml:"commands"`
	Profile  ProfileConfig  `toml:"profile"`
	Privacy  PrivacyConfig  `toml:"privacy"`
	Indexing IndexingConfig `toml:"indexing"`
}

type ProjectConfig struct {
	Name      string   `toml:"name"`
	Languages []string `toml:"languages"`
}

type CommandsConfig struct {
	Test   string `toml:"test"`
	Format string `toml:"format"`
	Vet    string `toml:"vet"`
}

type ProfileConfig struct {
	Default string `toml:"default"`
}

type PrivacyConfig struct {
	RemoteProvidersAllowed bool `toml:"remote_providers_allowed"`
	RedactSecrets          bool `toml:"redact_secrets"`
	IncludeGitignoredFiles bool `toml:"include_gitignored_files"`
}

type IndexingConfig struct {
	UseTreesitter  bool     `toml:"use_treesitter"`
	UseEmbeddings  bool     `toml:"use_embeddings"`
	SummariseFiles bool     `toml:"summarise_files"`
	Ignore         []string `toml:"ignore"`
}

func Default() Config {
	return Config{
		Project: ProjectConfig{
			Name:      "marshal",
			Languages: []string{"go", "markdown"},
		},
		Commands: CommandsConfig{
			Test:   "go test ./...",
			Format: "gofmt -w .",
			Vet:    "go vet ./...",
		},
		Profile: ProfileConfig{
			Default: "local_balanced",
		},
		Privacy: PrivacyConfig{
			RemoteProvidersAllowed: false,
			RedactSecrets:          true,
			IncludeGitignoredFiles: false,
		},
		Indexing: IndexingConfig{
			UseTreesitter:  false,
			UseEmbeddings:  false,
			SummariseFiles: false,
			Ignore:         []string{"node_modules/**", "vendor/**", "dist/**", ".git/**"},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

```bash
go test ./internal/app/config -run TestDefaultConfigValues -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
gofmt -w internal/app/config
git add internal/app/config go.mod
git commit -m "feat: add default config"
```

Expected: commit succeeds.

---

### Task 3: Config File Loading and Precedence

**Files:**
- Modify: `internal/app/config/config_test.go`
- Modify: `internal/app/config/config.go`
- Modify: `go.mod`
- Create or update: `go.sum`

**Interfaces:**
- Consumes: `config.Default() config.Config`
- Produces: `config.Load(options config.LoadOptions) (config.Config, error)`
- Produces: `config.LoadOptions{WorkingDir string, HomeDir string}`

- [ ] **Step 1: Add TOML dependency**

Run:

```bash
go get github.com/pelletier/go-toml/v2
```

Expected: `go.mod` and `go.sum` include the TOML parser.

- [ ] **Step 2: Write failing config load tests**

Append to `internal/app/config/config_test.go`:

```go
func TestLoadIgnoresMissingConfigFiles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want default marshal", cfg.Project.Name)
	}
}

func TestLoadProjectConfigOverridesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[project]
name = "global"
languages = ["go"]

[commands]
test = "global test"

[privacy]
remote_providers_allowed = true
redact_secrets = false
`)

	writeFile(t, work+"/.marshal/config.toml", `
[project]
name = "project"
languages = ["go", "markdown", "toml"]

[commands]
test = "project test"
format = "project format"

[privacy]
remote_providers_allowed = false
include_gitignored_files = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "project" {
		t.Fatalf("Project.Name = %q, want project", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown", "toml"}) {
		t.Fatalf("Project.Languages = %#v", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "project test" {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "project format" {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want project override false")
	}
	if cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = true, want global override false")
	}
	if !cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = false, want project override true")
	}
}

func TestLoadMalformedConfigReturnsPath(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	path := work + "/.marshal/config.toml"
	writeFile(t, path, "[project\nname = broken")

	_, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err == nil {
		t.Fatal("Load returned nil error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not contain path %q", err.Error(), path)
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
```

Update the imports in `internal/app/config/config_test.go`:

```go
import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/config -count=1
```

Expected: FAIL because `Load` and `LoadOptions` are not defined.

- [ ] **Step 4: Implement config loading**

Replace `internal/app/config/config.go` with:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Project  ProjectConfig  `toml:"project"`
	Commands CommandsConfig `toml:"commands"`
	Profile  ProfileConfig  `toml:"profile"`
	Privacy  PrivacyConfig  `toml:"privacy"`
	Indexing IndexingConfig `toml:"indexing"`
}

type ProjectConfig struct {
	Name      string   `toml:"name"`
	Languages []string `toml:"languages"`
}

type CommandsConfig struct {
	Test   string `toml:"test"`
	Format string `toml:"format"`
	Vet    string `toml:"vet"`
}

type ProfileConfig struct {
	Default string `toml:"default"`
}

type PrivacyConfig struct {
	RemoteProvidersAllowed bool `toml:"remote_providers_allowed"`
	RedactSecrets          bool `toml:"redact_secrets"`
	IncludeGitignoredFiles bool `toml:"include_gitignored_files"`
}

type IndexingConfig struct {
	UseTreesitter  bool     `toml:"use_treesitter"`
	UseEmbeddings  bool     `toml:"use_embeddings"`
	SummariseFiles bool     `toml:"summarise_files"`
	Ignore         []string `toml:"ignore"`
}

type LoadOptions struct {
	HomeDir    string
	WorkingDir string
}

type configFile struct {
	Project *struct {
		Name      *string  `toml:"name"`
		Languages []string `toml:"languages"`
	} `toml:"project"`
	Commands *struct {
		Test   *string `toml:"test"`
		Format *string `toml:"format"`
		Vet    *string `toml:"vet"`
	} `toml:"commands"`
	Profile *struct {
		Default *string `toml:"default"`
	} `toml:"profile"`
	Privacy *struct {
		RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
		RedactSecrets          *bool `toml:"redact_secrets"`
		IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
	} `toml:"privacy"`
	Indexing *struct {
		UseTreesitter  *bool    `toml:"use_treesitter"`
		UseEmbeddings  *bool    `toml:"use_embeddings"`
		SummariseFiles *bool    `toml:"summarise_files"`
		Ignore         []string `toml:"ignore"`
	} `toml:"indexing"`
}

func Default() Config {
	return Config{
		Project: ProjectConfig{
			Name:      "marshal",
			Languages: []string{"go", "markdown"},
		},
		Commands: CommandsConfig{
			Test:   "go test ./...",
			Format: "gofmt -w .",
			Vet:    "go vet ./...",
		},
		Profile: ProfileConfig{
			Default: "local_balanced",
		},
		Privacy: PrivacyConfig{
			RemoteProvidersAllowed: false,
			RedactSecrets:          true,
			IncludeGitignoredFiles: false,
		},
		Indexing: IndexingConfig{
			UseTreesitter:  false,
			UseEmbeddings:  false,
			SummariseFiles: false,
			Ignore:         []string{"node_modules/**", "vendor/**", "dist/**", ".git/**"},
		},
	}
}

func Load(opts LoadOptions) (Config, error) {
	cfg := Default()

	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("find home directory: %w", err)
		}
	}

	work := opts.WorkingDir
	if work == "" {
		var err error
		work, err = os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("find working directory: %w", err)
		}
	}

	for _, path := range []string{
		filepath.Join(home, ".config", "marshal", "config.toml"),
		filepath.Join(work, ".marshal", "config.toml"),
	} {
		next, err := loadFile(path)
		if err != nil {
			return Config{}, err
		}
		merge(&cfg, next)
	}

	return cfg, nil
}

func loadFile(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFile{}, nil
		}
		return configFile{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var file configFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return configFile{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, nil
}

func merge(cfg *Config, file configFile) {
	if file.Project != nil {
		if file.Project.Name != nil {
			cfg.Project.Name = *file.Project.Name
		}
		if file.Project.Languages != nil {
			cfg.Project.Languages = file.Project.Languages
		}
	}
	if file.Commands != nil {
		if file.Commands.Test != nil {
			cfg.Commands.Test = *file.Commands.Test
		}
		if file.Commands.Format != nil {
			cfg.Commands.Format = *file.Commands.Format
		}
		if file.Commands.Vet != nil {
			cfg.Commands.Vet = *file.Commands.Vet
		}
	}
	if file.Profile != nil && file.Profile.Default != nil {
		cfg.Profile.Default = *file.Profile.Default
	}
	if file.Privacy != nil {
		if file.Privacy.RemoteProvidersAllowed != nil {
			cfg.Privacy.RemoteProvidersAllowed = *file.Privacy.RemoteProvidersAllowed
		}
		if file.Privacy.RedactSecrets != nil {
			cfg.Privacy.RedactSecrets = *file.Privacy.RedactSecrets
		}
		if file.Privacy.IncludeGitignoredFiles != nil {
			cfg.Privacy.IncludeGitignoredFiles = *file.Privacy.IncludeGitignoredFiles
		}
	}
	if file.Indexing != nil {
		if file.Indexing.UseTreesitter != nil {
			cfg.Indexing.UseTreesitter = *file.Indexing.UseTreesitter
		}
		if file.Indexing.UseEmbeddings != nil {
			cfg.Indexing.UseEmbeddings = *file.Indexing.UseEmbeddings
		}
		if file.Indexing.SummariseFiles != nil {
			cfg.Indexing.SummariseFiles = *file.Indexing.SummariseFiles
		}
		if file.Indexing.Ignore != nil {
			cfg.Indexing.Ignore = file.Indexing.Ignore
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/config -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
gofmt -w internal/app/config
go mod tidy
git add internal/app/config go.mod go.sum
git commit -m "feat: load marshal config files"
```

Expected: commit succeeds.

---

### Task 4: Session State and Shutdown

**Files:**
- Create: `internal/app/session/session_test.go`
- Create: `internal/app/session/session.go`

**Interfaces:**
- Consumes: `config.Config`
- Produces: `session.New(cfg config.Config, workingDir string, now time.Time) *session.State`
- Produces: `(*session.State).AddMessage(role session.Role, content string)`
- Produces: `(*session.State).Messages() []session.Message`
- Produces: `(*session.State).Shutdown()`
- Produces: `(*session.State).Done() <-chan struct{}`

- [ ] **Step 1: Write failing session tests**

Create `internal/app/session/session_test.go`:

```go
package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestStateAppendsMessagesInOrder(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))

	state.AddMessage(RoleSystem, "ready")
	state.AddMessage(RoleUser, "hello")

	messages := state.Messages()
	if len(messages) != 2 {
		t.Fatalf("len(Messages()) = %d, want 2", len(messages))
	}
	if messages[0].Role != RoleSystem || messages[0].Content != "ready" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != RoleUser || messages[1].Content != "hello" {
		t.Fatalf("second message = %#v", messages[1])
	}
}

func TestMessagesReturnsCopy(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))
	state.AddMessage(RoleUser, "hello")

	messages := state.Messages()
	messages[0].Content = "mutated"

	got := state.Messages()[0].Content
	if got != "hello" {
		t.Fatalf("stored message = %q, want hello", got)
	}
}

func TestShutdownCancelsState(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))
	state.Shutdown()

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not cancelled")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/session -count=1
```

Expected: FAIL because package `internal/app/session` or `New` is not defined.

- [ ] **Step 3: Implement session state**

Create `internal/app/session/session.go`:

```go
package session

import (
	"context"
	"sync"
	"time"

	"marshal/internal/app/config"
)

type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
)

type Message struct {
	Role      Role
	Content   string
	CreatedAt time.Time
}

type State struct {
	Config     config.Config
	WorkingDir string
	StartedAt  time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	messages []Message
}

func New(cfg config.Config, workingDir string, now time.Time) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{
		Config:     cfg,
		WorkingDir: workingDir,
		StartedAt:  now,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *State) AddMessage(role Role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	})
}

func (s *State) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := make([]Message, len(s.messages))
	copy(messages, s.messages)
	return messages
}

func (s *State) Shutdown() {
	s.cancel()
}

func (s *State) Done() <-chan struct{} {
	return s.ctx.Done()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/session -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
gofmt -w internal/app/session
git add internal/app/session
git commit -m "feat: add session state"
```

Expected: commit succeeds.

---

### Task 5: Logging, App Runner, and CLI Delegation

**Files:**
- Create: `internal/app/logging/logging.go`
- Create: `internal/app/app.go`
- Modify: `cmd/marshal/main.go`

**Interfaces:**
- Consumes: `config.Load`, `session.New`
- Produces: `logging.New(w io.Writer, level slog.Level) *slog.Logger`
- Produces: `app.Run(ctx context.Context, stdout io.Writer, stderr io.Writer) error`

- [ ] **Step 1: Write failing app startup test**

Create `internal/app/app_test.go`:

```go
package app

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestRunReturnsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil), WithNow(func() time.Time {
		return time.Unix(100, 0)
	}))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/app -run TestRunReturnsWhenContextIsCancelled -count=1
```

Expected: FAIL because `Run` and `WithNow` are not defined.

- [ ] **Step 3: Implement logging and app runner**

Create `internal/app/logging/logging.go`:

```go
package logging

import (
	"io"
	"log/slog"
)

func New(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
```

Create `internal/app/app.go`:

```go
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
)

type options struct {
	now func() time.Time
}

type Option func(*options)

func WithNow(now func() time.Time) Option {
	return func(opts *options) {
		opts.now = now
	}
}

func Run(ctx context.Context, stdout io.Writer, stderr io.Writer, opts ...Option) error {
	runOpts := options{now: time.Now}
	for _, opt := range opts {
		opt(&runOpts)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}

	cfg, err := config.Load(config.LoadOptions{WorkingDir: workingDir})
	if err != nil {
		return err
	}

	logger := logging.New(stderr, slog.LevelInfo)
	state := session.New(cfg, workingDir, runOpts.now())
	defer state.Shutdown()

	logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

	select {
	case <-ctx.Done():
		return nil
	case <-state.Done():
		return nil
	default:
		_, _ = fmt.Fprintln(stdout, "Marshal")
		return nil
	}
}
```

- [ ] **Step 4: Update CLI entrypoint**

Replace `cmd/marshal/main.go` with:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"marshal/internal/app"
)

func main() {
	if err := app.Run(context.Background(), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
go test ./internal/app ./cmd/marshal -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
gofmt -w cmd/marshal internal/app
git add cmd/marshal internal/app
git commit -m "feat: add app runner"
```

Expected: commit succeeds.

---

### Task 6: Bubble Tea TUI Model

**Files:**
- Create: `internal/app/tui/model_test.go`
- Create: `internal/app/tui/model.go`
- Modify: `go.mod`
- Create or update: `go.sum`

**Interfaces:**
- Consumes: `session.State`, `session.RoleUser`
- Produces: `tui.New(state *session.State) tui.Model`
- Produces: Bubble Tea `Init`, `Update`, and `View` methods on `tui.Model`

- [ ] **Step 1: Add Bubble Tea dependencies**

Run:

```bash
go get github.com/charmbracelet/bubbletea github.com/charmbracelet/bubbles
```

Expected: `go.mod` and `go.sum` include Bubble Tea and Bubbles.

- [ ] **Step 2: Write failing TUI tests**

Create `internal/app/tui/model_test.go`:

```go
package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func TestEnterAppendsInputAndClearsPrompt(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].Role != session.RoleUser || messages[0].Content != "hello" {
		t.Fatalf("message = %#v", messages[0])
	}
	if model.input.Value() != "" {
		t.Fatalf("input = %q, want empty", model.input.Value())
	}
}

func TestEnterOnWhitespaceDoesNotAppendMessage(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if got := len(state.Messages()); got != 0 {
		t.Fatalf("len(messages) = %d, want 0", got)
	}
}

func TestQuitKeyRequestsShutdown(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("quit command is nil")
	}

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not shut down")
	}
}

func TestViewContainsExpectedPanels(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Marshal",
		"Status",
		"Transcript",
		"Streaming Output",
		"Command Palette",
		"Tool Log",
		"Diff",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/tui -count=1
```

Expected: FAIL because package `internal/app/tui` or `New` is not defined.

- [ ] **Step 4: Implement TUI model**

Create `internal/app/tui/model.go`:

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/session"
)

type Model struct {
	state *session.State
	input textinput.Model
}

func New(state *session.State) Model {
	input := textinput.New()
	input.Placeholder = "Ask Marshal..."
	input.Focus()
	input.CharLimit = 4000
	input.Width = 80

	return Model{
		state: state,
		input: input,
	}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.state.Shutdown()
			return m, tea.Quit
		case tea.KeyEnter:
			value := strings.TrimSpace(m.input.Value())
			if value == "" {
				return m, nil
			}
			m.state.AddMessage(session.RoleUser, value)
			m.input.Reset()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Marshal\n")
	fmt.Fprintf(&b, "Status: project=%s cwd=%s local-only=%t\n\n",
		m.state.Config.Project.Name,
		m.state.WorkingDir,
		!m.state.Config.Privacy.RemoteProvidersAllowed,
	)

	fmt.Fprintf(&b, "Transcript\n")
	messages := m.state.Messages()
	if len(messages) == 0 {
		fmt.Fprintf(&b, "  No messages yet.\n")
	}
	for _, message := range messages {
		fmt.Fprintf(&b, "  %s: %s\n", message.Role, message.Content)
	}

	fmt.Fprintf(&b, "\nStreaming Output\n")
	fmt.Fprintf(&b, "  No model output yet.\n")
	fmt.Fprintf(&b, "\nCommand Palette\n")
	fmt.Fprintf(&b, "  No commands available yet.\n")
	fmt.Fprintf(&b, "\nTool Log\n")
	fmt.Fprintf(&b, "  No tool calls yet.\n")
	fmt.Fprintf(&b, "\nDiff\n")
	fmt.Fprintf(&b, "  No patch proposed.\n")
	fmt.Fprintf(&b, "\n%s\n", m.input.View())

	return b.String()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
go test ./internal/app/tui -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
gofmt -w internal/app/tui
go mod tidy
git add internal/app/tui go.mod go.sum
git commit -m "feat: add tui shell model"
```

Expected: commit succeeds.

---

### Task 7: Wire TUI into App Runner

**Files:**
- Modify: `internal/app/app_test.go`
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: `tui.New(state *session.State) tui.Model`
- Produces: `app.WithProgramRunner(runner ProgramRunner) app.Option` for tests
- Produces: `type ProgramRunner func(model tea.Model, output io.Writer) error`

- [ ] **Step 1: Replace app test with runner wiring tests**

Replace `internal/app/app_test.go` with:

```go
package app

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunSkipsProgramWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithProgramRunner(func(model tea.Model, output io.Writer) error {
			called = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if called {
		t.Fatal("program runner was called after context cancellation")
	}
}

func TestRunStartsProgram(t *testing.T) {
	stdout := bytes.NewBuffer(nil)

	called := false
	err := Run(context.Background(), stdout, bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithProgramRunner(func(model tea.Model, output io.Writer) error {
			called = true
			if output != stdout {
				t.Fatal("runner did not receive stdout buffer")
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("program runner was not called")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app -count=1
```

Expected: FAIL because `WithProgramRunner` is not defined.

- [ ] **Step 3: Implement TUI wiring**

Replace `internal/app/app.go` with:

```go
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
)

type ProgramRunner func(model tea.Model, output io.Writer) error

type options struct {
	now           func() time.Time
	programRunner ProgramRunner
}

type Option func(*options)

func WithNow(now func() time.Time) Option {
	return func(opts *options) {
		opts.now = now
	}
}

func WithProgramRunner(runner ProgramRunner) Option {
	return func(opts *options) {
		opts.programRunner = runner
	}
}

func Run(ctx context.Context, stdout io.Writer, stderr io.Writer, opts ...Option) error {
	runOpts := options{
		now:           time.Now,
		programRunner: runProgram,
	}
	for _, opt := range opts {
		opt(&runOpts)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}

	cfg, err := config.Load(config.LoadOptions{WorkingDir: workingDir})
	if err != nil {
		return err
	}

	logger := logging.New(stderr, slog.LevelInfo)
	state := session.New(cfg, workingDir, runOpts.now())
	defer state.Shutdown()

	logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

	select {
	case <-ctx.Done():
		return nil
	default:
	}

	return runOpts.programRunner(tui.New(state), stdout)
}

func runProgram(model tea.Model, output io.Writer) error {
	program := tea.NewProgram(model, tea.WithOutput(output))
	_, err := program.Run()
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/app -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
gofmt -w internal/app
git add internal/app
git commit -m "feat: run tui from app"
```

Expected: commit succeeds.

---

### Task 8: Verify Full MVP A-B Slice

**Files:**
- No source edits expected unless verification reveals a defect.

**Interfaces:**
- Consumes: all packages from prior tasks.
- Produces: passing full test suite and runnable CLI smoke test.

- [ ] **Step 1: Run all tests**

Run:

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 2: Run CLI smoke test**

Run:

```bash
go run ./cmd/marshal
```

Expected: TUI starts, shows Marshal status, accepts typed input, displays it in Transcript, and exits with Ctrl+C or Esc.

- [ ] **Step 3: Fix defects with TDD if verification fails**

If a failure appears, write the smallest failing test that reproduces it before changing production code. Run the focused test to confirm it fails, implement the fix, run the focused test, then run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit verification fixes or empty verification note**

If files changed, run:

```bash
git add .
git commit -m "fix: stabilize mvp shell"
```

If no files changed, run:

```bash
git status --short
```

Expected: clean working tree.

---

### Task 9: Mark MVP Checklist Items Complete

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

**Interfaces:**
- Consumes: verified implementation from Tasks 1-8.
- Produces: checked-off Milestone A and B checklist items.

- [ ] **Step 1: Update completed checklist items**

In `docs/10-mvp-implementation-checklist.md`, change Milestone A and Milestone B to:

```markdown
## Milestone A: Project skeleton

- [x] Create Go module
- [x] Add CLI entrypoint at `cmd/marshal/main.go`
- [x] Add config loader
- [x] Add logging
- [x] Add basic app state
- [x] Add graceful shutdown handling

## Milestone B: TUI shell

- [x] Add Bubble Tea app skeleton
- [x] Add chat input
- [x] Add streaming output area
- [x] Add status bar
- [x] Add command palette placeholder
- [x] Add tool log panel placeholder
- [x] Add diff panel placeholder
```

- [ ] **Step 2: Run final tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Commit checklist update**

Run:

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark mvp shell checklist complete"
```

Expected: commit succeeds.

---

## Self-Review Notes

- Spec coverage: Tasks cover git initialization, config defaults/loading, logging, session state, graceful shutdown, Bubble Tea shell, chat input, placeholders, verification, and final checklist updates.
- Placeholder scan: The only intentional placeholders are user-facing TUI panels required by Milestone B.
- Type consistency: `config.Config`, `session.State`, `tui.Model`, and `app.Run` signatures are introduced before downstream use.
