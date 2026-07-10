package settings

import (
	"strconv"

	"charm.land/huh/v2"
)

func newSwarmPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		b := &struct{ fix, total string }{
			fix:   strconv.Itoa(s.cfg.Swarm.Budget.MaxFixRounds),
			total: strconv.Itoa(s.cfg.Swarm.Budget.MaxTotalTokens),
		}
		return newSectionForm(
			numField("Max fix rounds", &b.fix, 0, func(v int) { s.cfg.Swarm.Budget.MaxFixRounds = v }),
			numField("Max total tokens", &b.total, 0, func(v int) { s.cfg.Swarm.Budget.MaxTotalTokens = v }),
		)
	})
	return newMixedPane(form, newMapIntEditor("Tool iters", &s.cfg.Swarm.Budget.ToolIters))
}
