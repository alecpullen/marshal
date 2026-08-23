package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/repo"
	"marshal/internal/strutil"
)

// repoMapSeedMaxFiles caps the directory map embedded in the context pack.
const repoMapSeedMaxFiles = 200

// seedRepoContext renders the repo card and directory map from the index and
// installs them as context-pack sections, replacing any previous seed
// (AI-01). It is a no-op when the index is empty (fresh project whose
// startup scan has not completed yet) or the DB read fails — in both cases
// the agent behaves exactly as before this feature.
func seedRepoContext(state *session.State, database *db.DB, projectID int64) {
	if state == nil || database == nil {
		return
	}
	files, err := database.GetFileIndex(projectID, 0)
	if err != nil || len(files) == 0 {
		return
	}
	symbols, err := database.GetSymbols(projectID, repoMapSeedMaxFiles)
	if err != nil {
		symbols = nil // card + map without symbol suffixes beat no seed
	}

	projectName := state.Config.Project.Name
	if projectName == "" {
		projectName = filepath.Base(state.WorkingDir)
	}
	card := repo.RenderRepoCard(projectName, files)
	dirMap := repo.RenderDirectoryMap(files, symbols, repoMapSeedMaxFiles)

	state.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := pack.TokenUsage.MaxTokens
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeRepoSections(pack, card, dirMap, maxTokens, nil)
	})
}

// sessionSummarySeedLimit bounds how many ended-session summaries are seeded.
const sessionSummarySeedLimit = 3

// sessionSummaryMaxChars bounds each seeded summary line.
const sessionSummaryMaxChars = 200

// seedSessionSummaries installs a "Recent Sessions" context-pack section from
// the project's last few ended sessions (AI batch 4). No-op when there is no
// history or the read fails — the agent behaves exactly as before.
func seedSessionSummaries(state *session.State, database *db.DB, projectID int64) {
	if state == nil || database == nil {
		return
	}
	summaries, err := database.RecentSessionSummaries(projectID, state.SessionID(), sessionSummarySeedLimit)
	if err != nil || len(summaries) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("Recent sessions (prior work):\n")
	for _, s := range summaries {
		title := s.Title
		if title == "" {
			title = s.SessionID
		}
		summary := strings.Join(strings.Fields(s.Summary), " ")
		fmt.Fprintf(&b, "- %s: %s\n", title, strutil.Truncate(summary, sessionSummaryMaxChars, true))
	}
	state.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := pack.TokenUsage.MaxTokens
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeSessionSummaries(pack, b.String(), maxTokens, nil)
	})
}
