package settings

import (
	"fmt"
	"time"

	"charm.land/huh/v2"
)

func newWebPane(s *state) sectionPane {
	buf := &struct{ timeout string }{}
	return newScalarPane(func() *huh.Form {
		buf.timeout = s.cfg.Web.FetchTimeout.String()
		return newSectionForm(
			huh.NewConfirm().Key("Enabled").Title("Enabled").
				Description("allow web.fetch / web.search tools").Value(&s.cfg.Web.Enabled),
			huh.NewInput().Key("Fetch timeout").Title("Fetch timeout").Value(&buf.timeout).
				Validate(func(v string) error {
					d, err := time.ParseDuration(v)
					if err != nil {
						return fmt.Errorf("must be a duration like 30s")
					}
					s.cfg.Web.FetchTimeout = d
					return nil
				}),
			huh.NewInput().Key("Search provider").Title("Search provider").Value(&s.cfg.Web.SearchProvider),
			huh.NewInput().Key("Search URL").Title("Search URL").Value(&s.cfg.Web.SearchURL),
			secretField("Search key",
				func() string { return s.cfg.Web.SearchKey },
				func(v string) { s.cfg.Web.SearchKey = v }),
		)
	})
}
