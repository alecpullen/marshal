package settings

func newDiagnosticsPane(s *state) sectionPane {
	return newMapPane(newMapStringEditor("Commands", &s.cfg.Diagnostics.Commands))
}
