package settings

import "charm.land/huh/v2"

func newPrivacyPane(s *state) sectionPane {
	return newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewConfirm().Key("Remote providers allowed").Title("Remote providers allowed").
				Description("allow remote providers globally").Value(&s.cfg.Privacy.RemoteProvidersAllowed),
			huh.NewConfirm().Key("Redact secrets").Title("Redact secrets").
				Description("scrub likely secrets from context sent to models").Value(&s.cfg.Privacy.RedactSecrets),
			huh.NewConfirm().Key("Include gitignored files").Title("Include gitignored files").
				Description("let indexing and context include gitignored paths").Value(&s.cfg.Privacy.IncludeGitignoredFiles),
		)
	})
}
