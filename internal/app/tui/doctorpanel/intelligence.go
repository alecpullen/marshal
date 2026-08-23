package doctorpanel

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/lsp"
)

// Check is one intelligence-subsystem status line in the doctor panel.
type Check struct {
	Name   string // "Index", "Embeddings", "LSP", "Watcher"
	Status string // "ok", "warn", "off"
	Detail string // counts/timestamps, or a pointer at the relevant setting
}

// ComputeIntelligence derives subsystem liveness from config and the project
// DB. It deliberately uses no live runtime state: LSP detection is a PATH
// probe (no servers are started), and the watcher row reports configured
// behavior rather than goroutine liveness.
func ComputeIntelligence(cfg config.Config, database *db.DB, projectID int64) []Check {
	return []Check{
		indexCheck(database, projectID),
		embeddingsCheck(cfg, database, projectID),
		lspCheck(cfg),
		watcherCheck(cfg),
	}
}

func indexCheck(database *db.DB, projectID int64) Check {
	if database == nil {
		return Check{Name: "Index", Status: "off", Detail: "database unavailable"}
	}
	files, err := database.CountFiles(projectID)
	if err != nil || files == 0 {
		return Check{Name: "Index", Status: "off", Detail: "index never ran — repo map and symbol tools have no data"}
	}
	detail := fmt.Sprintf("%d files", files)
	if symbols, err := database.CountSymbols(projectID); err == nil {
		detail += fmt.Sprintf(" · %d symbols", symbols)
	}
	if ts, err := database.LatestIndexedAt(projectID); err == nil && !ts.IsZero() {
		detail += " · last indexed " + ts.Local().Format("2006-01-02 15:04")
	}
	return Check{Name: "Index", Status: "ok", Detail: detail}
}

func embeddingsConfigured(cfg config.Config) bool {
	return cfg.Indexing.EmbeddingPreset != "" || cfg.Indexing.UseEmbeddings
}

func embeddingsCheck(cfg config.Config, database *db.DB, projectID int64) Check {
	if !embeddingsConfigured(cfg) {
		return Check{Name: "Embeddings", Status: "off", Detail: "semantic search disabled — set indexing.embedding_preset"}
	}
	n := 0
	if database != nil {
		n, _ = database.CountEmbeddedChunks(projectID)
	}
	preset := cfg.Indexing.EmbeddingPreset
	if preset == "" {
		preset = "use_embeddings"
	}
	if n == 0 {
		return Check{Name: "Embeddings", Status: "warn", Detail: "configured (" + preset + ") but no embeddings written yet"}
	}
	return Check{Name: "Embeddings", Status: "ok", Detail: fmt.Sprintf("%d embedded chunks (%s)", n, preset)}
}

func lspCheck(cfg config.Config) Check {
	if cfg.LSP.Enabled != nil && !*cfg.LSP.Enabled {
		return Check{Name: "LSP", Status: "off", Detail: "disabled — lsp.enabled = false"}
	}
	configured := map[string]lsp.ServerSpec{}
	disabled := map[string]bool{}
	for lang, sc := range cfg.LSP.Servers {
		if sc.Disabled {
			disabled[lang] = true
			continue
		}
		configured[lang] = lsp.ServerSpec{Command: sc.Command, Args: sc.Args}
	}
	effective := lsp.DetectServers(configured, disabled)
	if len(effective) == 0 {
		return Check{Name: "LSP", Status: "off", Detail: "no language servers found on PATH — symbols/diagnostics fall back to tree-sitter"}
	}
	var found, missing []string
	for lang, spec := range effective {
		if _, err := exec.LookPath(spec.Command); err == nil {
			found = append(found, lang)
		} else {
			missing = append(missing, lang+" ("+spec.Command+")")
		}
	}
	sort.Strings(found)
	sort.Strings(missing)
	if len(found) == 0 {
		return Check{Name: "LSP", Status: "warn", Detail: "configured but not on PATH: " + strings.Join(missing, ", ")}
	}
	detail := "on PATH: " + strings.Join(found, ", ")
	if len(missing) > 0 {
		detail += " · missing: " + strings.Join(missing, ", ")
	}
	return Check{Name: "LSP", Status: "ok", Detail: detail}
}

func watcherCheck(cfg config.Config) Check {
	// The watcher auto-enables once embeddings are configured (semantic
	// search needs the index to stay fresh), or when explicitly enabled.
	if config.WatchEnabled(cfg.Indexing.Watch, embeddingsConfigured(cfg)) || embeddingsConfigured(cfg) {
		return Check{Name: "Watcher", Status: "ok", Detail: "index refreshes on file changes"}
	}
	return Check{Name: "Watcher", Status: "warn", Detail: "off — index refreshes only at startup; set indexing.watch = true"}
}
