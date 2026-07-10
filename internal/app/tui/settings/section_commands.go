package settings

import "charm.land/huh/v2"

func newCommandsPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewInput().Key("Test command").Title("Test command").Value(&s.cfg.Commands.Test),
			huh.NewInput().Key("Format command").Title("Format command").Value(&s.cfg.Commands.Format),
			huh.NewInput().Key("Vet command").Title("Vet command").Value(&s.cfg.Commands.Vet),
			huh.NewInput().Key("Project name").Title("Project name").Value(&s.cfg.Project.Name),
		)
	})
	return newMixedPane(form, newListStrings("Languages", &s.cfg.Project.Languages))
}
