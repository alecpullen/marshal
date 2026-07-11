package settings

import "charm.land/huh/v2"

func newIndexingPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewConfirm().Key("Use treesitter").Title("Use treesitter").Value(&s.cfg.Indexing.UseTreesitter),
			huh.NewConfirm().Key("Use embeddings").Title("Use embeddings").Value(&s.cfg.Indexing.UseEmbeddings),
			huh.NewConfirm().Key("Summarise files").Title("Summarise files").Value(&s.cfg.Indexing.SummariseFiles),
		)
	})
	return newMixedPane(form, newListStrings("Ignore patterns", &s.cfg.Indexing.Ignore))
}
