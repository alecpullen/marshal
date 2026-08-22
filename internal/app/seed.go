package app

import (
	"path/filepath"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/repo"
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
