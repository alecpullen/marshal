package settings

func newDiagnosticsPane(s *state) sectionPane {
	return newMapPane(newMapEditor("Commands", &s.cfg.Diagnostics.Commands))
}
